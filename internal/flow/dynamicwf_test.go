package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

const dynBase = `
connectors:
  svc: { type: fake }
workflows:
  greet:
    description: "post a greeting"
    inputs:
      who: { type: string, required: true }
    outputs:
      note: "greeted {{.inputs.who}}"
    steps:
      - { id: post, uses: svc.post, options: { text: "hi {{.inputs.who}}" } }
  scold:
    steps:
      - { id: post, uses: svc.post, options: { text: "tsk" } }
`

// TestDynamicWorkflowName: a templated workflow: name resolves at runtime
// from the trigger context, runs the picked workflow, and surfaces a clear
// error for an unknown resolved name.
func TestDynamicWorkflowName(t *testing.T) {
	cfg := loadConfig(t, dynBase)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: pick
    workflow: "{{.msg}}"
    with: { who: "sam" }
  - id: echo
    uses: svc.post
    options: { text: "{{.pick.note}}" }
`)
	// Validation accepts the templated name (no static lookup).
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("config with a dynamic workflow name must validate: %v", err)
	}
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "greet"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 2 || calls[0].Opts["text"] != "hi sam" || calls[1].Opts["text"] != "greeted sam" {
		t.Fatalf("calls: %+v", calls)
	}

	// An unknown resolved name is a clear runtime error naming the set.
	rig2 := newTestRunner(t, cfg, reg)
	runTrigger(rig2, newTrigger("ping", map[string]any{"msg": "nope"}), spec)
	failed, errStr := rig2.workflowFailed()
	if !failed || !strings.Contains(errStr, `unknown workflow "nope"`) || !strings.Contains(errStr, "greet") {
		t.Fatalf("unknown dynamic workflow: failed=%v err=%q", failed, errStr)
	}
	// `with:` keys against a dynamic name are checked at runtime too.
	rig3 := newTestRunner(t, cfg, reg)
	runTrigger(rig3, newTrigger("ping", map[string]any{"msg": "scold"}), spec)
	if failed, errStr := rig3.workflowFailed(); !failed || !strings.Contains(errStr, `unknown input "who"`) {
		t.Fatalf("dynamic with-check: failed=%v err=%q", failed, errStr)
	}
}

// TestDynamicWorkflowDepthGuard: a self-referencing dynamic name can't
// static-cycle-check, so the runtime depth cap halts it with a clear error.
func TestDynamicWorkflowDepthGuard(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
workflows:
  loop:
    steps:
      - { id: again, workflow: "{{ \"loop\" }}" }
`)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("dynamic self-reference must pass static validation: %v", err)
	}
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps: [ { id: go, workflow: loop } ]
`))
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "call depth") {
		t.Fatalf("depth guard: failed=%v err=%q", failed, errStr)
	}
}

// TestWorkflowDescriptionParses: description: round-trips on WorkflowDef and
// static cycle validation still rejects literal cycles.
func TestWorkflowDescriptionParses(t *testing.T) {
	cfg := loadConfig(t, dynBase)
	if cfg.Workflows["greet"].Description != "post a greeting" {
		t.Fatalf("description: %+v", cfg.Workflows["greet"])
	}
	// Static cycles still caught at load.
	cyc := loadConfig(t, `
connectors:
  svc: { type: fake }
workflows:
  a: { steps: [ { id: s, workflow: b } ] }
  b: { steps: [ { id: s, workflow: a } ] }
triggers:
  - { on: svc.ping, steps: [ { id: s, workflow: a } ] }
`)
	regc := buildRegistry(t, cyc)
	if err := Validate(cyc, regc); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("static cycle must fail validation: %v", err)
	}
	// Saved workflows resolve for dynamic lookups when the config has none.
	setSavedWorkflows(map[string]config.WorkflowDef{"extra": {Steps: []config.Step{}}})
	t.Cleanup(func() { setSavedWorkflows(nil) })
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	if _, ok := rig.Runner.lookupWorkflow("extra"); !ok {
		t.Fatal("saved workflows must resolve at runtime")
	}
	if !strings.Contains(rig.Runner.workflowNames(), "extra") {
		t.Fatal("saved workflows must appear in unknown-name errors")
	}
}
