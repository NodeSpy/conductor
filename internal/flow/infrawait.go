package flow

import (
	"context"
	"time"

	"github.com/NodeSpy/conductor/internal/blob"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// INFRASTRUCTURE WAIT.
//
// A step that fails because its agent runtime did not work
// (dispatch.ErrInfrastructure: the runtime could not reach its model,
// returned nothing, or did not start) has not failed: the same step run on a
// working runtime succeeds. So the run does not end. It is kept at that step
// — its record stays, marked waiting (store.InfraWait), its execution history
// recorded "waiting" — and no fail hooks fire and nothing is notified: what
// the run's start put up (a pending status, a reaction) still says pending,
// which it is. The engine then probes the runtime and resumes the run from
// that step once it answers, or drops it when its target is gone
// (engine/infrawait.go).
//
// The wait is bounded per run: a failure that only looks like the runtime's
// (the probe passes, the step fails again the same way) ends in an ordinary
// failure after MaxInfraResumes resumes.

// MaxInfraResumes bounds how many times one run is resumed after an
// infrastructure wait. Past it the next such failure fails the run.
const MaxInfraResumes = 5

// waitsOnInfra reports whether err puts the run into an infrastructure wait
// rather than failing it.
func (r *Runner) waitsOnInfra(run *store.WorkflowRun, err error) bool {
	return r.InfraWait != nil && run != nil && run.ID != "" && !r.DryRun &&
		dispatch.IsInfra(err) && run.InfraResumes < MaxInfraResumes
}

// enterInfraWait keeps run at its failed step, waiting for the runtime.
func (r *Runner) enterInfraWait(ctx context.Context, run store.WorkflowRun, t core.Trigger, err error, hist *histRec, acc *costAcc) {
	step := failedStepID(err)
	w := &store.InfraWait{Since: time.Now(), Step: step, Reason: r.redactErr(err)}
	if ie, ok := dispatch.AsInfra(err); ok {
		w.Reason = ie.Reason
		if r.Secrets != nil {
			w.Reason = r.Secrets.Redact(w.Reason)
		}
		w.Runtime, w.Model, w.Provider = ie.Runtime, ie.Model, ie.Provider
	} else {
		w.Subject = dispatch.InfraSubject(err)
		w.Reason = dispatch.Excerpt(w.Reason)
	}
	if hist != nil {
		w.History = hist.rec.ID
	}
	w.Head, _, _ = r.readHead(ctx, t)
	run.InfraWait = w
	run.Tokens, run.CostUSD, run.ApproxCost = acc.totals()
	if perr := r.Store.PutRun(run); perr != nil {
		// Without the record there is nothing to resume: fail it plainly.
		r.Log("%s workflow: could not record the infrastructure wait (%v)", flowTag(t), perr)
	}
	r.Log("%s workflow waiting at step %s — the agent runtime is not working (%s); it resumes when the runtime does", flowTag(t), step, w.Reason)
	r.audit(map[string]any{"event": "workflow_waiting", "repo": t.Target.Repo,
		"number": t.Target.Number, "kind": t.Kind, "step": step, "run": run.ID,
		"runtime": w.Runtime, "model": w.Model, "reason": w.Reason, "error": r.redactErr(err)})
	r.auditRunCost(t, run.ID, acc)
	hist.finish("waiting", r.redactErr(err), step, acc)
	// The run's deferred releases and call staging go now; its record and
	// artifacts stay for the resume.
	r.releaseAtRunEnd(ctx)
	removeRunCallStaging(ctx)
	r.InfraWait(run, t)
}

// TargetHead reads t's target's current revision and state (connector.Target*;
// "" when unknown) through the connector that emitted it.
func (r *Runner) TargetHead(ctx context.Context, t core.Trigger) (sha, state string) {
	sha, state, _ = r.readHead(ctx, t)
	return sha, state
}

// historyGetter is the optional Store surface for amending a recorded
// execution. *store.Store satisfies it.
type historyGetter interface {
	GetHistory(id string) (store.RunHistory, bool)
}

// EndWaiting ends a run that was waiting for its runtime without resuming it:
// its target is gone (closed, merged, moved past the revision it ran for).
// The run is moot, not failed: its stop hooks fire — taking down whatever its
// start put up — its waiting history record is closed "stopped" with reason,
// and its record and artifacts are released.
func (r *Runner) EndWaiting(ctx context.Context, run store.WorkflowRun, t core.Trigger, spec config.TriggerSpec, reason string) {
	if hasPhase(spec.Hooks, "stop") {
		data := baseData(t, r.SecretVals)
		var f runFacts
		f.headSHA, f.state, f.stopWords = r.readHead(ctx, t)
		f.reason = reason
		if f.state != "" && f.state != connector.TargetOpen {
			f.reason = stopReason(f.stopWords)
		}
		r.fireHooks(ctx, t, spec.Hooks, "stop", "stopped", run.ID, "", withRun(data, f), nil, "workflow")
	}
	if run.InfraWait != nil && run.InfraWait.History != "" {
		if hg, ok := r.Store.(historyGetter); ok {
			if rec, found := hg.GetHistory(run.InfraWait.History); found && rec.Status == "waiting" {
				rec.Status, rec.Error, rec.Finished = "stopped", reason, time.Now()
				_ = r.Store.PutHistory(rec)
			}
		}
		ctx = blob.WithOwner(ctx, run.InfraWait.History)
	}
	r.Log("%s workflow stopped while waiting for its runtime — %s", flowTag(t), reason)
	r.audit(map[string]any{"event": "workflow_stopped", "repo": t.Target.Repo,
		"number": t.Target.Number, "kind": t.Kind, "run": run.ID, "reason": reason})
	r.finishRun(ctx, run)
}
