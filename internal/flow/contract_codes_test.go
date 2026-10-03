package flow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// Every test below scripts the "fake" connector's "post" verb (helpers_test.go)
// via fakeState.errFn, a per-call error script that — unlike failTimes/failIf,
// which only ever produce a generic failure — can answer a specific plugin
// contract error (plugin-contract.md §1.11) directly, including a
// *connector.ContractError with no plugin wire round trip (AsContractError
// resolves it the same way it would one that crossed a real transport).

// -32011 target_gone stops the run (stop hooks, no failure, never retried) —
// exactly like a dispatch-detected target closure (dispatch.ErrTargetClosed).
func TestVerbStepTargetGoneStopsNotFails(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["post"] = func(int, map[string]any) error {
		// data.target must name the trigger's own key (finding 11):
		// newTrigger's is "o/r#7".
		return &connector.ContractError{Code: sdk.CodeTargetGone, Message: "the PR closed", Data: map[string]any{"target": "o/r#7"}}
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
    retry: { max: 2, backoff: 1ms }
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("target_gone must stop the run, not fail it: %s", errStr)
	}
	if got := st.count("post"); got != 1 {
		t.Fatalf("target_gone must never retry (even with retry: configured), got %d calls", got)
	}
	if len(rig.Store.auditsWithEvent("workflow_stopped")) != 1 {
		t.Fatal("expected one workflow_stopped audit")
	}
}

// -32012 invalid never retries, even with a generous retry: budget.
func TestVerbStepInvalidNeverRetries(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["post"] = func(int, map[string]any) error {
		return &connector.ContractError{Code: sdk.CodeInvalid, Message: "the request can never succeed"}
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
    retry: { max: 3, backoff: 1ms }
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("invalid must fail the run")
	}
	if got := st.count("post"); got != 1 {
		t.Fatalf("invalid must never retry, got %d calls (retry: max=3 configured)", got)
	}
}

// -32010 upstream retries only when BOTH data.retryable is true AND the
// step's own retry: allows it.
func TestVerbStepUpstreamRetryableRespectsStepRetry(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["post"] = func(idx int, _ map[string]any) error {
		if idx < 2 {
			return &connector.ContractError{Code: sdk.CodeUpstream, Data: map[string]any{"status": 503, "retryable": true}}
		}
		return nil
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
    retry: { max: 2, backoff: 1ms }
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("a retryable upstream error within budget must eventually succeed: %s", errStr)
	}
	if got := st.count("post"); got != 3 {
		t.Fatalf("want 3 attempts (2 retryable failures + success), got %d", got)
	}
}

// An upstream error explicitly marked NOT retryable must never be retried,
// even though the step configured a retry: budget — the plugin's own
// retryable flag overrides the step's willingness to retry.
func TestVerbStepUpstreamNotRetryableIgnoresStepRetry(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["post"] = func(int, map[string]any) error {
		return &connector.ContractError{Code: sdk.CodeUpstream, Data: map[string]any{"status": 400, "retryable": false}}
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
    retry: { max: 3, backoff: 1ms }
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("a non-retryable upstream error must fail the run")
	}
	if got := st.count("post"); got != 1 {
		t.Fatalf("a non-retryable upstream error must never retry, got %d calls (retry: max=3 configured)", got)
	}
}

// An upstream error with data.retryable ABSENT entirely (not even false)
// must fail closed exactly like an explicit false: UpstreamRetryable treats
// a missing field as not-retryable, so a plugin that forgets to set it never
// gets retried just because the step configured a retry: budget.
func TestVerbStepUpstreamAbsentRetryableIsNotStepRetried(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["post"] = func(int, map[string]any) error {
		return &connector.ContractError{Code: sdk.CodeUpstream, Data: map[string]any{"status": 500}} // no "retryable" key at all
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
    retry: { max: 3, backoff: 1ms }
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("an upstream error with no retryable field must fail the run (fail closed)")
	}
	if got := st.count("post"); got != 1 {
		t.Fatalf("an absent retryable field must never retry, got %d calls (retry: max=3 configured)", got)
	}
}

// -32013 rate_limited is retried after data.retry_after independently of the
// step's own retry: policy — here there is NO retry: block at all, and it
// still recovers.
func TestVerbStepRateLimitedRetriedWithoutStepRetryConfigured(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["post"] = func(idx int, _ map[string]any) error {
		if idx < 2 {
			return &connector.ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1ms"}}
		}
		return nil
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("rate_limited must retry and recover with no retry: block, got: %s", errStr)
	}
	if got := st.count("post"); got != 3 {
		t.Fatalf("want 3 attempts (2 rate_limited + success), got %d", got)
	}
}

// -32014 not_ready retries on a short, bounded schedule — independent of
// retry: — then gives up and fails the run.
func TestVerbStepNotReadyBoundedThenFails(t *testing.T) {
	origBackoff := connector.NotReadyBackoff
	connector.NotReadyBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { connector.NotReadyBackoff = origBackoff })

	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["post"] = func(int, map[string]any) error {
		return &connector.ContractError{Code: sdk.CodeNotReady}
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("a not_ready verb that never becomes ready must eventually fail the run")
	}
	want := 1 + len(connector.NotReadyBackoff)
	if got := st.count("post"); got != want {
		t.Fatalf("want %d calls (1 + %d bounded retries), got %d", want, len(connector.NotReadyBackoff), got)
	}
}

// -32013 rate_limited, once RetryContract's own bounded retry is exhausted
// (no usable retry_after here, so it gives up after the first call), must
// NOT be retried again by the step's own retry: — the contract layer already
// owns and exhausted this code's retry (and its budget, §1.11); retrying it
// again at the step level would compound the wait on every step attempt.
func TestVerbStepRateLimitedExhaustedNeverStepRetried(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["post"] = func(int, map[string]any) error {
		// No data.retry_after: RetryContract has nothing to wait on and gives
		// up on the very first call (TestRetryContractRateLimitedNoRetryAfterGivesUp).
		return &connector.ContractError{Code: sdk.CodeRateLimited}
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
    retry: { max: 3, backoff: 1ms }
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("an exhausted rate_limited must fail the run")
	}
	if got := st.count("post"); got != 1 {
		t.Fatalf("rate_limited must never be retried a second time by the step's own retry:, got %d calls (retry: max=3 configured)", got)
	}
}

// -32014 not_ready, once RetryContract's own bounded schedule is exhausted,
// must likewise not be retried again by the step's own retry:.
func TestVerbStepNotReadyExhaustedNeverStepRetried(t *testing.T) {
	origBackoff := connector.NotReadyBackoff
	connector.NotReadyBackoff = nil // empty schedule: RetryContract gives up after the first call
	t.Cleanup(func() { connector.NotReadyBackoff = origBackoff })

	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["post"] = func(int, map[string]any) error {
		return &connector.ContractError{Code: sdk.CodeNotReady}
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
    retry: { max: 3, backoff: 1ms }
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("an exhausted not_ready must fail the run")
	}
	if got := st.count("post"); got != 1 {
		t.Fatalf("not_ready must never be retried a second time by the step's own retry:, got %d calls (retry: max=3 configured)", got)
	}
}

// Hooks are best-effort: rate_limited/not_ready are retried the same bounded
// way as a verb step's own invoke (self-correcting, worth the short wait),
// so a hook that hits a transient contract error still ends up firing.
func TestHookRetriesRateLimitedThenSucceeds(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["notify"] = func(idx int, _ map[string]any) error {
		if idx < 1 {
			return &connector.ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1ms"}}
		}
		return nil
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
    hooks:
      - at: done
        uses: svc.notify
        options: { text: done }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if got := st.count("notify"); got != 2 {
		t.Fatalf("want 2 hook invocations (1 rate_limited + success), got %d", got)
	}
	for _, e := range rig.Store.auditsWithEvent("verb") {
		if e["verb"] == "notify" && e["outcome"] == "hook_failed" {
			t.Fatalf("the hook must have succeeded after the bounded retry, got a hook_failed audit: %+v", e)
		}
	}
}

// A hook is best-effort: target_gone (or any other code) there is logged and
// skipped like any other hook failure — it does not retroactively turn the
// run's own already-decided outcome into a stop, and it does not fail the
// run either.
func TestHookTargetGoneIsBestEffortNotAStop(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.errFn["notify"] = func(int, map[string]any) error {
		return &connector.ContractError{Code: sdk.CodeTargetGone}
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
    hooks:
      - at: done
        uses: svc.notify
        options: { text: done }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("a hook's own target_gone must not fail the run: %s", errStr)
	}
	if len(rig.Store.auditsWithEvent("workflow_stopped")) != 0 {
		t.Fatal("a hook's own target_gone must not retroactively stop the run")
	}
	found := false
	for _, e := range rig.Store.auditsWithEvent("verb") {
		if e["verb"] == "notify" && e["outcome"] == "hook_failed" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a best-effort hook_failed audit for the hook's own target_gone")
	}
}

// A skill verb (an agent's own live tool call) gets the same transparent
// rate_limited/not_ready retry as any other invoke.
func TestSkillVerbRetriesNotReadyThenSucceeds(t *testing.T) {
	origBackoff := connector.NotReadyBackoff
	connector.NotReadyBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { connector.NotReadyBackoff = origBackoff })

	rig, st := skillRig(t)
	st.errFn["post"] = func(idx int, _ map[string]any) error {
		if idx < 1 {
			return &connector.ContractError{Code: sdk.CodeNotReady}
		}
		return nil
	}
	id := SkillIdentity{Agent: "a", Verbs: []string{"svc.post"}}
	out, err := rig.Runner.RunSkillVerb(context.Background(), id, "svc.post", map[string]any{"text": "x"})
	if err != nil {
		t.Fatalf("expected the retry to recover: %v", err)
	}
	if out["id"] == nil {
		t.Fatalf("outputs: %+v", out)
	}
	if got := st.count("post"); got != 2 {
		t.Fatalf("want 2 calls (1 not_ready + success), got %d", got)
	}
}

// -32012 invalid is never retried on the skill-verb surface either — there
// is no retry: policy here to consult, so a single call is the whole story.
func TestSkillVerbInvalidNeverRetries(t *testing.T) {
	rig, st := skillRig(t)
	st.errFn["post"] = func(int, map[string]any) error {
		return &connector.ContractError{Code: sdk.CodeInvalid, Message: "bad input"}
	}
	id := SkillIdentity{Agent: "a", Verbs: []string{"svc.post"}}
	_, err := rig.Runner.RunSkillVerb(context.Background(), id, "svc.post", map[string]any{"text": "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := st.count("post"); got != 1 {
		t.Fatalf("invalid must never retry, got %d calls", got)
	}
	var ce *connector.ContractError
	if !errors.As(err, &ce) || !ce.IsInvalid() {
		t.Fatalf("the *connector.ContractError must still be reachable via errors.As despite the redacted message: %v", err)
	}
}

// target_gone on the skill-verb surface is tagged (dispatch.ErrTargetClosed,
// reachable via errors.Is/As) even though redactedErr keeps the returned
// TEXT redacted — redacting the message must not cost the type.
func TestSkillVerbTargetGoneIsTaggedAndRedacted(t *testing.T) {
	rig, st := skillRig(t)
	rig.Runner.Secrets.Track("s3kr1t-value")
	st.errFn["post"] = func(int, map[string]any) error {
		// data.target must name the trigger's own key (finding 11): an
		// untrusted-target, repo-less skill trigger's is "skill:skill".
		return &connector.ContractError{Code: sdk.CodeTargetGone, Message: "leak s3kr1t-value here", Data: map[string]any{"target": "skill:skill"}}
	}
	id := SkillIdentity{Agent: "a", Verbs: []string{"svc.post"}}
	_, err := rig.Runner.RunSkillVerb(context.Background(), id, "svc.post", map[string]any{"text": "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := st.count("post"); got != 1 {
		t.Fatalf("target_gone must never retry, got %d calls", got)
	}
	if !errors.Is(err, dispatch.ErrTargetClosed) {
		t.Fatalf("target_gone must be reachable via errors.Is(err, dispatch.ErrTargetClosed): %v", err)
	}
	var ce *connector.ContractError
	if !errors.As(err, &ce) || !ce.IsTargetGone() {
		t.Fatalf("the *connector.ContractError must still be reachable via errors.As: %v", err)
	}
	if got := err.Error(); got == "" || strings.Contains(got, "s3kr1t-value") {
		t.Fatalf("the returned message must be redacted, got %q", got)
	}
}

// A target with no repo (a chat message) is named by its declared key: a
// target_gone carrying that key stops the run; one naming another message
// fails it loudly.
func TestVerbStepTargetGoneMatchesTheDeclaredKey(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		stop      bool
	}{{"own message", "chat:C1:1.5", true}, {"another message", "chat:C9:9.9", false}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
			reg := buildRegistry(t, cfg)
			st := newFakeState(t, "svc")
			st.errFn["post"] = func(int, map[string]any) error {
				return &connector.ContractError{Code: sdk.CodeTargetGone, Message: "gone", Data: map[string]any{"target": tc.key}}
			}
			spec := mustSpec(t, `
on: svc.ping
steps:
  - id: post1
    uses: svc.post
    options: { text: x }
`)
			rig := newTestRunner(t, cfg, reg)
			rig.Runner.sleep = fastSleep
			tr := newTrigger("ping", map[string]any{"msg": "x"})
			tr.Target = core.Target{Key: "chat:C1:1.5"}
			runTrigger(rig, tr, spec)
			failed, errStr := rig.workflowFailed()
			if tc.stop && failed {
				t.Fatalf("target_gone for the run's own declared key must stop, not fail: %s", errStr)
			}
			if !tc.stop && !failed {
				t.Fatal("target_gone for another target must fail the run")
			}
		})
	}
}
