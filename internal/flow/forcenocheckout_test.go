package flow

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// TestSyntheticTargetForcesNoCheckoutOnAgentStep is the regression for the
// suspected bug flagged going in: a synthetic-target source (Slack, RSS,
// webhook) forces its TRIGGER-LEVEL action to Checkout: "none" via
// inbound.ForceNoCheckout — but execAgent used to build the DISPATCHED
// action straight from the step (`Checkout: step.Checkout`), never reading
// the trigger's own Action at all. A Slack-triggered agent step with no
// explicit `checkout:` of its own therefore silently defaulted (via
// effectiveStrategy/repoStrategy) to branch-off against the synthetic
// "slack:C123" target — attempting a git checkout of something that isn't a
// real repo.
//
// The fix: an agent step's dispatched Checkout falls back to the trigger's
// own Action.Checkout when the step sets none. This proves the fallback
// actually reaches dispatch for a trigger whose Action was forced to "none",
// and that an explicit step `checkout:` still wins over it.
func TestSyntheticTargetForcesNoCheckoutOnAgentStep(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
triggers:
  - on: svc.ping
    steps: [{ type: agent, prompt: handle the event }]
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	var got string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = req.Action.Checkout
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}

	// A synthetic-target trigger whose source forced Checkout: "none" on its
	// OWN action — exactly what every ForceNoCheckout call site (Slack, RSS,
	// webhook) does before emit, because "slack:C123" is not a clonable repo.
	trig := core.Trigger{
		Source: "slack", Instance: "fake", Kind: "ping", TargetTrusted: true,
		Target: core.Target{Repo: "slack:C123", Number: 42},
		Action: config.Action{Checkout: "none"},
	}
	runTrigger(rig, trig, cfg.Triggers[0])
	if got != "none" {
		t.Fatalf("agent step dispatched with Checkout=%q, want %q (the trigger's forced checkout must reach dispatch when the step sets none)", got, "none")
	}
}

// TestStepCheckoutWinsOverTriggerForced: an explicit step `checkout:` still
// overrides the trigger's own forced value — the fallback only fills a gap,
// it never clobbers an operator's own choice.
func TestStepCheckoutWinsOverTriggerForced(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
triggers:
  - on: svc.ping
    steps: [{ type: agent, prompt: handle it, checkout: branch-off }]
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	var got string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = req.Action.Checkout
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	trig := core.Trigger{
		Source: "slack", Instance: "fake", Kind: "ping", TargetTrusted: true,
		Target: core.Target{Repo: "slack:C123", Number: 42},
		Action: config.Action{Checkout: "none"},
	}
	runTrigger(rig, trig, cfg.Triggers[0])
	if got != "branch-off" {
		t.Fatalf("step's own checkout: must win, got %q", got)
	}
}
