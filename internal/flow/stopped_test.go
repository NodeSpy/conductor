package flow

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// A fixer conductor stopped because its PR closed ends the run as stopped —
// not failed, not retried, no failure notification.
func TestStoppedFixerIsNotAFailure(t *testing.T) {
	cfg := loadConfig(t, "connectors: { svc: { use: fake } }")
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	newFakeState(t, "svc")
	calls := 0
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		calls++
		return dispatch.RunRef{AgentID: "a1"}, dispatch.ErrTargetClosed
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: fix
    type: agent
    prompt: p
    retry: { max: 2, backoff: 1ms }
`)
	trig := newTrigger("ping", map[string]any{"msg": "x"})
	trig.Kind = "new_comment"
	runTrigger(rig, trig, spec)

	if calls != 1 {
		t.Fatalf("a stopped fixer was retried (%d dispatches)", calls)
	}
	if failed, e := rig.workflowFailed(); failed {
		t.Fatalf("a stopped fixer must not fail the run: %s", e)
	}
	if len(rig.Store.auditsWithEvent("workflow_stopped")) != 1 {
		t.Fatal("no workflow_stopped audit")
	}
	for _, e := range rig.Notifier.snapshot() {
		if e.Event == "failed" || e.Event == "escalate" {
			t.Fatalf("a stopped fixer notified as a failure: %+v", e)
		}
	}
}
