package engine

import (
	"context"
	"errors"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// resumeRecheckMaxAttempts bounds how many times ONE resumed (legacy,
// non-flow) run is rechecked after a credential mint error before being left
// for the next daemon start (finding 2, plugin-contract.md §1.11): without a
// cap, a plugin that never recovers would pin a timer on this run forever
// across the daemon's whole uptime.
const resumeRecheckMaxAttempts = 10

// resumeRecheckBackoff is the schedule used for a mint error that carries no
// retry_after/short-backoff of its own (anything other than
// rate_limited/not_ready — e.g. "plugin not running", a connector still
// starting up), by attempt index, capped at its last entry. A var so a test
// shrinks it rather than waiting for real.
var resumeRecheckBackoff = []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 5 * time.Minute}

// resumeRecheckKey namespaces r's recheck attempt counter in e.deferCounts —
// the same map deferAndReemit uses for triggers, keyed here by run ID
// instead of (target, kind, reason) so the two bounded-retry mechanisms
// never collide.
func resumeRecheckKey(runID string) string { return "resume:" + runID }

// scheduleResumeRecheck is ResumeWorkflows' (and its own recheck's) answer to
// a resumed run whose credential mint fails for a reason other than
// target_gone: instead of leaving the run "deferred" until the next daemon
// restart (the only option before this fix), it schedules ONE bounded
// re-check of exactly this run via time.AfterFunc — ctx-aware, and without
// blocking ResumeWorkflows' own sequential loop over every OTHER pending
// run, the same way deferAndReemit never blocks the dispatch loop. After
// resumeRecheckMaxAttempts it stops and leaves the run for the next start,
// logged and audited.
func (e *Engine) scheduleResumeRecheck(ctx context.Context, r store.WorkflowRun, t core.Trigger, act config.Action, mintErr error) {
	dk := resumeRecheckKey(r.ID)
	e.deferMu.Lock()
	attempt := e.deferCounts[dk]
	if attempt >= resumeRecheckMaxAttempts {
		delete(e.deferCounts, dk)
		e.deferMu.Unlock()
		e.log("%s resume: %d rechecks exhausted for run %s — leaving it for the next daemon start", tag(t), resumeRecheckMaxAttempts, r.ID)
		e.store.Audit(map[string]any{"event": "resume_recheck_exhausted", "repo": t.Target.Repo,
			"number": t.Target.Number, "kind": t.Kind, "run": r.ID, "attempts": resumeRecheckMaxAttempts})
		return
	}
	e.deferCounts[dk] = attempt + 1
	e.deferMu.Unlock()

	wait := resumeRecheckWait(mintErr, attempt)
	e.log("%s resume deferred — %v — rechecking run %s in %s (attempt %d/%d)",
		tag(t), mintErr, r.ID, wait.Round(time.Millisecond), attempt+1, resumeRecheckMaxAttempts)
	e.store.Audit(map[string]any{"event": "resume_deferred", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "run": r.ID, "wait": wait.String(), "attempt": attempt + 1})
	time.AfterFunc(wait, func() {
		if ctx.Err() != nil {
			return // shutting down: don't recheck into a dead engine
		}
		e.recheckResumeRun(ctx, r, t, act)
	})
}

// resumeRecheckWait picks the recheck's wait: a rate_limited/not_ready
// mint error honors the same bounded schedule deferAndReemit's deferralWait
// computes (retry_after capped at connector.RateLimitCap, or
// connector.NotReadyBackoff by attempt — both floored at minDeferredWait);
// any other error (no contract code at all, e.g. a transport error while the
// plugin is still starting) falls back to resumeRecheckBackoff by attempt.
func resumeRecheckWait(mintErr error, attempt int) time.Duration {
	if ce, ok := connector.AsContractError(mintErr); ok {
		if w, has := deferralWait(ce, attempt); has {
			return w
		}
	}
	sched := resumeRecheckBackoff
	if attempt >= len(sched) {
		attempt = len(sched) - 1
	}
	wait := sched[attempt]
	if wait < minDeferredWait {
		wait = minDeferredWait
	}
	return wait
}

// recheckResumeRun re-attempts credential mint and, on success, dispatch for
// exactly one persisted (legacy, non-flow) run — ResumeWorkflows' own loop
// body, pulled out so a scheduled recheck (above) can re-run it standalone,
// outside ResumeWorkflows' loop, without re-reading PendingRuns().
func (e *Engine) recheckResumeRun(ctx context.Context, r store.WorkflowRun, t core.Trigger, act config.Action) {
	creds, err := e.credentialsFor(ctx, t)
	if err != nil {
		if errors.Is(err, dispatch.ErrTargetClosed) {
			// target_gone (§1.11) while re-minting a resumed run's
			// credentials: its target is confirmed gone, so it stops here —
			// recorded as a stop, not rechecked again.
			e.log("%s resume: target gone — stopping", tag(t))
			e.store.Audit(map[string]any{"event": "workflow_stopped", "repo": t.Target.Repo,
				"number": t.Target.Number, "kind": t.Kind, "reason": "target closed"})
			e.finishRun(r)
			return
		}
		e.scheduleResumeRecheck(ctx, r, t, act, err)
		return
	}
	e.clearDeferred(resumeRecheckKey(r.ID))
	run := r
	if run.Outputs == nil {
		run.Outputs = map[string]map[string]any{}
	}
	e.log("%s resuming workflow from step %d", tag(t), r.StepIndex)
	e.store.Audit(map[string]any{"event": "resume", "repo": t.Target.Repo,
		"number": t.Target.Number, "kind": t.Kind, "step_index": r.StepIndex})
	go func() {
		// Wait for the slot here so resuming more runs than slots doesn't
		// stall startup.
		if !e.acquireFor(ctx, t.Interactive()) {
			return
		}
		defer e.release()
		defer e.recoverDispatch(ctx, t, run, "workflow resume")
		e.runSteps(ctx, run, t, act, creds, false)
	}()
}
