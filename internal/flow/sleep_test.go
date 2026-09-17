package flow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/store"
)

// The `sleep:` helper step (helpers.go): it really waits, cancellation cuts
// it short, a dry run does not wait at all, and it slots into the step loop
// (if:, outputs, history) like any other non-agent form.

const sleepCfg = `
connectors:
  svc: { use: fake }
`

// captureLog redirects the runner's log into a buffer a test can read.
func captureLog(rig *testRig) *logSink {
	sink := &logSink{}
	rig.Runner.Log = sink.write
	return sink
}

type logSink struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logSink) write(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b.WriteString(strings.TrimSpace(fmt.Sprintf(format, args...)) + "\n")
}

func (l *logSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestSleepStepActuallyWaits: a `sleep: 50ms` step delays the run by about
// that long — the wait is real, not a no-op that merely reports success.
func TestSleepStepActuallyWaits(t *testing.T) {
	cfg := loadConfig(t, sleepCfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: before, uses: svc.post, options: { text: a } }
  - { id: nap, sleep: 50ms }
  - { id: after, uses: svc.post, options: { text: b } }
`)

	start := time.Now()
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	elapsed := time.Since(start)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	// Timers fire no earlier than their deadline; allow a little slack for a
	// coarse clock rather than demanding the full 50ms to the nanosecond.
	if elapsed < 45*time.Millisecond {
		t.Fatalf("run took %s — the sleep step did not wait", elapsed)
	}
	// A wildly longer run would mean something other than the sleep blocked.
	if elapsed > 5*time.Second {
		t.Fatalf("run took %s — far longer than the 50ms sleep", elapsed)
	}
	if calls := st.snapshot(); len(calls) != 2 {
		t.Fatalf("expected the two verb steps to run around the sleep, got %d", len(calls))
	}
}

// TestSleepStepInterruptedByCancel: a cancelled run drops out of a `sleep:
// 10s` immediately instead of holding the run for ten seconds. This is the
// shutdown/budget/timeout path — r.sleep races the timer against ctx.Done().
func TestSleepStepInterruptedByCancel(t *testing.T) {
	cfg := loadConfig(t, sleepCfg)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: nap, sleep: 10s }
  - { id: never, uses: svc.post, options: { text: a } }
`)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	runTriggerCtx(ctx, rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	elapsed := time.Since(start)
	cancel()

	if elapsed > 3*time.Second {
		t.Fatalf("cancel took %s to interrupt a 10s sleep — the wait is not ctx-aware", elapsed)
	}
	failed, errStr := rig.workflowFailed()
	if !failed {
		t.Fatal("a cancelled sleep should fail its step, not report success")
	}
	if !strings.Contains(errStr, "context canceled") {
		t.Fatalf("error = %q, want the context's own error", errStr)
	}
}

// TestSleepStepDryRunDoesNotWait: shadow mode prints the intent and returns
// at once — a replay of a config with `sleep: 10s` must not take ten seconds.
func TestSleepStepDryRunDoesNotWait(t *testing.T) {
	cfg := loadConfig(t, sleepCfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	sink := captureLog(rig)
	rig.Runner.DryRun = true

	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: nap, sleep: 10s }
  - { id: after, uses: svc.post, options: { text: a } }
`)

	start := time.Now()
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	elapsed := time.Since(start)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("dry run took %s — it slept for real", elapsed)
	}
	if got := sink.String(); !strings.Contains(got, "would sleep 10s") {
		t.Fatalf("dry-run log missing the intent:\n%s", got)
	}
	if calls := st.snapshot(); len(calls) != 0 {
		t.Fatalf("dry run invoked %d verbs, want 0", len(calls))
	}
}

// TestSleepStepHonorsIfAndRecordsHistory: a sleep step is an ordinary step —
// `if:` skips it (and skipping is fast, i.e. it really did not sleep), and a
// sleep that does run lands in the run timeline with its own record.
func TestSleepStepHonorsIfAndRecordsHistory(t *testing.T) {
	cfg := loadConfig(t, sleepCfg)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	spec := mustSpec(t, `
on: svc.ping
name: naps
steps:
  - { id: skipped-nap, if: "{{.msg}} == never", sleep: 30s }
  - { id: taken-nap, if: "{{.msg}} == x", sleep: 20ms }
`)

	run := store.WorkflowRun{ID: "ping:nap", Outputs: map[string]map[string]any{}}
	start := time.Now()
	runTriggerWithRun(rig, run, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	elapsed := time.Since(start)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("run took %s — the if:-false 30s sleep was not skipped", elapsed)
	}

	rec, ok := rig.Store.lastHistory()
	if !ok {
		t.Fatal("no history recorded")
	}
	skipped, ok := rec.Step("skipped-nap")
	if !ok || skipped.Status != "skipped" {
		t.Fatalf("skipped-nap: %+v (found=%v)", skipped, ok)
	}
	taken, ok := rec.Step("taken-nap")
	if !ok || taken.Status != "ok" {
		t.Fatalf("taken-nap: %+v (found=%v)", taken, ok)
	}
	// Empty outputs, not a missing step: the helper reports nothing, and a
	// later template reading {{.steps.taken-nap.outputs}} gets a map.
	if len(taken.Outputs) != 0 {
		t.Fatalf("taken-nap outputs = %+v, want empty", taken.Outputs)
	}
}

// TestSleepBetweenVerbsRealConfig: the documented shape — a sleep wedged
// between two connector-verb steps — loads through the real loader and runs,
// with the verbs firing on either side of the wait in order.
func TestSleepBetweenVerbsRealConfig(t *testing.T) {
	cfg := loadConfigViaLoader(t, `
connectors:
  svc: { use: fake }
triggers:
  - on: svc.ping
    name: rerun
    steps:
      - { id: cancel, uses: svc.post, options: { text: "cancel {{.msg}}" } }
      - sleep: 25ms
      - { id: rerun, uses: svc.post, options: { text: "rerun {{.msg}}" } }
`)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	spec := cfg.Triggers[0]
	start := time.Now()
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "42"}), spec)
	elapsed := time.Since(start)

	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if elapsed < 20*time.Millisecond {
		t.Fatalf("run took %s — the sleep between the verbs did not happen", elapsed)
	}
	calls := st.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected 2 verb calls, got %d", len(calls))
	}
	if calls[0].Opts["text"] != "cancel 42" || calls[1].Opts["text"] != "rerun 42" {
		t.Fatalf("verb order/options: %+v", calls)
	}
}
