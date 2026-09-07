package flow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// TestConductorTriggerRunsSteps: a trigger on conductor.escalate validates
// against the declared lifecycle context and its steps read {{.message}} —
// alerting as an ordinary trigger.
func TestConductorTriggerRunsSteps(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
triggers:
  - name: alert
    on: conductor.escalate
    steps:
      - uses: svc.post
        options: { text: "ALERT {{.message}} (ref={{.ref}}, kind={{.kind}}, origin={{.origin_kind}})" }
`)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("conductor trigger must validate: %v", err)
	}
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	// The shape EmitLifecycle emits for this trigger.
	trig := core.Trigger{
		Source: "conductor", Instance: "conductor", Kind: "escalate", Variant: "alert",
		Target: core.Target{Repo: "o/r", Number: 7},
		Title:  "[escalate] o/r#7 merge_conflict gave up",
		Context: map[string]any{
			"message": "[escalate] o/r#7 merge_conflict gave up",
			"event":   "escalate", "ref": "o/r#7",
			"repo": "o/r", "number": 7, "origin_kind": "merge_conflict",
		},
		Force: true,
	}
	runTrigger(rig, trig, cfg.Triggers[0])
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 1 ||
		calls[0].Opts["text"] != "ALERT [escalate] o/r#7 merge_conflict gave up (ref=o/r#7, kind=escalate, origin=merge_conflict)" {
		t.Fatalf("calls: %+v", calls)
	}
}

// TestConductorTriggerRefValidation: an unknown lifecycle event and an
// unknown context ref fail at load.
func TestConductorTriggerRefValidation(t *testing.T) {
	base := `
connectors:
  svc: { type: fake }
triggers:
`
	valid := func(y string) error {
		cfg := loadConfig(t, base+y)
		reg := buildRegistry(t, cfg)
		return Validate(cfg, reg)
	}
	if err := valid(`  - { on: conductor.nope, steps: [ { uses: svc.post, options: { text: t } } ] }`); err == nil {
		t.Fatal("unknown lifecycle event must fail validation")
	}
	if err := valid(`  - { on: conductor.escalate, steps: [ { uses: svc.post, options: { text: "{{.bogus_field}}" } } ] }`); err == nil {
		t.Fatal("unknown context ref must fail validation")
	}
}

// TestConductorUpdateStepWithHooks: `uses: conductor.update` in a workflow,
// with pre/post steps and step-level hooks at start/done — and the at: fail
// hook when the update errors. The pattern update: { apply: workflow }
// enables: drain → update → smoke-test, announced along the way.
func TestConductorUpdateStepWithHooks(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
triggers:
  - name: gated-update
    on: conductor.update_available
    steps:
      - { id: drain, uses: svc.post, options: { text: "drain before {{.version}}" } }
      - id: up
        uses: conductor.update
        hooks:
          - { at: start, uses: svc.post, options: { text: "updating -> {{.version}}" } }
          - { at: done,  uses: svc.post, options: { text: "now on {{.up.version}}" } }
          - { at: fail,  uses: svc.post, options: { text: "update failed: {{.error}}" } }
      - { id: smoke, uses: svc.post, options: { text: "smoke-test after" } }
`)
	reg := buildRegistry(t, cfg)
	if err := Validate(cfg, reg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	connector.SetConductorOps(&connector.ConductorOps{
		Update: func(context.Context) (bool, string, error) { return true, "v3.1.4", nil },
	})
	t.Cleanup(func() { connector.SetConductorOps(nil) })

	trig := core.Trigger{
		Source: "conductor", Instance: "conductor", Kind: "update_available", Variant: "gated-update",
		Target:  core.Target{Repo: "conductor:update_available"},
		Context: map[string]any{"message": "release v3.1.4 available", "version": "v3.1.4", "event": "update_available"},
		Force:   true,
	}
	runTrigger(rig, trig, cfg.Triggers[0])
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	var texts []string
	for _, c := range fake.snapshot() {
		texts = append(texts, fmt.Sprint(c.Opts["text"]))
	}
	want := []string{
		"drain before v3.1.4",
		"updating -> v3.1.4",
		"now on v3.1.4",
		"smoke-test after",
	}
	if strings.Join(texts, " | ") != strings.Join(want, " | ") {
		t.Fatalf("order: %v", texts)
	}

	// Failure path: the at: fail hook fires and the workflow fails.
	connector.SetConductorOps(&connector.ConductorOps{
		Update: func(context.Context) (bool, string, error) { return false, "", fmt.Errorf("download exploded") },
	})
	fake2 := newFakeState(t, "svc")
	rig2 := newTestRunner(t, cfg, reg)
	runTrigger(rig2, trig, cfg.Triggers[0])
	if failed, _ := rig2.workflowFailed(); !failed {
		t.Fatal("update error must fail the workflow")
	}
	var texts2 []string
	for _, c := range fake2.snapshot() {
		texts2 = append(texts2, fmt.Sprint(c.Opts["text"]))
	}
	joined := strings.Join(texts2, " | ")
	if !strings.Contains(joined, "update failed: ") || !strings.Contains(joined, "download exploded") {
		t.Fatalf("fail hook: %v", texts2)
	}
	if strings.Contains(joined, "smoke-test after") {
		t.Fatalf("post-step ran after a failed update: %v", texts2)
	}
}
