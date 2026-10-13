package engine

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// TestResumeRecheckRecoversAfterATransientMintError is finding 2's regression
// test: ResumeWorkflows used to defer a run on a mint error and never revisit
// it until the next daemon restart. A transient error (the plugin is still
// starting) must recover on its own, within the SAME daemon run, once the
// scheduled recheck fires.
func TestResumeRecheckRecoversAfterATransientMintError(t *testing.T) {
	// Long enough that "not dispatched yet" right after ResumeWorkflows is
	// a real check, not a race with a 1ms timer under a loaded -race run.
	setDeferTimings(t, time.Millisecond, []time.Duration{500 * time.Millisecond})

	d := newStepFake()
	st := tempStore(t)
	cfg := &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
		{ID: "planner"}, {ID: "worker"}}}}}
	var calls int32
	e := New(Options{Config: cfg, Store: st, Dispatch: d, Notifier: &fakeNotifier{},
		Author: dispatch.Author{}, Connectors: forgeRegistry(t),
		InvokeVerb: func(context.Context, string, string, map[string]any) (map[string]any, error) {
			// The resumed run's first full mint attempt (read_token AND
			// write_token, 2 calls) fails; every attempt after that
			// succeeds.
			if atomic.AddInt32(&calls, 1) <= 2 {
				return nil, errors.New("plugin not running yet")
			}
			return map[string]any{"token": "t"}, nil
		}})
	tr := issueTrigger()
	tr.Instance, tr.TargetTrusted = "i", true
	tp := tr
	tp.Action = nil
	trigJSON, _ := json.Marshal(tp)
	actJSON, _ := json.Marshal(triageAction())
	if err := st.PutRun(store.WorkflowRun{ID: "run1", Instance: "i",
		Trigger: trigJSON, Action: actJSON, StepIndex: 1,
		Outputs: map[string]map[string]any{"evaluate": {"has_context": true}}}); err != nil {
		t.Fatal(err)
	}

	e.ResumeWorkflows(context.Background())
	if d.count() != 0 {
		t.Fatal("the run dispatched before its scheduled recheck even ran")
	}
	waitCond(t, "the resumed run recovers and dispatches after its transient mint error", func() bool {
		return d.count() > 0
	})
	// The run leaves the pending set just after it dispatches, on the same
	// goroutine — wait for that too rather than racing it.
	waitCond(t, "a recovered, dispatched run is no longer pending", func() bool {
		return len(st.PendingRuns()) == 0
	})
}

// TestResumeRecheckGivesUpAfterMaxAttempts is finding 2's bound: a plugin
// whose mint NEVER recovers must stop being rechecked after
// resumeRecheckMaxAttempts, leaving the run pending for the next daemon
// start — not rechecked forever, and not left with zero further attempts
// either (the bug this replaces).
func TestResumeRecheckGivesUpAfterMaxAttempts(t *testing.T) {
	setDeferTimings(t, time.Millisecond, []time.Duration{time.Millisecond})

	d := newStepFake()
	st := tempStore(t)
	cfg := &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
		{ID: "planner"}, {ID: "worker"}}}}}
	var calls int32
	e := New(Options{Config: cfg, Store: st, Dispatch: d, Notifier: &fakeNotifier{},
		Author: dispatch.Author{}, Connectors: forgeRegistry(t),
		InvokeVerb: func(context.Context, string, string, map[string]any) (map[string]any, error) {
			atomic.AddInt32(&calls, 1)
			return nil, errors.New("plugin never comes up")
		}})
	tr := issueTrigger()
	tr.Instance, tr.TargetTrusted = "i", true
	tp := tr
	tp.Action = nil
	trigJSON, _ := json.Marshal(tp)
	actJSON, _ := json.Marshal(triageAction())
	if err := st.PutRun(store.WorkflowRun{ID: "run1", Instance: "i",
		Trigger: trigJSON, Action: actJSON, StepIndex: 1,
		Outputs: map[string]map[string]any{"evaluate": {"has_context": true}}}); err != nil {
		t.Fatal(err)
	}

	e.ResumeWorkflows(context.Background())

	// The initial attempt plus resumeRecheckMaxAttempts rechecks, 2
	// invokeVerb calls (read_token, write_token) each.
	want := int32(2 * (resumeRecheckMaxAttempts + 1))
	waitForAtLeast(t, &calls, want, 3*time.Second, "the resume recheck never exhausted its bounded attempts")
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != want {
		t.Fatalf("invokeVerb called %d times, want exactly %d — the resume recheck did not stop at the cap", got, want)
	}
	if d.count() != 0 {
		t.Fatal("a run that never got credentials must never dispatch")
	}
	if len(st.PendingRuns()) != 1 {
		t.Fatal("the exhausted run must stay pending for the next daemon start")
	}
}
