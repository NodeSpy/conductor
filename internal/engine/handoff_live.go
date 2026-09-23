package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// liveHandoff is a currently-open interactive hand-off the daemon can act on out
// of band. The watch loop acts on its own hand-off directly (it holds cancel in
// closure); this registry is what the agent skill path (handoff.done) and the
// idle timer resolve through, keyed by the held agent id.
type liveHandoff struct {
	cancel context.CancelFunc // ends the Review loop
	prKey  string             // broker key for the draft/session
	stepID string
}

func (e *Engine) registerLiveHandoff(agentID string, cancel context.CancelFunc, prKey, stepID string) {
	if agentID == "" {
		return
	}
	e.liveHOMu.Lock()
	if e.liveHO == nil {
		e.liveHO = map[string]*liveHandoff{}
	}
	e.liveHO[agentID] = &liveHandoff{cancel: cancel, prKey: prKey, stepID: stepID}
	e.liveHOMu.Unlock()
}

func (e *Engine) deregisterLiveHandoff(agentID string) {
	if agentID == "" {
		return
	}
	e.liveHOMu.Lock()
	delete(e.liveHO, agentID)
	e.liveHOMu.Unlock()
}

func (e *Engine) lookupLiveHandoff(agentID string) *liveHandoff {
	e.liveHOMu.Lock()
	defer e.liveHOMu.Unlock()
	return e.liveHO[agentID]
}

// handoffDone releases the hand-off the given agent is holding: it ends the
// review loop, closes the draft, and drops the reaper hold so the workspace is
// reclaimed. Both handoff.done (agent-called) and the idle-timeout backstop land
// here. Idempotent — a second call after teardown is a no-op error.
func (e *Engine) handoffDone(ctx context.Context, agentID, reason string) error {
	lh := e.lookupLiveHandoff(agentID)
	if lh == nil {
		return fmt.Errorf("no live hand-off for agent %s", agentID)
	}
	e.log("hand-off %q (agent %s) released: %s", lh.stepID, agentID, reason)
	lh.cancel()
	if e.broker != nil {
		e.broker.Close(ctx, lh.prKey)
	}
	e.hold.Remove(agentID)
	e.deregisterLiveHandoff(agentID)
	// Actively reclaim the agent — do NOT rely on the reaper. A hand-off carries
	// no archive_when_done label (it's protected by the Held set instead), so the
	// reaper's archive=1 walk never lists it, and its orphan sweep skips a
	// workspace that still has a live agent; paseo's own session Close is a no-op.
	// Without this an agent that called done but stayed `running` (a wedged turn)
	// lingers forever. e.disp.Archive goes through the paseo backend: it interrupts
	// a running turn and reclaims the worktree. Detached ctx — lh.cancel() above
	// cancelled the hand-off's context, and the reclaim must still run.
	if e.disp != nil {
		if err := e.disp.Archive(context.WithoutCancel(ctx), agentID); err != nil {
			e.log("hand-off %q (agent %s): reclaim on done failed: %v", lh.stepID, agentID, err)
		}
	}
	return nil
}

// startIdleTimer releases a hand-off that is still open after d — the backstop
// for one nobody closed. The agent calling handoff.done (the precise signal)
// cancels runCtx first, so the timer only fires when the hand-off was abandoned.
func (e *Engine) startIdleTimer(parent, runCtx context.Context, t core.Trigger, stepID, agentID string, d time.Duration) {
	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-runCtx.Done():
			return // resolved or torn down before the timeout
		case <-timer.C:
			e.log("hand-off %q idle %s — releasing", stepID, d)
			_ = e.handoffDone(parent, agentID, "idle timeout")
		}
	}()
}

// HandoffOps exposes the daemon's live hand-off operations to the handoff
// connector (wired in main via connector.SetHandoffOps). Only Done is reachable
// from the agent skill surface; bail/refresh are driven by the watch loop
// directly, so they are left unset (the connector reports them unavailable off a
// watch rule). handoff.done and step.done share the StepDone handler — a
// hand-off's done additionally tears the hand-off state down (StepDone routes
// through handoffDone when the resolved agent holds one).
func (e *Engine) HandoffOps() *connector.HandoffOps {
	return &connector.HandoffOps{
		Done: func(ctx context.Context, dispatchID, agentID string) error {
			return e.StepDone(ctx, dispatchID, agentID, "agent signalled done")
		},
	}
}

// StepOps exposes step.done (wired in main via connector.SetStepOps).
func (e *Engine) StepOps() *connector.StepOps {
	return &connector.StepOps{
		Done: func(ctx context.Context, dispatchID, agentID, reason string) error {
			if reason == "" {
				reason = "agent signalled done"
			}
			return e.StepDone(ctx, dispatchID, agentID, reason)
		},
	}
}

// StepDone is THE done signal: the calling dispatch's agent has finished its
// task, so conductor reclaims what it launched — and only what it launched.
//
// The caller is identified by its token-bound dispatch id, resolved through the
// ownership ledger to the one agent that dispatch started (agentID is a legacy
// fallback for tokens minted before dispatch binding). Resolution order:
//
//   - a live hand-off → full hand-off teardown (watch loop, review channel,
//     hold) + archive, via handoffDone;
//   - a dispatch conductor is still blocked on (foreground, in flight) → mark
//     only: the step-boundary archive reclaims it after the output is captured,
//     so a done call can never cut off the output conductor is waiting on;
//   - otherwise → archive the agent + its workspace now (ledger-gated: the
//     dispatcher refuses anything conductor didn't launch).
func (e *Engine) StepDone(ctx context.Context, dispatchID, agentID, reason string) error {
	if resolved := e.disp.AgentForDispatch(dispatchID); resolved != "" {
		agentID = resolved
	}
	if agentID == "" {
		return fmt.Errorf("step.done: no conductor-launched agent for this session")
	}
	if lh := e.lookupLiveHandoff(agentID); lh != nil {
		return e.handoffDone(ctx, agentID, reason)
	}
	if dispatchID != "" && e.disp.DispatchInFlight(dispatchID) {
		e.log("step.done from agent %s (%s) — dispatch still in flight; reclaiming at step boundary", agentID, reason)
		return nil
	}
	e.log("step.done from agent %s: %s — reclaiming", agentID, reason)
	return e.disp.Archive(context.WithoutCancel(ctx), agentID)
}
