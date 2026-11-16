package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// A run bound to its target (the trigger declared bound_to_target) ends the
// moment the target closes: the work is moot, and anything it pushes from
// here lands on a dead branch — or re-creates a deleted one. Killing the
// runtime's agent (StopTarget) is not enough on its own: a dispatch still
// provisioning or opening its session has no agent to kill yet, a runtime may
// have no StopTarget at all, and the run's own later steps and end-of-step
// push run on the run's context, not the agent's. So the run's context is
// cancelled, with cause dispatch.ErrTargetClosed, in the same critical
// section that records the close: there is no instant at which a run of the
// target is live and the close is recorded but the run not yet told.

// targetRun is one live bound run's handle.
type targetRun struct {
	cancel context.CancelCauseFunc
}

type boundRunKey struct{}

// boundRunSince is when the bound run ctx belongs to started (ok=false: ctx
// is not a bound run's).
func boundRunSince(ctx context.Context) (time.Time, bool) {
	at, ok := ctx.Value(boundRunKey{}).(time.Time)
	return at, ok
}

// bindRunToTarget ties a run of t to t's target: the returned ctx is
// cancelled with cause dispatch.ErrTargetClosed when the target closes — or
// at once, when it closed at or after since (the run was waiting when it
// did). Not bound_to_target, or ctx already bound: ctx unchanged. The
// returned func unregisters the run; it does not cancel ctx (a background
// hand-off the run launched may outlive it, as with an operator stop).
func (e *Engine) bindRunToTarget(ctx context.Context, t core.Trigger, since time.Time) (context.Context, func()) {
	if !t.BoundToTarget() {
		return ctx, func() {}
	}
	if _, ok := boundRunSince(ctx); ok {
		return ctx, func() {}
	}
	ctx, cancel := context.WithCancelCause(ctx)
	ctx = context.WithValue(ctx, boundRunKey{}, since)
	key := t.Key()
	h := &targetRun{cancel: cancel}
	e.queuedMu.Lock()
	if at, ok := e.closedAt[key]; ok && !at.Before(since) {
		cancel(dispatch.ErrTargetClosed)
	}
	if e.targetRuns == nil {
		e.targetRuns = map[string]map[*targetRun]struct{}{}
	}
	if e.targetRuns[key] == nil {
		e.targetRuns[key] = map[*targetRun]struct{}{}
	}
	e.targetRuns[key][h] = struct{}{}
	e.queuedMu.Unlock()
	return ctx, func() {
		e.queuedMu.Lock()
		delete(e.targetRuns[key], h)
		if len(e.targetRuns[key]) == 0 {
			delete(e.targetRuns, key)
		}
		e.queuedMu.Unlock()
	}
}

// stopTargetRunsLocked cancels every live bound run of key. queuedMu held.
func (e *Engine) stopTargetRunsLocked(key string) int {
	n := 0
	for h := range e.targetRuns[key] {
		h.cancel(dispatch.ErrTargetClosed)
		n++
	}
	delete(e.targetRuns, key)
	return n
}

// dispatchTarget is the target a dispatch works on, and since when.
type dispatchTarget struct {
	key   string
	since time.Time
}

// noteDispatchTarget records the target dispatchID works on: the push gate
// (dispatchTargetClosed) reads it. A dispatch of a bound run counts from the
// run's start, any other from now.
func (e *Engine) noteDispatchTarget(ctx context.Context, dispatchID string, t core.Trigger) {
	if dispatchID == "" {
		return
	}
	since, ok := boundRunSince(ctx)
	now := time.Now()
	if !ok {
		since = now
	}
	e.queuedMu.Lock()
	defer e.queuedMu.Unlock()
	if e.dispatchTargets == nil {
		e.dispatchTargets = map[string]dispatchTarget{}
	}
	for id, d := range e.dispatchTargets {
		if now.Sub(d.since) > closedRetention {
			delete(e.dispatchTargets, id)
		}
	}
	e.dispatchTargets[dispatchID] = dispatchTarget{key: t.Key(), since: since}
}

// dispatchTargetClosed reports whether the target dispatchID works on closed
// after its work began: conductor pushes nothing for it from then on. A
// target reopened since keeps its new dispatches pushable — they began after
// the close.
func (e *Engine) dispatchTargetClosed(dispatchID string) bool {
	if dispatchID == "" {
		return false
	}
	e.queuedMu.Lock()
	defer e.queuedMu.Unlock()
	d, ok := e.dispatchTargets[dispatchID]
	if !ok {
		return false
	}
	at, ok := e.closedAt[d.key]
	return ok && !at.Before(d.since)
}

// The engine's record of closes is in memory. A run whose target closed just
// before the daemon stopped (cancelled, but interrupted by the shutdown
// before it ended as stopped) would resume as if the target were open. So a
// close is also marked on the stored runs bound to the target, and a resumed
// run takes it back.

// markRunsClosed records the close of key's target on the stored runs bound
// to it.
func (e *Engine) markRunsClosed(key string) {
	m, ok := e.store.(interface {
		MarkRunsTargetClosed(func(store.WorkflowRun) bool, time.Time) (int, error)
	})
	if !ok {
		return
	}
	_, _ = m.MarkRunsTargetClosed(func(r store.WorkflowRun) bool {
		var t core.Trigger
		return json.Unmarshal(r.Trigger, &t) == nil && t.Key() == key && t.BoundToTarget()
	}, time.Now())
}

// resumedSince is when a resumed run began, for bindRunToTarget: its recorded
// start, and no later than a close of its target recorded on it, which it
// puts back in the engine's memory of closes. Zero when the record says
// neither (written before either was kept): the run counts from its resume.
func (e *Engine) resumedSince(r store.WorkflowRun, key string) time.Time {
	since := r.StartedAt
	if at := r.TargetClosedAt; !at.IsZero() {
		e.queuedMu.Lock()
		if e.closedAt == nil {
			e.closedAt = map[string]time.Time{}
		}
		if e.closedAt[key].Before(at) {
			e.closedAt[key] = at
		}
		e.queuedMu.Unlock()
		if since.IsZero() || since.After(at) {
			since = at
		}
	}
	return since
}
