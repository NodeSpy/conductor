package flow

import (
	"context"
	"errors"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// CLASS-CLOSER: the `agents:`→flow migration made this package the SOLE
// dispatch path, and it dropped a second engine contract (the first was the
// `dispatch` audit row, see dispatchaudit_test.go): the engine escalated
// (audit `event:escalate` + Notif.Emit("escalate", …)) when a step's dispatch
// never reached a working runtime — an unknown/unrunnable controller, a
// worktree/workspace that never came up, or a crashed agent session. The
// migrated flow.Run instead folded EVERY step error into one
// `event:workflow_failed` + Notif.Emit("failed", …) bucket, silently
// downgrading a "this needs an operator" signal to an ordinary failure.
//
// `escalate` is a first-class lifecycle event or (internal/connector/
// conductor.go: "a target gave up after retries") notify sinks and
// `conductor.escalate` triggers key off of it — see the e2e's Group A4/J1/J2/J3.
//
// RED against the pre-fix flow.Run: both cases below land in
// event:workflow_failed / Notif "failed", so the unrecoverable case fails
// wantEscalate and the ordinary case (which is SUPPOSED to land there)
// incidentally still passes — the bug is a false negative on escalate, not a
// crash, which is exactly why the e2e (not `go test`) caught it first.
func TestFlowClassifiesUnrecoverableDispatchAsEscalate(t *testing.T) {
	for _, tc := range []struct {
		name         string
		dispatchErr  error
		wantEscalate bool
		why          string
	}{
		{
			name:         "an unrunnable/unknown controller",
			dispatchErr:  dispatch.Unrecoverable(errors.New("unknown controller \"ghost\"")),
			wantEscalate: true,
			why:          "A4/J1 — precedence: an explicit non-runnable controller must escalate",
		},
		{
			name:         "a worktree/workspace that never came up",
			dispatchErr:  dispatch.Unrecoverable(errors.New("create worktree for acme/web: workspace create failed")),
			wantEscalate: true,
			why:          "J2 — worktree creation failure is a LOUD escalate, never a silent scratch fallback",
		},
		{
			name:         "a crashed agent session",
			dispatchErr:  dispatch.Unrecoverable(errors.New("acp: start gemini: exec: \"gemini\": executable file not found")),
			wantEscalate: true,
			why:          "J3 — an ACP agent that dies opening its session must escalate, not vanish",
		},
		{
			name:         "an ordinary step error (ONE reached a runtime and it just failed)",
			dispatchErr:  errors.New("agent exited 1: syntax error in generated patch"),
			wantEscalate: false,
			why:          "a ordinary dispatch failure (a gate discard, a step's own error) stays workflow_failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRunner(t, dispatchAuditCfg(t), nil)
			rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
				return dispatch.RunRef{}, tc.dispatchErr
			}
			runTrigger(rig, newTrigger("ping", nil), mustSpec(t, dispatchAuditSpec))

			escalateRow := findAudit(rig, "escalate")
			failedRow := findAudit(rig, "workflow_failed")
			events := rig.Notifier.snapshot()
			gotEscalateNotif := false
			gotFailedNotif := false
			for _, e := range events {
				switch e.Event {
				case "escalate":
					gotEscalateNotif = true
				case "failed":
					gotFailedNotif = true
				}
			}

			if tc.wantEscalate {
				if escalateRow == nil {
					t.Errorf("no `event:escalate` audit row — %s", tc.why)
				} else if got, _ := escalateRow["error"].(string); got == "" {
					t.Error("the escalate row must carry the error")
				}
				if !gotEscalateNotif {
					t.Error("Notif.Emit was never called with \"escalate\" — the e2e's notify " +
						"sinks (J2) and conductor.escalate triggers key off this event")
				}
				if failedRow != nil {
					t.Error("an unrecoverable dispatch failure must NOT also be recorded workflow_failed")
				}
			} else {
				if failedRow == nil {
					t.Errorf("no `event:workflow_failed` audit row — %s", tc.why)
				}
				if !gotFailedNotif {
					t.Error("Notif.Emit was never called with \"failed\"")
				}
				if escalateRow != nil {
					t.Error("an ordinary step failure must NOT be recorded as escalate — " +
						"only an unrecoverable dispatch failure gets the loud treatment")
				}
			}
		})
	}
}
