package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// A credential mint that answers -32011 target_gone (plugin-contract.md
// §1.11) stops the run instead of failing it: declaredCredentials wraps the
// error as dispatch.ErrTargetClosed, the same sentinel a dispatch-detected
// closure produces, so every existing stop-hook check (runSteps, flow.go)
// treats it identically.
func TestMintTargetGoneStopsNotFails(t *testing.T) {
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	e.invokeVerb = func(context.Context, string, string, map[string]any) (map[string]any, error) {
		return nil, &connector.ContractError{Code: sdk.CodeTargetGone, Message: "the PR closed"}
	}
	sem := &sdk.ConnSemantics{Credentials: []sdk.Credential{
		{Name: "w", Role: "write", Mint: sdk.CredentialMint{Verb: "write_token"}, Value: "token", Env: []string{"GH_TOKEN"}},
	}}
	tr := core.Trigger{Instance: "i", TargetTrusted: true}
	_, err := e.declaredCredentials(context.Background(), tr, "i", sem, false)
	if !errors.Is(err, dispatch.ErrTargetClosed) {
		t.Fatalf("declaredCredentials error = %v, want it to wrap dispatch.ErrTargetClosed", err)
	}
	var ce *connector.ContractError
	if !errors.As(err, &ce) || !ce.IsTargetGone() {
		t.Fatalf("the original *connector.ContractError must still be reachable via errors.As: %v", err)
	}
}

// stopAsTargetGone (internal/engine/contract.go) only turns -32011
// target_gone into a stop. invalid, a non-retryable upstream answer, and a
// not_ready during mint (mint never retries it itself — see finding 2) must
// all stay ordinary errors: a FAILURE (dispatch_failed, the caller's own
// error handling), never silently treated as a stop just because they also
// came back from a mint call.
func TestMintNonTargetGoneCodesAreFailuresNotStops(t *testing.T) {
	cases := []struct {
		name string
		err  *connector.ContractError
	}{
		{"invalid", &connector.ContractError{Code: sdk.CodeInvalid, Message: "bad request"}},
		{"upstream not retryable", &connector.ContractError{Code: sdk.CodeUpstream, Data: map[string]any{"status": 400, "retryable": false}}},
		{"not_ready", &connector.ContractError{Code: sdk.CodeNotReady}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
			e.invokeVerb = func(context.Context, string, string, map[string]any) (map[string]any, error) {
				return nil, c.err
			}
			sem := &sdk.ConnSemantics{Credentials: []sdk.Credential{
				{Name: "w", Role: "write", Mint: sdk.CredentialMint{Verb: "write_token"}, Value: "token", Env: []string{"GH_TOKEN"}},
			}}
			tr := core.Trigger{Instance: "i", TargetTrusted: true}
			_, err := e.declaredCredentials(context.Background(), tr, "i", sem, false)
			if err == nil {
				t.Fatal("expected an error")
			}
			if errors.Is(err, dispatch.ErrTargetClosed) {
				t.Fatalf("%s must be a FAILURE, not a stop (dispatch.ErrTargetClosed): %v", c.name, err)
			}
			ce, ok := connector.AsContractError(err)
			if !ok || ce.Code != c.err.Code {
				t.Fatalf("the original *connector.ContractError (code %d) must still be reachable via errors.As: %v", c.err.Code, err)
			}
		})
	}
}

// A credential mint that answers -32013 rate_limited or -32014 not_ready is
// returned to the caller on the FIRST call, never retried inside mint
// itself: mint() runs synchronously in process(), fed by the engine's single
// dispatch loop (Run's `for t := <-e.ch`), and in ResumeWorkflows' sequential
// loop, so a blocking connector.RetryContract sleep here would stall every
// OTHER queued trigger behind this one connector. The caller (process(),
// via deferAndReemit) decides what to do with the error instead — see
// TestProcessDefersRateLimitedMintAndDispatchesOtherTriggers.
func TestMintReturnsRateLimitedImmediatelyWithoutRetrying(t *testing.T) {
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	calls := 0
	e.invokeVerb = func(context.Context, string, string, map[string]any) (map[string]any, error) {
		calls++
		return nil, &connector.ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1ms"}}
	}
	sem := &sdk.ConnSemantics{Credentials: []sdk.Credential{
		{Name: "w", Role: "write", Mint: sdk.CredentialMint{Verb: "write_token"}, Value: "token", Env: []string{"GH_TOKEN"}},
	}}
	tr := core.Trigger{Instance: "i", TargetTrusted: true}
	_, err := e.declaredCredentials(context.Background(), tr, "i", sem, false)
	if calls != 1 {
		t.Fatalf("calls=%d, want exactly 1 — mint must never block this trigger's processing in a retry sleep", calls)
	}
	ce, ok := connector.AsContractError(err)
	if !ok || !ce.IsRateLimited() {
		t.Fatalf("err=%v, want the rate_limited *connector.ContractError surfaced to the caller unretried", err)
	}
}

// The reviewer's scratch test: trigger A's credential mint answers
// rate_limited; trigger B (a separate target, same connector instance) must
// dispatch immediately rather than wait behind A, proving process() never
// blocks the engine's single dispatch loop on A's mint. A itself dispatches
// too, once its deferred re-emit fires, after the delay — driven through the
// REAL e.Run loop (e.Emit, not a direct e.process call), exercising the
// actual `for t := <-e.ch` dispatch path finding 2 is about.
func TestProcessDefersRateLimitedMintAndDispatchesOtherTriggers(t *testing.T) {
	var dispatched int32
	d := &fakeDispatcher{onDispatch: func(dispatch.Request) (dispatch.RunRef, error) {
		atomic.AddInt32(&dispatched, 1)
		return dispatch.RunRef{}, nil
	}}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)

	var writeTokenCalls int32
	e.invokeVerb = func(_ context.Context, instance, verb string, opts map[string]any) (map[string]any, error) {
		if verb == "read_token" {
			return map[string]any{"token": "atok"}, nil
		}
		if verb != "write_token" {
			return nil, fmt.Errorf("unexpected verb %s", verb)
		}
		// Only trigger A's FIRST write_token call is rate-limited — opts
		// carries the templated repo, which agentTrigger sets per-trigger.
		if opts["repo"] == "a/w" && atomic.AddInt32(&writeTokenCalls, 1) == 1 {
			return nil, &connector.ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "60ms"}}
		}
		return map[string]any{"token": "fresh"}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = e.Run(ctx) }()

	trA := agentTrigger("merge_conflict", "a/w", 1, "ha", "dedup_a", config.Action{Type: "agent", Agent: "w/fixer", Prompt: "go"})
	trB := agentTrigger("merge_conflict", "b/w", 2, "hb", "dedup_b", config.Action{Type: "agent", Agent: "w/fixer", Prompt: "go"})

	start := time.Now()
	e.Emit(ctx, trA)
	e.Emit(ctx, trB)

	// B carries no mint delay at all, so it must dispatch well before A's
	// 60ms deferred re-emit would even fire.
	waitForAtLeast(t, &dispatched, 1, 2*time.Second, "trigger B never dispatched — it must not wait behind trigger A's rate-limited mint")
	if elapsed := time.Since(start); elapsed >= 60*time.Millisecond {
		t.Fatalf("first dispatch took %s, want well under A's 60ms mint delay — B must not block behind A", elapsed)
	}

	// A dispatches too, once its deferred re-emit fires.
	waitForAtLeast(t, &dispatched, 2, 2*time.Second, "trigger A never dispatched after its deferred re-emit")
	if n := atomic.LoadInt32(&writeTokenCalls); n < 2 {
		t.Fatalf("write_token calls = %d, want at least 2 (the rate_limited attempt + the re-emitted retry)", n)
	}
}

// waitForAtLeast polls *n (an atomic counter) until it reaches want or the
// timeout elapses.
func waitForAtLeast(t *testing.T, n *int32, want int32, timeout time.Duration, failMsg string) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if atomic.LoadInt32(n) >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s (got %d, want >= %d)", failMsg, atomic.LoadInt32(n), want)
		case <-time.After(time.Millisecond):
		}
	}
}

// remediate's status/action verbs stop (handled, no fixer dispatch) on
// target_gone rather than falling through to the fixer the way any other
// error does — the target that remediation and the fixer would both act on
// no longer exists.
func TestRemediateStatusTargetGoneDropsWithoutFixer(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, baseCfg(), d, n, nil)
	var rerunCalled bool
	e.invokeVerb = func(_ context.Context, _ string, verb string, _ map[string]any) (map[string]any, error) {
		switch verb {
		case "read_token", "write_token":
			return map[string]any{"token": "t"}, nil
		case "get_run":
			return nil, &connector.ContractError{Code: sdk.CodeTargetGone}
		case "rerun_run":
			rerunCalled = true
			return nil, nil
		}
		return nil, errors.New("unexpected verb " + verb)
	}
	act := config.Action{Type: "agent", Agent: "w/fixer", FlakyRerun: config.FlakyRerun{Enabled: true, Max: 1}}
	tr := agentTrigger("failing_checks", "a/w", 8, "h", "fail@h", act)
	tr.Context["run_id"] = int64(555)

	e.process(context.Background(), tr)
	if rerunCalled {
		t.Fatal("a target_gone status read must never fall through to the remedy verb")
	}
	if len(d.reqs) != 0 {
		t.Fatalf("a target_gone remediation must not dispatch the fixer, got %d dispatches", len(d.reqs))
	}
}

// remediate's ACTION verb (the remedy itself, e.g. rerun_run) answering
// target_gone stops — handled, no fixer dispatch — exactly like the
// status-verb case above: the target the remedy and the fixer would both
// act on is confirmed gone.
func TestRemediateActionTargetGoneDropsWithoutFixer(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, baseCfg(), d, n, nil)
	e.invokeVerb = func(_ context.Context, _ string, verb string, _ map[string]any) (map[string]any, error) {
		switch verb {
		case "read_token", "write_token":
			return map[string]any{"token": "t"}, nil
		case "get_run":
			return map[string]any{"status": "completed"}, nil // done: fall through to the remedy
		case "rerun_run":
			return nil, &connector.ContractError{Code: sdk.CodeTargetGone}
		}
		return nil, errors.New("unexpected verb " + verb)
	}
	act := config.Action{Type: "agent", Agent: "w/fixer", FlakyRerun: config.FlakyRerun{Enabled: true, Max: 1}}
	tr := agentTrigger("failing_checks", "a/w", 11, "h", "fail@h", act)
	tr.Context["run_id"] = int64(999)

	e.process(context.Background(), tr)
	if len(d.reqs) != 0 {
		t.Fatalf("a target_gone remedy must not dispatch the fixer, got %d dispatches", len(d.reqs))
	}
}

// remediate's status verb answering rate_limited must defer (schedule a
// re-emit) rather than block process() in a sleep (it used to run through
// connector.RetryContract, synchronously, inside the engine's single
// dispatch loop) — and it must not fall straight through to the remedy verb
// on this pass either.
func TestRemediateStatusRateLimitedDefersInsteadOfBlocking(t *testing.T) {
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, baseCfg(), d, n, nil)
	var getRunCalls, rerunCalled int32
	e.invokeVerb = func(_ context.Context, _ string, verb string, _ map[string]any) (map[string]any, error) {
		switch verb {
		case "read_token", "write_token":
			return map[string]any{"token": "t"}, nil
		case "get_run":
			if atomic.AddInt32(&getRunCalls, 1) == 1 {
				return nil, &connector.ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "30ms"}}
			}
			return map[string]any{"status": "completed"}, nil
		case "rerun_run":
			atomic.AddInt32(&rerunCalled, 1)
			return nil, nil
		}
		return nil, errors.New("unexpected verb " + verb)
	}
	act := config.Action{Type: "agent", Agent: "w/fixer", FlakyRerun: config.FlakyRerun{Enabled: true, Max: 1}}
	tr := agentTrigger("failing_checks", "a/w", 9, "h", "fail@h", act)
	tr.Context["run_id"] = int64(777)

	start := time.Now()
	e.process(context.Background(), tr)
	elapsed := time.Since(start)
	if atomic.LoadInt32(&rerunCalled) != 0 {
		t.Fatal("a rate_limited status read must defer, not fall straight through to the remedy verb")
	}
	if len(d.reqs) != 0 {
		t.Fatal("a deferred remediation must not dispatch the fixer on this pass")
	}
	if elapsed >= 30*time.Millisecond {
		t.Fatalf("process() took %s — remediate() must not block in a sleep waiting out the rate_limited retry_after", elapsed)
	}
}

// Resuming a persisted (legacy, non-flow) run whose credential mint now
// answers target_gone stops the run — deleted and audited as a stop — rather
// than left "deferred" to be retried forever across every future restart.
func TestResumeStopsWhenCredentialMintSaysTargetGone(t *testing.T) {
	d := newStepFake()
	st := tempStore(t)
	cfg := &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
		{ID: "planner"}, {ID: "worker"}}}}}
	e := New(Options{Config: cfg, Store: st, Dispatch: d, Notifier: &fakeNotifier{},
		Author: dispatch.Author{}, Connectors: forgeRegistry(t),
		InvokeVerb: func(context.Context, string, string, map[string]any) (map[string]any, error) {
			return nil, &connector.ContractError{Code: sdk.CodeTargetGone, Message: "gone"}
		}})
	tr := issueTrigger()
	tr.Instance, tr.TargetTrusted = "i", true
	tp := tr
	tp.Action = nil
	trigJSON, _ := json.Marshal(tp)
	actJSON, _ := json.Marshal(triageAction())
	if err := st.PutRun(store.WorkflowRun{ID: "run1", Instance: "i",
		Trigger: trigJSON, Action: actJSON, StepIndex: 1,
		Outputs: map[string]map[string]any{"evaluate": {"has_context": true}}}); err != nil {
		t.Fatal(err)
	}

	e.ResumeWorkflows(context.Background())
	if d.count() != 0 {
		t.Fatalf("a target_gone resume must never dispatch, got %d dispatches", d.count())
	}
	if len(st.PendingRuns()) != 0 {
		t.Fatal("a run whose target is confirmed gone must not stay pending forever")
	}
}

// ResumeWorkflows' loop over PendingRuns is sequential: a rate_limited mint
// for ONE pending run must not block the next one behind a retry sleep —
// mint() no longer goes through connector.RetryContract at all, so a
// rate_limited answer (even one claiming a huge retry_after) reaches
// ResumeWorkflows' EXISTING defer-on-credential-error path ("resume deferred
// — …", left pending for the next start) immediately, and the next pending
// run resumes right behind it.
func TestResumeWorkflowsDefersRateLimitedMintWithoutBlocking(t *testing.T) {
	d := newStepFake()
	st := tempStore(t)
	cfg := &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
		{ID: "planner"}, {ID: "worker"}}}}}
	e := New(Options{Config: cfg, Store: st, Dispatch: d, Notifier: &fakeNotifier{},
		Author: dispatch.Author{}, Connectors: forgeRegistry(t),
		InvokeVerb: func(_ context.Context, _, verb string, opts map[string]any) (map[string]any, error) {
			if opts["repo"] == "acme/rate-limited" {
				return nil, &connector.ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1h"}}
			}
			return map[string]any{"token": "t"}, nil
		}})

	blocked := issueTrigger()
	blocked.Target.Repo, blocked.Target.Number, blocked.Target.Issue = "acme/rate-limited", 1, 1
	blocked.TargetTrusted = true
	bp := blocked
	bp.Action = nil
	bJSON, _ := json.Marshal(bp)
	actJSON, _ := json.Marshal(triageAction())
	if err := st.PutRun(store.WorkflowRun{ID: "run-blocked", Instance: "i",
		Trigger: bJSON, Action: actJSON, StepIndex: 1,
		Outputs: map[string]map[string]any{"evaluate": {"has_context": true}}}); err != nil {
		t.Fatal(err)
	}

	ok := issueTrigger()
	ok.Target.Repo, ok.Target.Number, ok.Target.Issue = "acme/ok", 2, 2
	ok.TargetTrusted = true
	op := ok
	op.Action = nil
	oJSON, _ := json.Marshal(op)
	if err := st.PutRun(store.WorkflowRun{ID: "run-ok", Instance: "i",
		Trigger: oJSON, Action: actJSON, StepIndex: 1,
		Outputs: map[string]map[string]any{"evaluate": {"has_context": true}}}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	e.ResumeWorkflows(context.Background())
	deadline := time.After(2 * time.Second)
	for d.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("run-ok never resumed")
		case <-time.After(time.Millisecond):
		}
	}
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("ResumeWorkflows took %s to reach run-ok — it must not block behind run-blocked's rate_limited mint (retry_after: 1h)", elapsed)
	}
	// run-ok finishes its (fake) steps asynchronously; give it a moment to
	// settle, then exactly run-blocked (never minted, never dispatched)
	// should remain pending.
	deadline = time.After(2 * time.Second)
	for {
		if len(st.PendingRuns()) == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("got %d pending runs, want exactly 1 (run-blocked, deferred to the next start)", len(st.PendingRuns()))
		case <-time.After(time.Millisecond):
		}
	}
	if p := st.PendingRuns(); len(p) != 1 || p[0].ID != "run-blocked" {
		t.Fatalf("pending runs = %+v, want only run-blocked", p)
	}
}
