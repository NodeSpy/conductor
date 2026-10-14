package controller

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/jail"
)

// The paseo provider relay. paseo's `conductor` provider (the plugin in
// internal/paseoplugin) is an ACP provider whose stream is this socket: one
// connection per paseo session, plus paseo's own short ones — a capability
// probe (initialize only) and a model listing (session/new with no
// identity). For a session, conductor launches the agent itself on the
// configured acp runtime, in the workspace jail, and relays ACP both ways.
//
// The jailed agent is keyed by the paseo agent's id (the plugin passes it in
// session/new|load `_meta._paseo.providerOptions._conductor`) and outlives
// the stream: a paseo restart or an unloaded agent drops the stream, and the
// next session/load re-attaches it to the same live agent. Only archiving
// the paseo agent — the plugin's `_conductor/archive`, from the provider's
// session.archive or the agent.archived hook — or DetachedTTL with no stream
// tears the jail down.
//
// What crosses: messages pass through as they are, except the request ids
// paseo picks (renumbered, so a re-attached stream's ids never meet a dead
// one's late answers), the session id (conductor's own, encoding the paseo
// agent's id, see relaySessionID), and paseo's own MCP server in
// session/new|load, which is stripped unless paseo_provider.paseo_tools.

// PaseoRelay serves the paseo provider socket.
type PaseoRelay struct {
	Command    []string // the acp runtime's launch argv
	Tool       string   // its tool (the jail's per-tool rules)
	Iso        *config.IsolationConfig
	ScrubEnv   bool
	PaseoTools bool
	Detached   time.Duration
	// ProbeDir is an empty directory the capability probe's session runs in.
	ProbeDir string
	Logf     func(string, ...any)
	Audit    func(map[string]any)

	// launch starts the agent for one paseo agent id in cwd; tests replace it.
	launch func(agentID, cwd string) (*acpProcess, error)
	now    func() time.Time

	mu     sync.Mutex
	agents map[string]*relayAgent
	probe  *relayProbe
	probed chan struct{}
	closed bool
}

// relayProbe is the agent's own answers to initialize and to a session/new,
// captured once: what paseo's capability probe and model listing are told.
type relayProbe struct {
	init json.RawMessage // initialize result
	sess json.RawMessage // session/new result (modes, config options, models)
	err  error
}

// relayAgent is one paseo agent's jailed agent process.
type relayAgent struct {
	id   string
	proc *acpProcess
	r    *PaseoRelay

	wmu sync.Mutex // agent stdin writes

	mu       sync.Mutex
	conn     *relayConn // the attached paseo stream (nil = detached)
	detached time.Time
	native   string // the agent's own session id
	nextID   int64
	pending  map[string]pendingCall // relay id → the paseo request it stands for
	initDone bool
	gone     bool
}

type pendingCall struct {
	conn   *relayConn
	id     json.RawMessage // paseo's id
	method string
	waiter chan relayMsg // relay-internal calls (initialize) wait here
}

// relayConn is one paseo stream.
type relayConn struct {
	c   net.Conn
	wmu sync.Mutex
}

func (c *relayConn) send(m relayMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.c.Write(append(b, '\n'))
	return err
}

// relayMsg is one JSON-RPC message, fields kept raw so what conductor does
// not touch passes through byte for byte.
type relayMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func (m relayMsg) isRequest() bool      { return m.Method != "" && len(m.ID) > 0 }
func (m relayMsg) isNotification() bool { return m.Method != "" && len(m.ID) == 0 }

func rpcErr(id json.RawMessage, code int, msg string) relayMsg {
	b, _ := json.Marshal(map[string]any{"code": code, "message": msg})
	return relayMsg{JSONRPC: "2.0", ID: id, Error: b}
}

func rpcResult(id json.RawMessage, v any) relayMsg {
	b, _ := json.Marshal(v)
	if raw, ok := v.(json.RawMessage); ok {
		b = raw
	}
	return relayMsg{JSONRPC: "2.0", ID: id, Result: b}
}

// relaySessionPrefix marks conductor's session ids: "cj1.<base64url agent
// id>.<the agent's own session id>". paseo stores it as the session's
// persistence and hands it back on session/load — so a resume names the
// jail it belongs to.
const relaySessionPrefix = "cj1."

func relaySessionID(agentID, native string) string {
	return relaySessionPrefix + base64.RawURLEncoding.EncodeToString([]byte(agentID)) + "." + native
}

func parseRelaySessionID(s string) (agentID, native string, ok bool) {
	rest, ok := strings.CutPrefix(s, relaySessionPrefix)
	if !ok {
		return "", "", false
	}
	enc, native, ok := strings.Cut(rest, ".")
	if !ok || native == "" {
		return "", "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(b) == 0 {
		return "", "", false
	}
	return string(b), native, true
}

// NewPaseoRelay builds the relay for the runtime cc.
func NewPaseoRelay(cc config.ControllerConfig, p *config.PaseoProviderConfig, probeDir string, logf func(string, ...any), audit func(map[string]any)) *PaseoRelay {
	r := &PaseoRelay{
		Command: acpCommand(cc), Tool: firstNonEmpty(cc.Tool, cc.Agent), Iso: cc.Isolation, ScrubEnv: cc.ScrubEnv,
		PaseoTools: p.PaseoTools, Detached: p.Detached(), ProbeDir: probeDir, Logf: logf, Audit: audit,
	}
	r.launch = r.launchJailed
	return r
}

func (r *PaseoRelay) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func (r *PaseoRelay) audit(row map[string]any) {
	if r.Audit != nil {
		row["event"] = "paseo_provider"
		r.Audit(row)
	}
}

func (r *PaseoRelay) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// launchJailed starts the agent for a paseo session: the runtime's command,
// always in the workspace jail (a jail that cannot be built fails the
// session — it never degrades to an unconfined agent), as a launch with no
// dispatch target, and with tool state of its own (keyed by the paseo agent).
func (r *PaseoRelay) launchJailed(agentID, cwd string) (*acpProcess, error) {
	if JailManager == nil {
		return nil, errors.New("paseo provider: the workspace jail is not available on this box")
	}
	req := dispatch.Request{DispatchID: "paseo-" + agentID}
	opt := agentLaunchOpts(true, r.Tool, r.Iso, req)
	if opt.jail == nil {
		return nil, errors.New("paseo provider: the runtime's isolation opts out of the workspace jail")
	}
	opt.jailDefault = false
	opt.jail.Label = "paseo " + agentID
	opt.jail.AgentID = agentID
	opt.jail.StateKey = "paseo-" + agentID
	return startACPProcess(r.Command, cwd, nil, "", opt, r.ScrubEnv)
}

// Serve accepts paseo streams on l until ctx ends. Each connection must come
// from this uid (checkPeer).
func (r *PaseoRelay) Serve(ctx context.Context, l net.Listener) error {
	r.mu.Lock()
	if r.agents == nil {
		r.agents = map[string]*relayAgent{}
	}
	if r.probed == nil {
		r.probed = make(chan struct{})
		go r.runProbe()
	}
	r.mu.Unlock()
	go func() {
		<-ctx.Done()
		_ = l.Close()
		r.Close()
	}()
	go r.reapLoop(ctx)
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if why := checkPeer(c); why != "" {
			r.logf("paseo provider: refused a connection: %s", why)
			_ = c.Close()
			continue
		}
		go r.serveConn(c)
	}
}

// Close tears down every live agent.
func (r *PaseoRelay) Close() {
	r.mu.Lock()
	r.closed = true
	agents := r.agents
	r.agents = map[string]*relayAgent{}
	r.mu.Unlock()
	for _, a := range agents {
		a.stop()
	}
}

// runProbe launches the agent once in ProbeDir and records its answers to
// initialize and session/new.
func (r *PaseoRelay) runProbe() {
	p := &relayProbe{}
	defer func() {
		r.mu.Lock()
		r.probe = p
		close(r.probed)
		r.mu.Unlock()
		if p.err != nil {
			r.logf("paseo provider: capability probe failed: %v", p.err)
		}
	}()
	if r.ProbeDir != "" {
		if err := os.MkdirAll(r.ProbeDir, 0o700); err != nil {
			p.err = err
			return
		}
	}
	proc, err := r.launch("probe", r.ProbeDir)
	if err != nil {
		p.err = err
		return
	}
	defer proc.cleanup()
	rd := bufio.NewReader(proc.stdout)
	call := func(id int, method string, params any) (json.RawMessage, error) {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if _, err := proc.stdin.Write(append(b, '\n')); err != nil {
			return nil, err
		}
		for {
			line, err := readLine(rd)
			if err != nil {
				return nil, err
			}
			var m relayMsg
			if json.Unmarshal(line, &m) != nil || m.Method != "" || string(m.ID) != strconv.Itoa(id) {
				continue // a notification, or the agent's own request
			}
			if len(m.Error) > 0 {
				return nil, fmt.Errorf("%s: %s", method, m.Error)
			}
			return m.Result, nil
		}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var res json.RawMessage
		if res, p.err = call(1, "initialize", map[string]any{
			"protocolVersion": 1, "clientCapabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "conductor", "version": "1"},
		}); p.err != nil {
			return
		}
		p.init = probeInit(res)
		p.sess, p.err = call(2, "session/new", map[string]any{"cwd": r.ProbeDir, "mcpServers": []any{}})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		p.err = errors.New("the agent did not answer initialize/session/new within 2m")
	}
}

// probeInit is the agent's initialize result as paseo is told it: session
// listing is not relayed (it would need an agent launched per listing).
func probeInit(res json.RawMessage) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(res, &m) != nil {
		return res
	}
	if caps, ok := m["agentCapabilities"].(map[string]any); ok {
		if sc, ok := caps["sessionCapabilities"].(map[string]any); ok {
			delete(sc, "list")
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func (r *PaseoRelay) probeResult(ctx context.Context) (*relayProbe, error) {
	select {
	case <-r.probed:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.probe.err != nil {
		return nil, fmt.Errorf("the agent's capability probe failed: %w", r.probe.err)
	}
	return r.probe, nil
}

func readLine(rd *bufio.Reader) ([]byte, error) {
	for {
		line, err := rd.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			return line, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// serveConn handles one paseo stream.
func (r *PaseoRelay) serveConn(c net.Conn) {
	conn := &relayConn{c: c}
	defer c.Close()
	rd := bufio.NewReaderSize(c, 1<<16)
	next := func() (relayMsg, error) {
		line, err := readLine(rd)
		if err != nil {
			return relayMsg{}, err
		}
		var m relayMsg
		if err := json.Unmarshal(line, &m); err != nil {
			return relayMsg{}, fmt.Errorf("not JSON-RPC: %w", err)
		}
		return m, nil
	}
	m, err := next()
	if err != nil {
		return
	}
	if m.Method == "_conductor/archive" {
		r.handleArchive(conn, m)
		return
	}
	if m.Method != "initialize" || !m.isRequest() {
		_ = conn.send(rpcErr(m.ID, -32600, "the first message must be initialize"))
		return
	}
	init := m
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	p, err := r.probeResult(ctx)
	cancel()
	if err != nil {
		_ = conn.send(rpcErr(m.ID, -32603, "conductor: "+err.Error()))
		return
	}
	_ = conn.send(rpcResult(m.ID, p.init))

	m, err = next()
	if err != nil {
		return // paseo's capability probe: initialize only
	}
	if m.Method != "session/new" && m.Method != "session/load" {
		_ = conn.send(rpcErr(m.ID, -32601, "conductor: "+m.Method+" before a session"))
		return
	}
	agentID, cwd := sessionIdentity(m.Params)
	if agentID == "" {
		if m.Method == "session/new" {
			r.serveCatalog(conn, m, p, next)
			return
		}
		_ = conn.send(rpcErr(m.ID, -32602, "conductor: session/load without the paseo agent's identity"))
		return
	}
	if m.Method == "session/load" {
		sid := jsonString(m.Params, "sessionId")
		owner, _, ok := parseRelaySessionID(sid)
		if !ok || owner != agentID {
			_ = conn.send(rpcErr(m.ID, -32602, "conductor: session "+sid+" is not this agent's"))
			return
		}
	}
	a, err := r.attach(agentID, cwd, conn, init)
	if err != nil {
		r.logf("paseo provider: agent %s: %v", agentID, err)
		_ = conn.send(rpcErr(m.ID, -32603, "conductor: "+err.Error()))
		return
	}
	defer a.detach(conn)
	for {
		a.fromPaseo(conn, m)
		if m, err = next(); err != nil {
			return
		}
	}
}

// serveCatalog answers paseo's model listing — a session/new with no paseo
// agent behind it — from the probe, without launching anything.
func (r *PaseoRelay) serveCatalog(conn *relayConn, m relayMsg, p *relayProbe, next func() (relayMsg, error)) {
	var res map[string]json.RawMessage
	_ = json.Unmarshal(p.sess, &res)
	if res == nil {
		res = map[string]json.RawMessage{}
	}
	res["sessionId"] = json.RawMessage(`"cj-catalog"`)
	_ = conn.send(rpcResult(m.ID, res))
	for {
		m, err := next()
		if err != nil {
			return
		}
		if !m.isRequest() {
			continue
		}
		if m.Method == "session/close" {
			_ = conn.send(rpcResult(m.ID, map[string]any{}))
			continue
		}
		_ = conn.send(rpcErr(m.ID, -32601, "conductor: "+m.Method+" is not served on a model listing"))
	}
}

// sessionIdentity reads the paseo agent's id (the plugin's
// `_meta._paseo.providerOptions._conductor.agentId`) and the cwd from a
// session/new|load's params.
func sessionIdentity(params json.RawMessage) (agentID, cwd string) {
	var p struct {
		Cwd  string `json:"cwd"`
		Meta struct {
			Paseo struct {
				ProviderOptions struct {
					Conductor struct {
						AgentID string `json:"agentId"`
					} `json:"_conductor"`
				} `json:"providerOptions"`
			} `json:"_paseo"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(params, &p)
	return p.Meta.Paseo.ProviderOptions.Conductor.AgentID, p.Cwd
}

func jsonString(raw json.RawMessage, key string) string {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	var s string
	_ = json.Unmarshal(m[key], &s)
	return s
}

// attach binds conn to agentID's live agent, launching it (and handing it
// paseo's initialize) when there is none.
func (r *PaseoRelay) attach(agentID, cwd string, conn *relayConn, init relayMsg) (*relayAgent, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, errors.New("conductor is shutting down")
	}
	a := r.agents[agentID]
	if a == nil {
		if cwd == "" || !filepath.IsAbs(cwd) {
			r.mu.Unlock()
			return nil, fmt.Errorf("session cwd %q is not an absolute path", cwd)
		}
		a = &relayAgent{id: agentID, r: r, pending: map[string]pendingCall{}}
		r.agents[agentID] = a
		r.mu.Unlock()
		proc, err := r.launch(agentID, cwd)
		if err != nil {
			r.mu.Lock()
			delete(r.agents, agentID)
			r.mu.Unlock()
			return nil, fmt.Errorf("launch: %w", err)
		}
		a.mu.Lock()
		a.proc = proc
		a.mu.Unlock()
		go a.pump()
		r.audit(map[string]any{"status": "launched", "agent": agentID, "cwd": cwd})
	} else {
		r.mu.Unlock()
	}
	a.mu.Lock()
	for a.proc == nil && !a.gone { // a concurrent attach is still launching it
		a.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		a.mu.Lock()
	}
	if a.gone {
		a.mu.Unlock()
		return nil, errors.New("the agent exited")
	}
	old := a.conn
	a.conn, a.detached = conn, time.Time{}
	needInit := !a.initDone
	a.mu.Unlock()
	if old != nil && old != conn {
		_ = old.c.Close() // one paseo stream per agent: the newest wins
	}
	if needInit {
		res, err := a.call("initialize", init.Params)
		if err != nil {
			return nil, fmt.Errorf("initialize: %w", err)
		}
		if len(res.Error) > 0 {
			return nil, fmt.Errorf("initialize: %s", res.Error)
		}
		a.mu.Lock()
		a.initDone = true
		a.mu.Unlock()
	}
	r.audit(map[string]any{"status": "attached", "agent": agentID})
	return a, nil
}

// call sends a relay-internal request to the agent and waits for its answer.
func (a *relayAgent) call(method string, params json.RawMessage) (relayMsg, error) {
	w := make(chan relayMsg, 1)
	a.mu.Lock()
	a.nextID++
	rid := json.RawMessage(strconv.FormatInt(a.nextID, 10))
	a.pending[string(rid)] = pendingCall{method: method, waiter: w}
	a.mu.Unlock()
	if err := a.write(relayMsg{JSONRPC: "2.0", ID: rid, Method: method, Params: params}); err != nil {
		return relayMsg{}, err
	}
	select {
	case m, ok := <-w:
		if !ok {
			return relayMsg{}, errors.New("the agent exited")
		}
		return m, nil
	case <-a.proc.exited:
		return relayMsg{}, errors.New("the agent exited")
	case <-time.After(2 * time.Minute):
		return relayMsg{}, errors.New("no answer within 2m")
	}
}

func (a *relayAgent) write(m relayMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	a.wmu.Lock()
	defer a.wmu.Unlock()
	_, err = a.proc.stdin.Write(append(b, '\n'))
	return err
}

// fromPaseo forwards one message from conn to the agent.
func (a *relayAgent) fromPaseo(conn *relayConn, m relayMsg) {
	switch {
	case m.isRequest():
		params := a.toAgentParams(m.Method, m.Params)
		a.mu.Lock()
		a.nextID++
		rid := json.RawMessage(strconv.FormatInt(a.nextID, 10))
		a.pending[string(rid)] = pendingCall{conn: conn, id: m.ID, method: m.Method}
		a.mu.Unlock()
		if err := a.write(relayMsg{JSONRPC: "2.0", ID: rid, Method: m.Method, Params: params}); err != nil {
			_ = conn.send(rpcErr(m.ID, -32603, "conductor: the agent is gone"))
		}
	case m.isNotification():
		_ = a.write(relayMsg{JSONRPC: "2.0", Method: m.Method, Params: a.toAgentParams(m.Method, m.Params)})
	default:
		// paseo's answer to the agent's own request (a permission prompt):
		// the agent's id space, passed through.
		_ = a.write(m)
	}
}

// toAgentParams rewrites a paseo message's params for the agent: the
// session id back to the agent's own, and for session/new|load, the
// identity removed and paseo's own MCP server stripped (unless
// paseo_tools).
func (a *relayAgent) toAgentParams(method string, raw json.RawMessage) json.RawMessage {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil || p == nil {
		return raw
	}
	if sid := jsonString(raw, "sessionId"); sid != "" {
		if _, native, ok := parseRelaySessionID(sid); ok {
			p["sessionId"], _ = json.Marshal(native)
		}
	}
	if method == "session/new" || method == "session/load" {
		p["_meta"] = dropConductorMeta(p["_meta"])
		if !a.r.PaseoTools {
			var stripped []string
			p["mcpServers"], stripped = stripPaseoMCP(p["mcpServers"])
			if len(stripped) > 0 {
				a.r.audit(map[string]any{"status": "mcp_stripped", "agent": a.id, "servers": stripped})
			}
		}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return raw
	}
	return b
}

// dropConductorMeta removes `_paseo.providerOptions._conductor` (the plugin's
// note to conductor, not the agent's business) from a session's _meta.
func dropConductorMeta(raw json.RawMessage) json.RawMessage {
	var meta map[string]map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &meta) != nil || meta["_paseo"] == nil {
		return raw
	}
	var opts map[string]json.RawMessage
	if json.Unmarshal(meta["_paseo"]["providerOptions"], &opts) == nil && opts != nil {
		delete(opts, "_conductor")
		meta["_paseo"]["providerOptions"], _ = json.Marshal(opts)
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return raw
	}
	return b
}

// stripPaseoMCP removes paseo's own MCP server from an ACP mcpServers list:
// an http/sse server at paseo's agent-tools path (/mcp/agents), or one named
// "paseo". It carries a paseo bearer token, and its tools create and drive
// agents on any provider — an unjailed one included.
func stripPaseoMCP(raw json.RawMessage) (json.RawMessage, []string) {
	var servers []map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &servers) != nil {
		return raw, nil
	}
	var kept []map[string]json.RawMessage
	var stripped []string
	for _, s := range servers {
		var name, u string
		_ = json.Unmarshal(s["name"], &name)
		_ = json.Unmarshal(s["url"], &u)
		paseo := name == "paseo"
		if pu, err := url.Parse(u); err == nil && u != "" && pu.Path == "/mcp/agents" {
			paseo = true
		}
		if paseo {
			stripped = append(stripped, name)
			continue
		}
		kept = append(kept, s)
	}
	if kept == nil {
		kept = []map[string]json.RawMessage{}
	}
	b, err := json.Marshal(kept)
	if err != nil {
		return raw, nil
	}
	return b, stripped
}

// pump reads the agent's stdout for its whole life and delivers each
// message to the attached paseo stream.
func (a *relayAgent) pump() {
	rd := bufio.NewReaderSize(a.proc.stdout, 1<<16)
	for {
		line, err := readLine(rd)
		if err != nil {
			break
		}
		var m relayMsg
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		a.fromAgent(m)
	}
	a.exited()
}

func (a *relayAgent) fromAgent(m relayMsg) {
	if m.Method == "" { // an answer to a request the relay forwarded
		a.mu.Lock()
		pc, ok := a.pending[string(m.ID)]
		delete(a.pending, string(m.ID))
		cur := a.conn
		a.mu.Unlock()
		if !ok {
			return
		}
		if pc.waiter != nil {
			pc.waiter <- m
			return
		}
		if pc.conn == nil || pc.conn != cur {
			return // its stream is gone
		}
		if pc.method == "session/new" && len(m.Result) > 0 {
			m.Result = a.rewriteNewSession(m.Result)
		}
		if pc.method == "session/load" && len(m.Error) == 0 {
			a.r.audit(map[string]any{"status": "resumed", "agent": a.id})
		}
		m.ID = pc.id
		_ = pc.conn.send(m)
		return
	}
	// The agent's own notification or request: its session id becomes
	// conductor's.
	m.Params = a.toPaseoParams(m.Params)
	a.mu.Lock()
	cur := a.conn
	a.mu.Unlock()
	if cur == nil {
		if m.isRequest() {
			_ = a.write(rpcErr(m.ID, -32603, "conductor: no paseo client is attached to this agent"))
		}
		return
	}
	_ = cur.send(m)
}

// rewriteNewSession records the agent's own session id and answers paseo
// with conductor's.
func (a *relayAgent) rewriteNewSession(res json.RawMessage) json.RawMessage {
	var p map[string]json.RawMessage
	if json.Unmarshal(res, &p) != nil {
		return res
	}
	var native string
	_ = json.Unmarshal(p["sessionId"], &native)
	if native == "" {
		return res
	}
	a.mu.Lock()
	a.native = native
	a.mu.Unlock()
	p["sessionId"], _ = json.Marshal(relaySessionID(a.id, native))
	b, err := json.Marshal(p)
	if err != nil {
		return res
	}
	return b
}

func (a *relayAgent) toPaseoParams(raw json.RawMessage) json.RawMessage {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil || p == nil {
		return raw
	}
	var sid string
	if json.Unmarshal(p["sessionId"], &sid) != nil || sid == "" {
		return raw
	}
	p["sessionId"], _ = json.Marshal(relaySessionID(a.id, sid))
	b, err := json.Marshal(p)
	if err != nil {
		return raw
	}
	return b
}

// detach marks conn gone; the agent stays up, waiting for a session/load.
func (a *relayAgent) detach(conn *relayConn) {
	a.mu.Lock()
	if a.conn != conn {
		a.mu.Unlock()
		return
	}
	a.conn, a.detached = nil, a.r.clock()
	a.mu.Unlock()
	a.r.audit(map[string]any{"status": "detached", "agent": a.id})
}

// exited is the agent process ending on its own: forget it, and close its
// stream (paseo reports the session's runtime failed).
func (a *relayAgent) exited() {
	a.mu.Lock()
	a.gone = true
	conn := a.conn
	a.conn = nil
	waiters := a.pending
	a.pending = map[string]pendingCall{}
	a.mu.Unlock()
	for _, pc := range waiters {
		if pc.waiter != nil {
			close(pc.waiter)
		}
	}
	r := a.r
	r.mu.Lock()
	if r.agents[a.id] == a {
		delete(r.agents, a.id)
	}
	r.mu.Unlock()
	if conn != nil {
		_ = conn.c.Close()
	}
	_ = a.proc.cleanup()
	r.audit(map[string]any{"status": "exited", "agent": a.id})
}

// stop ends the agent and its jail.
func (a *relayAgent) stop() {
	a.mu.Lock()
	proc, conn := a.proc, a.conn
	a.mu.Unlock()
	if conn != nil {
		_ = conn.c.Close()
	}
	if proc != nil {
		_ = proc.cleanup()
	}
}

// Archive tears down the paseo agent's jail (idempotent).
func (r *PaseoRelay) Archive(agentID, why string) bool {
	r.mu.Lock()
	a := r.agents[agentID]
	delete(r.agents, agentID)
	r.mu.Unlock()
	if a == nil {
		return false
	}
	a.stop()
	r.audit(map[string]any{"status": "archived", "agent": agentID, "reason": why})
	return true
}

// handleArchive serves the plugin's `_conductor/archive` control call:
// {agentId} (the agent.archived hook) or {sessionId} (the provider's
// session.archive, with the persisted conductor session id).
func (r *PaseoRelay) handleArchive(conn *relayConn, m relayMsg) {
	var p struct {
		AgentID   string `json:"agentId"`
		SessionID string `json:"sessionId"`
	}
	_ = json.Unmarshal(m.Params, &p)
	id := p.AgentID
	if id == "" {
		id, _, _ = parseRelaySessionID(p.SessionID)
	}
	if id == "" {
		_ = conn.send(rpcErr(m.ID, -32602, "conductor: archive names no agent"))
		return
	}
	stopped := r.Archive(id, "paseo archived the agent")
	_ = conn.send(rpcResult(m.ID, map[string]any{"stopped": stopped}))
}

// reapLoop ends agents that sat detached past Detached.
func (r *PaseoRelay) reapLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.reap()
		}
	}
}

func (r *PaseoRelay) reap() {
	now := r.clock()
	r.mu.Lock()
	var stale []*relayAgent
	for id, a := range r.agents {
		a.mu.Lock()
		if a.conn == nil && !a.detached.IsZero() && now.Sub(a.detached) > r.Detached {
			stale = append(stale, a)
			delete(r.agents, id)
		}
		a.mu.Unlock()
	}
	r.mu.Unlock()
	for _, a := range stale {
		a.stop()
		r.audit(map[string]any{"status": "expired", "agent": a.id, "reason": "detached past paseo_provider.detached_ttl"})
	}
}

// checkPeer refuses a connection from another uid (the socket's directory is
// already owner-only; this is the belt to that brace).
func checkPeer(c net.Conn) string {
	uid, ok := jail.PeerUID(c)
	if !ok {
		return "no peer credentials"
	}
	if uid != uint32(os.Getuid()) {
		return fmt.Sprintf("peer uid %d is not conductor's", uid)
	}
	return ""
}
