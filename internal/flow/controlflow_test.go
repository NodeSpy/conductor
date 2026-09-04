package flow

import (
	"strings"
	"testing"
	"time"
)

// TestForEachFansOverList: for_each maps one step over a context list with
// {{.item}}/{{.index}} in scope; outputs land under {items, count}.
func TestForEachFansOverList(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: fan
    for_each: "{{.hosts}}"
    uses: svc.post
    options: { text: "{{.index}}:{{.item}}" }
  - id: after
    uses: svc.post
    options: { text: "did {{.fan.count}}" }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"hosts": []any{"a", "b", "c"}}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := st.snapshot()
	if len(calls) != 4 {
		t.Fatalf("want 3 fan calls + 1 after, got %d", len(calls))
	}
	for i, want := range []string{"0:a", "1:b", "2:c"} {
		if got := calls[i].Opts["text"]; got != want {
			t.Errorf("iteration %d text = %v, want %q", i, got, want)
		}
	}
	if got := calls[3].Opts["text"]; got != "did 3" {
		t.Errorf("after step should read the fan-out count, got %v", got)
	}
}

// TestParallelBranchesJoin: both branches run, and the join publishes each
// branch step's outputs into the parent scope for later steps.
func TestParallelBranchesJoin(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.outputs["post"] = map[string]any{"id": 9}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: par
    parallel:
      - [ { id: pa, uses: svc.post, options: { text: branch-a } } ]
      - [ { id: pb, uses: svc.post, options: { text: branch-b } } ]
  - id: join
    uses: svc.post
    options: { text: "a={{.pa.id}} b={{.pb.id}} n={{.par.branches}}" }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := st.snapshot()
	if len(calls) != 3 {
		t.Fatalf("want 2 branch calls + 1 join, got %d", len(calls))
	}
	texts := map[string]bool{}
	for _, c := range calls[:2] {
		texts[c.Opts["text"].(string)] = true
	}
	if !texts["branch-a"] || !texts["branch-b"] {
		t.Errorf("both branches should run, got %v", texts)
	}
	if got := calls[2].Opts["text"]; got != "a=9 b=9 n=2" {
		t.Errorf("join should read both branches' outputs, got %v", got)
	}
}

// TestParallelBranchFailureFailsWorkflow: a failing branch fails the join.
func TestParallelBranchFailureFailsWorkflow(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: par
    parallel:
      - [ { id: good, uses: svc.post, options: { text: ok } } ]
      - [ { id: bad, uses: svc.fail } ]
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "branch") {
		t.Fatalf("want a branch failure, got failed=%v err=%q", failed, errStr)
	}
}

// TestStepTimeout: a step's timeout: bounds a slow verb — the workflow fails
// promptly with the deadline error instead of waiting the verb out.
func TestStepTimeout(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.slowMS["slow"] = 5 * time.Second
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: s, uses: svc.slow, timeout: 50ms }
`)
	rig := newTestRunner(t, cfg, reg)
	start := time.Now()
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	took := time.Since(start)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "context deadline exceeded") {
		t.Fatalf("want a deadline failure, got failed=%v err=%q", failed, errStr)
	}
	if took > 2*time.Second {
		t.Fatalf("timeout did not bound the step (took %s)", took)
	}
}

// TestStepIfSkips: a false if: skips the step (no invocation, no failure);
// later steps still run.
func TestStepIfSkips(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: skipped, if: '{{.msg}} == "other"', uses: svc.fail }
  - { id: ran, uses: svc.post, options: { text: after-skip } }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "hello"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := st.snapshot()
	if len(calls) != 1 || calls[0].Verb != "post" {
		t.Fatalf("only the post step should run, got %+v", calls)
	}
}

// TestStepRetrySucceedsAfterFlakes: retry.max re-runs a flaky verb until it
// succeeds; the workflow completes.
func TestStepRetrySucceedsAfterFlakes(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.failTimes["post"] = 2
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: flaky
    uses: svc.post
    options: { text: x }
    retry: { max: 2, backoff: 1ms }
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if got := st.count("post"); got != 3 {
		t.Fatalf("want 3 attempts (2 flakes + success), got %d", got)
	}
}

// TestStepRetryExhaustedFails: more failures than retry.max fails the step.
func TestStepRetryExhaustedFails(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { type: fake }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	st.failTimes["post"] = 5
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: flaky
    uses: svc.post
    options: { text: x }
    retry: { max: 1, backoff: 1ms }
`)
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.sleep = fastSleep
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "flaky") {
		t.Fatalf("want retry exhaustion, got failed=%v err=%q", failed, errStr)
	}
	if got := st.count("post"); got != 2 {
		t.Fatalf("want 2 attempts (1 + 1 retry), got %d", got)
	}
}
