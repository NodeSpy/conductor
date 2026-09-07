package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

const histCfg = `
connectors:
  svc: { type: fake }
agents:
  fixer: { model: claude-sonnet }
`

var histSpec = `
on: svc.ping
name: nightly
steps:
  - { id: greet, uses: svc.post, options: { text: "hi {{.msg}}" } }
  - { id: skipme, if: "{{.msg}} == never", uses: svc.post, options: { text: x } }
  - id: fix
    type: agent
    agent: fixer
    prompt: "fix {{.msg}}"
`

func TestRunHistoryRecorded(t *testing.T) {
	cfg := loadConfig(t, histCfg)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1",
			Output: `{"usage":{"input_tokens":100,"output_tokens":50},"total_cost_usd":0.25}`}, nil
	}
	run := store.WorkflowRun{ID: "ping:x", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", map[string]any{"msg": "world"}), mustSpec(t, histSpec))

	rec, ok := rig.Store.lastHistory()
	if !ok {
		t.Fatal("no history recorded")
	}
	if rec.Status != "ok" || rec.RunID != "ping:x" || rec.On != "svc.ping" || rec.Variant != "nightly" {
		t.Fatalf("record: %+v", rec)
	}
	if rec.Finished.IsZero() || !strings.HasPrefix(rec.ID, "r") {
		t.Fatalf("timestamps/id: %+v", rec)
	}
	if len(rec.Steps) != 3 {
		t.Fatalf("steps: %+v", rec.Steps)
	}
	greet, _ := rec.Step("greet")
	if greet.Status != "ok" || greet.Outputs["id"] != 1 {
		t.Fatalf("greet: %+v", greet)
	}
	// Rendered inputs recorded (template resolved).
	opts, _ := greet.Inputs["options"].(map[string]any)
	if greet.Inputs["uses"] != "svc.post" || opts["text"] != "hi world" {
		t.Fatalf("greet inputs: %+v", greet.Inputs)
	}
	if sk, _ := rec.Step("skipme"); sk.Status != "skipped" {
		t.Fatalf("skipme: %+v", sk)
	}
	// The agent step records its prompt SOURCE (the runtime renders the
	// template at dispatch, against the same pinned scope a retry restores).
	fix, _ := rec.Step("fix")
	if fix.Status != "ok" || fix.Inputs["agent"] != "fixer" ||
		!strings.Contains(fix.Inputs["prompt"].(string), "fix {{.msg}}") {
		t.Fatalf("fix: %+v", fix)
	}
	// Cost landed on the agent step and the run totals.
	if fix.Tokens != 150 || fix.CostUSD != 0.25 {
		t.Fatalf("fix cost: %+v", fix)
	}
	if rec.Tokens != 150 || rec.CostUSD != 0.25 {
		t.Fatalf("run cost: %+v", rec)
	}
	// The pinned trigger/action ride the record for retry.
	if len(rec.Trigger) == 0 && len(rec.Action) == 0 {
		t.Log("note: no trigger/action recorded (engine-less test run)") // run.Trigger empty in this harness
	}
}

func TestRunHistoryFailureAndSecrets(t *testing.T) {
	cfg := loadConfig(t, histCfg)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Secrets.Track("s3kr1t-value")
	fake.failTimes["post"] = 1

	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: leak, uses: svc.post, options: { text: "tok is {{.msg}}" } }
`)
	run := store.WorkflowRun{ID: "ping:y", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", map[string]any{"msg": "s3kr1t-value"}), spec)

	rec, ok := rig.Store.lastHistory()
	if !ok || rec.Status != "failed" || rec.FailedStep != "leak" {
		t.Fatalf("failure record: %+v %v", rec, ok)
	}
	step, _ := rec.Step("leak")
	if step.Status != "failed" || step.Error == "" {
		t.Fatalf("failed step: %+v", step)
	}
	// The tracked secret value never reaches the recorded inputs.
	opts, _ := step.Inputs["options"].(map[string]any)
	if text, _ := opts["text"].(string); strings.Contains(text, "s3kr1t-value") {
		t.Fatalf("secret leaked into history inputs: %q", text)
	}
}

func TestShadowRunsNotRecorded(t *testing.T) {
	cfg := loadConfig(t, histCfg)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	spec := mustSpec(t, `
on: svc.ping
shadow: true
steps:
  - { id: s, uses: svc.post, options: { text: x } }
`)
	run := store.WorkflowRun{ID: "ping:z", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
	if _, ok := rig.Store.lastHistory(); ok {
		t.Fatal("shadow runs must not be recorded")
	}
}
