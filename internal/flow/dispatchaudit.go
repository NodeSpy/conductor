package flow

import (
	"errors"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// THE DISPATCH AUDIT CONTRACT.
//
// `conductor report` builds its dispatch table from audit rows shaped
// `{"event":"dispatch","kind":…,"outcome":…}` (cmd/conductor/report.go), and
// the e2e asserts backend resolution from the same rows. That contract was
// written against the ENGINE's action path (engine.auditDispatch).
//
// When `agents:` was removed, every agent step moved to this package — so the
// flow runner became the SOLE dispatch path, and it emitted `agent_usage` and
// `step` rows but no `dispatch` row. Nothing errored; the report's dispatch
// stats simply went empty, and would have stayed empty until someone noticed
// the numbers were missing. The e2e caught it because Group A asserts backend
// resolution off these rows.
//
// So this is not new telemetry — it is the contract the engine already
// published, re-published from the path that now owns dispatching. The
// `agent_usage` (cost/tokens) and `step` rows are unchanged; this is additive.
//
// FIELDS, matching engine.auditDispatch so one consumer reads both:
//
//	event    "dispatch"
//	repo,number,kind   the target and what fired
//	backend  the RESOLVED backend — paseo | acp | cli | native | local |
//	         session. This is the dispatcher/transport the request actually
//	         reached (RunRef.Backend), NOT the runtime's configured name: two
//	         runtimes named `pae` and `deflt` both resolve to paseo, and it is
//	         the backend an operator needs to see.
//	outcome  ok | failed | skipped | adopted | queued | shadow | deferred
//	step     the step id (flow-only; the engine had no step here)
//	shadow, agent_id, error
//
// argv is deliberately NOT carried. The engine redacts it before writing
// (redactArgv); this package has no argv redactor, and an unredacted argv in
// the audit log could carry a secret a step passed on a command line. No
// consumer reads it.

// auditDispatch records one dispatch that REACHED a backend.
func (r *Runner) auditDispatch(t core.Trigger, step string, ref dispatch.RunRef, err error) {
	entry := map[string]any{
		"event": "dispatch", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "backend": ref.Backend, "step": step,
		"shadow": ref.Shadowed, "agent_id": ref.AgentID,
		"outcome": dispatchOutcome(ref, err),
	}
	if err != nil {
		entry["error"] = r.redactErr(err)
	}
	r.audit(entry)
}

// dispatchOutcome classifies a dispatch exactly as engine.auditDispatch does,
// so a row from either path buckets the same way in `conductor report`.
func dispatchOutcome(ref dispatch.RunRef, err error) string {
	switch {
	case err != nil:
		return "failed"
	case ref.Skipped:
		return "skipped"
	case ref.Adopted:
		return "adopted"
	case ref.Queued:
		return "queued"
	case ref.Shadowed:
		return "shadow"
	}
	return "ok"
}

// auditDispatchDeferred records a dispatch a GATE held back before it reached
// any backend — the per-hour rate counter or the cost budget.
//
// The engine never recorded these: it shed before its audit point, so a budget
// that was quietly eating a repo's work left no row saying so. `deferred` is
// its own outcome rather than `queued` because the two are different states —
// `queued` means another agent took the work, `deferred` means nobody did and
// the sweep has to re-derive it. Bucketing them together would hide exactly
// the case an operator is looking for when the dispatch count drops.
func (r *Runner) auditDispatchDeferred(t core.Trigger, step, reason string, err error) {
	r.audit(map[string]any{
		"event": "dispatch", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "step": step, "backend": "", "outcome": "deferred",
		"reason": reason, "error": r.redactErr(err),
	})
}

// auditBudgetShed records a workflow/runtime/global spend cap tripping BEFORE
// the step's agent ever dispatches (#36 §14). This is the flow-side half of
// the engine's spend-budget contract (internal/engine/budget.go): the
// engine's CheckBudget seam knows only the runtime and the resolved scope/
// reason, not which target/step/agent got shed, so the flow runner — which
// has all of that in scope at the call site — writes the one rich row. err's
// scope/reason are read off it via cost.BudgetError when present; a CheckBudget
// stub that returns a plain error (tests) still gets a row, just without them.
func (r *Runner) auditBudgetShed(t core.Trigger, step, identity string, err error) {
	entry := map[string]any{
		"event": "budget_shed", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "step": step, "agent": identity,
	}
	var be *cost.BudgetError
	if errors.As(err, &be) {
		entry["scope"] = be.Scope
		entry["reason"] = be.Reason
	}
	r.audit(entry)
}
