package flow

import "testing"

// TestWorkflowHooksOrderingAndData covers item 3 (workflow-level hooks):
// at:start fires before steps, at:done sees step outputs, at:fail sees
// {{.error}}/{{.failed_step}}, a failing hook doesn't fail the workflow, and
// if:false skips a hook.
func TestWorkflowHooksOrderingAndData(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc:
    type: fake
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
hooks:
  - at: start
    uses: svc.post
    options: {text: "start-hook"}
  - at: start
    if: "false"
    uses: svc.post
    options: {text: "should-be-skipped"}
  - at: done
    uses: svc.post
    options: {text: "done-saw-{{.a.id}}"}
  - at: done
    uses: svc.fail
    options: {}
steps:
  - id: a
    uses: svc.post
    options: {text: "step-a"}
`)

	rig := newTestRunner(t, cfg, reg)
	trig := newTrigger("ping", map[string]any{"msg": "x"})
	runTrigger(rig, trig, spec)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed (hook failure must not fail workflow): %s", errStr)
	}

	calls := st.snapshot()
	var texts []string
	for _, c := range calls {
		if c.Verb == "post" {
			texts = append(texts, c.Opts["text"].(string))
		}
	}
	if len(texts) != 3 {
		t.Fatalf("expected 3 post calls (start hook, step, done hook), got %d: %v", len(texts), texts)
	}
	if texts[0] != "start-hook" {
		t.Errorf("first post call = %q, want start hook to fire before the step", texts[0])
	}
	if texts[1] != "step-a" {
		t.Errorf("second post call = %q, want the step itself", texts[1])
	}
	if texts[2] != "done-saw-1" {
		t.Errorf("third post call = %q, want done hook to see step a's output", texts[2])
	}

	// The failing "at: done" hook (svc.fail) should be recorded as a
	// best-effort hook failure (a verb audit entry with outcome hook_failed),
	// not surfaced as a workflow failure.
	failed := 0
	for _, e := range rig.Store.auditsWithEvent("verb") {
		if e["outcome"] == "hook_failed" {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("expected 1 hook_failed verb audit entry, got %d", failed)
	}
}

// TestWorkflowFailHookSeesError covers the at:fail hook branch: a failing
// step causes at:fail hooks to run with {{.error}} / {{.failed_step}} in
// scope, and the workflow is recorded as failed.
func TestWorkflowFailHookSeesError(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc:
    type: fake
`)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
hooks:
  - at: fail
    uses: svc.post
    options:
      text: "failed-step={{.failed_step}} error={{.error}}"
steps:
  - id: boom
    uses: svc.fail
    options: {}
`)

	rig := newTestRunner(t, cfg, reg)
	trig := newTrigger("ping", map[string]any{"msg": "x"})
	runTrigger(rig, trig, spec)

	failed, errStr := rig.workflowFailed()
	if !failed {
		t.Fatalf("expected workflow to fail")
	}
	if errStr == "" {
		t.Errorf("expected non-empty workflow error")
	}

	st := getOrCreateFakeState("svc")
	var hookText string
	for _, c := range st.snapshot() {
		if c.Verb == "post" {
			hookText = c.Opts["text"].(string)
		}
	}
	if hookText == "" {
		t.Fatalf("at:fail hook did not run")
	}
	if got := "failed-step=boom"; len(hookText) < len(got) || hookText[:len(got)] != got {
		t.Errorf("fail hook text = %q, want prefix %q", hookText, got)
	}
}

// TestStepHooks covers the step-level hook portion of item 3: at:start runs
// before the step, at:done includes the step's own output, and a step
// failure runs step at:fail hooks then workflow fail hooks (and, without
// continue_on_error, fails the workflow).
func TestStepHooks(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc:
    type: fake
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.outputs["post"] = map[string]any{"id": 7}

	spec := mustSpec(t, `
on: svc.ping
hooks:
  - at: fail
    uses: svc.post
    options: {text: "workflow-fail-hook"}
steps:
  - id: a
    uses: svc.post
    options: {text: "step-a"}
    hooks:
      - at: start
        uses: svc.post
        options: {text: "a-start-hook"}
      - at: done
        uses: svc.post
        options: {text: "a-done-saw-{{.a.id}}"}
  - id: b
    uses: svc.fail
    options: {}
    hooks:
      - at: fail
        uses: svc.post
        options: {text: "b-fail-hook"}
`)

	rig := newTestRunner(t, cfg, reg)
	trig := newTrigger("ping", map[string]any{"msg": "x"})
	runTrigger(rig, trig, spec)

	failed, _ := rig.workflowFailed()
	if !failed {
		t.Fatalf("expected workflow to fail (step b has no continue_on_error)")
	}

	var texts []string
	for _, c := range st.snapshot() {
		if c.Verb == "post" {
			texts = append(texts, c.Opts["text"].(string))
		}
	}
	want := []string{"a-start-hook", "step-a", "a-done-saw-7", "b-fail-hook", "workflow-fail-hook"}
	if len(texts) != len(want) {
		t.Fatalf("post call sequence = %v, want %v", texts, want)
	}
	for i, w := range want {
		if texts[i] != w {
			t.Errorf("post call %d = %q, want %q (full: %v)", i, texts[i], w, texts)
		}
	}
}
