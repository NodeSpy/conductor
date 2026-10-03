package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// TestDeferredReemitCapsConsecutiveRateLimitedAnswers is finding 3's
// regression test: a plugin answering rate_limited FOREVER must stop being
// re-emitted once maxDeferredReemits is reached, not forever.
//
// The bug: deferAndReemit's time.AfterFunc callback cleared the
// (target, kind, reason) attempt counter BEFORE calling e.Emit to re-enqueue
// the trigger. So by the time the re-emitted trigger reached deferAndReemit
// again, e.deferCounts[dk] had already been reset to 0 — maxDeferredReemits
// was compared against an attempt count that could never advance past where
// it started, so it never tripped. The fix clears the counter only once a
// later attempt gets PAST the deferral point (success, or a different,
// non-deferrable error) — clearDeferredFor, called by process()/remediate(),
// never by the AfterFunc itself.
func TestDeferredReemitCapsConsecutiveRateLimitedAnswers(t *testing.T) {
	oldMin := minDeferredWait
	minDeferredWait = time.Millisecond
	t.Cleanup(func() { minDeferredWait = oldMin })

	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, baseCfg(), d, n, nil)
	var calls int32
	e.invokeVerb = func(context.Context, string, string, map[string]any) (map[string]any, error) {
		atomic.AddInt32(&calls, 1)
		return nil, &connector.ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1ms"}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	tr := agentTrigger("merge_conflict", "a/w", 1, "h", "dedup_cap", config.Action{Type: "agent", Agent: "w/fixer", Prompt: "go"})
	e.Emit(ctx, tr)

	// maxDeferredReemits successful defers, then the (maxDeferredReemits+1)th
	// attempt hits the cap and gives up. The fixture forge declares TWO
	// credentials (read, write): declaredCredentials mints both on every
	// attempt before returning, so invokeVerb is called twice per attempt
	// even though they share one "credential mint" re-emit budget (one
	// deferAndReemit call per attempt, keyed on the trigger+reason, not per
	// credential).
	want := int32(2 * (maxDeferredReemits + 1))
	waitForAtLeast(t, &calls, want, 2*time.Second, "the deferred re-emits never reached the cap")
	// Give a (buggy) further re-emit every chance to land, then confirm it
	// stopped EXACTLY at the cap rather than continuing forever.
	time.Sleep(200 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != want {
		t.Fatalf("invokeVerb called %d times, want exactly %d (%d deferred re-emits then the cap, x2 credentials) — the re-emit cap never stopped it",
			got, want, maxDeferredReemits)
	}
}

// TestDeferredReemitEnforcesAMinimumDelay is finding 3's second requirement:
// even a plugin-claimed retry_after far below a sane floor must not turn
// deferred re-emits into a hot loop.
func TestDeferredReemitEnforcesAMinimumDelay(t *testing.T) {
	wait, ok := deferralWait(&connector.ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1ns"}}, 0)
	if !ok {
		t.Fatal("a tiny positive retry_after must still be deferrable")
	}
	if wait < minDeferredWait {
		t.Fatalf("wait = %s, want at least minDeferredWait (%s)", wait, minDeferredWait)
	}
}
