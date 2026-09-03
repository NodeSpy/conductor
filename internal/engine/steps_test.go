package engine

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
	"github.com/NodeSpy/paseo-conductor/internal/handoff"
	"github.com/NodeSpy/paseo-conductor/internal/notify"
	"github.com/NodeSpy/paseo-conductor/internal/store"
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
	archive   map[string]bool     // step id -> req.Profile.ArchiveWhenDone (→ archive=1 label)
	prompts   map[string]string   // step id -> dispatched prompt (with guidance appended)
}

func (f *stepFake) Dispatch(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
	id := req.Action.ID
	f.mu.Lock()
	f.ran = append(f.ran, id)
	f.provider[id] = req.Profile.Provider
	f.waited[id] = req.Wait
	f.archive[id] = req.Profile.ArchiveWhenDone
	f.prompts[id] = req.Action.Prompt
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

func (f *stepFake) WaitForAgent(context.Context, string, time.Duration) {}
func (f *stepFake) HasLiveAgent(context.Context, string, string) bool   { return false }
func (f *stepFake) Archive(context.Context, string) error               { return nil }

func (f *stepFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ran)
}

func newStepFake() *stepFake {
	return &stepFake{outputs: map[string]string{}, provider: map[string]string{},
		seenSteps: map[string][]string{}, waited: map[string]bool{}, archive: map[string]bool{},
		prompts: map[string]string{}}
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
	return core.Trigger{Source: "github", Instance: "i", Kind: "issue_labeled",
		Target: core.Target{Repo: "acme/w", Issue: 42, Number: 42}}
}

func TestWorkflowBranchHasContext(t *testing.T) {
	d := newStepFake()
	d.outputs["evaluate"] = `{"has_context": true, "summary": "clear repro"}`
	e := stepEngine(t, d)

	e.runSteps(context.Background(), store.WorkflowRun{Outputs: map[string]map[string]any{}}, issueTrigger(), triageAction(), "app", "usr", false)

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

	e.runSteps(context.Background(), store.WorkflowRun{Outputs: map[string]map[string]any{}}, issueTrigger(), triageAction(), "app", "usr", false)

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
	e.runSteps(context.Background(), store.WorkflowRun{Outputs: map[string]map[string]any{}}, issueTrigger(), act, "app", "usr", false)

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
	// ...and is instructed to conclude by ASKING (so paseo shows "needs your input",
	// not just "ready"): the hand-off guidance must be appended to its prompt.
	if !strings.Contains(d.prompts["handoff"], "AskUserQuestion") {
		t.Fatalf("hand-off prompt should carry HandoffGuidance (ask via AskUserQuestion); got: %q", d.prompts["handoff"])
	}
}

// TestWorkflowBackgroundStepNotReapable pins the fix for the review handoff that
// got reaped 53s after launch: a background step is handed to the user to drive, so
// it must be forced non-archivable (no archive=1 label → the reaper never touches
// it), even when its profile opts into archive_when_done.
func TestWorkflowBackgroundStepNotReapable(t *testing.T) {
	d := newStepFake()
	cfg := &config.Config{Agents: map[string]config.AgentProfile{
		// Profile mistakenly (or staleley) opts into archiving.
		"interactive": {Provider: "claude-sonnet", ArchiveWhenDone: true},
	}}
	cfg.Control.Enabled = ptrBool(true)
	e := New(Options{Config: cfg, Store: tempStore(t), Dispatch: d, Notifier: &fakeNotifier{},
		Author: dispatch.Author{}, UserToken: func() (string, error) { return "u", nil }})

	act := config.Action{Steps: []config.Action{
		{ID: "handoff", Type: "agent", Agent: "interactive", Background: true, Prompt: "hand off {{.issue}}"},
	}}
	e.runSteps(context.Background(), store.WorkflowRun{Outputs: map[string]map[string]any{}}, issueTrigger(), act, "app", "usr", false)

	if d.archive["handoff"] {
		t.Fatal("background hand-off must dispatch with ArchiveWhenDone=false so the reaper can't cull it")
	}
}

// TestWorkflowBackgroundStepUnknownHandoffEscalates covers a step naming a
// `handoff:` the registry doesn't have (config validation should normally catch
// this before a live trigger reaches here — see config.CheckAgentRefs — but the
// engine must still fail loudly rather than silently, if it somehow does).
func TestWorkflowBackgroundStepUnknownHandoffEscalates(t *testing.T) {
	d := newStepFake()
	n := &fakeNotifier{}
	cfg := &config.Config{Agents: map[string]config.AgentProfile{
		"interactive": {Provider: "claude-sonnet"},
	}}
	cfg.Control.Enabled = ptrBool(true)
	reg := handoff.NewRegistry(nil, "", nil) // no entries at all
	e := New(Options{Config: cfg, Store: tempStore(t), Dispatch: d, Notifier: n, Handoffs: reg,
		Author: dispatch.Author{}, UserToken: func() (string, error) { return "u", nil }})

	act := config.Action{Steps: []config.Action{
		{ID: "handoff", Type: "agent", Agent: "interactive", Background: true,
			Handoff: "does-not-exist", Prompt: "draft and hand off {{.issue}}"},
	}}
	e.runSteps(context.Background(), store.WorkflowRun{Outputs: map[string]map[string]any{}}, issueTrigger(), act, "app", "usr", false)

	if !n.has(notify.EventEscalate) {
		t.Fatalf("an unresolvable handoff name should escalate, got %v", n.events)
	}
	// It must still fall back to the standard "open it in paseo" needs_input, same
	// as when no handoff is configured at all.
	if !n.has(notify.EventNeedsInput) {
		t.Fatalf("should still fall back to needs_input, got %v", n.events)
	}
}

// TestWorkflowBackgroundStepHandoffResolvesButNoBrokerFallsBack: a real handoff
// resolves cleanly, but with no session broker configured the engine can't rewire
// the review loop, so it must keep today's plain fallback (no escalate).
func TestWorkflowBackgroundStepHandoffResolvesButNoBrokerFallsBack(t *testing.T) {
	d := newStepFake()
	n := &fakeNotifier{}
	cfg := &config.Config{Agents: map[string]config.AgentProfile{
		"interactive": {Provider: "claude-sonnet"},
	}}
	cfg.Control.Enabled = ptrBool(true)
	reg := handoff.NewRegistry(map[string]config.HandoffConfig{
		"page": {Web: &config.HandoffWeb{BaseURL: "https://conductor.example.com"}},
	}, "", nil)
	e := New(Options{Config: cfg, Store: tempStore(t), Dispatch: d, Notifier: n, Handoffs: reg,
		Author: dispatch.Author{}, UserToken: func() (string, error) { return "u", nil }})

	act := config.Action{Steps: []config.Action{
		{ID: "handoff", Type: "agent", Agent: "interactive", Background: true,
			Handoff: "page", Prompt: "draft and hand off {{.issue}}"},
	}}
	e.runSteps(context.Background(), store.WorkflowRun{Outputs: map[string]map[string]any{}}, issueTrigger(), act, "app", "usr", false)

	if n.has(notify.EventEscalate) {
		t.Fatalf("a resolvable handoff with no broker should not escalate, got %v", n.events)
	}
	if !n.has(notify.EventNeedsInput) {
		t.Fatalf("should fall back to needs_input (no broker to rewire the review loop), got %v", n.events)
	}
}

func TestLogOutputs(t *testing.T) {
	if got := logOutputs(nil); got != "" {
		t.Fatalf("empty outputs should render nothing, got %q", got)
	}
	got := logOutputs(map[string]any{"decision": "manual"})
	if got != ` → {"decision":"manual"}` {
		t.Fatalf("unexpected render: %q", got)
	}
}

func TestWorkflowResumesFromCheckpoint(t *testing.T) {
	d := newStepFake()
	st := tempStore(t)
	cfg := &config.Config{Agents: map[string]config.AgentProfile{
		"planner": {Provider: "claude-haiku"}, "worker": {Provider: "claude-opus"}}}
	cfg.Control.Enabled = ptrBool(true)
	e := New(Options{Config: cfg, Store: st, Dispatch: d, Notifier: &fakeNotifier{},
		Author: dispatch.Author{}, UserToken: func() (string, error) { return "u", nil },
		RefreshAppToken: func(core.Trigger) (string, error) { return "app", nil }})

	// Persist a run checkpointed AFTER step 0 (evaluate) with has_context=true.
	tr := issueTrigger()
	tr.Instance = "i"
	tp := tr
	tp.Action = nil
	trigJSON, _ := json.Marshal(tp)
	actJSON, _ := json.Marshal(triageAction())
	_ = st.PutRun(store.WorkflowRun{ID: "run1", Instance: "i",
		Trigger: trigJSON, Action: actJSON, StepIndex: 1,
		Outputs: map[string]map[string]any{"evaluate": {"has_context": true}}})

	e.ResumeWorkflows(context.Background())
	waitFor(t, func() bool { return d.count() >= 1 })

	// evaluate (step 0) must NOT re-run; only "work" (step 1) does.
	if got := d.ran; len(got) != 1 || got[0] != "work" {
		t.Fatalf("resume should run only 'work' from the checkpoint, got %v", got)
	}
	// The run is cleared once it completes.
	waitFor(t, func() bool { return len(st.PendingRuns()) == 0 })
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

func TestTailOutput(t *testing.T) {
	// Drops the (long) go-download preamble, keeps the meaningful tail (last 8 lines).
	in := "go: downloading a\ngo: downloading b\ngo: downloading c\ngo: downloading d\n" +
		"go: downloading e\ngo: downloading f\ngo: downloading g\ngo: downloading h\n" +
		"reviewing X#1 (post=true)\nstatus: done\nposted: true\n"
	got := tailOutput(in)
	if !strings.Contains(got, "status: done") || !strings.Contains(got, "posted: true") {
		t.Fatalf("tail should keep the result lines, got: %q", got)
	}
	if strings.Contains(got, "downloading a") {
		t.Fatalf("tail should drop the download preamble, got: %q", got)
	}
	if tailOutput("") != "" || tailOutput("\n\n  \n") != "" {
		t.Fatal("blank output should yield empty tail")
	}
}
