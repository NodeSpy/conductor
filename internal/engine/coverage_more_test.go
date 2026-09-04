package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// TestEmitRunLoop: Emit feeds the Run loop; a trigger dispatches through the
// engine end to end; cancel stops Run with ctx.Err().
func TestEmitRunLoop(t *testing.T) {
	g := &gateFake{waitCh: make(chan struct{})}
	close(g.waitCh) // agents complete instantly
	e := New(Options{Config: baseCfg(), Store: tempStore(t), Dispatch: g, Notifier: &fakeNotifier{},
		Author: dispatch.Author{}, UserToken: func() (string, error) { return "u", nil }})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()

	act := config.Action{Type: "agent", Agent: "fixer", Prompt: "fix"}
	e.Emit(ctx, agentTrigger("merge_conflict", "a/w", 1, "h1", "s1", act))
	deadline := time.Now().Add(5 * time.Second)
	for g.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if g.count() != 1 {
		t.Fatal("emitted trigger never processed")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestAcquireCancelled(t *testing.T) {
	cfg := baseCfg()
	one := 1
	cfg.Control.MaxConcurrentAgents = &one
	d := &fakeDispatcher{}
	e, _ := newEng(t, cfg, d, &fakeNotifier{}, nil)
	if !e.acquire(context.Background()) {
		t.Fatal("first slot free")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e.acquire(ctx) {
		t.Fatal("cancelled ctx must not acquire")
	}
	e.release()
}

func TestToInt64AndWaitTimeout(t *testing.T) {
	if toInt64(int64(5)) != 5 || toInt64(6) != 6 || toInt64(7.0) != 7 || toInt64("x") != 0 {
		t.Fatal("toInt64 table")
	}
	if got := agentWaitTimeout(config.AgentProfile{}); got != time.Hour {
		t.Fatalf("default wait timeout: %v", got)
	}
	if got := agentWaitTimeout(config.AgentProfile{WaitTimeout: config.Duration(10 * time.Minute)}); got != 15*time.Minute {
		t.Fatalf("profile wait timeout + grace: %v", got)
	}
}

// TestRerunFailed shells the flaky rerun through a stubbed gh.
func TestRerunFailed(t *testing.T) {
	dir := t.TempDir()
	argvFile := filepath.Join(dir, "argv")
	script := "#!/usr/bin/env bash\necho \"$@\" > " + argvFile + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := &fakeDispatcher{}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	tr := agentTrigger("failing_checks", "a/w", 2, "h", "s", config.Action{})
	e.rerunFailed(context.Background(), tr, 991)
	argv, _ := os.ReadFile(argvFile)
	if !strings.Contains(string(argv), "run rerun 991 --failed --repo a/w") {
		t.Fatalf("gh argv: %s", argv)
	}

	// A failing gh logs, never panics.
	os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/usr/bin/env bash\nexit 1\n"), 0o755)
	e.rerunFailed(context.Background(), tr, 992)
}

func TestControllerFor(t *testing.T) {
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	if _, err := e.controllerFor(config.AgentProfile{}); err != nil {
		t.Fatalf("default controller: %v", err)
	}
	if _, err := e.controllerFor(config.AgentProfile{Controller: "ghost"}); err == nil {
		t.Fatal("unknown controller must error")
	}
}

// TestFlowAgentServices: the engine-owned service funcs the flow runner gets —
// agent dispatch routes through runnerFor, command dispatch through the plain
// dispatcher, tokens are minted, background falls back to a needs_input
// notification, archive proxies.
func TestFlowAgentServices(t *testing.T) {
	eng, _, notif, _ := buildFlowEngine(t, gateCfg)
	svcs := eng.flowAgentServices()

	// Tokens: user token minted via the engine's token funcs (nil here → zero).
	toks := svcs.Tokens(flowTrigger("d-svc"))
	_ = toks

	// Command dispatch goes through the engine's dispatcher.
	ref, err := svcs.Dispatch(context.Background(), dispatch.Request{
		Action: config.Action{Type: "command", Command: []string{"true"}},
	})
	if err != nil {
		t.Fatalf("command dispatch: %v (%+v)", err, ref)
	}

	// Agent dispatch resolves the runner for the profile (unknown -> error).
	if _, err := svcs.Dispatch(context.Background(), dispatch.Request{
		Action:  config.Action{Type: "agent", Agent: "a", Prompt: "p"},
		Profile: config.AgentProfile{Controller: "ghost"},
	}); err == nil {
		t.Fatal("unknown controller must fail the agent dispatch")
	}

	// Background with no ask channel emits needs_input.
	svcs.Background(context.Background(), flowTrigger("d-bg"), "review",
		config.AgentProfile{}, dispatch.RunRef{AgentID: "a-9"}, "")
	notif.mu.Lock()
	found := false
	for _, ev := range notif.events {
		if strings.Contains(ev, "needs_input") {
			found = true
		}
	}
	notif.mu.Unlock()
	if !found {
		t.Fatal("background without a channel should emit needs_input")
	}

	// Archive proxies to the dispatcher without blocking.
	svcs.Archive("a-9")
}

// TestAskChannelFor: an unknown name falls to the legacy registry (nil here),
// yielding the runtime-native fallback.
func TestAskChannelFor(t *testing.T) {
	eng, _, _, _ := buildFlowEngine(t, gateCfg)
	if ch := eng.askChannelFor("nope"); ch != nil {
		t.Fatal("unknown ask channel should be nil (runtime-native)")
	}
	if ch := eng.askChannelFor(""); ch != nil {
		t.Fatal("empty name should be nil without a handoff registry")
	}
}
