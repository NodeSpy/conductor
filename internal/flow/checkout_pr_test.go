package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// The shape that broke review-eval: a MANUAL run replays PRs through a
// workflow whose agent step checks out the PR. The manual trigger's own
// target is synthetic ("manual:<name>"), so without checkout_pr the checkout
// cloned that — "git@<host>:manual:<name>.git".
const checkoutPRBase = `
connectors:
  svc: { use: fake }
workflows:
  team:
    inputs:
      repo: { type: string, required: true }
      pr: { type: integer, required: true }
    steps:
      - id: lens
        type: agent
        prompt: "review {{.inputs.repo}}#{{.inputs.pr}}"
        checkout: checkout-pr
        checkout_pr: { repo: "{{.inputs.repo}}", pr: "{{.inputs.pr}}" }
`

const checkoutPRReplay = `
on: svc.ping
steps:
  - id: replay
    workflow: team
    with: { repo: "{{.inputs.repo}}", pr: "{{.inputs.pr}}" }
`

// runCheckoutPR runs spec on trig and returns the dispatched requests and the
// workflow's error ("" when it succeeded).
func runCheckoutPR(t *testing.T, trig core.Trigger, spec string) ([]dispatch.Request, string) {
	t.Helper()
	cfg := loadConfig(t, checkoutPRBase)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	rig := newTestRunner(t, cfg, reg)
	var got []dispatch.Request
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = append(got, req)
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	runTrigger(rig, trig, mustSpec(t, spec))
	if _, errStr := rig.workflowFailed(); errStr != "" {
		return got, errStr
	}
	// a refused checkout is Unrecoverable: the run escalates
	if row := findAudit(rig, "escalate"); row != nil {
		for _, k := range []string{"error", "reason"} {
			if e, _ := row[k].(string); e != "" {
				return got, e
			}
		}
		return got, "escalated"
	}
	return got, ""
}

// in is a run's context carrying workflow inputs, as `conductor run --input` builds it.
func in(repo string, pr any) map[string]any {
	return map[string]any{"inputs": map[string]any{"repo": repo, "pr": pr}}
}

func manualTrigger(operator bool, ctx map[string]any) core.Trigger {
	return core.Trigger{
		Source: "manual", Instance: "manual", Kind: "manual", Variant: "review-eval",
		Target:  core.Target{Repo: "manual:review-eval", Key: "manual:review-eval"},
		Context: ctx, Operator: operator, TargetTrusted: true,
	}
}

func TestAManualReplayChecksOutTheInputPR(t *testing.T) {
	got, errStr := runCheckoutPR(t, manualTrigger(true, in("acme/app", 12)), checkoutPRReplay)
	if errStr != "" || len(got) != 1 {
		t.Fatalf("err=%q dispatches=%d", errStr, len(got))
	}
	tr := got[0].Trigger
	if tr.CheckoutPR == nil || *tr.CheckoutPR != (core.PRRef{Repo: "acme/app", Number: 12}) {
		t.Fatalf("CheckoutPR = %+v, want acme/app#12", tr.CheckoutPR)
	}
	if tr.Target.CheckoutRepo() != "acme/app" {
		t.Fatalf("checkout repo = %q, want acme/app", tr.Target.CheckoutRepo())
	}
	// forge authority stays on the run's own target
	if tr.Target.Repo != "manual:review-eval" {
		t.Fatalf("Target.Repo = %q, want the trigger's own", tr.Target.Repo)
	}
	co, ok := tr.Checkout()
	if !ok || co.FetchRef != "refs/pull/12/head" || co.Hints["pr_number"] != "12" || co.Remote != "" || co.PushBranch != "" {
		t.Fatalf("Checkout() = %+v, %v", co, ok)
	}
	if got[0].Action.Checkout != "checkout-pr" {
		t.Fatalf("checkout = %q", got[0].Action.Checkout)
	}
}

func TestOnlyTheOperatorOrConfigMayRedirectACheckout(t *testing.T) {
	pinged := func(src string, ctx map[string]any) core.Trigger {
		tr := newTrigger("ping", ctx)
		tr.Source = src
		tr.Target = core.Target{Repo: "o/r", PR: 7, Number: 7}
		return tr
	}
	other := in("evil/repo", 99)
	for name, trig := range map[string]core.Trigger{
		// a remote invocation of a manual trigger: its inputs are the caller's
		"remote invocation": manualTrigger(false, other),
		// an event: its fields are the sender's
		"event": pinged("fake", other),
	} {
		got, errStr := runCheckoutPR(t, trig, checkoutPRReplay)
		if len(got) != 0 {
			t.Errorf("%s: dispatched %+v, want none", name, got[0].Trigger.CheckoutPR)
		}
		if !strings.Contains(errStr, "checkout_pr: evil/repo#99 is not this") {
			t.Errorf("%s: err = %q", name, errStr)
		}
	}
	// the event's own repo but ANOTHER PR is a redirect too
	got, errStr := runCheckoutPR(t, pinged("fake", in("o/r", 99)), checkoutPRReplay)
	if len(got) != 0 || !strings.Contains(errStr, "checkout_pr: o/r#99 is not this") {
		t.Errorf("same repo, other PR: dispatched=%d err=%q", len(got), errStr)
	}
	// a cron schedule's run is config's own
	got, errStr = runCheckoutPR(t, pinged("cron", other), checkoutPRReplay)
	if errStr != "" || len(got) != 1 || got[0].Trigger.CheckoutPR == nil || got[0].Trigger.CheckoutPR.Repo != "evil/repo" {
		t.Fatalf("cron: err=%q got=%+v", errStr, got)
	}
	// an event naming its OWN PR: nothing is redirected, nothing refused —
	// one step serves its event-driven and its manual callers
	got, errStr = runCheckoutPR(t, pinged("fake", in("o/r", 7)), checkoutPRReplay)
	if errStr != "" || len(got) != 1 || got[0].Trigger.CheckoutPR != nil || got[0].Trigger.Target.Project != "" {
		t.Fatalf("own PR: err=%q got=%+v", errStr, got)
	}
}

// A forge repo mapped to another checkout project (project_map): the event's
// own PR under either name is still its own — never refused, never moved.
func TestTheOwnPRMatchesByForgeOrProjectName(t *testing.T) {
	for _, name := range []string{"o/r", "O/R", "local/r"} {
		tr := newTrigger("ping", in(name, 7))
		tr.Target = core.Target{Repo: "o/r", PR: 7, Number: 7, Project: "local/r"}
		got, errStr := runCheckoutPR(t, tr, checkoutPRReplay)
		if errStr != "" || len(got) != 1 || got[0].Trigger.CheckoutPR != nil || got[0].Trigger.Target.Project != "local/r" {
			t.Errorf("%s: err=%q got=%+v", name, errStr, got)
		}
	}
}

// checkout: none checks nothing out: its checkout_pr is never applied, nor refused.
func TestCheckoutPRIsInertUnderCheckoutNone(t *testing.T) {
	spec := `
on: svc.ping
steps:
  - id: lens
    type: agent
    prompt: "review"
    checkout: none
    checkout_pr: { repo: "{{.inputs.repo}}", pr: "{{.inputs.pr}}" }
`
	got, errStr := runCheckoutPR(t, newTrigger("ping", in("evil/repo", 99)), spec)
	if errStr != "" || len(got) != 1 || got[0].Trigger.CheckoutPR != nil || got[0].Trigger.Target.Project != "" {
		t.Fatalf("err=%q got=%+v", errStr, got)
	}
}

func TestAConfigWrittenCheckoutPRAppliesOnAnyRun(t *testing.T) {
	spec := `
on: svc.ping
steps:
  - id: lens
    type: agent
    prompt: "review"
    checkout: checkout-pr
    checkout_pr: { repo: acme/app, pr: 3 }
`
	tr := newTrigger("ping", map[string]any{"repo": "evil/repo"})
	got, errStr := runCheckoutPR(t, tr, spec)
	if errStr != "" || len(got) != 1 || got[0].Trigger.CheckoutPR == nil || *got[0].Trigger.CheckoutPR != (core.PRRef{Repo: "acme/app", Number: 3}) {
		t.Fatalf("err=%q got=%+v", errStr, got)
	}
}

func TestARenderedCheckoutPRIsValidated(t *testing.T) {
	for _, ctx := range []map[string]any{
		in("../../etc", 1), in("acme/app", 0), in("git@evil.example:x/y", 1), in("acme/app", "1 --upload-pack=x"),
	} {
		got, errStr := runCheckoutPR(t, manualTrigger(true, ctx), checkoutPRReplay)
		if len(got) != 0 || !strings.Contains(errStr, "checkout_pr.") {
			t.Errorf("%v: dispatched=%d err=%q", ctx, len(got), errStr)
		}
	}
}

func TestAnAgentCannotSetCheckoutPR(t *testing.T) {
	s := &config.Step{Type: "agent", Checkout: "checkout-pr", CheckoutPR: &config.CheckoutPR{Repo: "acme/app", PR: "1"}}
	if err := checkAgentAuthoredFields("plan[0](x)", s); err == nil || !strings.Contains(err.Error(), "checkout_pr:") {
		t.Fatalf("checkAgentAuthoredFields = %v", err)
	}
	sanitizeAgentAuthoredStep(s)
	if s.CheckoutPR != nil {
		t.Fatal("sanitizeAgentAuthoredStep kept checkout_pr")
	}
}

// A templated checkout_pr is checked at load like every other templated
// field: a template that can't parse fails Validate, not the first dispatch.
func TestCheckoutPRReferencesAreValidated(t *testing.T) {
	cfg := loadConfig(t, strings.Replace(checkoutPRBase, `repo: "{{.inputs.repo}}"`, `repo: "{{.inputs.repo"`, 1))
	err := Validate(cfg, buildRegistry(t, cfg))
	if err == nil || !strings.Contains(err.Error(), "checkout_pr.repo") {
		t.Fatalf("Validate = %v, want an error naming checkout_pr.repo", err)
	}
}
