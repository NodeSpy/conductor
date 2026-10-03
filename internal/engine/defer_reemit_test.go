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

// TestDeferredReemitCounterResetsAfterASuccessStartsFreshStreak is the
// missing half of the cap test above: deleting the success-path
// e.clearDeferredFor(t, "credential mint") call in process() (engine.go,
// right after credentialsFor resolves) fails no existing test, yet without
// it a (target, kind, reason) that was deferred a few times, then recovered,
// would carry its old attempt count into a LATER, unrelated rate_limited
// streak instead of getting a fresh budget — maxDeferredReemits would be a
// lifetime cap across every recovery instead of a per-streak one.
//
// This drives the real process() path (not deferAndReemit/clearDeferredFor
// directly) so a regression at the actual call site is what the test catches.
func TestDeferredReemitCounterResetsAfterASuccessStartsFreshStreak(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, baseCfg(), d, n, nil)

	rateLimited := func(context.Context, string, string, map[string]any) (map[string]any, error) {
		return nil, &connector.ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1ms"}}
	}
	// read_token/write_token both succeed; get_run/rerun_run are unused here.
	mintSucceeds := remediationVerbs(nil, func() string { return "completed" })

	act := config.Action{Type: "agent", Agent: "w/fixer", Prompt: "go"}
	tr1 := agentTrigger("merge_conflict", "a/w", 1, "h", "sig-1", act)
	dk := tr1.Key() + "|" + tr1.Kind + "#" + tr1.Variant + "|credential mint"

	// Streak 1: a handful of deferred rate_limited mint answers, well under
	// the cap. Credential mint failures never reach dispatch, so these never
	// record an attempt or a dedup signature.
	const streak1 = 5
	e.invokeVerb = rateLimited
	for i := 0; i < streak1; i++ {
		e.process(context.Background(), tr1)
	}
	if len(d.reqs) != 0 {
		t.Fatalf("rate_limited mint must not dispatch, got %d dispatches", len(d.reqs))
	}
	if got := e.deferCounts[dk]; got != streak1 {
		t.Fatalf("after streak 1, deferCounts[%q] = %d, want %d", dk, got, streak1)
	}

	// The mint then succeeds: process() must get PAST the deferral point and
	// dispatch, clearing the counter via the success-path clearDeferredFor
	// call (engine.go ~989).
	e.invokeVerb = mintSucceeds
	e.process(context.Background(), tr1)
	if len(d.reqs) != 1 {
		t.Fatalf("a successful mint should have dispatched, got %d", len(d.reqs))
	}
	// merge_conflict is level-triggered (completion.level in the fixture
	// forge's decl): a successful dispatch records an ATTEMPT, not a dedup
	// signature, so tr2 below (a later, different delivery) is never gated
	// on a recorded signature either way — only deferCounts is this test's
	// concern.
	if _, ok := e.deferCounts[dk]; ok {
		t.Fatalf("a successful mint did not clear deferCounts[%q]", dk)
	}

	// A LATER, different delivery for the same target/kind (a different
	// Dedup, so the dedup gate doesn't suppress it; everything else — repo,
	// PR, kind, head — unchanged, so it shares the SAME re-emit budget key)
	// starts its own rate_limited streak. With the counter correctly reset,
	// it gets a full maxDeferredReemits budget: every one of these must defer.
	tr2 := agentTrigger("merge_conflict", "a/w", 1, "h", "sig-2", act)
	if dk2 := tr2.Key() + "|" + tr2.Kind + "#" + tr2.Variant + "|credential mint"; dk2 != dk {
		t.Fatalf("test setup: tr2 must share tr1's re-emit key, got %q want %q", dk2, dk)
	}
	e.invokeVerb = rateLimited
	for i := 0; i < maxDeferredReemits; i++ {
		e.process(context.Background(), tr2)
	}
	if len(d.reqs) != 1 {
		t.Fatalf("streak 2 must still be deferring, not dispatching: got %d dispatches", len(d.reqs))
	}
	// A lifetime counter (the bug) would have capped partway through streak 2
	// (it would have inherited streak 1's leftover count instead of starting
	// at 0) and deleted dk's entry on give-up; the fix reaches the end of a
	// full fresh budget still counting.
	if got := e.deferCounts[dk]; got != maxDeferredReemits {
		t.Fatalf("streak 2 deferCounts[%q] = %d, want %d (a fresh budget) — the cap is per streak, not lifetime (leftover count from streak 1 leaked through)",
			dk, got, maxDeferredReemits)
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
