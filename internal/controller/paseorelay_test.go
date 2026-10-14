package controller

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// relayFakeAgent is an in-process ACP agent on a pipe pair: it answers
// initialize, session/new|load|prompt|close, sends a session/update
// notification per prompt, and records every message it got.
type relayFakeAgent struct {
	mu       sync.Mutex
	got      []relayMsg
	sessions int
	in       *io.PipeReader // what the relay writes
	out      *io.PipeWriter // what the relay reads
	exited   chan struct{}
	stopped  bool
	// requestWhenPrompted makes a prompt send the client a request first.
	requestWhenPrompted bool
}

func (f *relayFakeAgent) received(method string) []relayMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []relayMsg
	for _, m := range f.got {
		if m.Method == method {
			out = append(out, m)
		}
	}
	return out
}

func (f *relayFakeAgent) send(m any) {
	b, _ := json.Marshal(m)
	_, _ = f.out.Write(append(b, '\n'))
}

func (f *relayFakeAgent) run() {
	rd := bufio.NewReader(f.in)
	for {
		line, err := readLine(rd)
		if err != nil {
			return
		}
		var m relayMsg
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		f.mu.Lock()
		f.got = append(f.got, m)
		f.mu.Unlock()
		if !m.isRequest() {
			continue
		}
		reply := func(v any) { f.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": v}) }
		switch m.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{
				"loadSession": true, "sessionCapabilities": map[string]any{"list": map[string]any{}}}})
		case "session/new":
			f.mu.Lock()
			f.sessions++
			n := f.sessions
			f.mu.Unlock()
			reply(map[string]any{"sessionId": fmt.Sprintf("nat-%d", n), "modes": map[string]any{"currentModeId": "default"}})
		case "session/load", "session/close":
			reply(map[string]any{})
		case "session/prompt":
			sid := jsonString(m.Params, "sessionId")
			if f.requestWhenPrompted {
				f.send(map[string]any{"jsonrpc": "2.0", "id": 900, "method": "session/request_permission",
					"params": map[string]any{"sessionId": sid}})
			}
			f.send(map[string]any{"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{"sessionId": sid, "update": map[string]any{"sessionUpdate": "agent_message_chunk"}}})
			reply(map[string]any{"stopReason": "end_turn"})
		default:
			f.send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "nope"}})
		}
	}
}

// relayRig is a relay on a real unix socket with fake agents.
type relayRig struct {
	t      *testing.T
	r      *PaseoRelay
	sock   string
	mu     sync.Mutex
	agents map[string][]*relayFakeAgent // by paseo agent id ("probe" too)
	audits []map[string]any
	now    time.Time
}

func newRelayRig(t *testing.T) *relayRig {
	t.Helper()
	rig := &relayRig{t: t, agents: map[string][]*relayFakeAgent{}, now: time.Unix(1_700_000_000, 0)}
	dir, err := tempShortDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeAll(dir) })
	rig.sock = filepath.Join(dir, "p.sock")
	rig.r = &PaseoRelay{Detached: time.Hour, ProbeDir: dir,
		Audit: func(row map[string]any) { rig.mu.Lock(); rig.audits = append(rig.audits, row); rig.mu.Unlock() }}
	rig.r.now = func() time.Time { rig.mu.Lock(); defer rig.mu.Unlock(); return rig.now }
	rig.r.launch = func(agentID, cwd string) (*acpProcess, error) {
		toAgentR, toAgentW := io.Pipe()
		fromAgentR, fromAgentW := io.Pipe()
		f := &relayFakeAgent{in: toAgentR, out: fromAgentW, exited: make(chan struct{})}
		rig.mu.Lock()
		rig.agents[agentID] = append(rig.agents[agentID], f)
		rig.mu.Unlock()
		go f.run()
		var once sync.Once
		return &acpProcess{stdin: toAgentW, stdout: fromAgentR, exited: f.exited, cleanup: func() error {
			once.Do(func() {
				f.mu.Lock()
				f.stopped = true
				f.mu.Unlock()
				_ = toAgentW.Close()
				_ = fromAgentW.Close()
				close(f.exited)
			})
			return nil
		}}, nil
	}
	l, err := net.Listen("unix", rig.sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = rig.r.Serve(ctx, l) }()
	return rig
}

func (rig *relayRig) launched(agentID string) []*relayFakeAgent {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	return append([]*relayFakeAgent(nil), rig.agents[agentID]...)
}

func (rig *relayRig) audited(status string) []map[string]any {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	var out []map[string]any
	for _, a := range rig.audits {
		if a["status"] == status {
			out = append(out, a)
		}
	}
	return out
}

// paseoSide is one paseo stream to the relay.
type paseoSide struct {
	t   *testing.T
	c   net.Conn
	rd  *bufio.Reader
	id  int
	got []relayMsg // notifications and requests seen while waiting
}

func (rig *relayRig) dial() *paseoSide {
	rig.t.Helper()
	c, err := net.Dial("unix", rig.sock)
	if err != nil {
		rig.t.Fatal(err)
	}
	rig.t.Cleanup(func() { c.Close() })
	return &paseoSide{t: rig.t, c: c, rd: bufio.NewReader(c)}
}

func (p *paseoSide) write(m any) {
	p.t.Helper()
	b, _ := json.Marshal(m)
	if _, err := p.c.Write(append(b, '\n')); err != nil {
		p.t.Fatal(err)
	}
}

// call sends a request and returns its answer (collecting what else comes).
func (p *paseoSide) call(method string, params any) relayMsg {
	p.t.Helper()
	p.id++
	id := p.id
	p.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	_ = p.c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		line, err := readLine(p.rd)
		if err != nil {
			p.t.Fatalf("%s: no answer: %v", method, err)
		}
		var m relayMsg
		if err := json.Unmarshal(line, &m); err != nil {
			p.t.Fatal(err)
		}
		if m.Method == "" && string(m.ID) == fmt.Sprint(id) {
			return m
		}
		p.got = append(p.got, m)
	}
}

func identity(agentID, cwd string, mcp []any) map[string]any {
	if mcp == nil {
		mcp = []any{}
	}
	return map[string]any{"cwd": cwd, "mcpServers": mcp, "_meta": map[string]any{"_paseo": map[string]any{
		"systemPrompt":    "sp",
		"providerOptions": map[string]any{"member": "m", "_conductor": map[string]any{"agentId": agentID, "paseoSession": "ps-1"}},
	}}}
}

func resultField(t *testing.T, m relayMsg, key string) string {
	t.Helper()
	if len(m.Error) > 0 {
		t.Fatalf("error answer: %s", m.Error)
	}
	return jsonString(m.Result, key)
}

const paseoMCP = `{"type":"http","name":"paseo","url":"http://127.0.0.1:6767/mcp/agents?callerAgentId=a1","headers":[{"name":"Authorization","value":"Bearer secret-paseo-token"}]}`

// paseo's capability probe (initialize only) and model listing (session/new
// with no agent behind it) are answered from the one probe launch — and the
// probe hides session listing, which the relay does not serve.
func TestPaseoRelayProbeAndCatalogLaunchNothing(t *testing.T) {
	rig := newRelayRig(t)
	p := rig.dial()
	init := p.call("initialize", map[string]any{"protocolVersion": 1})
	if !strings.Contains(string(init.Result), `"loadSession":true`) || strings.Contains(string(init.Result), `"list"`) {
		t.Fatalf("initialize from the probe, without session listing: %s", init.Result)
	}
	c := rig.dial()
	c.call("initialize", map[string]any{"protocolVersion": 1})
	cat := c.call("session/new", map[string]any{"cwd": "/somewhere", "mcpServers": []any{}})
	if resultField(t, cat, "sessionId") != "cj-catalog" || !strings.Contains(string(cat.Result), `"currentModeId":"default"`) {
		t.Fatalf("the catalog is the probe's session/new: %s", cat.Result)
	}
	if got := c.call("session/close", map[string]any{"sessionId": "cj-catalog"}); len(got.Error) > 0 {
		t.Fatalf("closing the catalog session: %s", got.Error)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if len(rig.agents) != 1 || len(rig.agents["probe"]) != 1 {
		t.Fatalf("only the one probe launch, got %v", rig.agents)
	}
}

// A session: conductor launches the agent for the paseo agent, answers with
// its own session id (encoding the agent), translates it both ways, keeps
// its note and paseo's MCP server away from the agent, and passes the rest
// through.
func TestPaseoRelaySessionTranslatesAndStrips(t *testing.T) {
	rig := newRelayRig(t)
	p := rig.dial()
	p.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}})
	other := `{"type":"stdio","name":"docs","command":"docs-mcp","args":[],"env":[]}`
	var mcp []any
	_ = json.Unmarshal([]byte("["+paseoMCP+","+other+"]"), &mcp)
	res := p.call("session/new", identity("agent-1", "/work/a", mcp))
	sid := resultField(t, res, "sessionId")
	owner, native, ok := parseRelaySessionID(sid)
	if !ok || owner != "agent-1" || native != "nat-1" {
		t.Fatalf("conductor's session id encodes the agent: %q", sid)
	}
	agents := rig.launched("agent-1")
	if len(agents) != 1 {
		t.Fatalf("one launch for agent-1, got %d", len(agents))
	}
	f := agents[0]
	if len(f.received("initialize")) != 1 {
		t.Fatal("the agent gets paseo's initialize")
	}
	nw := f.received("session/new")[0]
	if strings.Contains(string(nw.Params), "_conductor") || strings.Contains(string(nw.Params), "secret-paseo-token") ||
		strings.Contains(string(nw.Params), "/mcp/agents") {
		t.Fatalf("the agent must not see conductor's note or paseo's MCP server: %s", nw.Params)
	}
	if !strings.Contains(string(nw.Params), `"docs-mcp"`) || !strings.Contains(string(nw.Params), `"member":"m"`) || !strings.Contains(string(nw.Params), `"systemPrompt":"sp"`) {
		t.Fatalf("everything else passes through: %s", nw.Params)
	}
	if len(rig.audited("mcp_stripped")) != 1 {
		t.Fatal("the strip is audited")
	}
	pr := p.call("session/prompt", map[string]any{"sessionId": sid, "prompt": []any{}})
	if resultField(t, pr, "stopReason") != "end_turn" {
		t.Fatalf("prompt answer: %s", pr.Result)
	}
	if got := jsonString(f.received("session/prompt")[0].Params, "sessionId"); got != "nat-1" {
		t.Fatalf("the agent sees its own session id, got %q", got)
	}
	if len(p.got) != 1 || jsonString(p.got[0].Params, "sessionId") != sid {
		t.Fatalf("the agent's update reaches paseo under conductor's session id: %+v", p.got)
	}
}

// paseo_tools: true keeps paseo's MCP server (the documented footgun).
func TestPaseoRelayPaseoToolsKeepsTheServer(t *testing.T) {
	rig := newRelayRig(t)
	rig.r.PaseoTools = true
	p := rig.dial()
	p.call("initialize", map[string]any{})
	var mcp []any
	_ = json.Unmarshal([]byte("["+paseoMCP+"]"), &mcp)
	p.call("session/new", identity("agent-1", "/work/a", mcp))
	if nw := rig.launched("agent-1")[0].received("session/new")[0]; !strings.Contains(string(nw.Params), "/mcp/agents") {
		t.Fatalf("paseo_tools keeps the server: %s", nw.Params)
	}
}

// A paseo restart drops the stream; the jailed agent stays up, and the next
// stream's session/load re-attaches to the same process, under fresh request
// ids — a late answer meant for the dead stream never reaches the new one.
func TestPaseoRelayReattachesOnLoad(t *testing.T) {
	rig := newRelayRig(t)
	p := rig.dial()
	p.call("initialize", map[string]any{})
	sid := resultField(t, p.call("session/new", identity("agent-1", "/work/a", nil)), "sessionId")
	p.c.Close()
	relayWait(t, func() bool { return len(rig.audited("detached")) == 1 })

	q := rig.dial()
	q.call("initialize", map[string]any{})
	load := q.call("session/load", map[string]any{"sessionId": sid, "cwd": "/work/a", "mcpServers": []any{},
		"_meta": identity("agent-1", "/work/a", nil)["_meta"]})
	if len(load.Error) > 0 {
		t.Fatalf("session/load re-attaches: %s", load.Error)
	}
	agents := rig.launched("agent-1")
	if len(agents) != 1 {
		t.Fatalf("re-attach reuses the live agent, got %d launches", len(agents))
	}
	f := agents[0]
	if len(f.received("initialize")) != 1 {
		t.Fatal("the live agent is not initialized twice")
	}
	if got := jsonString(f.received("session/load")[0].Params, "sessionId"); got != "nat-1" {
		t.Fatalf("the agent loads its own session id, got %q", got)
	}
	ids := map[string]bool{}
	for _, m := range f.messages() {
		if m.isRequest() {
			if ids[string(m.ID)] {
				t.Fatalf("request id %s reused across streams", m.ID)
			}
			ids[string(m.ID)] = true
		}
	}
	if len(rig.audited("resumed")) != 1 {
		t.Fatal("the resume is audited")
	}
}

// A session/load naming another agent's session, or with no identity, is
// refused — a stream cannot attach to someone else's jail.
func TestPaseoRelayLoadMustBeTheAgentsOwn(t *testing.T) {
	rig := newRelayRig(t)
	p := rig.dial()
	p.call("initialize", map[string]any{})
	sid := resultField(t, p.call("session/new", identity("agent-1", "/work/a", nil)), "sessionId")
	q := rig.dial()
	q.call("initialize", map[string]any{})
	params := identity("agent-2", "/work/a", nil)
	params["sessionId"] = sid
	if got := q.call("session/load", params); len(got.Error) == 0 {
		t.Fatal("agent-2 must not load agent-1's session")
	}
	r := rig.dial()
	r.call("initialize", map[string]any{})
	if got := r.call("session/load", map[string]any{"sessionId": sid, "cwd": "/work/a"}); len(got.Error) == 0 {
		t.Fatal("a load with no identity is refused")
	}
}

// Archive — by the persisted session id (the provider's session.archive) or
// by the agent id (the agent.archived hook) — ends the jailed agent; nothing
// else does, short of detached_ttl.
func TestPaseoRelayArchiveAndTTL(t *testing.T) {
	rig := newRelayRig(t)
	for _, by := range []string{"session", "agent"} {
		t.Run(by, func(t *testing.T) {
			p := rig.dial()
			p.call("initialize", map[string]any{})
			id := "agent-" + by
			sid := resultField(t, p.call("session/new", identity(id, "/work/a", nil)), "sessionId")
			p.c.Close()
			relayWait(t, func() bool { return rig.live() > 0 })
			c := rig.dial()
			params := map[string]any{"agentId": id}
			if by == "session" {
				params = map[string]any{"sessionId": sid}
			}
			if got := c.call("_conductor/archive", params); jsonString(got.Result, "stopped") != "" || !strings.Contains(string(got.Result), "true") {
				t.Fatalf("archive stops the agent: %s", got.Result)
			}
			if !rig.launched(id)[0].isStopped() {
				t.Fatal("the archived agent's process is ended")
			}
		})
	}
	p := rig.dial()
	p.call("initialize", map[string]any{})
	p.call("session/new", identity("agent-ttl", "/work/a", nil))
	p.c.Close()
	relayWait(t, func() bool {
		for _, a := range rig.audited("detached") {
			if a["agent"] == "agent-ttl" {
				return true
			}
		}
		return false
	})
	rig.r.reap()
	if rig.launched("agent-ttl")[0].isStopped() {
		t.Fatal("a freshly detached agent is kept")
	}
	rig.mu.Lock()
	rig.now = rig.now.Add(2 * time.Hour)
	rig.mu.Unlock()
	rig.r.reap()
	if !rig.launched("agent-ttl")[0].isStopped() || len(rig.audited("expired")) != 1 {
		t.Fatal("an agent detached past detached_ttl is ended, and audited")
	}
}

// While no stream is attached, the agent's own requests (a permission
// prompt) are answered with an error rather than left hanging.
func TestPaseoRelayDetachedAgentRequestIsRefused(t *testing.T) {
	rig := newRelayRig(t)
	p := rig.dial()
	p.call("initialize", map[string]any{})
	p.call("session/new", identity("agent-1", "/work/a", nil))
	f := rig.launched("agent-1")[0]
	p.c.Close()
	relayWait(t, func() bool { return len(rig.audited("detached")) == 1 })
	f.send(map[string]any{"jsonrpc": "2.0", "id": 77, "method": "session/request_permission", "params": map[string]any{"sessionId": "nat-1"}})
	relayWait(t, func() bool {
		for _, m := range f.messages() {
			if string(m.ID) == "77" && len(m.Error) > 0 {
				return true
			}
		}
		return false
	})
}

func TestRelaySessionIDRoundTrip(t *testing.T) {
	for _, id := range []string{"a1", "6f1c-…-uuid", "with.dots.in.it"} {
		sid := relaySessionID(id, "native.with.dots")
		if a, n, ok := parseRelaySessionID(sid); !ok || a != id || n != "native.with.dots" {
			t.Fatalf("%q → %q → %q %q %v", id, sid, a, n, ok)
		}
	}
	for _, bad := range []string{"", "nat-1", "cj1.", "cj1.!!.x", "cj1.YQ"} {
		if _, _, ok := parseRelaySessionID(bad); ok {
			t.Fatalf("%q must not parse", bad)
		}
	}
}

// Only conductor's own uid may use the socket.
func TestPaseoRelayPeerIsConductorsUID(t *testing.T) {
	dir, err := tempShortDir()
	if err != nil {
		t.Fatal(err)
	}
	defer removeAll(dir)
	l, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := net.Dial("unix", filepath.Join(dir, "s"))
		if err == nil {
			defer c.Close()
			time.Sleep(200 * time.Millisecond)
		}
	}()
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if why := checkPeer(c); why != "" {
		t.Fatalf("our own uid is accepted: %s", why)
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if checkPeer(a) == "" {
		t.Fatal("a connection with no peer credentials is refused")
	}
}

func tempShortDir() (string, error) { return os.MkdirTemp("/tmp", "cjr") } // a unix socket path is capped near 104 bytes
func removeAll(dir string)            { _ = os.RemoveAll(dir) }

func (rig *relayRig) live() int {
	rig.r.mu.Lock()
	defer rig.r.mu.Unlock()
	return len(rig.r.agents)
}

func (f *relayFakeAgent) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

func (f *relayFakeAgent) messages() []relayMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]relayMsg(nil), f.got...)
}

func relayWait(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
