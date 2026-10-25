package engine

import (
	"context"
	"testing"
)

// StepDone is the merged done signal (step.done + handoff.done). These pin its
// resolution order: token-bound dispatch id → ledger agent; a live hand-off
// gets the full hand-off teardown; an in-flight foreground dispatch defers to
// the step-boundary archive; anything unresolvable is refused.
func TestStepDoneArchivesResolvedAgent(t *testing.T) {
	d := &fakeDispatcher{
		archived:       make(chan string, 1),
		dispatchAgents: map[string]string{"disp-1": "ag-9"},
	}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)

	if err := e.StepDone(context.Background(), "disp-1", "", "task complete"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-d.archived:
		if got != "ag-9" {
			t.Fatalf("archived %q, want the dispatch's own agent ag-9", got)
		}
	default:
		t.Fatal("step.done must archive the resolved agent")
	}
}

func TestStepDoneInFlightDefersToStepBoundary(t *testing.T) {
	d := &fakeDispatcher{
		archived:       make(chan string, 1),
		dispatchAgents: map[string]string{"disp-1": "ag-9"},
		inflightIDs:    map[string]bool{"disp-1": true},
	}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)

	if err := e.StepDone(context.Background(), "disp-1", "", "eager done mid-run"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-d.archived:
		t.Fatalf("an in-flight dispatch must not archive on done (got %q) — the step boundary owns it", got)
	default:
	}
	if !e.TakeEarlyDone("disp-1") {
		t.Fatal("an in-flight done must leave a mark for the step boundary")
	}
}

// A foreground paseo run reports its agent id only when it returns, so an agent
// calling step.done as its last action is always ahead of the dispatch binding.
// That done must still be accepted (and marked for the step boundary), not
// refused as unresolvable.
func TestStepDoneInFlightBeforeBindingIsAccepted(t *testing.T) {
	d := &fakeDispatcher{
		archived:    make(chan string, 1),
		inflightIDs: map[string]bool{"disp-1": true},
	}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)

	if err := e.StepDone(context.Background(), "disp-1", "", "done before the run returned"); err != nil {
		t.Fatalf("an in-flight dispatch's done must be accepted before its agent is bound: %v", err)
	}
	select {
	case got := <-d.archived:
		t.Fatalf("an in-flight dispatch must not archive on done (got %q)", got)
	default:
	}
	if !e.TakeEarlyDone("disp-1") {
		t.Fatal("the done must be marked for the step boundary")
	}
	if e.TakeEarlyDone("disp-1") {
		t.Fatal("the mark must be consumed by the first take")
	}
}

func TestStepDoneUnresolvableIsRefused(t *testing.T) {
	d := &fakeDispatcher{archived: make(chan string, 1)}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)

	if err := e.StepDone(context.Background(), "unknown-dispatch", "", "x"); err == nil {
		t.Fatal("a done call that resolves to no conductor-launched agent must be refused")
	}
	select {
	case got := <-d.archived:
		t.Fatalf("nothing may be archived for an unresolvable done (got %q)", got)
	default:
	}
}

func TestStepDoneRoutesLiveHandoffThroughTeardown(t *testing.T) {
	d := &fakeDispatcher{
		archived:       make(chan string, 1),
		dispatchAgents: map[string]string{"disp-h": "ag-h"},
	}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	e.hold.Add("ag-h")
	_, cancel := context.WithCancel(context.Background())
	e.registerLiveHandoff("ag-h", cancel, "o/r#1", "", "review")

	if err := e.StepDone(context.Background(), "disp-h", "", "review concluded"); err != nil {
		t.Fatal(err)
	}
	if e.hold.Has("ag-h") {
		t.Fatal("done on a live hand-off must release its hold")
	}
	if e.lookupLiveHandoff("ag-h") != nil {
		t.Fatal("done on a live hand-off must deregister it")
	}
	select {
	case got := <-d.archived:
		if got != "ag-h" {
			t.Fatalf("archived %q, want ag-h", got)
		}
	default:
		t.Fatal("done on a live hand-off must archive its agent")
	}
}

// A cli/acp dispatch's step.done resolves on the runner that launched it,
// not the paseo dispatcher: deferred while that runner is still blocked on
// it, archived through that runner after.
func TestStepDoneResolvesOnTheLaunchingRunner(t *testing.T) {
	paseo := &fakeDispatcher{archived: make(chan string, 1)}
	cli := &fakeDispatcher{
		archived:       make(chan string, 1),
		dispatchAgents: map[string]string{"disp-c": "claude-code-1"},
		inflightIDs:    map[string]bool{"disp-c": true},
	}
	e, _ := newEng(t, baseCfg(), paseo, &fakeNotifier{}, nil)
	e.rememberDispatch("disp-c", cli)
	e.rememberDispatcher("claude-code-1", cli)

	if err := e.StepDone(context.Background(), "disp-c", "", "done mid-turn"); err != nil {
		t.Fatalf("an in-flight cli dispatch's done is accepted: %v", err)
	}
	if len(cli.archived)+len(paseo.archived) != 0 {
		t.Fatal("in flight: the step boundary archives, not the done call")
	}
	// The in-flight done is parked for the step boundary.
	if !e.TakeEarlyDone("disp-c") {
		t.Fatal("an in-flight done must leave a mark for the step boundary")
	}
	cli.inflightIDs = nil
	if err := e.StepDone(context.Background(), "disp-c", "", "done"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-cli.archived:
		if got != "claude-code-1" {
			t.Fatalf("archived %q", got)
		}
	default:
		t.Fatal("archived through the cli runner that launched it")
	}
	if len(paseo.archived) != 0 {
		t.Fatal("never through the paseo dispatcher")
	}
	// An unknown dispatch still resolves (or not) on paseo, as before.
	if err := e.StepDone(context.Background(), "nope", "", "x"); err == nil {
		t.Fatal("unresolvable")
	}
}

// paseo binds a dispatch to its agent only when its run returns: a done
// sent mid-run, before that binding, is deferred — not refused.
func TestStepDoneInFlightBeforeTheAgentIsBound(t *testing.T) {
	d := &fakeDispatcher{archived: make(chan string, 1), inflightIDs: map[string]bool{"disp-1": true}}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	if err := e.StepDone(context.Background(), "disp-1", "", "eager"); err != nil {
		t.Fatalf("mid-run done before binding: %v", err)
	}
	if len(d.archived) != 0 {
		t.Fatal("nothing archived mid-run")
	}
}

// The flow runner reads the in-flight done mark through the services the
// engine hands it; without that wiring no step boundary ever sees it.
func TestEarlyDoneReachesTheFlowRunner(t *testing.T) {
	d := &fakeDispatcher{archived: make(chan string, 1), inflightIDs: map[string]bool{"disp-1": true}}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	if err := e.StepDone(context.Background(), "disp-1", "", "done"); err != nil {
		t.Fatal(err)
	}
	take := e.flowAgentServices().TakeDone
	if take == nil || !take("disp-1") {
		t.Fatal("the flow runner's TakeDone must read the engine's in-flight done mark")
	}
}
