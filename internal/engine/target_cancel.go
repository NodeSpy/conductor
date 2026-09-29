package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/notify"
)

// cancelTargetAgents tears down every live or queued agent working t's target
// when observeClosed (outcome.go) learns — from a TRUSTED `_closed` signal —
// that the target itself just died. A dispatch is long-lived: an agent can keep
// working, and keep calling skill verbs, long after the PR it was launched for
// is merged or closed out from under it. targets.Registry.CheckWrite/CheckPush
// refuse those stray writes going forward; this is the other half — stop the
// agent itself rather than let it keep spending time (and money) on a dead
// target.
//
// It cancels across every registered controller AND the paseo dispatcher (see
// controller.Registry.CancelTarget), drops any flow still queued waiting for a
// concurrency slot for this target, and releases any open interactive hand-off
// bound to it. The audit row and notification only fire when something was
// actually running or queued — the caller (observeClosed) marks the target
// closed unconditionally regardless.
func (e *Engine) cancelTargetAgents(ctx context.Context, t core.Trigger, outcome string) {
	prKey := t.Key()
	reason := "target " + outcome

	var ids []string
	if e.controllers != nil {
		ids = e.controllers.CancelTarget(ctx, prKey, reason)
	}
	dropped := e.dropQueuedFlows(prKey)
	handoffs := e.closeHandoffsForTarget(ctx, prKey, reason)

	if len(ids) == 0 && dropped == 0 && handoffs == 0 {
		return
	}
	e.log("%s cancelled %d agent(s), %d queued flow(s), %d hand-off(s) — %s",
		tag(t), len(ids), dropped, handoffs, reason)
	e.store.Audit(map[string]any{"event": "cancelled", "reason": reason,
		"repo": t.Target.Repo, "number": t.Target.Number, "agents": ids})
	e.notif.Emit(ctx, notify.EventCancelled, t, fmt.Sprintf("cancelled: %s", reason))
}

// dropQueuedFlows removes every flow waiting for a concurrency slot whose
// target is targetKey (across every FlowRef — a target can have more than one
// flow queued). A goroutine already past this point (acquireFor returned, or
// about to) still runs to completion on whatever trigger it captured; this
// stops it from being picked up as "the newest coalesced event" and, more
// importantly, keeps a fresh trigger for the now-dead target from silently
// joining a defunct wait. Returns how many entries it dropped.
func (e *Engine) dropQueuedFlows(targetKey string) int {
	suffix := "\x00" + targetKey
	e.queuedMu.Lock()
	defer e.queuedMu.Unlock()
	n := 0
	for qk := range e.queued {
		if strings.HasSuffix(qk, suffix) {
			delete(e.queued, qk)
			n++
		}
	}
	return n
}

// closeHandoffsForTarget releases every open interactive hand-off bound to
// prKey — ending its review loop, closing the broker draft, dropping the
// reaper hold, and reclaiming the agent (see handoffDone) — so a hand-off
// doesn't linger presenting a draft for a PR that just merged or closed.
// Returns how many it released.
func (e *Engine) closeHandoffsForTarget(ctx context.Context, prKey, reason string) int {
	e.liveHOMu.Lock()
	var agents []string
	for agentID, lh := range e.liveHO {
		if lh.prKey == prKey {
			agents = append(agents, agentID)
		}
	}
	e.liveHOMu.Unlock()
	for _, agentID := range agents {
		_ = e.handoffDone(ctx, agentID, reason)
	}
	return len(agents)
}
