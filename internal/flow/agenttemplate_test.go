package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// TestAgentStepIdentityIsDispatched pins what `agent:` means after the
// profile section was removed (docs/design/agents-removal.md §6): it is a
// free-form ATTRIBUTION label, not a selector. A step carries its own
// behavior, and what the dispatch is keyed by — memory scope, session pool,
// track record — is the step's stable IDENTITY. A templated `agent:` still
// renders (so an operator's label can vary), but it selects nothing.
func TestAgentStepIdentityIsDispatched(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
steps:
  opus: { type: agent, name: opus, model: claude-opus }
  sonnet: { type: agent, name: sonnet, model: claude-sonnet }
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	var got, gotModel string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got, gotModel = req.Identity, req.Step.Model.Ref
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
name: t
steps:
  - id: r
    type: agent
    step: sonnet
    prompt: "review"
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"who": "sonnet"}), spec)
	if failed, e := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", e)
	}
	// extends: pulls in the template's behavior AND its name, so the step
	// shares that identity — the continuity a shared `agent: sonnet` gave.
	if got != "sonnet" {
		t.Fatalf("identity should be the extended template's name, got %q", got)
	}
	if gotModel != "claude-sonnet" {
		t.Fatalf("the template's model should carry through, got %q", gotModel)
	}
}

// A step that extends nothing takes a STRUCTURAL identity: the enclosing
// trigger plus its slot. Stable across a prompt edit, distinct per trigger.
func TestAgentStepStructuralIdentity(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	var got string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = req.Identity
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
name: t
steps:
  - id: r
    type: agent
    prompt: "review"
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if got != "t/r" {
		t.Fatalf("structural identity = %q, want t/r", got)
	}
}

// TestValidateAllowsTemplatedAgent pins that load-time validation accepts a
// templated agent (resolved at dispatch), while a literal unknown name still
// fails. Reverting the `!strings.Contains(...,"{{")` guard rejects the templated
// step with "unknown agent profile" and fails the positive case.
func TestValidateAgentStepShape(t *testing.T) {
	good := loadConfig(t, `
connectors:
  svc: { use: fake }
steps:
  opus: { type: agent, name: opus, model: claude-opus }
triggers:
  - on: svc.ping
    name: t
    steps:
      - { id: r, type: agent, agent: "{{.who}}", prompt: "x" }
`)
	reg := buildRegistry(t, good)
	if err := Validate(good, reg); err != nil {
		t.Fatalf("templated agent should validate, got: %v", err)
	}

	// `agent:` names nothing resolvable now, so an arbitrary label is fine.
	// What an agent step still needs is a PROMPT — that is the check that
	// replaced the profile lookup.
	bad := loadConfig(t, `
connectors:
  svc: { use: fake }
triggers:
  - on: svc.ping
    name: t
    steps:
      - { id: r, type: agent, agent: nope }
`)
	err := Validate(bad, buildRegistry(t, bad))
	if err == nil || !strings.Contains(err.Error(), "needs a prompt") {
		t.Fatalf("an agent step with no prompt must fail validation, got %v", err)
	}
}
