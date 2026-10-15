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
