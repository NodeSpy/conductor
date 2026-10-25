package engine

import (
	"context"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// HELD LEVEL EVENTS.
//
// A level-triggered (completion: level) workflow is not re-run while an agent
// is already working or parked on its target; the event is skipped. For most
// level events that is the whole story: the condition is current state ("a
// review is pending on me", "the branch conflicts") and the next poll re-reads
// it. Replaying the skipped event later would act on a stale reading — a
// review request already answered, a conflict already resolved.
//
// An event that carries a cursor is different: it names new items (feedback
// submitted since the target's mark), and the mark advances only when an event
// is accepted. Skipping it and then accepting a LATER event for the same
// target raised the mark past it, so neither a webhook redelivery nor a poll
// would ever bring it back — the feedback was dropped for good.
//
// So a cursor-carrying event the live gate skips is held, newest per (target,
// event#variant), and re-delivered when a run on its target ends. It then goes
// through every gate again: still live → held again; its cursor at or below
// the mark (a run since then covered it) → dropped. The target closing drops
// what it held. Held events live in memory only; past a restart the source's
// own poll re-derives them.

// heldLevel is the engine's held-event table: target key → dedup kind → the
// newest held trigger.
type heldLevel struct {
	mu sync.Mutex
	m  map[string]map[string]core.Trigger
}

// put holds t, keeping whichever of t and an already-held trigger carries the
// higher cursor (ties go to t, the newer delivery).
func (h *heldLevel) put(key, dkind string, t core.Trigger) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.m == nil {
		h.m = map[string]map[string]core.Trigger{}
	}
	byKind := h.m[key]
	if byKind == nil {
		byKind = map[string]core.Trigger{}
		h.m[key] = byKind
	}
	if old, ok := byKind[dkind]; ok {
		oid, _, _ := old.Cursor()
		nid, _, _ := t.Cursor()
		if oid > nid {
			return
		}
	}
	byKind[dkind] = t
}

// take removes and returns everything held for key.
func (h *heldLevel) take(key string) []core.Trigger {
	h.mu.Lock()
	defer h.mu.Unlock()
	byKind := h.m[key]
	delete(h.m, key)
	out := make([]core.Trigger, 0, len(byKind))
	for _, t := range byKind {
		out = append(out, t)
	}
	return out
}

// holdLevel holds a cursor-carrying level event the live gate skipped (see
// the note above). It reports false for an event without a cursor, which the
// caller skips as before.
func (e *Engine) holdLevel(ctx context.Context, t core.Trigger, act config.Action, key, dkind string) bool {
	if _, _, ok := t.Cursor(); !ok || t.Force {
		return false
	}
	e.held.put(key, dkind, t)
	e.log("%s held — an agent is already working/parked for it; re-delivered when its run ends", tag(t))
	// The run may have ended between the live check and the hold, in which
	// case its end already found nothing to re-deliver. Look again.
	if !e.hasLiveAgentFor(ctx, act, key, t.Kind) {
		e.redeliverHeld(key)
	}
	return true
}

// redeliverHeld re-emits every event held for key (a run on it just ended).
// Emit never blocks, so this is safe from the engine loop and run goroutines.
func (e *Engine) redeliverHeld(key string) {
	ctx := e.baseCtx
	if ctx == nil {
		ctx = context.Background()
	}
	for _, t := range e.held.take(key) {
		e.log("%s re-delivering the event held while an agent was live", tag(t))
		e.Emit(ctx, t)
	}
}

// dropHeld forgets what key held: the target closed.
func (e *Engine) dropHeld(key string) { _ = e.held.take(key) }
