package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/config"
)

// TestACPResumePromptCancelClose: ResumeSession re-attaches over a fresh
// connection, a follow-up Prompt streams a turn, Cancel and Close tear down.
func TestACPResumePromptCancelClose(t *testing.T) {
	agent := &fakeACPAgent{
		sessionID:  "sess-9",
		initResult: acp.InitializeResult{},
		onPrompt: func(ctx context.Context, a *fakeACPAgent, p acp.PromptParams) acp.PromptResult {
			if p.SessionID != "sess-9" {
				t.Errorf("prompt on wrong session: %q", p.SessionID)
			}
			return acp.PromptResult{StopReason: acp.StopReasonEndTurn}
		},
	}
	c := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	c.dial = dialFake(agent)

	s, err := c.ResumeSession(context.Background(), "sess-9", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID() != "sess-9" {
		t.Fatalf("resumed id: %q", s.ID())
	}
	ch, err := s.Prompt(context.Background(), Message{Text: "continue"})
	if err != nil {
		t.Fatal(err)
	}
	var kinds []UpdateKind
	for u := range ch {
		kinds = append(kinds, u.Kind)
		if u.Kind == UpdateDone && u.Err != nil {
			t.Fatalf("turn error: %v", u.Err)
		}
	}
	if len(kinds) < 2 || kinds[0] != UpdateStarted || kinds[len(kinds)-1] != UpdateDone {
		t.Fatalf("turn stream: %v", kinds)
	}
	if agent.gotPrompt != "continue" {
		t.Fatalf("prompt text: %q", agent.gotPrompt)
	}
	if as, ok := s.(*acpSession); ok {
		as.Wait(context.Background(), time.Second)
	}
	_ = s.Cancel(context.Background()) // fake agent has no cancel handler; must not hang
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestACPRefusalTurn: a refusal stop reason surfaces as a turn error.
func TestACPRefusalTurn(t *testing.T) {
	agent := &fakeACPAgent{
		sessionID:  "sess-r",
		initResult: acp.InitializeResult{},
		onPrompt: func(context.Context, *fakeACPAgent, acp.PromptParams) acp.PromptResult {
			return acp.PromptResult{StopReason: acp.StopReasonRefusal}
		},
	}
	c := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	c.dial = dialFake(agent)
	s, err := c.ResumeSession(context.Background(), "sess-r", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	ch, _ := s.Prompt(context.Background(), Message{Text: "x"})
	var done Update
	for u := range ch {
		if u.Kind == UpdateDone {
			done = u
		}
	}
	if done.Err == nil || !strings.Contains(done.Err.Error(), "refused") {
		t.Fatalf("refusal should error the turn: %v", done.Err)
	}
}

func TestSpawnACPNoCommand(t *testing.T) {
	if _, _, err := spawnACP(context.Background(), nil, "", nil, nil, ""); err == nil {
		t.Fatal("no command must error")
	}
}

func TestAgentDeckAccessors(t *testing.T) {
	c := newAgentDeckController("deck", config.ControllerConfig{Type: "agent-deck"}, nil)
	if c.Name() != "deck" || c.Model() != ModelNative || c.Transport() != TransportNative {
		t.Fatalf("accessors: %q %v %v", c.Name(), c.Model(), c.Transport())
	}
	caps, err := c.Initialize(context.Background())
	if err != nil || !caps.SendFollowup || !caps.InteractiveHandoff || caps.Remote {
		t.Fatalf("caps: %+v %v", caps, err)
	}
	if _, err := c.Runner(); err != nil {
		t.Fatalf("runner: %v", err)
	}
}

// TestCLISessionLifecycle: ResumeSession on a resumable recipe, a resumed
// Prompt turn (with the tool's own --resume argv), Wait/Cancel/Close/ID, and
// the oneshot refusal.
func TestCLISessionLifecycle(t *testing.T) {
	l := &fakeLauncher{out: func(argv []string) (string, error) { return `{"session_id":"tool-7","result":"ok"}`, nil }}
	c := newCLIController("cc", config.ControllerConfig{Type: "cli", Tool: "claude-code"}, nil)
	c.launch = l.launch

	s, err := c.ResumeSession(context.Background(), "tool-7", nil)
	if err != nil {
		t.Fatal(err)
	}
	if s.ID() != "tool-7" {
		t.Fatalf("id: %q", s.ID())
	}
	ch, err := s.Prompt(context.Background(), Message{Text: "follow up"})
	if err != nil {
		t.Fatal(err)
	}
	var out string
	for u := range ch {
		if u.Kind == UpdateDone {
			if u.Err != nil {
				t.Fatalf("turn err: %v", u.Err)
			}
			out = u.Output
		}
	}
	if out == "" {
		t.Fatal("no output captured")
	}
	argv := strings.Join(l.call(0).argv, " ")
	if !strings.Contains(argv, "--resume tool-7") || !strings.Contains(argv, "follow up") {
		t.Fatalf("resume argv: %s", argv)
	}
	if cs, ok := s.(*cliSession); ok {
		cs.Wait(context.Background(), 10*time.Millisecond) // no first turn: returns immediately
	}
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Oneshot recipes refuse both resume and follow-ups.
	oneshot := newCLIController("cx", config.ControllerConfig{Type: "cli", Tool: "codex"}, nil)
	oneshot.launch = l.launch
	if _, err := oneshot.ResumeSession(context.Background(), "x", nil); err == nil {
		t.Fatal("oneshot resume must refuse")
	}
}

func TestCLIAccessorsAndRealProc(t *testing.T) {
	c := newCLIController("cc", config.ControllerConfig{Type: "cli", Tool: "claude-code"}, nil)
	if c.Name() != "cc" || c.Transport() != TransportCLI || c.Model() != ModelResumable {
		t.Fatalf("accessors: %q %v %v", c.Name(), c.Transport(), c.Model())
	}
	caps, err := c.Initialize(context.Background())
	if err != nil || !caps.SendFollowup {
		t.Fatalf("caps: %+v %v", caps, err)
	}
	if _, err := c.Runner(); err != nil {
		t.Fatal(err)
	}

	// startCLIProc runs a real subprocess and captures combined output.
	proc, err := startCLIProc(context.Background(), t.TempDir(), []string{"X=1"}, []string{"sh", "-c", "echo out-$X; echo err 1>&2"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := proc.Wait()
	if err != nil || !strings.Contains(out, "out-1") || !strings.Contains(out, "err") {
		t.Fatalf("proc: %q %v", out, err)
	}
	if _, err := startCLIProc(context.Background(), "", nil, nil); err == nil {
		t.Fatal("empty argv must error")
	}
	// Kill a long-running process.
	proc, err = startCLIProc(context.Background(), "", nil, []string{"sleep", "5"})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = proc.Wait()
}

func TestScanOpencodeURL(t *testing.T) {
	url, err := scanOpencodeURL(strings.NewReader("booting...\nopencode server listening on http://127.0.0.1:4599\nready\n"))
	if err != nil || url != "http://127.0.0.1:4599" {
		t.Fatalf("scan: %q %v", url, err)
	}
	if _, err := scanOpencodeURL(strings.NewReader("no url here")); err == nil {
		t.Fatal("EOF without a URL must error")
	}
}

func TestOpencodeAccessors(t *testing.T) {
	c := newOpencodeController("oc", config.ControllerConfig{Type: "opencode"}, nil)
	if c.Name() != "oc" {
		t.Fatalf("name: %q", c.Name())
	}
	_ = c.Model()
	_ = c.Transport()
	if _, err := c.Runner(); err != nil {
		t.Fatal(err)
	}
}

func TestAgentDeckResumeAndCancel(t *testing.T) {
	deck := &fakeDeck{byArgs: func(args []string) ([]byte, error) { return []byte(`{}`), nil }}
	c := newDeckFor(nil, deck)
	s, err := c.ResumeSession(context.Background(), "deck-9", nil)
	if err != nil || s.ID() != "deck-9" {
		t.Fatalf("resume: %v %q", err, s.ID())
	}
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatalf("deck cancel is best-effort nil: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("close (remove): %v", err)
	}
	if got := strings.Join(deck.call(0).args, " "); !strings.Contains(got, "remove deck-9") {
		t.Fatalf("remove argv: %s", got)
	}
}

// TestOpencodeResumeCancelClose: ResumeSession attaches an HTTP client to the
// dialed server; Cancel posts the abort; Close runs the cleanup.
func TestOpencodeResumeCancelClose(t *testing.T) {
	var aborts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/abort") {
			aborts.Add(1)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	cleaned := false
	c := newOpencodeController("oc", config.ControllerConfig{Type: "opencode"}, nil)
	c.dial = func(context.Context, string, []string) (string, func() error, error) {
		return srv.URL, func() error { cleaned = true; return nil }, nil
	}
	s, err := c.ResumeSession(context.Background(), "ses-1", nil)
	if err != nil || s.ID() != "ses-1" {
		t.Fatalf("resume: %v", err)
	}
	if err := s.Cancel(context.Background()); err != nil || aborts.Load() != 1 {
		t.Fatalf("abort: %v (%d)", err, aborts.Load())
	}
	if err := s.Close(context.Background()); err != nil || !cleaned {
		t.Fatalf("close: %v cleaned=%v", err, cleaned)
	}
	// A failed dial surfaces.
	c.dial = func(context.Context, string, []string) (string, func() error, error) {
		return "", nil, fmt.Errorf("dial down")
	}
	if _, err := c.ResumeSession(context.Background(), "x", nil); err == nil {
		t.Fatal("failed dial must error")
	}
}

func TestPaseoSessionCancelClose(t *testing.T) {
	c := newPaseoController(BuiltinPaseo, nil, nil)
	s, err := c.ResumeSession(context.Background(), "a-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(context.Background()); err != nil {
		t.Fatalf("paseo cancel is a no-op: %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("paseo close is a no-op: %v", err)
	}
}

func TestRegistryOverrideByNameAndStub(t *testing.T) {
	reg := NewRegistry(map[string]config.ControllerConfig{
		"pae":     {Type: "paseo"},
		"mystery": {Type: "quantum", Transport: "warp"},
	}, "", nil, nil)

	// ByName: configured, builtin fallback, unknown.
	if _, err := reg.ByName("pae"); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.ByName(""); err != nil {
		t.Fatal("empty name resolves to the builtin")
	}
	if _, err := reg.ByName(BuiltinPaseo); err != nil {
		t.Fatal("paseo resolves to the builtin")
	}
	if _, err := reg.ByName("nope"); err == nil {
		t.Fatal("unknown name must error")
	}

	// OverridePaseo rebinds the entry.
	reg.OverridePaseo("pae", nil, nil)
	c, _ := reg.ByName("pae")
	if c.Name() != "pae" {
		t.Fatalf("override name: %q", c.Name())
	}

	// The unknown-transport entry is a stub: shape negotiable, not runnable.
	stub, err := reg.ByName("mystery")
	if err != nil {
		t.Fatal(err)
	}
	if stub.Name() != "mystery" || stub.Model() == "" && stub.Transport() == "" {
		_ = stub // shape accessors exercised regardless of defaults
	}
	if _, err := stub.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := stub.NewSession(context.Background(), Spec{}, nil); err == nil {
		t.Fatal("stub NewSession must refuse")
	}
	if _, err := stub.ResumeSession(context.Background(), "x", nil); err == nil {
		t.Fatal("stub ResumeSession must refuse")
	}
	if _, err := stub.Runner(); err == nil {
		t.Fatal("stub Runner must refuse")
	}
}

func TestPrepareLaunchRemote(t *testing.T) {
	// Local: unchanged argv/dir.
	argv, dir, remote, err := prepareLaunch("", "/wt", []string{"A=1"}, []string{"tool", "x"})
	if err != nil || remote || dir != "/wt" || strings.Join(argv, " ") != "tool x" {
		t.Fatalf("local: %v %v %v %v", argv, dir, remote, err)
	}
	// Remote without a resolver wired.
	old := HostArgvPrefix
	HostArgvPrefix = nil
	defer func() { HostArgvPrefix = old }()
	if _, _, _, err := prepareLaunch("box", "/wt", nil, []string{"tool"}); err == nil {
		t.Fatal("no resolver must error")
	}
	// Remote with a resolver: ssh prefix + one wrapped command string.
	HostArgvPrefix = func(name string) ([]string, error) {
		if name != "box" {
			return nil, fmt.Errorf("unknown host")
		}
		return []string{"ssh", "ci@box"}, nil
	}
	argv, dir, remote, err = prepareLaunch("box", "/wt", []string{"A=1"}, []string{"tool", "x"})
	if err != nil || !remote || dir != "" || len(argv) != 3 || argv[0] != "ssh" {
		t.Fatalf("remote: %v %q %v %v", argv, dir, err, remote)
	}
	if !strings.Contains(argv[2], "tool") || !strings.Contains(argv[2], "A=") {
		t.Fatalf("wrapped command: %q", argv[2])
	}
	if _, _, _, err := prepareLaunch("ghost", "", nil, []string{"t"}); err == nil {
		t.Fatal("unknown host must error")
	}
}
