package engine

import (
	"context"
	"time"

	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// maxDeferredReemits bounds how many times ONE (target, kind#variant, reason)
// is re-scheduled after a rate_limited/not_ready credential mint or
// remediation call (plugin-contract.md §1.11): without a cap, a plugin that
// never recovers would re-emit the same trigger forever.
const maxDeferredReemits = 20

// deferAndReemit is process()'s and remediate()'s answer to a rate_limited or
// not_ready contract error on their own synchronous paths (mint, remediation
// status/action): instead of blocking the engine's single dispatch loop in a
// sleep (RetryContract), it schedules t to be re-emitted after the error's
// own wait, via time.AfterFunc → e.Emit, and returns immediately so Run's
// `for t := <-e.ch` loop keeps dispatching every OTHER queued trigger in the
// meantime.
//
// reason distinguishes independent callers sharing the same trigger (a mint
// vs. a remediation status read) so their re-emit budgets don't interfere.
// It reports whether it scheduled a re-emit; false means the error was not
// deferrable (not rate_limited/not_ready, no usable retry_after, or the
// bounded schedule/attempt count is exhausted) and the caller should fall
// back to its normal error handling.
func (e *Engine) deferAndReemit(ctx context.Context, t core.Trigger, reason string, err error) bool {
	ce, ok := connector.AsContractError(err)
	if !ok || (!ce.IsRateLimited() && !ce.IsNotReady()) {
		return false
	}
	dk := t.Key() + "|" + t.Kind + "#" + t.Variant + "|" + reason
	e.deferMu.Lock()
	attempt := e.deferCounts[dk]
	wait, deferrable := deferralWait(ce, attempt)
	if !deferrable || attempt >= maxDeferredReemits {
		delete(e.deferCounts, dk)
		e.deferMu.Unlock()
		return false
	}
	e.deferCounts[dk] = attempt + 1
	e.deferMu.Unlock()

	e.log("%s %s %s — re-emitting in %s instead of blocking the dispatch loop (attempt %d/%d)",
		tag(t), reason, ce.Error(), wait.Round(time.Millisecond), attempt+1, maxDeferredReemits)
	e.store.Audit(map[string]any{
		"event": "dispatch_deferred", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "reason": reason, "wait": wait.String(), "attempt": attempt + 1,
	})
	time.AfterFunc(wait, func() {
		if ctx.Err() != nil {
			return // shutting down: don't re-emit into a dead engine
		}
		e.clearDeferred(dk)
		e.Emit(ctx, t)
	})
	return true
}

// clearDeferred drops dk's re-emit counter once its trigger is back in the
// queue, so a subsequent, UNRELATED rate_limited/not_ready answer for the
// same (target, kind, reason) gets its own full budget rather than picking
// up where this one left off.
func (e *Engine) clearDeferred(dk string) {
	e.deferMu.Lock()
	delete(e.deferCounts, dk)
	e.deferMu.Unlock()
}

// deferralWait computes the wait before the attempt-th (0-based) deferred
// re-emit for ce, and whether to defer at all:
//
//   - rate_limited: data.retry_after, capped at connector.RateLimitCap — the
//     same cap RetryContract itself applies. No retry_after at all means
//     there is nothing to wait on, matching RetryContract's own bail-out.
//   - not_ready: connector.NotReadyBackoff's fixed schedule, by attempt
//     index; once attempt runs past the schedule, it is no longer
//     deferrable (matches RetryContract's own give-up point).
func deferralWait(ce *connector.ContractError, attempt int) (time.Duration, bool) {
	switch {
	case ce.IsRateLimited():
		wait, has := ce.RetryAfter()
		if !has || wait <= 0 {
			return 0, false
		}
		if wait > connector.RateLimitCap {
			wait = connector.RateLimitCap
		}
		return wait, true
	case ce.IsNotReady():
		sched := connector.NotReadyBackoff
		if attempt >= len(sched) {
			return 0, false
		}
		return sched[attempt], true
	}
	return 0, false
}
