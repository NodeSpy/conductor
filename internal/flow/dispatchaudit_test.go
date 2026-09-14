package flow

import (
	"context"
	"errors"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// CLASS-CLOSER: the flow runner is the SOLE dispatch path, so it must publish
// the dispatch audit contract `conductor report` consumes.
//
// When `agents:` was removed, every agent step moved into this package. The
// engine's action path — which owned engine.auditDispatch — stopped being
// reached, and nothing replaced the row. No error, no failing unit test: the
// report's dispatch table just silently went empty for the only model that
// still exists. The e2e caught it because Group A asserts backend resolution
// off these rows; this test is the cheap version of that canary.
//
// It asserts the fields report.go actually reads (`event`, `kind`, `outcome`)
// plus the `backend` the e2e resolves on, across every outcome a dispatch can
// reach.
func TestFlowAgentDispatchEmitsTheDispatchAuditRecord(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ref         dispatch.RunRef
		err         error
		wantOutcome string
		wantBackend string
		why         string
	}{
		{"a plain dispatch", dispatch.RunRef{Backend: "paseo", Output: "{}"}, nil, "ok", "paseo",
			"the common case — one agent, one backend"},
		{"an acp controller", dispatch.RunRef{Backend: "acp", Output: "{}"}, nil, "ok", "acp",
			"backend is the RESOLVED backend, which is what Group A verifies"},
		{"a cli controller", dispatch.RunRef{Backend: "cli", Output: "{}"}, nil, "ok", "cli", ""},
		{"queued onto a live agent", dispatch.RunRef{Backend: "paseo", Queued: true, Output: "{}"}, nil,
			"queued", "paseo",
			"Group D counts these: a burst onto one live session must show ok=1 + queued>=2"},
		{"adopted into an open workspace", dispatch.RunRef{Backend: "paseo", Adopted: true, Output: "{}"}, nil,
			"adopted", "paseo", ""},
		{"skipped", dispatch.RunRef{Backend: "paseo", Skipped: true, Output: "{}"}, nil, "skipped", "paseo", ""},
		{"a failed dispatch", dispatch.RunRef{Backend: "paseo"}, errors.New("backend exploded"), "failed", "paseo",
			"a failed dispatch is exactly the row an operator goes looking for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRunner(t, dispatchAuditCfg(t), nil)
			rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
				return tc.ref, tc.err
			}
			runTrigger(rig, newTrigger("ping", nil), mustSpec(t, dispatchAuditSpec))

			row := findAudit(rig, "dispatch")
			if row == nil {
				t.Fatalf("NO dispatch audit row — `conductor report` builds its dispatch "+
					"table from these, and this is the only dispatch path there is. %s", tc.why)
			}
			if got, _ := row["outcome"].(string); got != tc.wantOutcome {
				t.Errorf("outcome=%q want %q — %s", got, tc.wantOutcome, tc.why)
			}
			if got, _ := row["backend"].(string); got != tc.wantBackend {
				t.Errorf("backend=%q want %q — the RESOLVED backend, not the runtime's "+
					"configured name", got, tc.wantBackend)
			}
			// report.go keys its table on kind, and buckets on outcome.
			if got, _ := row["kind"].(string); got != "ping" {
				t.Errorf("kind=%q want %q — report.go keys the dispatch table on it", got, "ping")
			}
			if _, ok := row["step"]; !ok {
				t.Error("the row should name its step")
			}
		})
	}
}

// A dispatch a GATE held back before it reached a backend is recorded too.
// The engine shed before its audit point, so a budget quietly eating a repo's
// work left no row at all — the operator saw the dispatch count fall and had
// nothing to explain it.
func TestGatedDispatchEmitsADeferredRecord(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		install      func(*testRig)
	}{
		{"the per-hour rate counter", "rate", func(rig *testRig) {
			rig.Runner.Agents.CheckRate = func() error { return errors.New("rate cap reached") }
		}},
		{"the cost budget", "budget", func(rig *testRig) {
			rig.Runner.Agents.CheckBudget = func(string, *config.BudgetPolicy, string, cost.Usage) (*cost.Reservation, error) {
				return nil, errors.New("budget exhausted")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRunner(t, dispatchAuditCfg(t), nil)
			tc.install(rig)
			runTrigger(rig, newTrigger("ping", nil), mustSpec(t, dispatchAuditSpec))

			row := findAudit(rig, "dispatch")
			if row == nil {
				t.Fatal("a gated dispatch must still leave a row — otherwise a budget " +
					"silently eating a repo's work is invisible in the report")
			}
			if got, _ := row["outcome"].(string); got != "deferred" {
				t.Errorf("outcome=%q want \"deferred\" — distinct from \"queued\", which "+
					"means another agent TOOK the work; deferred means nobody did", got)
			}
			if got, _ := row["reason"].(string); got != tc.reason {
				t.Errorf("reason=%q want %q", got, tc.reason)
			}
		})
	}
}

const dispatchAuditSpec = `
on: manual
steps:
  - { id: fix, type: agent, prompt: p }
`

func dispatchAuditCfg(t *testing.T) *config.Config {
	t.Helper()
	return loadConfig(t, `
connectors: { svc: { use: fake } }
`)
}

// findAudit returns the first `event: <event>` audit row.
func findAudit(rig *testRig, event string) map[string]any {
	rows := rig.Store.auditsWithEvent(event)
	if len(rows) == 0 {
		return nil
	}
	return rows[0]
}
