package engine

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/notify"
)

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

// stepFake returns canned per-step output and records what ran. It's mutex-
// guarded because process() runs the workflow in a goroutine.
type stepFake struct {
	mu        sync.Mutex
	outputs   map[string]string   // step id -> JSON output
	ran       []string            // step ids, in order
	provider  map[string]string   // step id -> agent provider used
	seenSteps map[string][]string // step id -> which prior step outputs were visible
	waited    map[string]bool     // step id -> req.Wait
}

func (f *stepFake) Dispatch(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	id := req.Action.ID
	f.mu.Lock()
	f.ran = append(f.ran, id)
	f.provider[id] = req.Profile.Provider
	f.waited[id] = req.Wait
	if s, ok := req.Data["steps"].(map[string]any); ok {
		keys := []string{}
		for k := range s {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		f.seenSteps[id] = keys
	}
	out := f.outputs[id]
	f.mu.Unlock()
	return dispatch.RunRef{Backend: "test", Kind: req.Trigger.Kind, Output: out}, nil
}

func (f *stepFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ran)
}

func newStepFake() *stepFake {
	return &stepFake{outputs: map[string]string{}, provider: map[string]string{},
		seenSteps: map[string][]string{}, waited: map[string]bool{}}
}

func triageAction() config.Action {
	return config.Action{Steps: []config.Action{
		{ID: "evaluate", Type: "agent", Agent: "planner", Prompt: "evaluate {{.issue}}",
			OutputSchema: map[string]any{"type": "object"}},
		{ID: "work", If: "steps.evaluate.outputs.has_context == true", Type: "agent",
			Agent: "worker", Prompt: "work: {{.steps.evaluate.outputs.summary}}"},
		{ID: "ask", If: "steps.evaluate.outputs.has_context == false", Type: "command",
			Command: []string{"gh", "issue", "comment", "{{.repo}}#{{.issue}}", "--body", "need more info"}},
	}}
}

func stepEngine(t *testing.T, d *stepFake) *Engine {
	cfg := &config.Config{Agents: map[string]config.AgentProfile{
		"planner": {Provider: "claude-haiku"}, // cheap model to plan
		"worker":  {Provider: "claude-opus"},  // strong model to do the work
	}}
	cfg.Control.Enabled = ptrBool(true)
	return New(Options{Config: cfg, Store: tempStore(t), Dispatch: d, Notifier: &fakeNotifier{},
		Author: dispatch.Author{}, UserToken: func() (string, error) { return "u", nil }})
}

func issueTrigger() core.Trigger {
	return core.Trigger{Source: "github", Instance: "i", Kind: "issue_ready",
		Target: core.Target{Repo: "acme/w", Issue: 42, Number: 42}}
}

func TestWorkflowBranchHasContext(t *testing.T) {
	d := newStepFake()
	d.outputs["evaluate"] = `{"has_context": true, "summary": "clear repro"}`
	e := stepEngine(t, d)

	e.runSteps(context.Background(), issueTrigger(), triageAction(), "app", "usr", false)

	if got := d.ran; len(got) != 2 || got[0] != "evaluate" || got[1] != "work" {
		t.Fatalf("expected evaluate→work, got %v", got)
	}
	// Different models per step.
	if d.provider["evaluate"] != "claude-haiku" || d.provider["work"] != "claude-opus" {
		t.Fatalf("per-step models wrong: %v", d.provider)
	}
	// The worker step saw the evaluate outputs.
	if len(d.seenSteps["work"]) != 1 || d.seenSteps["work"][0] != "evaluate" {
		t.Fatalf("work step did not see evaluate outputs: %v", d.seenSteps["work"])
	}
	// Steps run foreground (wait) to capture output.
	if !d.waited["evaluate"] || !d.waited["work"] {
		t.Fatalf("steps should run with Wait=true: %v", d.waited)
	}
}

func TestWorkflowBranchNoContext(t *testing.T) {
	d := newStepFake()
	d.outputs["evaluate"] = `{"has_context": false}`
	e := stepEngine(t, d)

	e.runSteps(context.Background(), issueTrigger(), triageAction(), "app", "usr", false)

	if got := d.ran; len(got) != 2 || got[0] != "evaluate" || got[1] != "ask" {
		t.Fatalf("expected evaluate→ask, got %v", got)
	}
}

func TestWorkflowBackgroundStepHandsOff(t *testing.T) {
	d := newStepFake()
	n := &fakeNotifier{}
	cfg := &config.Config{Agents: map[string]config.AgentProfile{
		"interactive": {Provider: "claude-sonnet"},
	}}
	cfg.Control.Enabled = ptrBool(true)
	e := New(Options{Config: cfg, Store: tempStore(t), Dispatch: d, Notifier: n,
		Author: dispatch.Author{}, UserToken: func() (string, error) { return "u", nil }})

	act := config.Action{Steps: []config.Action{
		{ID: "handoff", Type: "agent", Agent: "interactive", Background: true,
			Prompt: "draft and hand off {{.issue}}"},
	}}
	e.runSteps(context.Background(), issueTrigger(), act, "app", "usr", false)

	if got := d.ran; len(got) != 1 || got[0] != "handoff" {
		t.Fatalf("expected handoff to run, got %v", got)
	}
	// A background step launches a live agent instead of blocking on it.
	if d.waited["handoff"] {
		t.Fatalf("background step should dispatch with Wait=false")
	}
	// ...and tells the user it's waiting for them.
	if !n.has(notify.EventNeedsInput) {
		t.Fatalf("background step should emit needs_input, got %v", n.events)
	}
}

func TestWorkflowViaProcess(t *testing.T) {
	// Exercised through process() (which records dedup and spawns the workflow).
	d := newStepFake()
	d.outputs["evaluate"] = `{"has_context": true}`
	e := stepEngine(t, d)
	tr := issueTrigger()
	tr.Dedup = "sig-1"
	tr.Action = triageAction()

	e.process(context.Background(), tr)
	// process spawns runSteps in a goroutine; wait for it to record + run.
	waitFor(t, func() bool { return d.count() == 2 })
	if e.store.LastSignature(tr.Key(), tr.Kind) != "sig-1" {
		t.Fatal("workflow should record its dedup signature")
	}
}
