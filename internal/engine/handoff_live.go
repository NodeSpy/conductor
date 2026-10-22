package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/handoff"
	"github.com/NodeSpy/conductor/internal/skill"
)

// liveHandoff is a currently-open interactive hand-off the daemon can act on out
// of band. The watch loop acts on its own hand-off directly (it holds cancel in
// closure); this registry is what the agent skill path (handoff.done) and the
// idle timer resolve through, keyed by the held agent id.
type liveHandoff struct {
	cancel context.CancelFunc // ends the Review loop
	prKey  string             // broker key for the draft/session
	kind   string             // trigger kind that produced it (re-engage retires by key+kind)
	stepID string
}

func (e *Engine) registerLiveHandoff(agentID string, cancel context.CancelFunc, prKey, kind, stepID string) {
	if agentID == "" {
		return
	}
	e.liveHOMu.Lock()
	if e.liveHO == nil {
		e.liveHO = map[string]*liveHandoff{}
	}
	e.liveHO[agentID] = &liveHandoff{cancel: cancel, prKey: prKey, kind: kind, stepID: stepID}
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
	e.releaseHandoff(ctx, agentID, lh.prKey, lh.stepID, lh.cancel, reason)
	return nil
}

// releaseHandoff is THE hand-off teardown — done, idle timeout, watch bail,
// watch supersede, and a re-engage on a new revision all land here: end the
// review loop + watch/idle goroutines, close the draft, drop the hold and the
// registry entry, and archive the agent + its workspace.
//
// Actively reclaim the agent — do NOT rely on a reaper. A hand-off carries no
// archive_when_done label (it's protected by the Held set instead), and there is
// no background sweep that would reclaim a released one; dropping only the hold
// strands the workspace in paseo forever. e.archiveAgent goes through the
// backend: it interrupts a running turn and reclaims the worktree. Detached ctx —
// cancel() ended the hand-off's context, and the reclaim must still run.
// Synchronous, so a supersede's replacement never coexists with the stale one.
func (e *Engine) releaseHandoff(ctx context.Context, agentID, prKey, stepID string, cancel context.CancelFunc, reason string) {
	e.log("hand-off %q (agent %s) released: %s", stepID, agentID, reason)
	if cancel != nil {
		cancel()
	}
	if e.broker != nil {
		e.broker.Close(ctx, prKey)
	}
	e.hold.Remove(agentID)
	e.deregisterLiveHandoff(agentID)
	e.revokeHandoffSession(agentID)
	if agentID == "" || e.affinityOwns(agentID) || e.disp == nil {
		return
	}
	if err := e.archiveAgent(context.WithoutCancel(ctx), agentID); err != nil {
		e.log("hand-off %q (agent %s): reclaim failed: %v", stepID, agentID, err)
	}
}

// revokeHandoffSession kills the released hand-off's conductor session token
// now, rather than leaving it valid (and persisted) until its sliding expiry.
// Runs before the archive, which forgets the agent's dispatch bindings.
func (e *Engine) revokeHandoffSession(agentID string) {
	b := skill.Active()
	if b == nil || agentID == "" {
		return
	}
	if dl, ok := e.disp.(interface{ DispatchesForAgent(string) []string }); ok {
		for _, d := range dl.DispatchesForAgent(agentID) {
			b.RevokeDispatch(d)
		}
	}
}

// retireStaleHandoffs releases every open hand-off for this target+kind — the
// re-engage path calls it before dispatching a review of a NEW revision, so the
// draft of the old revision doesn't sit beside its replacement.
//
// Two sources, because the live registry is in-memory: the registry (hand-offs
// this daemon armed — full teardown), then the backend's labelled agents for
// key+kind that are still HELD (the persisted hand-off marker) — a hand-off
// armed before a restart. Only held agents: a still-running step of an older
// flow run is not a parked hand-off, and archiving it mid-step would fail that
// run instead of retiring a draft.
func (e *Engine) retireStaleHandoffs(ctx context.Context, t core.Trigger, key, reason string) int {
	e.liveHOMu.Lock()
	type entry struct {
		id string
		lh *liveHandoff
	}
	var hit []entry
	for id, lh := range e.liveHO {
		if lh.prKey == key && lh.kind == t.Kind {
			hit = append(hit, entry{id, lh})
		}
	}
	e.liveHOMu.Unlock()
	done := map[string]bool{}
	for _, h := range hit {
		e.releaseHandoff(ctx, h.id, h.lh.prKey, h.lh.stepID, h.lh.cancel, reason)
		done[h.id] = true
	}
	if la, ok := e.disp.(interface {
		LiveAgents(context.Context, string, string) []string
	}); ok {
		for _, id := range la.LiveAgents(ctx, key, t.Kind) {
			if done[id] || !e.hold.Has(id) {
				continue
			}
			e.releaseHandoff(ctx, id, key, t.Kind, nil, reason)
			done[id] = true
		}
	}
	return len(done)
}

// startNativeHandoff arms the lifecycle of a hand-off with no ask channel —
// the agent is left live in paseo for you to drive directly. It still gets the
// step's watch: rules, idle_timeout:, and a live-registry entry (so handoff.done
// and a re-engage can find it); the hand-off ends when one of those releases it.
func (e *Engine) startNativeHandoff(ctx context.Context, t core.Trigger, stepID, identity string, profile config.Step, agentID string, actions dispatch.HandoffActions) {
	if agentID == "" {
		return
	}
	runCtx, _ := e.armHandoff(ctx, t, stepID, identity, profile, agentID, t.Key(), nil, actions)
	go func() {
		<-runCtx.Done()
		e.deregisterLiveHandoff(agentID)
	}()
}

// armHandoff registers a live hand-off and starts its watch and idle timer
// under a cancelable ctx, so it can be ended out of band: a watch rule
// (bail/supersede), the agent calling handoff.done, the idle timeout, or a
// re-engage on a new revision — each cancels via releaseHandoff.
func (e *Engine) armHandoff(ctx context.Context, t core.Trigger, stepID, identity string, profile config.Step, agentID, prKey string, ch handoff.Channel, actions dispatch.HandoffActions) (context.Context, context.CancelFunc) {
	runCtx, cancel := context.WithCancel(ctx)
	e.registerLiveHandoff(agentID, cancel, prKey, t.Kind, stepID)
	if profile.Watch != nil {
		e.startHandoffWatch(ctx, runCtx, cancel, t, stepID, identity, agentID, prKey, profile, ch, actions)
	}
	if d := time.Duration(profile.IdleTimeout); d > 0 {
		e.startIdleTimer(ctx, runCtx, t, stepID, agentID, d)
	}
	return runCtx, cancel
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

// StepOps exposes step.done (wired in main via connector.SetStepOps).
func (e *Engine) StepOps() *connector.StepOps {
	return &connector.StepOps{Done: e.doneSignal}
}

// doneSignal is THE shared done handler behind step.done and handoff.done:
// deliver the output first when one rides the call (a schema dispatch is
// waiting on it, and a validation failure must reach the agent as THE error —
// it fixes the value and calls again; done semantics don't run yet), then the
// done/teardown/archive path. hasOutput, not a nil check: false/0/"" are real
// deliveries for boolean/number/string schemas.
func (e *Engine) doneSignal(ctx context.Context, dispatchID, agentID, reason string, output any, hasOutput bool) error {
	if reason == "" {
		reason = "agent signalled done"
	}
	if hasOutput {
		found, err := e.disp.DeliverOutput(dispatchID, output)
		if err != nil {
			return err
		}
		if !found {
			e.log("done: output supplied but no schema dispatch is waiting (dispatch %s) — ignored", dispatchID)
		}
	}
	return e.StepDone(ctx, dispatchID, agentID, reason)
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
//     only: the step boundary reclaims it after the output is captured (see
//     TakeEarlyDone), so a done call can never cut off the output conductor is
//     waiting on. This resolves even before the dispatch is bound to its agent:
//     a foreground paseo run reports the agent id only when it returns, so an
//     agent calling step.done as its last action is always ahead of the binding;
//   - otherwise → archive the agent + its workspace now, through the runner
//     that opened it (ledger-gated: it refuses anything conductor didn't
//     launch).
//
// "In flight" and "bound" cover every runtime: the paseo dispatcher's ledger,
// and the engine's own record of a controller-runtime (cli/acp/opencode)
// dispatch (dispatchAgent).
func (e *Engine) StepDone(ctx context.Context, dispatchID, agentID, reason string) error {
	if resolved := e.agentForDispatch(dispatchID); resolved != "" {
		agentID = resolved
	}
	inFlight := e.dispatchInFlight(dispatchID)
	if agentID == "" && !inFlight {
		return fmt.Errorf("step.done: no conductor-launched agent for this session")
	}
	if agentID != "" {
		if lh := e.lookupLiveHandoff(agentID); lh != nil {
			return e.handoffDone(ctx, agentID, reason)
		}
	}
	if inFlight {
		e.earlyDone.Store(dispatchID, reason)
		e.log("step.done from dispatch %s (%s) — still in flight; reclaiming at step boundary", dispatchID, reason)
		return nil
	}
	e.log("step.done from agent %s: %s — reclaiming", agentID, reason)
	// Through the runner that opened it (a cli agent is not paseo's).
	return e.archiveAgent(context.WithoutCancel(ctx), agentID)
}

// TakeEarlyDone reports, and clears, whether the dispatch's agent called
// step.done while conductor was still blocked on it. The step boundary consults
// it once the output is captured: an agent that signalled done is reclaimed
// even when its step keeps agents (archive_when_done: false), since nothing
// else would ever archive it.
func (e *Engine) TakeEarlyDone(dispatchID string) bool {
	if dispatchID == "" {
		return false
	}
	_, ok := e.earlyDone.LoadAndDelete(dispatchID)
	return ok
}
