package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/store"
)

// TestWorkflowCallBindsInputsAndReturnsOutputs (G3): a workflow:/with: step binds
// declared inputs in the callee (with a default filling the unset one), the
// callee does NOT see the caller's step outputs, and the caller reads the
// workflow's declared outputs off the call step's id.
func TestWorkflowCallBindsInputsAndReturnsOutputs(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
workflows:
  greet:
    inputs:
      who:     { type: string, required: true }
      channel: { type: string, default: "#general" }
    outputs:
      note: "{{.p.id}}"
    steps:
      - id: p
        uses: svc.post
        options: { text: "hi {{.inputs.who}} in {{.inputs.channel}} leak={{.prior}}" }
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.outputs["post"] = map[string]any{"id": 42}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: prior, uses: svc.post, options: { text: before } }
  - { id: call, workflow: greet, with: { who: "{{.msg}}" } }
  - { id: after, uses: svc.post, options: { text: "note={{.call.note}}" } }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "world"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := st.snapshot()
	if len(calls) != 3 {
		t.Fatalf("want prior + callee + after, got %d calls", len(calls))
	}
	// Inputs bound (required from with:, default applied); the caller's prior
	// step output is NOT in the callee's scope (encapsulation).
	if got := calls[1].Opts["text"]; got != "hi world in #general leak=" {
		t.Errorf("callee scope wrong: %v", got)
	}
	// The caller reads the declared output off the call step's id.
	if got := calls[2].Opts["text"]; got != "note=42" {
		t.Errorf("caller should read workflow output, got %v", got)
	}
}

// TestWorkflowCallMissingRequiredInput: an unbound required input fails the
// call with an error naming workflow and input.
func TestWorkflowCallMissingRequiredInput(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
workflows:
  greet:
    inputs: { who: { type: string, required: true } }
    steps: [ { id: p, uses: svc.post, options: { text: "{{.inputs.who}}" } } ]
`)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps: [ { id: call, workflow: greet } ]
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, `missing required input "who"`) {
		t.Fatalf("want missing-input failure, got failed=%v err=%q", failed, errStr)
	}
}

// TestResumeSkipsCompletedSteps (G4): a persisted run with StepIndex/Outputs
// resumes AFTER the last checkpointed step — completed steps' verbs never
// re-fire, later steps see the restored outputs, and the finished run is
// deleted from the store.
func TestResumeSkipsCompletedSteps(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: a, uses: svc.post, options: { text: step-a } }
  - { id: b, uses: svc.post, options: { text: step-b } }
  - { id: c, uses: svc.post, options: { text: "c-sees-{{.a.id}}-{{.b.id}}" } }
`)
	rig := newTestRunner(t, cfg, reg)
	run := store.WorkflowRun{
		ID:        "flow:resume-test",
		StepIndex: 2, // a and b completed before the restart
		Outputs: map[string]map[string]any{
			"a": {"id": 7},
			"b": {"id": 8},
		},
	}
	runTriggerWithRun(rig, run, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := st.snapshot()
	if len(calls) != 1 {
		t.Fatalf("completed steps must not re-invoke; want only step c, got %+v", calls)
	}
	if got := calls[0].Opts["text"]; got != "c-sees-7-8" {
		t.Errorf("resumed step should see restored outputs, got %v", got)
	}
	rig.Store.mu.Lock()
	deleted := len(rig.Store.delLog) == 1 && rig.Store.delLog[0] == "flow:resume-test"
	rig.Store.mu.Unlock()
	if !deleted {
		t.Error("finished run should be deleted from the store")
	}
}

// TestContinueOnErrorSuppressesFailure (G5): a failing step marked
// continue_on_error keeps the workflow going — later steps see the failure
// marker outputs, the step_error is audited, and the run completes.
func TestContinueOnErrorSuppressesFailure(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: bad, uses: svc.fail, continue_on_error: true }
  - { id: next, uses: svc.post, options: { text: "failed={{.bad.failed}}" } }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("continue_on_error should suppress the failure, got: %s", errStr)
	}
	if got := st.count("post"); got != 1 {
		t.Fatalf("the next step should still run, got %d post calls", got)
	}
	calls := st.snapshot()
	if got := calls[len(calls)-1].Opts["text"]; got != "failed=true" {
		t.Errorf("later steps should see the failure marker, got %v", got)
	}
	if n := len(rig.Store.auditsWithEvent("step_error")); n != 1 {
		t.Errorf("the suppressed failure should still audit step_error, got %d", n)
	}
	var complete bool
	for _, e := range rig.Notifier.snapshot() {
		if e.Event == "complete" {
			complete = true
		}
	}
	if !complete {
		t.Error("workflow should complete")
	}
}
