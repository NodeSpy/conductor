package engine

import (
	"context"
	"encoding/json"
	"errors"
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
	_, err := e.declaredCredentials(context.Background(), tr, "i", sem)
	if !errors.Is(err, dispatch.ErrTargetClosed) {
		t.Fatalf("declaredCredentials error = %v, want it to wrap dispatch.ErrTargetClosed", err)
	}
	var ce *connector.ContractError
	if !errors.As(err, &ce) || !ce.IsTargetGone() {
		t.Fatalf("the original *connector.ContractError must still be reachable via errors.As: %v", err)
	}
}

// A credential mint that answers -32013 rate_limited or -32014 not_ready is
// retried, bounded, inside the one mint call — no special wiring needed
// beyond connector.RetryContract, which e.mint already runs through.
func TestMintRetriesRateLimitedThenSucceeds(t *testing.T) {
	restoreSleep := connector.Sleep
	connector.Sleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { connector.Sleep = restoreSleep })

	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	calls := 0
	e.invokeVerb = func(context.Context, string, string, map[string]any) (map[string]any, error) {
		calls++
		if calls < 3 {
			return nil, &connector.ContractError{Code: sdk.CodeRateLimited, Data: map[string]any{"retry_after": "1ms"}}
		}
		return map[string]any{"token": "fresh"}, nil
	}
	sem := &sdk.ConnSemantics{Credentials: []sdk.Credential{
		{Name: "w", Role: "write", Mint: sdk.CredentialMint{Verb: "write_token"}, Value: "token", Env: []string{"GH_TOKEN"}},
	}}
	tr := core.Trigger{Instance: "i", TargetTrusted: true}
	c, err := e.declaredCredentials(context.Background(), tr, "i", sem)
	if err != nil || c.Env["GH_TOKEN"] != "fresh" || calls != 3 {
		t.Fatalf("creds=%+v err=%v calls=%d, want 3 calls ending in success", c, err, calls)
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
