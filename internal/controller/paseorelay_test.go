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
// initialize, session/new|load|prompt|close, sends a session/update per
// prompt (and per load, as a replay), and records every message it got.
type relayFakeAgent struct {
	mu       sync.Mutex
	got      []relayMsg
	sessions int
	in       *io.PipeReader // what the relay writes
	out      *io.PipeWriter // what the relay reads
	stopped  bool
	launch   *relaySession
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

func (f *relayFakeAgent) messages() []relayMsg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]relayMsg(nil), f.got...)
}

func (f *relayFakeAgent) isStopped() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
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
		case "session/load":
			f.send(map[string]any{"jsonrpc": "2.0", "method": "session/update",
				"params": map[string]any{"sessionId": jsonString(m.Params, "sessionId"), "update": map[string]any{"sessionUpdate": "user_message_chunk"}}})
			reply(map[string]any{})
		case "session/close", "session/set_mode":
			reply(map[string]any{})
		case "session/prompt":
			sid := jsonString(m.Params, "sessionId")
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
	dir    string
	cancel context.CancelFunc
	mu     sync.Mutex
	agents []*relayFakeAgent
	audits []map[string]any
	now    time.Time
	starts int
}

func newRelayRig(t *testing.T) *relayRig {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cjr") // a unix socket path is capped near 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	rig := &relayRig{t: t, dir: dir, now: time.Unix(1_700_000_000, 0)}
	rig.start()
	return rig
}

// start (re)starts the relay on the rig's state dir — a second start is a
// conductor restart.
func (rig *relayRig) start() {
	t := rig.t
	var allow []string
	if rig.cancel != nil {
		allow = rig.r.MCPServers
		rig.cancel()
		time.Sleep(50 * time.Millisecond)
	}
	rig.starts++
	rig.sock = filepath.Join(rig.dir, fmt.Sprintf("p%d.sock", rig.starts))
	r := &PaseoRelay{Detached: time.Hour, StateDir: filepath.Join(rig.dir, "state"), MCPServers: allow,
		Audit: func(row map[string]any) { rig.mu.Lock(); rig.audits = append(rig.audits, row); rig.mu.Unlock() }}
	r.now = func() time.Time { rig.mu.Lock(); defer rig.mu.Unlock(); return rig.now }
	r.launch = func(s *relaySession) (*acpProcess, error) {
		toAgentR, toAgentW := io.Pipe()
		fromAgentR, fromAgentW := io.Pipe()
		f := &relayFakeAgent{in: toAgentR, out: fromAgentW, launch: s}
		rig.mu.Lock()
		rig.agents = append(rig.agents, f)
		rig.mu.Unlock()
		go f.run()
		exited := make(chan struct{})
		var once sync.Once
		return &acpProcess{stdin: toAgentW, stdout: fromAgentR, exited: exited, cleanup: func() error {
			once.Do(func() {
				f.mu.Lock()
				f.stopped = true
				f.mu.Unlock()
				_ = toAgentW.Close()
				_ = fromAgentW.Close()
				close(exited)
			})
			return nil
		}}, nil
	}
	l, err := net.Listen("unix", rig.sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rig.cancel = cancel
	t.Cleanup(cancel)
	rig.r = r
	go func() { _ = r.Serve(ctx, l) }()
}

// sessionAgents are the agents launched for sessions (not the probe).
func (rig *relayRig) sessionAgents() []*relayFakeAgent {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	var out []*relayFakeAgent
	for _, f := range rig.agents {
		if f.launch.ID != "probe" {
			out = append(out, f)
		}
	}
	return out
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

func (rig *relayRig) probeInit(t *testing.T) string {
	t.Helper()
	p, err := rig.r.probeResult(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return string(p.init)
}

// paseoSide is one paseo stream to the relay, initialized.
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
	p := &paseoSide{t: rig.t, c: c, rd: bufio.NewReader(c)}
	mustOK(rig.t, p.call("initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}))
	return p
}

// call sends a request and returns its answer (collecting what else comes).
func (p *paseoSide) call(method string, params any) relayMsg {
	p.t.Helper()
	p.id++
	id := p.id
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if _, err := p.c.Write(append(b, '\n')); err != nil {
		p.t.Fatal(err)
	}
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

func mustOK(t *testing.T, m relayMsg) relayMsg {
	t.Helper()
	if len(m.Error) > 0 {
		t.Fatalf("error answer: %s", m.Error)
	}
	return m
}

func newParams(cwd string, mcp ...string) map[string]any {
	servers := []any{}
	for _, s := range mcp {
		var v any
		_ = json.Unmarshal([]byte(s), &v)
		servers = append(servers, v)
	}
	return map[string]any{"cwd": cwd, "mcpServers": servers, "_meta": map[string]any{"anything": "passes through"}}
}

// open is a session opened and used (so its agent is launched).
func (rig *relayRig) open(t *testing.T, cwd string) (*paseoSide, string) {
	t.Helper()
	p := rig.dial()
	sid := jsonString(mustOK(t, p.call("session/new", newParams(cwd))).Result, "sessionId")
	mustOK(t, p.call("session/prompt", map[string]any{"sessionId": sid, "prompt": []any{}}))
	return p, sid
}

func (rig *relayRig) load(t *testing.T, sid, cwd string) (*paseoSide, relayMsg) {
	t.Helper()
	q := rig.dial()
	params := newParams(cwd)
	params["sessionId"] = sid
	return q, q.call("session/load", params)
}

const (
	paseoMCP = `{"type":"http","name":"paseo","url":"http://127.0.0.1:6767/mcp/agents","headers":[{"name":"Authorization","value":"Bearer secret-paseo-token"}]}`
	docsMCP  = `{"name":"docs","command":"docs-mcp","args":[],"env":[]}`
)

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 10s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// paseo's capability probe (initialize only) and model listing (session/new
// then session/close) launch nothing but the one probe, and leave no record.
func TestPaseoRelayProbeAndCatalogLaunchNothing(t *testing.T) {
	rig := newRelayRig(t)
	_ = rig.dial().c.Close()
	c := rig.dial()
	cat := mustOK(t, c.call("session/new", newParams("/work/a")))
	sid := jsonString(cat.Result, "sessionId")
	if !strings.HasPrefix(sid, relaySessionPrefix) || !strings.Contains(string(cat.Result), `"currentModeId":"default"`) {
		t.Fatalf("session/new is answered from the probe, under conductor's id: %s", cat.Result)
	}
	mustOK(t, c.call("session/close", map[string]any{"sessionId": sid}))
	if n := len(rig.sessionAgents()); n != 0 {
		t.Fatalf("a model listing launches nothing, got %d", n)
	}
	rig.r.mu.Lock()
	n := len(rig.r.sessions)
	rig.r.mu.Unlock()
	if n != 0 {
		t.Fatal("a closed, never-used session leaves no record")
	}
	if init := rig.probeInit(t); strings.Contains(init, `"list"`) || !strings.Contains(init, `"loadSession":true`) {
		t.Fatalf("paseo is told the agent's capabilities, minus session listing: %s", init)
	}
}

// The first prompt launches the agent: paseo's initialize, a session/new
// with paseo's params (its MCP servers dropped), then the prompt — the
// session id translated both ways.
func TestPaseoRelayLaunchesLazilyAndTranslates(t *testing.T) {
	rig := newRelayRig(t)
	p := rig.dial()
	sid := jsonString(mustOK(t, p.call("session/new", newParams("/work/a", paseoMCP, docsMCP))).Result, "sessionId")
	if len(rig.sessionAgents()) != 0 {
		t.Fatal("nothing launched before the session is used")
	}
	pr := mustOK(t, p.call("session/prompt", map[string]any{"sessionId": sid, "prompt": []any{}}))
	if jsonString(pr.Result, "stopReason") != "end_turn" {
		t.Fatalf("prompt answer: %s", pr.Result)
	}
	agents := rig.sessionAgents()
	if len(agents) != 1 {
		t.Fatalf("one launch, got %d", len(agents))
	}
	f := agents[0]
	if len(f.received("initialize")) != 1 || len(f.received("session/new")) != 1 {
		t.Fatal("the launch initializes and opens the session")
	}
	nw := string(f.received("session/new")[0].Params)
	if strings.Contains(nw, "secret-paseo-token") || strings.Contains(nw, "docs-mcp") {
		t.Fatalf("every MCP server paseo passes is dropped by default: %s", nw)
	}
	if !strings.Contains(nw, `"anything":"passes through"`) || !strings.Contains(nw, `"/work/a"`) {
		t.Fatalf("the rest passes through: %s", nw)
	}
	if got := rig.audited("mcp_dropped"); len(got) != 1 || fmt.Sprint(got[0]["servers"]) != "[paseo docs]" {
		t.Fatalf("the drop is audited, by name: %v", got)
	}
	if got := jsonString(f.received("session/prompt")[0].Params, "sessionId"); got != "nat-1" {
		t.Fatalf("the agent sees its own session id, got %q", got)
	}
	if len(p.got) != 1 || jsonString(p.got[0].Params, "sessionId") != sid {
		t.Fatalf("the agent's update reaches paseo under conductor's id: %+v", p.got)
	}
}

// paseo_provider.mcp_servers keeps the servers it names; "*" keeps all.
func TestPaseoRelayMCPAllowList(t *testing.T) {
	for _, tc := range []struct {
		allow      []string
		keep, drop []string
	}{
		{[]string{"docs"}, []string{"docs-mcp"}, []string{"secret-paseo-token"}},
		{[]string{"*"}, []string{"docs-mcp", "secret-paseo-token"}, nil},
	} {
		rig := newRelayRig(t)
		rig.r.MCPServers = tc.allow
		p := rig.dial()
		sid := jsonString(mustOK(t, p.call("session/new", newParams("/work/a", paseoMCP, docsMCP))).Result, "sessionId")
		mustOK(t, p.call("session/prompt", map[string]any{"sessionId": sid, "prompt": []any{}}))
		nw := string(rig.sessionAgents()[0].received("session/new")[0].Params)
		for _, k := range tc.keep {
			if !strings.Contains(nw, k) {
				t.Errorf("%v keeps %s: %s", tc.allow, k, nw)
			}
		}
		for _, d := range tc.drop {
			if strings.Contains(nw, d) {
				t.Errorf("%v drops %s: %s", tc.allow, d, nw)
			}
		}
	}
}

// A dropped stream leaves the agent up; session/load on a new stream
// re-attaches to the same process (no second launch, no second initialize)
// under fresh request ids, and the agent replays its own session.
func TestPaseoRelayReattachesALiveAgent(t *testing.T) {
	rig := newRelayRig(t)
	p, sid := rig.open(t, "/work/a")
	_ = p.c.Close()
	waitUntil(t, func() bool { return len(rig.audited("detached")) == 1 })
	q, load := rig.load(t, sid, "/work/a")
	mustOK(t, load)
	agents := rig.sessionAgents()
	if len(agents) != 1 || len(agents[0].received("initialize")) != 1 {
		t.Fatalf("re-attach reuses the live, initialized agent: %d launches", len(agents))
	}
	if got := jsonString(agents[0].received("session/load")[0].Params, "sessionId"); got != "nat-1" {
		t.Fatalf("the agent loads its own session, got %q", got)
	}
	if len(q.got) == 0 || jsonString(q.got[0].Params, "sessionId") != sid {
		t.Fatalf("the replay reaches paseo under conductor's id: %+v", q.got)
	}
	ids := map[string]bool{}
	for _, m := range agents[0].messages() {
		if m.isRequest() {
			if ids[string(m.ID)] {
				t.Fatalf("request id %s reused across streams", m.ID)
			}
			ids[string(m.ID)] = true
		}
	}
}

// session/close only detaches: the agent keeps running for a load.
func TestPaseoRelayCloseOnlyDetaches(t *testing.T) {
	rig := newRelayRig(t)
	p, sid := rig.open(t, "/work/a")
	mustOK(t, p.call("session/close", map[string]any{"sessionId": sid}))
	_ = p.c.Close()
	waitUntil(t, func() bool { return len(rig.audited("detached")) == 1 })
	if rig.sessionAgents()[0].isStopped() {
		t.Fatal("session/close must not end the agent")
	}
	_, load := rig.load(t, sid, "/work/a")
	mustOK(t, load)
	if len(rig.sessionAgents()) != 1 {
		t.Fatal("the closed session's agent is re-attached, not relaunched")
	}
}

// detached_ttl ends a detached agent; its session survives: a later
// session/load relaunches the agent and has it load its own session — as
// does one after conductor itself restarted (the records are kept in state).
func TestPaseoRelayRelaunchesAfterTTLAndRestart(t *testing.T) {
	rig := newRelayRig(t)
	p, sid := rig.open(t, "/work/a")
	_ = p.c.Close()
	waitUntil(t, func() bool { return len(rig.audited("detached")) == 1 })
	rig.r.reap()
	if rig.sessionAgents()[0].isStopped() {
		t.Fatal("a freshly detached agent is kept")
	}
	rig.mu.Lock()
	rig.now = rig.now.Add(2 * time.Hour)
	rig.mu.Unlock()
	rig.r.reap()
	if !rig.sessionAgents()[0].isStopped() || len(rig.audited("expired")) != 1 {
		t.Fatal("an agent detached past detached_ttl is ended, and audited")
	}
	relaunch := func(want int) {
		t.Helper()
		_, load := rig.load(t, sid, "/work/a")
		mustOK(t, load)
		agents := rig.sessionAgents()
		if len(agents) != want {
			t.Fatalf("%d launches, want %d", len(agents), want)
		}
		f := agents[want-1]
		if len(f.received("initialize")) != 1 || len(f.received("session/new")) != 0 {
			t.Fatal("the relaunch initializes and loads — it opens no new session")
		}
		if got := jsonString(f.received("session/load")[0].Params, "sessionId"); got != "nat-1" {
			t.Fatalf("the relaunched agent loads its own session, got %q", got)
		}
	}
	relaunch(2)
	if len(rig.audited("resumed")) != 1 {
		t.Fatal("the resume is audited")
	}
	rig.start() // conductor restarts
	relaunch(3)
}

// ArchivePaseoCwd ends every session opened in that cwd and forgets it — a
// later load is refused — and leaves other cwds alone. Two live sessions in
// one cwd are audited.
func TestPaseoRelayArchiveByCwd(t *testing.T) {
	rig := newRelayRig(t)
	_, a1 := rig.open(t, "/work/a")
	if len(rig.audited("cwd_shared")) != 0 {
		t.Fatal("one session is not shared")
	}
	rig.open(t, "/work/a/") // the same cwd
	if got := rig.audited("cwd_shared"); len(got) != 1 {
		t.Fatalf("two live sessions in one cwd are audited: %v", got)
	}
	_, b := rig.open(t, "/work/b")
	if n := ArchivePaseoCwd("/work/a"); n != 2 {
		t.Fatalf("archived %d, want both sessions of /work/a", n)
	}
	agents := rig.sessionAgents()
	if !agents[0].isStopped() || !agents[1].isStopped() || agents[2].isStopped() {
		t.Fatal("only /work/a's agents are ended")
	}
	if _, load := rig.load(t, a1, "/work/a"); len(load.Error) == 0 {
		t.Fatal("an archived session cannot be loaded")
	}
	_, load := rig.load(t, b, "/work/b")
	mustOK(t, load)
}

// The cwd → identity lookup names the session's identity: its agent's tool
// state and its jail's AgentID. An unknown cwd gets an identity of its own.
func TestPaseoRelayIdentityFromCwd(t *testing.T) {
	SetPaseoIdentity(func(cwd string) (string, bool) {
		if strings.HasPrefix(cwd, "/members/ada/") {
			return "ada", true
		}
		return "", false
	})
	t.Cleanup(func() { SetPaseoIdentity(nil) })
	rig := newRelayRig(t)
	rig.open(t, "/members/ada/s1")
	rig.open(t, "/elsewhere")
	agents := rig.sessionAgents()
	if s := agents[0].launch; s.Identity != "ada" || s.StateKey != "paseo-ada" {
		t.Fatalf("a member's workspace: %+v", s)
	}
	if s := agents[1].launch; s.Identity != s.ID || s.StateKey != "paseo-"+s.ID {
		t.Fatalf("an unknown cwd is its own identity: %+v", s)
	}
}

// While no stream is attached, the agent's own requests (a permission
// prompt) are answered with an error rather than left hanging.
func TestPaseoRelayDetachedAgentRequestIsRefused(t *testing.T) {
	rig := newRelayRig(t)
	p, _ := rig.open(t, "/work/a")
	f := rig.sessionAgents()[0]
	_ = p.c.Close()
	waitUntil(t, func() bool { return len(rig.audited("detached")) == 1 })
	f.send(map[string]any{"jsonrpc": "2.0", "id": 77, "method": "session/request_permission", "params": map[string]any{"sessionId": "nat-1"}})
	waitUntil(t, func() bool {
		for _, m := range f.messages() {
			if string(m.ID) == "77" && len(m.Error) > 0 {
				return true
			}
		}
		return false
	})
}

// Only conductor's own uid may use the socket.
func TestPaseoRelayPeerIsConductorsUID(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "cjr")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	l, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		if c, err := net.Dial("unix", filepath.Join(dir, "s")); err == nil {
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

// A paseo provider session never runs unjailed: where the jail cannot be
// built the launch fails — it does not degrade the way a dispatch's
// synthesized default jail does.
func TestPaseoRelayNeverLaunchesUnjailed(t *testing.T) {
	withTestJail(t, false) // the jail is unavailable on this "box"
	r := &PaseoRelay{Command: []string{"/bin/true"}, Tool: "claude-code"}
	proc, err := r.launchJailed(&relaySession{ID: "cj2.x", Cwd: t.TempDir(), Identity: "i", StateKey: "paseo-i"})
	if err == nil {
		_ = proc.cleanup()
		t.Fatal("a provider session must fail rather than launch unjailed")
	}
	if !strings.Contains(err.Error(), "cannot be built") {
		t.Fatalf("the refusal says why: %v", err)
	}
}
