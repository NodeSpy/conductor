package connector

import (
	"context"
	"errors"
	"testing"
	"time"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// withFastRetry replaces Sleep with a no-op that records every requested
// duration, and restores it on cleanup — every RetryContract test uses this
// so none of them actually blocks for seconds/minutes.
func withFastRetry(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	origSleep, origCap, origBackoff := Sleep, RateLimitCap, NotReadyBackoff
	Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	t.Cleanup(func() { Sleep, RateLimitCap, NotReadyBackoff = origSleep, origCap, origBackoff })
	return &waits
}

func TestRetryContractSuccessNoRetry(t *testing.T) {
	withFastRetry(t)
	calls := 0
	out, err := RetryContract(context.Background(), func() (map[string]any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	if err != nil || calls != 1 || out["ok"] != true {
		t.Fatalf("out=%v err=%v calls=%d", out, err, calls)
	}
}

func TestRetryContractRateLimitedThenSucceeds(t *testing.T) {
	waits := withFastRetry(t)
	calls := 0
	out, err := RetryContract(context.Background(), func() (map[string]any, error) {
		calls++
		if calls < 3 {
			return nil, &ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "5s"}}
		}
		return map[string]any{"done": true}, nil
	})
	if err != nil || calls != 3 || out["done"] != true {
		t.Fatalf("out=%v err=%v calls=%d", out, err, calls)
	}
	if len(*waits) != 2 || (*waits)[0] != 5*time.Second || (*waits)[1] != 5*time.Second {
		t.Fatalf("waits = %v, want two 5s waits", *waits)
	}
}

func TestRetryContractRateLimitedCapped(t *testing.T) {
	waits := withFastRetry(t)
	RateLimitCap = 1 * time.Minute
	calls := 0
	_, err := RetryContract(context.Background(), func() (map[string]any, error) {
		calls++
		if calls < 2 {
			return nil, &ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1h"}}
		}
		return map[string]any{}, nil
	})
	if err != nil || calls != 2 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if len(*waits) != 1 || (*waits)[0] != 1*time.Minute {
		t.Fatalf("waits = %v, want one capped 1m wait (not the claimed 1h)", *waits)
	}
}

func TestRetryContractRateLimitedNoRetryAfterGivesUp(t *testing.T) {
	withFastRetry(t)
	calls := 0
	_, err := RetryContract(context.Background(), func() (map[string]any, error) {
		calls++
		return nil, &ContractError{Code: sdk.CodeRateLimited} // no data.retry_after
	})
	ce, ok := AsContractError(err)
	if !ok || !ce.IsRateLimited() || calls != 1 {
		t.Fatalf("err=%v calls=%d, want a single call and the error surfaced (nothing to wait on)", err, calls)
	}
}

func TestRetryContractNotReadyBoundedThenGivesUp(t *testing.T) {
	NotReadyBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	waits := withFastRetry(t)
	calls := 0
	_, err := RetryContract(context.Background(), func() (map[string]any, error) {
		calls++
		return nil, &ContractError{Code: sdk.CodeNotReady}
	})
	ce, ok := AsContractError(err)
	if !ok || !ce.IsNotReady() {
		t.Fatalf("want a surfaced not_ready error, got %v", err)
	}
	// 3 backoff slots → 1 initial call + 3 retries = 4 calls total.
	if calls != 4 || len(*waits) != 3 {
		t.Fatalf("calls=%d waits=%v, want 4 calls and 3 waits (bounded, then give up)", calls, *waits)
	}
}

func TestRetryContractNotReadyThenSucceeds(t *testing.T) {
	withFastRetry(t)
	calls := 0
	out, err := RetryContract(context.Background(), func() (map[string]any, error) {
		calls++
		if calls < 2 {
			return nil, &ContractError{Code: sdk.CodeNotReady}
		}
		return map[string]any{"ready": true}, nil
	})
	if err != nil || calls != 2 || out["ready"] != true {
		t.Fatalf("out=%v err=%v calls=%d", out, err, calls)
	}
}

// target_gone, invalid, upstream and a plain non-contract error must all
// pass straight through on the FIRST call — RetryContract only ever retries
// rate_limited/not_ready; every other outcome is the caller's own decision.
func TestRetryContractPassesThroughOtherCodes(t *testing.T) {
	withFastRetry(t)
	cases := []error{
		&ContractError{Code: sdk.CodeTargetGone},
		&ContractError{Code: sdk.CodeInvalid},
		&ContractError{Code: sdk.CodeUpstream, Data: map[string]any{"retryable": true}},
		errors.New("not a contract error at all"),
	}
	for _, want := range cases {
		calls := 0
		_, err := RetryContract(context.Background(), func() (map[string]any, error) {
			calls++
			return nil, want
		})
		if err != want || calls != 1 {
			t.Fatalf("for %v: err=%v calls=%d, want exactly one call and the error untouched", want, err, calls)
		}
	}
}

// A plugin that keeps answering rate_limited forever must not park its
// caller forever: once the cumulative wait crosses RateLimitBudget, the
// error is surfaced instead of sleeping again.
func TestRetryContractRateLimitedBudgetExhausted(t *testing.T) {
	waits := withFastRetry(t)
	origBudget := RateLimitBudget
	RateLimitBudget = 25 * time.Second
	t.Cleanup(func() { RateLimitBudget = origBudget })
	calls := 0
	_, err := RetryContract(context.Background(), func() (map[string]any, error) {
		calls++
		return nil, &ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "10s"}}
	})
	ce, ok := AsContractError(err)
	if !ok || !ce.IsRateLimited() {
		t.Fatalf("want a surfaced rate_limited error once the budget is exhausted, got %v", err)
	}
	// 10s waits: 10, 20 (<=25 budget so it retries), 30 would exceed 25 →
	// stop before a third wait. So exactly 2 waits, 3 calls.
	if calls != 3 || len(*waits) != 2 {
		t.Fatalf("calls=%d waits=%v, want 3 calls and 2 waits before the 30s cumulative total exceeds the 25s budget", calls, *waits)
	}
}

// A plugin answering a very short retry_after many, many times must still
// eventually give up, bounded by RateLimitAttemptCap, even though each wait
// individually stays well under RateLimitBudget.
func TestRetryContractRateLimitedAttemptCapExhausted(t *testing.T) {
	withFastRetry(t)
	origCap := RateLimitAttemptCap
	RateLimitAttemptCap = 3
	t.Cleanup(func() { RateLimitAttemptCap = origCap })
	calls := 0
	_, err := RetryContract(context.Background(), func() (map[string]any, error) {
		calls++
		return nil, &ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1ms"}}
	})
	ce, ok := AsContractError(err)
	if !ok || !ce.IsRateLimited() {
		t.Fatalf("want a surfaced rate_limited error once the attempt cap is exhausted, got %v", err)
	}
	if calls != 4 {
		t.Fatalf("calls=%d, want 1 initial + 3 capped retries = 4", calls)
	}
}

// ctx cancellation during a rate_limited/not_ready wait must stop the retry
// loop rather than spin or block past shutdown.
func TestRetryContractStopsOnCtxCancel(t *testing.T) {
	origSleep := Sleep
	Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	t.Cleanup(func() { Sleep = origSleep })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := RetryContract(ctx, func() (map[string]any, error) {
		calls++
		return nil, &ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1s"}}
	})
	if calls != 1 {
		t.Fatalf("calls=%d, want exactly one call before the cancelled sleep stops the loop", calls)
	}
	ce, ok := AsContractError(err)
	if !ok || !ce.IsRateLimited() {
		t.Fatalf("want the last rate_limited error surfaced on cancellation, got %v", err)
	}
}
