package flow

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The batch-2 helper steps (helpers.go): log, set, assert, fail, wait_for.
// Each slots into the step loop (id:, if:, outputs, history) like sleep does,
// and each is exercised here through the real runner and the fake connector.

const b2Cfg = `
connectors:
  svc: { use: fake }
`

// TestSetPublishesOutputs: a `set:` step computes values (types preserved) that
// a later step reads at {{.<id>.<key>}}.
func TestSetPublishesOutputs(t *testing.T) {
	cfg := loadConfig(t, b2Cfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: vars, set: { greeting: "hi {{.msg}}", n: 7 } }
  - { id: use, uses: svc.post, options: { text: "{{.vars.greeting}}", meta: { count: "{{.vars.n}}" } } }
`)

	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "world"}), spec)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := st.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 verb call, got %d", len(calls))
	}
	if calls[0].Opts["text"] != "hi world" {
		t.Fatalf("set string not read downstream: %+v", calls[0].Opts)
	}
	// n: 7 is an int in the config and must round-trip as one, not "7": a sole
	// {{.vars.n}} resolves to the underlying typed value.
	meta, _ := calls[0].Opts["meta"].(map[string]any)
	if meta == nil || meta["count"] != 7 {
		t.Fatalf("set int did not round-trip typed: meta=%+v", meta)
	}
}

// TestAssertPassesAndFails: assert is a guard — a truthy condition is a no-op,
// a falsy one fails the run with a clear message.
func TestAssertPassesAndFails(t *testing.T) {
	cfg := loadConfig(t, b2Cfg)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")

	// Passing assertion: the run completes.
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "ok"}), mustSpec(t, `
on: svc.ping
steps:
  - { id: guard, assert: "msg == ok" }
`))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("truthy assert should not fail: %s", errStr)
	}

	// Failing assertion: the run fails, naming the condition.
	rig2 := newTestRunner(t, cfg, reg)
	runTrigger(rig2, newTrigger("ping", map[string]any{"msg": "nope"}), mustSpec(t, `
on: svc.ping
steps:
  - { id: guard, assert: "msg == ok" }
`))
	failed, errStr := rig2.workflowFailed()
	if !failed {
		t.Fatal("falsy assert should fail the run")
	}
	if !strings.Contains(errStr, "assertion failed") {
		t.Fatalf("error = %q, want it to name the failed assertion", errStr)
	}
}

// TestFailStopsRun: `fail:` aborts with a rendered message; behind an `if:` it
// only fires when the guard is true.
func TestFailStopsRun(t *testing.T) {
	cfg := loadConfig(t, b2Cfg)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")

	// if: false → fail is skipped, the run completes.
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "fine"}), mustSpec(t, `
on: svc.ping
steps:
  - { id: bail, if: "msg == broken", fail: "msg was {{.msg}}" }
`))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("if:-false fail should be skipped: %s", errStr)
	}

	// if: true → fail fires with the rendered message.
	rig2 := newTestRunner(t, cfg, reg)
	runTrigger(rig2, newTrigger("ping", map[string]any{"msg": "broken"}), mustSpec(t, `
on: svc.ping
steps:
  - { id: bail, if: "msg == broken", fail: "msg was {{.msg}}" }
`))
	failed, errStr := rig2.workflowFailed()
	if !failed {
		t.Fatal("fail behind a true if: should abort the run")
	}
	if !strings.Contains(errStr, "msg was broken") {
		t.Fatalf("error = %q, want the rendered fail message", errStr)
	}
}

// TestLogEmits: `log:` renders its message into the run log.
func TestLogEmits(t *testing.T) {
	cfg := loadConfig(t, b2Cfg)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	sink := captureLog(rig)

	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "here"}), mustSpec(t, `
on: svc.ping
steps:
  - { id: note, log: "reached {{.msg}}" }
`))

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if got := sink.String(); !strings.Contains(got, "reached here") {
		t.Fatalf("log message not in run log:\n%s", got)
	}
}

// TestWaitForPollsUntilCondition: wait_for polls a read verb, re-templating its
// options each attempt, until the condition (over the verb's outputs) holds —
// then a later step can read the final poll's outputs. The scripted verb is
// in_progress for the first two polls, completed on the third.
func TestWaitForPollsUntilCondition(t *testing.T) {
	cfg := loadConfig(t, b2Cfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.outputsFn["poll"] = func(callIdx int, opts map[string]any) map[string]any {
		if callIdx < 2 {
			return map[string]any{"status": "in_progress"}
		}
		return map[string]any{"status": "completed", "conclusion": "cancelled"}
	}
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep // no real waiting between polls

	spec := mustSpec(t, `
on: svc.ping
steps:
  - wait_for:
      uses: svc.poll
      options: { repo: "{{.repo}}" }
      until: "status == completed"
      every: 5ms
      timeout: 5s
    id: wait
  - { id: after, uses: svc.post, options: { text: "done {{.wait.conclusion}}" } }
`)

	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if n := st.count("poll"); n != 3 {
		t.Fatalf("expected 3 polls (in_progress, in_progress, completed), got %d", n)
	}
	// Options are rendered per poll: the verb saw the templated repo each time.
	for _, c := range st.snapshot() {
		if c.Verb == "poll" && c.Opts["repo"] != "o/r" {
			t.Fatalf("poll options not templated per attempt: %+v", c.Opts)
		}
	}
	// The wait's final outputs are addressable by the following step.
	var post *fakeCall
	for i := range st.snapshot() {
		if c := st.snapshot()[i]; c.Verb == "post" {
			post = &c
		}
	}
	if post == nil || post.Opts["text"] != "done cancelled" {
		t.Fatalf("wait_for outputs not readable downstream: %+v", post)
	}
}

// TestWaitForTimesOut: a condition that never holds fails the step when the
// timeout elapses, with a message that says so. Real (short) waits here so the
// deadline is reached rather than spun past.
func TestWaitForTimesOut(t *testing.T) {
	cfg := loadConfig(t, b2Cfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.outputsFn["poll"] = func(callIdx int, opts map[string]any) map[string]any {
		return map[string]any{"status": "in_progress"} // never completes
	}
	rig := newTestRunner(t, cfg, reg)

	spec := mustSpec(t, `
on: svc.ping
steps:
  - wait_for:
      uses: svc.poll
      until: "status == completed"
      every: 10ms
      timeout: 40ms
    id: wait
  - { id: never, uses: svc.post, options: { text: a } }
`)

	start := time.Now()
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	elapsed := time.Since(start)

	failed, errStr := rig.workflowFailed()
	if !failed {
		t.Fatal("a wait_for whose condition never holds should time out and fail")
	}
	if !strings.Contains(errStr, "timed out") {
		t.Fatalf("error = %q, want a timeout message", errStr)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("timeout took %s — the bound is not being enforced", elapsed)
	}
	if st.count("post") != 0 {
		t.Fatal("the step after a timed-out wait_for should not run")
	}
	if st.count("poll") == 0 {
		t.Fatal("wait_for never polled")
	}
}

// TestWaitForDryRun: shadow mode does one stubbed poll, does not wait, and says
// what it would do — a replay of a config with `timeout: 10s` must be instant.
func TestWaitForDryRun(t *testing.T) {
	cfg := loadConfig(t, b2Cfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	sink := captureLog(rig)
	rig.Runner.DryRun = true

	spec := mustSpec(t, `
on: svc.ping
steps:
  - wait_for:
      uses: svc.poll
      until: "status == completed"
      every: 1s
      timeout: 10s
    id: wait
`)

	start := time.Now()
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	elapsed := time.Since(start)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("dry-run wait_for failed: %s", errStr)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("dry run took %s — it waited for real", elapsed)
	}
	if got := sink.String(); !strings.Contains(got, "would poll svc.poll until") {
		t.Fatalf("dry-run log missing the intent:\n%s", got)
	}
	// A dry run stubs the verb rather than really invoking it.
	if st.count("poll") != 0 {
		t.Fatalf("dry run really invoked the poll verb %d times, want 0", st.count("poll"))
	}
}

// TestWaitForCancelInterrupts: a shutdown drops out of a long wait immediately
// with the context's error, not our timeout message.
func TestWaitForCancelInterrupts(t *testing.T) {
	cfg := loadConfig(t, b2Cfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.outputsFn["poll"] = func(callIdx int, opts map[string]any) map[string]any {
		return map[string]any{"status": "in_progress"}
	}
	rig := newTestRunner(t, cfg, reg)

	spec := mustSpec(t, `
on: svc.ping
steps:
  - wait_for:
      uses: svc.poll
      until: "status == completed"
      every: 50ms
      timeout: 30s
    id: wait
`)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()

	start := time.Now()
	runTriggerCtx(ctx, rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	elapsed := time.Since(start)
	cancel()

	if elapsed > 3*time.Second {
		t.Fatalf("cancel took %s to interrupt a 30s-timeout wait — not ctx-aware", elapsed)
	}
	failed, errStr := rig.workflowFailed()
	if !failed {
		t.Fatal("a cancelled wait_for should fail its step")
	}
	if strings.Contains(errStr, "timed out") {
		t.Fatalf("error = %q, want the context's error, not our timeout", errStr)
	}
	if !strings.Contains(errStr, "context canceled") {
		t.Fatalf("error = %q, want 'context canceled'", errStr)
	}
	_ = st
}
