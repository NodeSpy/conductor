package flow

import (
	"testing"

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
