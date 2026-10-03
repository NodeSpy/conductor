package flow

import (
	"errors"
	"testing"

	"github.com/NodeSpy/conductor/internal/store"
)

// TestWorkflowHooksOrderingAndData covers item 3 (workflow-level hooks):
// at:start fires before steps, at:done sees step outputs, at:fail sees
// {{.error}}/{{.failed_step}}, a failing hook doesn't fail the workflow, and
// if:false skips a hook.
func TestWorkflowHooksOrderingAndData(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc:
    use: fake
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
    use: fake
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
    use: fake
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

// TestStartHooksDoNotRefireOnResumePastStepZero is finding 4(a): a
// workflow-level `at: start` hook (the generic successor to a chat
// connector's own `ack` feedback, docs/design/plugin-contract.md §2.2
// option_hooks) must fire exactly once per run attempt — a daemon restart
// that resumes a run already past its first step must NOT re-post it. The
// reviewer's scratch test: a 2-step flow "checkpointed" at StepIndex 1 (as
// resumeFlowRun would load it from the store), run again from there.
func TestStartHooksDoNotRefireOnResumePastStepZero(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc:
    use: fake
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
hooks:
  - at: start
    uses: svc.post
    options: {text: "ack"}
steps:
  - id: one
    uses: svc.post
    options: {text: "step-one"}
  - id: two
    uses: svc.post
    options: {text: "step-two"}
`)

	rig := newTestRunner(t, cfg, reg)
	trig := newTrigger("ping", map[string]any{"msg": "x"})

	// A FRESH run (StepIndex 0, StartHooksFired false — the zero value):
	// the ack fires.
	fresh := store.WorkflowRun{ID: "r1", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, fresh, trig, spec)

	acks := func() int {
		n := 0
		for _, c := range st.snapshot() {
			if c.Verb == "post" && c.Opts["text"] == "ack" {
				n++
			}
		}
		return n
	}
	if n := acks(); n != 1 {
		t.Fatalf("fresh run: expected the ack to fire once, got %d", n)
	}

	// A run "checkpointed at StepIndex 1" — exactly what resumeFlowRun loads
	// from the store after step "one" completed and the daemon restarted
	// before step "two" ran. StartHooksFired is true (persisted by the
	// FIRST Run() call above, same as the real checkpoint/PutRun path) —
	// the ack must NOT fire again.
	resumed := store.WorkflowRun{
		ID: "r1", StepIndex: 1, StartHooksFired: true,
		Outputs: map[string]map[string]any{"one": {}},
	}
	runTriggerWithRun(rig, resumed, trig, spec)

	if n := acks(); n != 1 {
		t.Fatalf("resume past step 0: the ack must NOT re-fire, but saw %d total ack posts", n)
	}
	var steps []string
	for _, c := range st.snapshot() {
		if c.Verb == "post" && c.Opts["text"] != "ack" {
			steps = append(steps, c.Opts["text"].(string))
		}
	}
	want := []string{"step-one", "step-two", "step-two"}
	if len(steps) != len(want) {
		t.Fatalf("step posts = %v, want %v (step-one once from the fresh run, step-two once from each run)", steps, want)
	}
}

// TestStartHooksFireOnFreshRunEvenWithNonZeroID proves the companion half:
// a run's very first pass through Run() (StartHooksFired false, the zero
// value a freshly persisted run always starts with — store.WorkflowRun) DOES
// fire its start hooks, same as before finding 4(a)'s fix — the gate is
// StartHooksFired, not run.ID or run.StepIndex alone.
func TestStartHooksFireOnFreshRunEvenWithNonZeroID(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc:
    use: fake
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
hooks:
  - at: start
    uses: svc.post
    options: {text: "ack"}
steps:
  - id: one
    uses: svc.post
    options: {text: "step-one"}
`)
	rig := newTestRunner(t, cfg, reg)
	trig := newTrigger("ping", map[string]any{"msg": "x"})

	fresh := store.WorkflowRun{ID: "r2", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, fresh, trig, spec)

	n := 0
	for _, c := range st.snapshot() {
		if c.Verb == "post" && c.Opts["text"] == "ack" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a fresh run must fire its start hook, got %d acks", n)
	}

	// And the persisted record now carries StartHooksFired — the state a
	// real resume would load back.
	last, ok := rig.Store.lastPut("r2")
	if !ok {
		t.Fatal("run r2 was never persisted")
	}
	if !last.StartHooksFired {
		t.Fatal("Run must persist StartHooksFired=true after firing the start hook")
	}
}

// TestStartHooksLostWriteNeverDoublesFireOnResume is finding 6: StartHooksFired
// is set and PERSISTED BEFORE firing, not after. Before the fix, firing came
// first and the persist's error was discarded — a crash (or here, any failed
// write) between the fire and the persist landing left StartHooksFired still
// false on disk, so a later resume fired a SECOND time. After the fix, a
// failed persist must skip firing entirely THIS pass (logged, not fatal to
// the run) rather than risk firing without anything durable backing it; a
// later resume — which, with nothing durable changed, still sees
// StartHooksFired false — then persists successfully and fires EXACTLY once,
// never zero-then-two.
func TestStartHooksLostWriteNeverDoublesFireOnResume(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc:
    use: fake
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
hooks:
  - at: start
    uses: svc.post
    options: {text: "ack"}
steps:
  - id: one
    uses: svc.post
    options: {text: "step-one"}
`)
	rig := newTestRunner(t, cfg, reg)
	trig := newTrigger("ping", map[string]any{"msg": "x"})

	acks := func() int {
		n := 0
		for _, c := range st.snapshot() {
			if c.Verb == "post" && c.Opts["text"] == "ack" {
				n++
			}
		}
		return n
	}

	// Pass 1: the store's PutRun fails (a lost/failed write — a disk-full
	// daemon crash's analog). StartHooksFired must NOT fire without a
	// durable record of it backing that fire.
	rig.Store.putRunErr = errors.New("disk full")
	fresh := store.WorkflowRun{ID: "r1", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, fresh, trig, spec)
	if n := acks(); n != 0 {
		t.Fatalf("a failed persist must not fire the start hook at all, got %d acks", n)
	}
	if _, ok := rig.Store.lastPut("r1"); ok {
		t.Fatal("a failed PutRun must not be recorded as a successful put")
	}

	// Pass 2: "resume" — the store works again now, and (since nothing
	// durable changed in pass 1) the loaded record still shows
	// StartHooksFired false, exactly what a real resume would read back.
	rig.Store.putRunErr = nil
	resumed := store.WorkflowRun{ID: "r1", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, resumed, trig, spec)
	if n := acks(); n != 1 {
		t.Fatalf("after the lost write, the next attempt must fire the start hook EXACTLY once, got %d total", n)
	}
	last, ok := rig.Store.lastPut("r1")
	if !ok || !last.StartHooksFired {
		t.Fatal("the successful pass must persist StartHooksFired=true")
	}
}
