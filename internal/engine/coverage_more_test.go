package engine

import (
	"context"
	"errors"
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
		Author: dispatch.Author{}})
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
	cfg.Policy = &config.Policy{Concurrency: &config.Concurrency{MaxAgents: &one}}
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
	if got := agentWaitTimeout(config.Step{}); got != time.Hour {
		t.Fatalf("default wait timeout: %v", got)
	}
	if got := agentWaitTimeout(config.Step{WaitTimeout: config.Duration(10 * time.Minute)}); got != 15*time.Minute {
		t.Fatalf("profile wait timeout + grace: %v", got)
	}
}

// A declared remediation carries the event's facts into the plugin's verbs:
// the status read and the remedy get the args the declaration names, typed
// (the run id stays an integer), and the remedy is the plugin's — conductor
// shells out to nothing.
func TestRemediationInvokesTheDeclaredVerbs(t *testing.T) {
	d := &fakeDispatcher{}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	var calls []string
	var rerunOpts map[string]any
	e.invokeVerb = func(_ context.Context, inst, verb string, opts map[string]any) (map[string]any, error) {
		if isMintVerb(verb) {
			return map[string]any{"token": "t"}, nil // the dispatch's declared credentials
		}
		calls = append(calls, inst+"."+verb)
		if verb == "rerun_run" {
			rerunOpts = opts
		}
		return map[string]any{"status": "completed"}, nil
	}
	tr := agentTrigger("failing_checks", "a/w", 2, "h", "s", config.Action{Type: "agent", Agent: "w/fixer", FlakyRerun: config.FlakyRerun{Enabled: true}})
	tr.Instance = "gh"
	tr.Context["repo"] = "a/w" // the event's own facts, as the source publishes them
	tr.Context["run_id"] = int64(991)
	e.process(context.Background(), tr)
	if strings.Join(calls, ",") != "gh.get_run,gh.rerun_run" || len(d.reqs) != 0 {
		t.Fatalf("calls=%v dispatched=%d", calls, len(d.reqs))
	}
	if rerunOpts["repo"] != "a/w" || rerunOpts["run_id"] != int64(991) || rerunOpts["failed_only"] != true {
		t.Fatalf("remedy args = %#v", rerunOpts)
	}
	// An event that declares no remediation dispatches the fixer straight away.
	calls = nil
	other := agentTrigger("merge_conflict", "a/w", 3, "h", "s", config.Action{Type: "agent", Agent: "w/fixer", FlakyRerun: config.FlakyRerun{Enabled: true}})
	other.Context["run_id"] = int64(5)
	e.process(context.Background(), other)
	if len(calls) != 0 || len(d.reqs) != 1 {
		t.Fatalf("undeclared remediation ran: calls=%v dispatched=%d", calls, len(d.reqs))
	}
}

func TestControllerFor(t *testing.T) {
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	if _, err := e.controllerFor(config.Step{}); err != nil {
		t.Fatalf("default controller: %v", err)
	}
	if _, err := e.controllerFor(config.Step{Runtime: "ghost"}); err == nil {
		t.Fatal("unknown controller must error")
	}
	// #54 regression: a `runtime:`-only profile must route through RuntimeName()
	// on the plain dispatch path (not just the affinity path). Before the fix,
	// Runtime:"" resolved to the default and this unknown name was ignored.
	if _, err := e.controllerFor(config.Step{Runtime: "ghost-runtime"}); err == nil {
		t.Fatal("unknown runtime: must error (RuntimeName routing)")
	}
	if _, err := e.runnerFor(config.Step{Runtime: "ghost-runtime"}); err == nil {
		t.Fatal("runnerFor must honor runtime: and error on unknown name")
	}
}

// TestFlowAgentServices: the engine-owned service funcs the flow runner gets —
// agent dispatch routes through runnerFor, command dispatch through the plain
// dispatcher, tokens are minted, background falls back to a needs_input
// notification, archive proxies.
func TestFlowAgentServices(t *testing.T) {
	eng, _, notif, _ := buildFlowEngine(t, gateCfg)
	svcs := eng.flowAgentServices()

	// Credentials: resolved through the engine (the event's connector here
	// declares none).
	if c, err := svcs.Credentials(context.Background(), flowTrigger("d-svc")); err != nil || len(c.Env) != 0 {
		t.Fatalf("a connector declaring no credentials gave some: %+v", c)
	}

	// Command dispatch goes through the engine's dispatcher.
	ref, err := svcs.Dispatch(context.Background(), dispatch.Request{
		Action: config.Action{Type: "command", Command: []string{"true"}},
	})
	if err != nil {
		t.Fatalf("command dispatch: %v (%+v)", err, ref)
	}

	// Agent dispatch resolves the runner for the profile (unknown -> error).
	if _, err := svcs.Dispatch(context.Background(), dispatch.Request{
		Action: config.Action{Type: "agent", Agent: "a", Prompt: "p"},
		Step:   config.Step{Runtime: "ghost"},
	}); err == nil {
		t.Fatal("unknown controller must fail the agent dispatch")
	}

	// Background with no ask channel emits needs_input.
	svcs.Background(context.Background(), flowTrigger("d-bg"), "review", "a",
		config.Step{}, dispatch.RunRef{AgentID: "a-9"}, "", dispatch.HandoffActions{})
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
