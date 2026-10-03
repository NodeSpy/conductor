package connector

import (
	"context"
	"time"
)

// RateLimitCap bounds a CodeRateLimited wait (§1.11) regardless of what the
// plugin's retry_after claims: a confused or hostile plugin must never park
// a caller for longer than this. A var (not a const) so a test can shrink it
// rather than actually waiting; restore it when done.
var RateLimitCap = 15 * time.Minute

// NotReadyBackoff is the bounded short backoff for CodeNotReady (§1.11): a
// fixed, short schedule, then give up. A var for the same reason as
// RateLimitCap.
var NotReadyBackoff = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second}

// Sleep is the ctx-aware wait every RetryContract caller shares. A var so
// tests drive it without blocking for real durations; restore it when done.
var Sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RetryContract runs invoke, honoring the two plugin contract codes (§1.11)
// that are retried independently of a caller's own retry policy:
//
//   - rate_limited waits data.retry_after (capped at RateLimitCap), then
//     retries — for as long as the plugin keeps answering rate_limited,
//     bounded only by ctx. There is no knob to make a caller give up on a
//     plugin that is honestly reporting it is still rate-limited.
//   - not_ready retries on NotReadyBackoff's fixed, short schedule, then
//     gives up.
//
// Every other outcome — success, a non-contract error, target_gone, invalid,
// or upstream — is returned to the caller untouched: those are the caller's
// own decision (stop the run, never retry, retry only if retryable and the
// caller's own policy allows it).
func RetryContract(ctx context.Context, invoke func() (map[string]any, error)) (map[string]any, error) {
	notReadyAttempt := 0
	for {
		out, err := invoke()
		if err == nil {
			return out, nil
		}
		ce, ok := AsContractError(err)
		if !ok {
			return nil, err
		}
		switch {
		case ce.IsRateLimited():
			wait, has := ce.RetryAfter()
			if !has || wait <= 0 {
				return nil, err
			}
			if wait > RateLimitCap {
				wait = RateLimitCap
			}
			if serr := Sleep(ctx, wait); serr != nil {
				return nil, err
			}
		case ce.IsNotReady():
			if notReadyAttempt >= len(NotReadyBackoff) {
				return nil, err
			}
			if serr := Sleep(ctx, NotReadyBackoff[notReadyAttempt]); serr != nil {
				return nil, err
			}
			notReadyAttempt++
		default:
			return nil, err
		}
	}
}
