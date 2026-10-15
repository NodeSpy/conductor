package controller

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

// The paseo provider relay. paseo's `conductor` provider (internal/
// paseoplugin: public plugin API only) is an ACP provider whose stream is
// this socket. conductor launches each session's agent itself, on the
// paseo_provider runtime, in the workspace jail, and relays ACP both ways.
//
// It speaks only the ACP spec — nothing of paseo's internals, so a paseo
// release needs no change here:
//   - A session is keyed by the session id conductor mints in its
//     session/new answer; paseo stores it and echoes it on session/load.
//     The record (cwd, identity, the agent's own session id) is kept in
//     conductor's state, so a load after the agent was ended — detached
//     past detached_ttl, or across a conductor restart — relaunches it and
//     has it load its own session.
//   - Its identity is the cwd it is opened in: conductor created the
//     member and worker workspaces, so SetPaseoIdentity's lookup names whose
//     they are. An unknown cwd gets an identity of its own (the session's).
//   - The agent is launched lazily, on the first request that needs it (a
//     prompt, a mode or model change): paseo's model listing (session/new
//     then session/close) is indistinguishable from a real session/new over
//     ACP, and is answered from the one capability probe without a launch.
//   - Every MCP server paseo passes is dropped unless named in
//     paseo_provider.mcp_servers (paseo's own server carries its credential
//     and a way to start unjailed agents; ACP cannot tell it apart).
//   - Lifecycle is conductor's: a stream ending or session/close only
//     detaches; ArchivePaseoCwd (conductor archiving the agent it created)
//     ends the jail; detached_ttl ends a jail nothing re-attached.
//
// What crosses is otherwise untouched, except paseo's request ids
// (renumbered, so a dead stream's late answers never reach a new one) and
// the session id (conductor's toward paseo, the agent's own toward it).

// PaseoRelay serves the paseo provider socket.
type PaseoRelay struct {
	Command    []string // the acp runtime's launch argv
	Tool       string   // its tool (the jail's per-tool rules)
	Iso        *config.IsolationConfig
	ScrubEnv   bool
	MCPServers []string // kept by name; "*" = all
	Detached   time.Duration
	// StateDir holds the session records and the capability probe's
	// workspace (<state>/paseo-provider).
	StateDir string
	Logf     func(string, ...any)
	Audit    func(map[string]any)

	// launch starts the agent for a session; tests replace it.
	launch func(s *relaySession) (*acpProcess, error)
	now    func() time.Time

	mu       sync.Mutex
	sessions map[string]*relaySession
	probe    *relayProbe
	probed   chan struct{}
	closed   bool
}

var (
	paseoMu       sync.Mutex
	paseoIdentity func(cwd string) (identity string, ok bool)
	paseoRelay    *PaseoRelay
)

// SetPaseoIdentity installs the lookup that names the identity a paseo
// provider session's cwd belongs to — the member or worker conductor created
// that workspace for. ok=false: not one of conductor's; the session gets an
// identity of its own. The identity keys the agent's tool state
// (<state>/agent-state/paseo-<identity>) and is the jail's AgentID; it is
// read once, when the session is created.
func SetPaseoIdentity(lookup func(cwd string) (identity string, ok bool)) {
	paseoMu.Lock()
	paseoIdentity = lookup
	paseoMu.Unlock()
}

// ArchivePaseoCwd ends the jail of every paseo provider session opened in cwd
// and forgets them (a later session/load of one is refused), returning how
// many. conductor calls it when it archives an agent it created; each live
// agent has a cwd of its own (the relay audits `cwd_shared` otherwise).
func ArchivePaseoCwd(cwd string) int {
	paseoMu.Lock()
	r := paseoRelay
	paseoMu.Unlock()
	if r == nil {
		return 0
	}
	return r.ArchiveCwd(cwd)
}

// JailDigestFile is the sha256 of the file path, as the jail's broker will
// read it for the agent with jail identity `identity` (a paseo session's
// identity, or a cli worker's dispatch id) — through a confined run's
// copy-on-write layer when the file is there. It is jail.Manager.DigestFile
// on the daemon's jail: what the member gate hashes a host-command proposal's
// files with, so the grant it mints pins the bytes the broker checks.
func JailDigestFile(identity, path string) (string, error) {
	if JailManager == nil {
		return "", errors.New("the agent jail is not wired on this daemon")
	}
	return JailManager.DigestFile(identity, path)
}

// relayProbe is the agent's own answers to initialize and to a session/new,
// captured once: what paseo's capability probe and model listing are told,
// and every session/new.
type relayProbe struct {
	init json.RawMessage
	sess json.RawMessage
	err  error
}

// relaySession is one paseo session.
type relaySession struct {
	r *PaseoRelay

	// The record, persisted.
	ID       string `json:"-"`
	Cwd      string `json:"cwd"`
	Identity string `json:"identity"`
	StateKey string `json:"state_key"`
	Native   string `json:"native,omitempty"` // the agent's own session id ("" = never launched)

	mu        sync.Mutex
	conn      *relayConn
	detached  time.Time
	proc      *acpProcess
	launching chan struct{}
	initRaw   json.RawMessage // paseo's initialize params, for a lazy launch
	openRaw   json.RawMessage // the session/new|load params to open with
	nextID    int64
	pending   map[string]pendingCall
	wmu       sync.Mutex
}

type pendingCall struct {
	conn   *relayConn
	id     json.RawMessage // paseo's id
	method string
	waiter chan relayMsg // relay-internal calls wait here
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
	if raw, ok := v.(json.RawMessage); ok {
		return relayMsg{JSONRPC: "2.0", ID: id, Result: raw}
	}
	b, _ := json.Marshal(v)
	return relayMsg{JSONRPC: "2.0", ID: id, Result: b}
}

// relaySessionPrefix marks conductor's session ids.
const relaySessionPrefix = "cj2."

func newRelaySessionID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return relaySessionPrefix + hex.EncodeToString(b[:])
}

// NewPaseoRelay builds the relay for the runtime cc.
func NewPaseoRelay(cc config.ControllerConfig, p *config.PaseoProviderConfig, stateDir string, logf func(string, ...any), audit func(map[string]any)) *PaseoRelay {
	r := &PaseoRelay{
		Command: acpCommand(cc), Tool: firstNonEmpty(cc.Tool, cc.Agent), Iso: cc.Isolation, ScrubEnv: cc.ScrubEnv,
		MCPServers: p.MCPServers, Detached: p.Detached(), StateDir: stateDir, Logf: logf, Audit: audit,
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

// launchJailed starts a session's agent: the runtime's command, always in
// the workspace jail (one that cannot be built fails the session — it never
// degrades to an unconfined agent), as a launch with no dispatch target,
// with the tool state of the session's identity.
func (r *PaseoRelay) launchJailed(s *relaySession) (*acpProcess, error) {
	if JailManager == nil {
		return nil, errors.New("the workspace jail is not available on this box")
	}
	opt := agentLaunchOpts(true, r.Tool, r.Iso, dispatch.Request{DispatchID: "paseo-" + s.ID})
	if opt.jail == nil {
		return nil, errors.New("the runtime's isolation opts out of the workspace jail")
	}
	opt.jailDefault = false
	opt.jail.Label = "paseo " + s.Identity
	opt.jail.AgentID = s.Identity
	opt.jail.StateKey = s.StateKey
	return startACPProcess(r.Command, s.Cwd, nil, "", opt, r.ScrubEnv)
}

// ---- records -------------------------------------------------------------

func (r *PaseoRelay) recordsPath() string { return filepath.Join(r.StateDir, "sessions.json") }

// loadRecords reads the persisted session records (Serve, once).
func (r *PaseoRelay) loadRecords() {
	b, err := os.ReadFile(r.recordsPath())
	if err != nil {
		return
	}
	var recs map[string]*relaySession
	if err := json.Unmarshal(b, &recs); err != nil {
		r.logf("paseo provider: %s: %v — starting without session records", r.recordsPath(), err)
		return
	}
	for id, s := range recs {
		if s == nil || !strings.HasPrefix(id, relaySessionPrefix) {
			continue
		}
		s.ID, s.r, s.pending = id, r, map[string]pendingCall{}
		r.sessions[id] = s
	}
}

// saveRecordsLocked persists every session record. Called with r.mu held
// (lock order: r.mu, then a session's mu — never the other way).
func (r *PaseoRelay) saveRecordsLocked() {
	if r.StateDir == "" {
		return
	}
	type record struct {
		Cwd      string `json:"cwd"`
		Identity string `json:"identity"`
		StateKey string `json:"state_key"`
		Native   string `json:"native,omitempty"`
	}
	recs := map[string]record{}
	for id, s := range r.sessions {
		s.mu.Lock()
		recs[id] = record{Cwd: s.Cwd, Identity: s.Identity, StateKey: s.StateKey, Native: s.Native}
		s.mu.Unlock()
	}
	b, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(r.StateDir, 0o700); err != nil {
		return
	}
	tmp := r.recordsPath() + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, r.recordsPath())
	}
}

func (r *PaseoRelay) save() {
	r.mu.Lock()
	r.saveRecordsLocked()
	r.mu.Unlock()
}

// ---- serving -------------------------------------------------------------

// Serve accepts paseo streams on l until ctx ends. Each connection must come
// from this uid (checkPeer).
func (r *PaseoRelay) Serve(ctx context.Context, l net.Listener) error {
	r.mu.Lock()
	if r.sessions == nil {
		r.sessions = map[string]*relaySession{}
		r.loadRecords()
	}
	if r.probed == nil {
		r.probed = make(chan struct{})
		go r.runProbe()
	}
	r.mu.Unlock()
	paseoMu.Lock()
	paseoRelay = r
	paseoMu.Unlock()
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

// Close ends every live agent (the records stay: a session/load after a
// restart relaunches).
func (r *PaseoRelay) Close() {
	r.mu.Lock()
	r.closed = true
	var live []*relaySession
	for _, s := range r.sessions {
		live = append(live, s)
	}
	r.mu.Unlock()
	paseoMu.Lock()
	if paseoRelay == r {
		paseoRelay = nil
	}
	paseoMu.Unlock()
	for _, s := range live {
		s.stop()
	}
}

// runProbe launches the agent once in the probe workspace and records its
// answers to initialize and session/new.
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
	dir := filepath.Join(r.StateDir, "probe")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		p.err = err
		return
	}
	proc, err := r.launch(&relaySession{ID: "probe", Cwd: dir, Identity: "probe", StateKey: "paseo-probe"})
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
		p.sess, p.err = call(2, "session/new", map[string]any{"cwd": dir, "mcpServers": []any{}})
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

// sessionAnswer is the probe's session/new answer under conductor's id (the
// answer to every session/new, and to a load of a never-launched session).
func (p *relayProbe) sessionAnswer(id string) json.RawMessage {
	var res map[string]json.RawMessage
	_ = json.Unmarshal(p.sess, &res)
	if res == nil {
		res = map[string]json.RawMessage{}
	}
	if id == "" {
		delete(res, "sessionId")
	} else {
		res["sessionId"], _ = json.Marshal(id)
	}
	b, _ := json.Marshal(res)
	return b
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

// serveConn handles one paseo stream (paseo opens one per session, plus a
// capability probe that only initializes).
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
	if m.Method != "initialize" || !m.isRequest() {
		_ = conn.send(rpcErr(m.ID, -32600, "the first message must be initialize"))
		return
	}
	initRaw := m.Params
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	p, err := r.probeResult(ctx)
	cancel()
	if err != nil {
		_ = conn.send(rpcErr(m.ID, -32603, "conductor: "+err.Error()))
		return
	}
	_ = conn.send(rpcResult(m.ID, p.init))

	var s *relaySession
	defer func() {
		if s != nil {
			s.detach(conn)
		}
	}()
	for {
		if m, err = next(); err != nil {
			return
		}
		switch {
		case m.Method == "session/new" && s == nil:
			s = r.newSession(conn, m, initRaw, p)
		case m.Method == "session/load" && s == nil:
			s = r.loadSession(conn, m, initRaw, p)
		case s == nil:
			if m.isRequest() {
				_ = conn.send(rpcErr(m.ID, -32601, "conductor: "+m.Method+" before a session"))
			}
		case m.Method == "session/new" || m.Method == "session/load":
			_ = conn.send(rpcErr(m.ID, -32600, "conductor: one session per stream"))
		default:
			s.fromPaseo(conn, m)
		}
	}
}

// newSession answers session/new at once — conductor's id, the probe's
// modes and options — and launches nothing yet.
func (r *PaseoRelay) newSession(conn *relayConn, m relayMsg, initRaw json.RawMessage, p *relayProbe) *relaySession {
	cwd := jsonString(m.Params, "cwd")
	if cwd == "" || !filepath.IsAbs(cwd) {
		_ = conn.send(rpcErr(m.ID, -32602, fmt.Sprintf("conductor: session cwd %q is not an absolute path", cwd)))
		return nil
	}
	cwd = filepath.Clean(cwd)
	s := &relaySession{r: r, ID: newRelaySessionID(), Cwd: cwd, pending: map[string]pendingCall{}, conn: conn, initRaw: initRaw}
	paseoMu.Lock()
	lookup := paseoIdentity
	paseoMu.Unlock()
	if lookup != nil {
		if id, ok := lookup(cwd); ok && id != "" {
			s.Identity = id
		}
	}
	if s.Identity == "" {
		s.Identity = s.ID
	}
	s.StateKey = "paseo-" + s.Identity
	s.openRaw = r.filterMCP(s, "session/new", m.Params)
	r.mu.Lock()
	r.sessions[s.ID] = s
	r.saveRecordsLocked()
	r.mu.Unlock()
	_ = conn.send(rpcResult(m.ID, p.sessionAnswer(s.ID)))
	return s
}

// loadSession re-attaches paseo to a session: its live agent, else a
// relaunched one that loads its own session.
func (r *PaseoRelay) loadSession(conn *relayConn, m relayMsg, initRaw json.RawMessage, p *relayProbe) *relaySession {
	id := jsonString(m.Params, "sessionId")
	r.mu.Lock()
	s := r.sessions[id]
	r.mu.Unlock()
	if s == nil {
		_ = conn.send(rpcErr(m.ID, -32602, "conductor: no session "+id+" (archived, or not conductor's)"))
		return nil
	}
	s.mu.Lock()
	old := s.conn
	s.conn, s.detached, s.initRaw = conn, time.Time{}, initRaw
	native, live := s.Native, s.proc != nil
	if native == "" {
		// Never launched: it opens with a session/new when it is first used.
		s.openRaw = r.filterMCP(s, "session/new", withoutKey(m.Params, "sessionId"))
	}
	s.mu.Unlock()
	if old != nil && old != conn {
		_ = old.c.Close() // one stream per session: the newest wins
	}
	r.audit(map[string]any{"status": "attached", "session": s.ID, "identity": s.Identity, "live": live})
	if native == "" {
		_ = conn.send(rpcResult(m.ID, p.sessionAnswer("")))
		return s
	}
	if err := s.ensureLaunched(); err != nil {
		_ = conn.send(rpcErr(m.ID, -32603, "conductor: "+err.Error()))
		return s
	}
	// The agent loads its own session (replaying it to paseo); a live one
	// that already has it loaded does the same.
	s.forward(conn, m.ID, "session/load", r.filterMCP(s, "session/load", m.Params))
	r.audit(map[string]any{"status": "resumed", "session": s.ID, "identity": s.Identity, "relaunched": !live})
	return s
}

func withoutKey(raw json.RawMessage, key string) json.RawMessage {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil || p == nil {
		return raw
	}
	delete(p, key)
	b, _ := json.Marshal(p)
	return b
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

// filterMCP drops the MCP servers paseo passes (ACP's mcpServers) that
// paseo_provider.mcp_servers does not name, auditing what went.
func (r *PaseoRelay) filterMCP(s *relaySession, method string, raw json.RawMessage) json.RawMessage {
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil || p == nil {
		return raw
	}
	var servers []map[string]json.RawMessage
	if json.Unmarshal(p["mcpServers"], &servers) != nil {
		return raw
	}
	keep := map[string]bool{}
	for _, n := range r.MCPServers {
		keep[n] = true
	}
	kept := []map[string]json.RawMessage{}
	var dropped []string
	for _, srv := range servers {
		var name string
		_ = json.Unmarshal(srv["name"], &name)
		if keep["*"] || keep[name] {
			kept = append(kept, srv)
			continue
		}
		dropped = append(dropped, name)
	}
	if len(dropped) > 0 {
		r.audit(map[string]any{"status": "mcp_dropped", "session": s.ID, "method": method, "servers": dropped})
	}
	p["mcpServers"], _ = json.Marshal(kept)
	b, err := json.Marshal(p)
	if err != nil {
		return raw
	}
	return b
}

// ---- one session ---------------------------------------------------------

// ensureLaunched starts the session's agent if it is not running: the
// launch, paseo's initialize, and its session opened — a session/new (whose
// answer names the agent's own id) for a never-launched session.
func (s *relaySession) ensureLaunched() error {
	s.mu.Lock()
	for s.launching != nil {
		ch := s.launching
		s.mu.Unlock()
		<-ch
		s.mu.Lock()
	}
	if s.proc != nil {
		s.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	s.launching = ch
	initRaw, openRaw, native := s.initRaw, s.openRaw, s.Native
	s.mu.Unlock()
	err := s.launchNow(initRaw, openRaw, native)
	s.mu.Lock()
	s.launching = nil
	close(ch)
	s.mu.Unlock()
	return err
}

func (s *relaySession) launchNow(initRaw, openRaw json.RawMessage, native string) error {
	r := s.r
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return errors.New("conductor is shutting down")
	}
	proc, err := r.launch(s)
	if err != nil {
		r.audit(map[string]any{"status": "launch_failed", "session": s.ID, "identity": s.Identity, "reason": err.Error()})
		return fmt.Errorf("launch: %w", err)
	}
	s.mu.Lock()
	s.proc = proc
	s.mu.Unlock()
	go s.pump(proc)
	r.audit(map[string]any{"status": "launched", "session": s.ID, "identity": s.Identity, "cwd": s.Cwd})
	r.auditSharedCwd(s)
	if res, err := s.call("initialize", initRaw); err != nil || len(res.Error) > 0 {
		s.stop()
		return fmt.Errorf("initialize: %v%s", err, res.Error)
	}
	if native != "" {
		return nil // loadSession forwards the session/load itself
	}
	res, err := s.call("session/new", openRaw)
	if err == nil && len(res.Error) > 0 {
		err = fmt.Errorf("%s", res.Error)
	}
	got := jsonString(res.Result, "sessionId")
	if err == nil && got == "" {
		err = errors.New("no sessionId in its answer")
	}
	if err != nil {
		s.stop()
		return fmt.Errorf("session/new: %w", err)
	}
	s.mu.Lock()
	s.Native = got
	s.mu.Unlock()
	r.save()
	return nil
}

// auditSharedCwd records two live sessions in one cwd: conductor archives by
// cwd, and each live agent is meant to have its own.
func (r *PaseoRelay) auditSharedCwd(s *relaySession) {
	r.mu.Lock()
	var others []string
	for id, o := range r.sessions {
		if o == s {
			continue
		}
		o.mu.Lock()
		if o.Cwd == s.Cwd && o.proc != nil {
			others = append(others, id)
		}
		o.mu.Unlock()
	}
	r.mu.Unlock()
	if len(others) > 0 {
		r.audit(map[string]any{"status": "cwd_shared", "session": s.ID, "cwd": s.Cwd, "others": others})
		r.logf("paseo provider: session %s shares cwd %s with live session(s) %s — archiving by cwd ends them all", s.ID, s.Cwd, strings.Join(others, ", "))
	}
}

// call sends a relay-internal request to the agent and waits for its answer.
func (s *relaySession) call(method string, params json.RawMessage) (relayMsg, error) {
	w := make(chan relayMsg, 1)
	s.mu.Lock()
	proc := s.proc
	s.nextID++
	rid := json.RawMessage(strconv.FormatInt(s.nextID, 10))
	s.pending[string(rid)] = pendingCall{method: method, waiter: w}
	s.mu.Unlock()
	if proc == nil {
		return relayMsg{}, errors.New("the agent is not running")
	}
	if err := s.write(proc, relayMsg{JSONRPC: "2.0", ID: rid, Method: method, Params: params}); err != nil {
		return relayMsg{}, err
	}
	select {
	case m, ok := <-w:
		if !ok {
			return relayMsg{}, errors.New("the agent exited")
		}
		return m, nil
	case <-time.After(2 * time.Minute):
		return relayMsg{}, errors.New("no answer within 2m")
	}
}

func (s *relaySession) write(proc *acpProcess, m relayMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = proc.stdin.Write(append(b, '\n'))
	return err
}

// forward sends a paseo request (id) to the agent under a fresh id.
func (s *relaySession) forward(conn *relayConn, id json.RawMessage, method string, params json.RawMessage) {
	s.mu.Lock()
	proc := s.proc
	s.nextID++
	rid := json.RawMessage(strconv.FormatInt(s.nextID, 10))
	s.pending[string(rid)] = pendingCall{conn: conn, id: id, method: method}
	s.mu.Unlock()
	if proc == nil || s.write(proc, relayMsg{JSONRPC: "2.0", ID: rid, Method: method, Params: s.toAgent(params)}) != nil {
		_ = conn.send(rpcErr(id, -32603, "conductor: the agent is gone"))
	}
}

// fromPaseo handles one message on an open session.
func (s *relaySession) fromPaseo(conn *relayConn, m relayMsg) {
	s.mu.Lock()
	proc := s.proc
	s.mu.Unlock()
	switch {
	case m.Method == "session/close":
		if proc == nil {
			// Never launched — paseo's model listing, or a session closed
			// before it was used: nothing to keep.
			if s.native() == "" {
				s.r.forget(s)
			}
			if m.isRequest() {
				_ = conn.send(rpcResult(m.ID, map[string]any{}))
			}
			return
		}
		if m.isRequest() {
			s.forward(conn, m.ID, m.Method, m.Params)
		}
	case m.isRequest():
		if proc == nil {
			if err := s.ensureLaunched(); err != nil {
				_ = conn.send(rpcErr(m.ID, -32603, "conductor: "+err.Error()))
				return
			}
		}
		s.forward(conn, m.ID, m.Method, m.Params)
	case m.isNotification():
		if proc != nil {
			_ = s.write(proc, relayMsg{JSONRPC: "2.0", Method: m.Method, Params: s.toAgent(m.Params)})
		}
	default:
		// paseo's answer to the agent's own request (a permission prompt):
		// the agent's id space, passed through.
		if proc != nil {
			_ = s.write(proc, m)
		}
	}
}

// toAgent rewrites params for the agent: conductor's session id to the
// agent's own.
func (s *relaySession) toAgent(raw json.RawMessage) json.RawMessage {
	return swapSessionID(raw, s.ID, s.native())
}

func (s *relaySession) native() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Native
}

// swapSessionID replaces params.sessionId == from with to.
func swapSessionID(raw json.RawMessage, from, to string) json.RawMessage {
	if to == "" || jsonString(raw, "sessionId") != from {
		return raw
	}
	var p map[string]json.RawMessage
	if json.Unmarshal(raw, &p) != nil {
		return raw
	}
	p["sessionId"], _ = json.Marshal(to)
	b, err := json.Marshal(p)
	if err != nil {
		return raw
	}
	return b
}

// pump reads the agent's stdout for its whole life and delivers each
// message to the attached paseo stream.
func (s *relaySession) pump(proc *acpProcess) {
	rd := bufio.NewReaderSize(proc.stdout, 1<<16)
	for {
		line, err := readLine(rd)
		if err != nil {
			break
		}
		var m relayMsg
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		s.fromAgent(proc, m)
	}
	s.exited(proc)
}

func (s *relaySession) fromAgent(proc *acpProcess, m relayMsg) {
	if m.Method == "" { // an answer to a request the relay forwarded
		s.mu.Lock()
		pc, ok := s.pending[string(m.ID)]
		delete(s.pending, string(m.ID))
		cur := s.conn
		s.mu.Unlock()
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
		m.ID = pc.id
		_ = pc.conn.send(m)
		return
	}
	// The agent's own notification or request, under conductor's session id.
	m.Params = swapSessionID(m.Params, s.native(), s.ID)
	s.mu.Lock()
	cur := s.conn
	s.mu.Unlock()
	if cur == nil {
		if m.isRequest() {
			_ = s.write(proc, rpcErr(m.ID, -32603, "conductor: no paseo client is attached to this session"))
		}
		return
	}
	_ = cur.send(m)
}

// detach marks conn gone; the agent stays up, waiting for a session/load.
func (s *relaySession) detach(conn *relayConn) {
	s.mu.Lock()
	if s.conn != conn {
		s.mu.Unlock()
		return
	}
	s.conn, s.detached = nil, s.r.clock()
	live := s.proc != nil
	s.mu.Unlock()
	if live {
		s.r.audit(map[string]any{"status": "detached", "session": s.ID, "identity": s.Identity})
	}
}

// exited is the agent process ending (on its own, or stopped): the session
// keeps its record — a session/load relaunches it — and its stream closes.
func (s *relaySession) exited(proc *acpProcess) {
	s.mu.Lock()
	if s.proc != proc {
		s.mu.Unlock()
		return
	}
	s.proc = nil
	conn := s.conn
	waiters := s.pending
	s.pending = map[string]pendingCall{}
	s.mu.Unlock()
	for _, pc := range waiters {
		if pc.waiter != nil {
			close(pc.waiter)
		}
	}
	if conn != nil {
		_ = conn.c.Close()
	}
	_ = proc.cleanup()
	s.r.audit(map[string]any{"status": "exited", "session": s.ID, "identity": s.Identity})
}

// stop ends the agent and its jail.
func (s *relaySession) stop() {
	s.mu.Lock()
	proc := s.proc
	s.mu.Unlock()
	if proc != nil {
		_ = proc.cleanup()
	}
}

// forget drops a session's record.
func (r *PaseoRelay) forget(s *relaySession) {
	r.mu.Lock()
	if r.sessions[s.ID] == s {
		delete(r.sessions, s.ID)
		r.saveRecordsLocked()
	}
	r.mu.Unlock()
}

// ArchiveCwd ends and forgets every session opened in cwd.
func (r *PaseoRelay) ArchiveCwd(cwd string) int {
	cwd = filepath.Clean(cwd)
	r.mu.Lock()
	var gone []*relaySession
	for id, s := range r.sessions {
		if s.Cwd == cwd {
			gone = append(gone, s)
			delete(r.sessions, id)
		}
	}
	if len(gone) > 0 {
		r.saveRecordsLocked()
	}
	r.mu.Unlock()
	for _, s := range gone {
		s.mu.Lock()
		conn := s.conn
		s.mu.Unlock()
		if conn != nil {
			_ = conn.c.Close()
		}
		s.stop()
		r.audit(map[string]any{"status": "archived", "session": s.ID, "identity": s.Identity, "cwd": cwd})
	}
	return len(gone)
}

// reapLoop ends agents nothing re-attached within Detached.
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
	var stale []*relaySession
	for _, s := range r.sessions {
		s.mu.Lock()
		if s.proc != nil && s.conn == nil && !s.detached.IsZero() && now.Sub(s.detached) > r.Detached {
			stale = append(stale, s)
		}
		s.mu.Unlock()
	}
	r.mu.Unlock()
	for _, s := range stale {
		s.stop()
		r.audit(map[string]any{"status": "expired", "session": s.ID, "identity": s.Identity, "reason": "detached past paseo_provider.detached_ttl"})
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
