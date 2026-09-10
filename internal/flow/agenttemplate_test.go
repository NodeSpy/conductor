package flow

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// TestAgentStepResolvesTemplatedProfile pins that `agent:` may be a template, so a
// workflow can choose the reviewing/assessing profile — and so the runtime — per
// invocation (agent: "{{.inputs.reviewer}}") without editing the workflow. The
// templated name is resolved against the step's data before the profile lookup and
// dispatch. Reverting the render in execAgent dispatches the literal "{{.who}}"
// and fails this test.
func TestAgentStepResolvesTemplatedProfile(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
agents:
  opus:   { model: claude-opus }
  sonnet: { model: claude-sonnet }
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	var got string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = req.Action.Agent
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
name: t
steps:
  - id: r
    type: agent
    agent: "{{.who}}"
    prompt: "review"
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"who": "sonnet"}), spec)
	if failed, e := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", e)
	}
	if got != "sonnet" {
		t.Fatalf("templated agent should resolve to \"sonnet\", dispatched %q", got)
	}
}

// TestValidateAllowsTemplatedAgent pins that load-time validation accepts a
// templated agent (resolved at dispatch), while a literal unknown name still
// fails. Reverting the `!strings.Contains(...,"{{")` guard rejects the templated
// step with "unknown agent profile" and fails the positive case.
func TestValidateAllowsTemplatedAgent(t *testing.T) {
	good := loadConfig(t, `
connectors:
  svc: { use: fake }
agents:
  opus: { model: claude-opus }
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

	bad := loadConfig(t, `
connectors:
  svc: { use: fake }
agents:
  opus: { model: claude-opus }
triggers:
  - on: svc.ping
    name: t
    steps:
      - { id: r, type: agent, agent: nope, prompt: "x" }
`)
	if err := Validate(bad, buildRegistry(t, bad)); err == nil {
		t.Fatal("a literal unknown agent profile must still fail validation")
	}
}
