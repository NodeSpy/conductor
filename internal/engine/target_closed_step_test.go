package engine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/store"
)

// perStepErrDispatcher dispatches each step, failing the ones named in errs
// (by step id) with the given error; every other step "succeeds" with no
// output. Unlike stepFake (steps_test.go), it can fail a specific step, which
// is what proves a later step never runs.
type perStepErrDispatcher struct {
	mu   sync.Mutex
	errs map[string]error
	ran  []string
}

func (d *perStepErrDispatcher) Dispatch(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	id := req.Action.ID
	d.mu.Lock()
	d.ran = append(d.ran, id)
	d.mu.Unlock()
	return dispatch.RunRef{Backend: "test", Kind: req.Trigger.Kind}, d.errs[id]
}
func (d *perStepErrDispatcher) WaitForAgent(context.Context, string, time.Duration) {}
func (d *perStepErrDispatcher) HasLiveAgent(context.Context, string, string) bool   { return false }
func (d *perStepErrDispatcher) Archive(context.Context, string) error               { return nil }
func (d *perStepErrDispatcher) AgentForDispatch(string) string                      { return "" }
func (d *perStepErrDispatcher) DispatchInFlight(string) bool                        { return false }
func (d *perStepErrDispatcher) DeliverOutput(string, any) (bool, error)             { return false, nil }

// TestRunStepsStopsOnErrTargetClosed proves a step whose dispatch fails with
// dispatch.ErrTargetClosed (the PR closed mid-flight and stopFixers had the
// runner's StopTarget kill the turn) stops the workflow before its next step,
// and does NOT escalate: stopFixers already audited and notified, so a second
// "step failed" escalate would be a duplicate, misleading alarm for an
// expected outcome — the engine-path counterpart of flow's workflow_stopped.
func TestRunStepsStopsOnErrTargetClosed(t *testing.T) {
	d := &perStepErrDispatcher{errs: map[string]error{
		"first": fmt.Errorf("step first: %w", dispatch.ErrTargetClosed),
	}}
	n := &fakeNotifier{}
	e := New(Options{
		Config: &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
			{ID: "planner"}, {ID: "worker"},
		}}}},
		Store: tempStore(t), Dispatch: d, Notifier: n,
		UserToken: func() (string, error) { return "u", nil },
	})
	e.cfg.Control.Enabled = ptrBool(true)

	act := config.Action{Steps: []config.Action{
		{ID: "first", Type: "agent", Agent: "w/planner", Prompt: "x"},
		{ID: "second", Type: "agent", Agent: "w/worker", Prompt: "y"},
	}}
	e.runSteps(context.Background(), store.WorkflowRun{Outputs: map[string]map[string]any{}}, issueTrigger(), act, "app", "usr", false)

	if got := d.ran; len(got) != 1 || got[0] != "first" {
		t.Fatalf("expected only the cancelled step to run, got %v", got)
	}
	if n.has(notify.EventEscalate) {
		t.Fatalf("a target-closed step must not escalate (already notified at cancellation), got %v", n.events)
	}
}
