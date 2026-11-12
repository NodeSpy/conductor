package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/store"
)

// RECOVERING FROM INFRASTRUCTURE FAILURES.
//
// A run whose step failed because its agent runtime did not work waits at
// that step (flow/infrawait.go). This is the other half: the engine keeps
// every waiting run, probes the runtime they wait on with one tiny turn on a
// backoff, and once it answers resumes each of them from its failed step —
// the recorded inputs pinned, exactly as `conductor runs retry` would. A run
// whose target is gone by then (closed, merged, moved to a new revision) is
// dropped instead. Waiting runs are records, so all of this survives a
// restart (ResumeWorkflows re-registers them).
//
// While a run waits, its target counts as live to the level gate: a sweep
// re-emitting the event does not start a second run that would only fail the
// same way. When the run resumes or is dropped the gate opens again, and what
// the gate held for the target is re-delivered.
//
// A long outage is reported ONCE per runtime — not per run, not per probe —
// and a restart does not report it again.

var (
	// infraProbeBackoff is the wait before each probe of a runtime, by
	// attempt, capped at its last entry. Vars so tests shrink them.
	infraProbeBackoff = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}
	// infraEscalateAfter is how long an outage lasts before it is reported.
	infraEscalateAfter = 30 * time.Minute
	// infraProbeTimeout bounds one probe turn.
	infraProbeTimeout = 5 * time.Minute
)

// infraProbePrompt is the probe's whole turn: the cheapest prompt that still
// needs the model to answer.
const infraProbePrompt = "This is a connectivity check. Reply with the single word OK and nothing else."

// infraWaits is the engine's table of waiting runs.
type infraWaits struct {
	// backoff / escalateAfter are this engine's copies of the package
	// clocks, taken when it is built.
	backoff       []time.Duration
	escalateAfter time.Duration

	mu        sync.Mutex
	runs      map[string]*infraWaiter // run id → waiter
	probing   map[string]bool         // probe key → its loop is running
	escalated map[string]bool         // probe key → this outage was reported
}

// infraWaiter is one waiting run.
type infraWaiter struct {
	run  store.WorkflowRun
	t    core.Trigger
	act  config.Action
	key  string // probeKey
	live string // inFlightKey: what it holds live to the level gate
	// claimed: a resume or a drop has taken it (guarded by infraWaits.mu).
	// It stays in the table — still live to the level gate — until the
	// claimer is done, and nothing else may resume or drop it.
	claimed bool
}

// claim takes w for one resume or drop, reporting false when another
// already has it.
func (e *Engine) claim(w *infraWaiter) bool {
	e.infra.mu.Lock()
	defer e.infra.mu.Unlock()
	if w.claimed || e.infra.runs[w.run.ID] != w {
		return false
	}
	w.claimed = true
	return true
}

// probeKey groups waiting runs by what they wait on: one probe answers for
// every run on the same runtime and model.
func probeKey(w *store.InfraWait) string {
	if w.Subject != "" && w.Runtime == "" {
		return "subject\x00" + w.Subject
	}
	return w.Runtime + "\x00" + w.Model + "\x00" + w.Provider
}

func (e *Engine) infraCtx() context.Context {
	if e.baseCtx != nil {
		return e.baseCtx
	}
	return context.Background()
}

// onInfraWait is the flow runner's InfraWait hook: a run just entered the
// wait.
func (e *Engine) onInfraWait(run store.WorkflowRun, t core.Trigger) {
	act, _ := t.Action.(config.Action)
	e.registerInfraWait(e.infraCtx(), run, t, act)
}

// registerInfraWait adds a waiting run to the table and makes sure its
// runtime is being probed.
func (e *Engine) registerInfraWait(ctx context.Context, run store.WorkflowRun, t core.Trigger, act config.Action) {
	if run.InfraWait == nil {
		return
	}
	if a, _, _, ok := e.resolveFlow(act); ok {
		act = a
	}
	t.Action = act
	w := &infraWaiter{run: run, t: t, act: act, key: probeKey(run.InfraWait), live: inFlightKey(act.FlowRef, t)}
	e.infra.mu.Lock()
	if e.infra.runs == nil {
		e.infra.runs, e.infra.probing, e.infra.escalated = map[string]*infraWaiter{}, map[string]bool{}, map[string]bool{}
	}
	e.infra.runs[run.ID] = w
	if run.InfraWait.Escalated {
		e.infra.escalated[w.key] = true
	}
	start := !e.infra.probing[w.key]
	e.infra.probing[w.key] = true
	e.infra.mu.Unlock()
	if start {
		go e.infraProbeLoop(ctx, w.key)
	}
}

// infraWaitingFor reports a run of act's flow waiting on t's target at t's
// revision — live to the level gate, like one running.
func (e *Engine) infraWaitingFor(t core.Trigger, act config.Action) bool {
	if act.FlowRef == "" || e.flow == nil {
		return false
	}
	if a, _, _, ok := e.resolveFlow(act); ok {
		act = a
	}
	k := inFlightKey(act.FlowRef, t)
	e.infra.mu.Lock()
	defer e.infra.mu.Unlock()
	for _, w := range e.infra.runs {
		if w.live == k {
			return true
		}
	}
	return false
}

// waitersFor lists the runs waiting on key, oldest first.
func (e *Engine) waitersFor(key string) []*infraWaiter {
	e.infra.mu.Lock()
	defer e.infra.mu.Unlock()
	var out []*infraWaiter
	for _, w := range e.infra.runs {
		if w.key == key && !w.claimed {
			out = append(out, w)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].run.InfraWait.Since.Before(out[j].run.InfraWait.Since) })
	return out
}

// forgetWaiter drops w from the table — w itself: a run that already
// waits again (its resume failed the same way) is a new entry.
func (e *Engine) forgetWaiter(w *infraWaiter) {
	e.infra.mu.Lock()
	if e.infra.runs[w.run.ID] == w {
		delete(e.infra.runs, w.run.ID)
	}
	e.infra.mu.Unlock()
}

// infraProbeLoop probes key's runtime on a backoff until its waiting runs
// are resumed or dropped, or the engine stops.
func (e *Engine) infraProbeLoop(ctx context.Context, key string) {
	for attempt := 0; ; attempt++ {
		wait := e.infra.backoff[min(attempt, len(e.infra.backoff)-1)]
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if e.infraTick(ctx, key) {
			return
		}
	}
}

// infraTick is one pass over key's waiting runs: drop the ones whose target
// is gone, probe the runtime for the rest, resume them all if it answers.
// It reports whether the loop is done (nothing left waiting on key).
func (e *Engine) infraTick(ctx context.Context, key string) bool {
	waiters := e.dropGoneWaiters(ctx, e.waitersFor(key))
	if len(waiters) == 0 {
		return e.stopProbing(key)
	}
	if err := e.probeRuntime(ctx, waiters[0]); err != nil {
		e.log("engine: %s still not working — %s; %d run(s) waiting", describeKey(waiters[0]), probeReason(err), len(waiters))
		e.store.Audit(map[string]any{"event": "infra_probe", "outcome": "failed",
			"runtime": waiters[0].run.InfraWait.Runtime, "model": waiters[0].run.InfraWait.Model,
			"waiting": len(waiters), "error": probeReason(err)})
		e.maybeEscalateOutage(ctx, key, waiters, err)
		return false
	}
	e.store.Audit(map[string]any{"event": "infra_probe", "outcome": "ok",
		"runtime": waiters[0].run.InfraWait.Runtime, "model": waiters[0].run.InfraWait.Model, "waiting": len(waiters)})
	e.infra.mu.Lock()
	reported := e.infra.escalated[key]
	delete(e.infra.escalated, key)
	e.infra.mu.Unlock()
	e.log("engine: %s works again — resuming %d waiting run(s)", describeKey(waiters[0]), len(waiters))
	if reported {
		e.store.Audit(map[string]any{"event": "infra_recovered", "runtime": waiters[0].run.InfraWait.Runtime,
			"model": waiters[0].run.InfraWait.Model, "resumed": len(waiters)})
	}
	for _, w := range waiters {
		e.resumeWaiter(ctx, w)
	}
	return e.stopProbing(key)
}

// stopProbing ends key's probe loop unless a run started waiting on it since
// the loop last looked, in which case the loop goes on.
func (e *Engine) stopProbing(key string) bool {
	e.infra.mu.Lock()
	defer e.infra.mu.Unlock()
	for _, w := range e.infra.runs {
		if w.key == key && !w.claimed {
			return false
		}
	}
	delete(e.infra.probing, key)
	return true
}

// dropGoneWaiters ends every waiting run whose target is gone and returns the
// rest.
func (e *Engine) dropGoneWaiters(ctx context.Context, ws []*infraWaiter) []*infraWaiter {
	kept := ws[:0]
	for _, w := range ws {
		if reason := e.waiterGone(ctx, w); reason != "" {
			e.dropWaiter(ctx, w, reason)
			continue
		}
		kept = append(kept, w)
	}
	return kept
}

// waiterGone says why w's work is moot ("" when it is not).
func (e *Engine) waiterGone(ctx context.Context, w *infraWaiter) string {
	if _, _, _, ok := e.resolveFlow(w.act); !ok {
		return "its trigger is no longer in the config"
	}
	if e.closedSince(w.t.Key(), w.run.InfraWait.Since) {
		return "its target closed"
	}
	sha, state := e.flow.TargetHead(ctx, w.t)
	switch {
	case state == connector.TargetClosed:
		return "its target closed"
	case state == connector.TargetAccepted:
		return "its target was merged"
	case w.run.InfraWait.Head != "" && sha != "" && sha != w.run.InfraWait.Head:
		return fmt.Sprintf("its target moved to a new revision (%s → %s)", short(w.run.InfraWait.Head), short(sha))
	}
	return ""
}

// dropWaiter ends a waiting run without resuming it, then re-opens its target
// to the level gate.
func (e *Engine) dropWaiter(ctx context.Context, w *infraWaiter, reason string) {
	if !e.claim(w) {
		return
	}
	e.log("%s dropping the run waiting for its runtime — %s", tag(w.t), reason)
	e.store.Audit(map[string]any{"event": "infra_wait_dropped", "repo": w.t.Target.Repo,
		"number": w.t.Target.Number, "kind": w.t.Kind, "run": w.run.ID, "reason": reason})
	if _, spec, _, ok := e.resolveFlow(w.act); ok {
		if extra := connector.OptionHooks(w.t, spec.Options); len(extra) > 0 {
			spec.Hooks = append(append([]config.Hook{}, spec.Hooks...), extra...)
		}
		e.flow.EndWaiting(ctx, w.run, w.t, spec, reason)
	} else {
		_ = e.store.DeleteRun(w.run.ID)
	}
	e.forgetWaiter(w)
	e.redeliverHeld(w.t.Key())
}

// resumeWaiter resumes a waiting run from its failed step, its execution
// history linked to the one the wait ended.
func (e *Engine) resumeWaiter(ctx context.Context, w *infraWaiter) {
	if !e.claim(w) {
		return
	}
	r := w.run
	hist := r.InfraWait.History
	r.InfraWait = nil
	r.InfraResumes++
	_ = e.store.PutRun(r)
	e.log("%s resuming the run that waited for its runtime (resume %d/%d)", tag(w.t), r.InfraResumes, flow.MaxInfraResumes)
	e.store.Audit(map[string]any{"event": "infra_wait_resumed", "repo": w.t.Target.Repo,
		"number": w.t.Target.Number, "kind": w.t.Kind, "run": r.ID, "step_index": r.StepIndex,
		"resume": r.InfraResumes})
	rctx := ctx
	if hist != "" {
		rctx = flow.WithRetryOf(ctx, hist)
	}
	// Counted in flight (synchronously, inside resumeFlowRun) before it
	// stops counting as waiting: the level gate never sees the target free.
	e.resumeFlowRun(rctx, r, w.t, w.act)
	e.forgetWaiter(w)
	// The level gate skipped the target's events while the run waited; with
	// it resumed they are the resumed run's, and what was held for the
	// target is re-delivered when it ends (runFlow).
}

// probeRuntime runs one tiny turn on the runtime w waits on. nil: it
// answered.
func (e *Engine) probeRuntime(ctx context.Context, w *infraWaiter) error {
	iw := w.run.InfraWait
	if iw.Subject != "" && iw.Runtime == "" {
		// Not an agent runtime (a plugin that stopped answering): there is
		// no cheap turn to try, so the backoff elapsing is the probe and
		// the run's own step is the attempt — bounded by MaxInfraResumes.
		return nil
	}
	step := config.Step{Name: "conductor-probe", Type: "agent", Runtime: iw.Runtime,
		Model: config.ModelSpecOf(iw.Model), Checkout: "none", ArchiveWhenDone: config.Bool(true)}
	act := config.Action{Prompt: infraProbePrompt, Checkout: "none"}
	t := w.t
	t.Action = act
	runner, err := e.runnerFor(step)
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, infraProbeTimeout)
	defer cancel()
	ref, err := runner.Dispatch(pctx, dispatch.Request{Trigger: t, Action: act, Step: step,
		Model: iw.Model, Provider: iw.Provider, Wait: true, LiteralPrompt: true})
	if ref.AgentID != "" {
		_ = runner.Archive(context.WithoutCancel(ctx), ref.AgentID)
	}
	if err != nil {
		return err
	}
	if ie := dispatch.ClassifyReply(ref.Output); ie != nil {
		return ie
	}
	return nil
}

// maybeEscalateOutage reports a long outage on key once.
func (e *Engine) maybeEscalateOutage(ctx context.Context, key string, ws []*infraWaiter, probeErr error) {
	since := ws[0].run.InfraWait.Since
	e.infra.mu.Lock()
	done := e.infra.escalated[key]
	due := !done && time.Since(since) >= e.infra.escalateAfter
	if due {
		e.infra.escalated[key] = true
	}
	e.infra.mu.Unlock()
	if !due {
		return
	}
	reason := probeReason(probeErr)
	what := fmt.Sprintf("agents can't reach the model (runtime %s)", describeKey(ws[0]))
	if s := ws[0].run.InfraWait.Subject; s != "" && ws[0].run.InfraWait.Runtime == "" {
		what = s + " is not answering"
	}
	msg := fmt.Sprintf("%s: %s; %d run(s) waiting since %s, will resume automatically",
		what, reason, len(ws), since.Format(time.RFC3339))
	e.log("engine: %s", msg)
	e.store.Audit(map[string]any{"event": "infra_outage_escalated", "runtime": ws[0].run.InfraWait.Runtime,
		"model": ws[0].run.InfraWait.Model, "waiting": len(ws), "since": since, "reason": reason})
	if e.notif != nil {
		e.notif.Emit(ctx, notify.EventEscalate, ws[0].t, msg)
	}
	// Remembered on the records, so a restart does not report it again.
	for _, w := range ws {
		e.infra.mu.Lock()
		taken := w.claimed || e.infra.runs[w.run.ID] != w
		if !taken {
			w.run.InfraWait.Escalated = true
		}
		e.infra.mu.Unlock()
		if !taken {
			_ = e.store.PutRun(w.run)
		}
	}
}

// probeReason is a probe failure as a log/notification line: the runtime's
// own reason, redacted.
func probeReason(err error) string {
	msg := err.Error()
	if ie, ok := dispatch.AsInfra(err); ok {
		msg = ie.Reason
	}
	return dispatch.Excerpt(msg)
}

// describeKey names what a waiter waits on.
func describeKey(w *infraWaiter) string {
	iw := w.run.InfraWait
	if iw.Subject != "" && iw.Runtime == "" {
		return iw.Subject
	}
	parts := []string{orDefault(iw.Runtime, "default")}
	if iw.Model != "" {
		parts = append(parts, iw.Model)
	}
	return strings.Join(parts, "/")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
