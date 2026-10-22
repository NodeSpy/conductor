package engine

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
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

// A controller runtime (cli) runs its dispatch through a runner the paseo
// ledger never sees. Its agent's step.done — sent as the agent's last action,
// while the engine is still blocked in dispatchAgent — must resolve as in
// flight (marked for the step boundary, nothing archived), and a done after
// the dispatch returned must archive through that runner, not paseo.
func TestStepDoneControllerRuntimeDispatch(t *testing.T) {
	paseo := &fakeDispatcher{archived: make(chan string, 1)}
	e, _ := newEng(t, baseCfg(), paseo, &fakeNotifier{}, nil)
	cli := &fakeDispatcher{archived: make(chan string, 1)}
	var doneErr error
	cli.onDispatch = func(r dispatch.Request) (dispatch.RunRef, error) {
		doneErr = e.StepDone(context.Background(), r.DispatchID, "", "finished")
		return dispatch.RunRef{AgentID: "claude-code-1"}, nil
	}

	if _, err := e.dispatchAgent(context.Background(), cli, dispatch.Request{DispatchID: "disp-cli"}); err != nil {
		t.Fatal(err)
	}
	if doneErr != nil {
		t.Fatalf("an in-flight cli dispatch's done must be accepted: %v", doneErr)
	}
	if !e.TakeEarlyDone("disp-cli") {
		t.Fatal("the in-flight done must be marked for the step boundary")
	}
	if e.dispatchInFlight("disp-cli") {
		t.Fatal("the dispatch is no longer in flight once dispatchAgent returned")
	}

	if err := e.StepDone(context.Background(), "disp-cli", "", "late done"); err != nil {
		t.Fatalf("a done after the dispatch returned must resolve to its agent: %v", err)
	}
	select {
	case got := <-cli.archived:
		if got != "claude-code-1" {
			t.Fatalf("archived %q, want the dispatch's agent", got)
		}
	case got := <-paseo.archived:
		t.Fatalf("a cli agent was archived through paseo (%q)", got)
	default:
		t.Fatal("a late done must archive the cli agent through its runner")
	}
}
