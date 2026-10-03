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
		capped := attempt >= maxDeferredReemits
		delete(e.deferCounts, dk)
		e.deferMu.Unlock()
		if capped {
			// The cap, not an undeferrable error, is what stopped this —
			// worth its own log/audit line distinct from the ordinary
			// dispatch_failed the caller logs next, so a plugin stuck
			// answering rate_limited/not_ready forever is visible as
			// exactly that rather than as an opaque failure.
			e.log("%s %s: %d consecutive deferred re-emits reached the cap — giving up", tag(t), reason, maxDeferredReemits)
			e.store.Audit(map[string]any{
				"event": "dispatch_deferred_cap", "repo": t.Target.Repo, "number": t.Target.Number,
				"kind": t.Kind, "reason": reason, "attempts": maxDeferredReemits,
			})
		}
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
		// Deliberately NOT clearing dk here: clearing on re-enqueue (rather
		// than when processing actually gets past the deferral point) is
		// what let maxDeferredReemits never cap at all — the re-emitted
		// trigger's very next deferAndReemit call would read attempt back at
		// 0 regardless of how many times this had already happened. The
		// counter is cleared by clearDeferredFor, called once t's processing
		// for reason succeeds or hits a different, non-deferrable error (see
		// its doc).
		e.Emit(ctx, t)
	})
	return true
}

// clearDeferredFor clears t's re-emit counter for reason once its processing
// gets PAST the deferral point — a successful mint/dispatch, or a different,
// non-deferrable error — rather than merely being re-enqueued (see
// deferAndReemit's AfterFunc, which deliberately does not clear). Call sites:
// process() after credentialsFor resolves (success or a non-deferred error)
// and remediate() after each of its two deferAndReemit checks falls through.
// Without this, a later, UNRELATED rate_limited/not_ready answer for the
// same (target, kind, reason) would inherit whatever count an earlier,
// already-resolved occurrence left behind instead of its own full budget.
func (e *Engine) clearDeferredFor(t core.Trigger, reason string) {
	e.clearDeferred(t.Key() + "|" + t.Kind + "#" + t.Variant + "|" + reason)
}

// clearDeferred drops dk's re-emit counter.
func (e *Engine) clearDeferred(dk string) {
	e.deferMu.Lock()
	delete(e.deferCounts, dk)
	e.deferMu.Unlock()
}

// minDeferredWait is the floor on every deferred re-emit's wait: a plugin
// answering rate_limited with a tiny retry_after (or a hostile one claiming
// 0 but somehow reaching here, or a clock/unit mistake) must never turn
// deferAndReemit into a hot loop of re-emits. A var (not a const), like
// connector.RateLimitCap, so a test shrinks it rather than actually waiting;
// restore it when done.
var minDeferredWait = time.Second

// deferralWait computes the wait before the attempt-th (0-based) deferred
// re-emit for ce, and whether to defer at all:
//
//   - rate_limited: data.retry_after, capped at connector.RateLimitCap — the
//     same cap RetryContract itself applies. No retry_after at all means
//     there is nothing to wait on, matching RetryContract's own bail-out.
//   - not_ready: connector.NotReadyBackoff's fixed schedule, by attempt
//     index; once attempt runs past the schedule, it is no longer
//     deferrable (matches RetryContract's own give-up point).
//
// Either way the result is floored at minDeferredWait.
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
		if wait < minDeferredWait {
			wait = minDeferredWait
		}
		return wait, true
	case ce.IsNotReady():
		sched := connector.NotReadyBackoff
		if attempt >= len(sched) {
			return 0, false
		}
		wait := sched[attempt]
		if wait < minDeferredWait {
			wait = minDeferredWait
		}
		return wait, true
	}
	return 0, false
}
