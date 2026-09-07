package flow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

// drainEvents collects everything published so far (the hub buffers per
// subscriber; runs here are synchronous so no waiting is needed).
func drainEvents(ch <-chan RunEvent) []RunEvent {
	var out []RunEvent
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func TestRunEventsStream(t *testing.T) {
	cfg := loadConfig(t, histCfg)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	hub := NewEventHub()
	rig.Runner.Events = hub
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}

	ch, cancel := hub.Subscribe("")
	defer cancel()
	run := store.WorkflowRun{ID: "ping:x", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", map[string]any{"msg": "m"}), mustSpec(t, histSpec))

	evs := drainEvents(ch)
	var kinds []string
	for _, ev := range evs {
		kinds = append(kinds, ev.Type+":"+ev.Step+":"+ev.Status)
	}
	want := []string{
		"run_started::running",
		"step_started:greet:running", "step_done:greet:ok",
		"step_done:skipme:skipped",
		"step_started:fix:running", "step_done:fix:ok",
		"run_done::ok",
	}
	if strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Fatalf("event sequence:\n got %v\nwant %v", kinds, want)
	}
	// Every event carries the run identity.
	for _, ev := range evs {
		if ev.Run == "" || ev.RunID != "ping:x" || ev.TS.IsZero() {
			t.Fatalf("event identity: %+v", ev)
		}
	}
}

func TestRunEventsFilterAndSlowSubscriber(t *testing.T) {
	hub := NewEventHub()
	all, cancelAll := hub.Subscribe("")
	defer cancelAll()
	one, cancelOne := hub.Subscribe("rX")
	defer cancelOne()

	hub.Publish(RunEvent{Type: "run_started", Run: "rX"})
	hub.Publish(RunEvent{Type: "run_started", Run: "rY", RunID: "kind:key"})
	hub.Publish(RunEvent{Type: "run_done", Run: "rY"})

	if got := drainEvents(all); len(got) != 3 {
		t.Fatalf("all: %d", len(got))
	}
	got := drainEvents(one)
	if len(got) != 1 || got[0].Run != "rX" {
		t.Fatalf("filtered: %+v", got)
	}
	// A RunID filter matches too.
	byRunID, cancel2 := hub.Subscribe("kind:key")
	defer cancel2()
	hub.Publish(RunEvent{Type: "gate", Run: "rY", RunID: "kind:key"})
	if got := drainEvents(byRunID); len(got) != 1 {
		t.Fatalf("run-id filter: %+v", got)
	}

	// A full buffer drops instead of blocking Publish.
	tiny, cancel3 := hub.Subscribe("")
	defer cancel3()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			hub.Publish(RunEvent{Type: "step_done", Run: "spam"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Publish must never block on a slow subscriber")
	}
	_ = tiny
}

func TestShadowRunsEmitNothing(t *testing.T) {
	cfg := loadConfig(t, histCfg)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	hub := NewEventHub()
	rig.Runner.Events = hub
	ch, cancel := hub.Subscribe("")
	defer cancel()

	spec := mustSpec(t, `
on: svc.ping
shadow: true
steps:
  - { id: s, uses: svc.post, options: { text: x } }
`)
	run := store.WorkflowRun{ID: "ping:z", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
	if evs := drainEvents(ch); len(evs) != 0 {
		t.Fatalf("shadow events: %+v", evs)
	}
}

// The proposed diff (#36 §17) lands in the agent step's outputs — read from
// its worktree, secret-scrubbed — and rides into scope for later steps.
func TestAgentStepCapturesProposedDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// A real worktree with an uncommitted change carrying a tracked secret.
	wd := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", wd}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(wd, "app.env"), []byte("KEY=old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(wd, "app.env"), []byte("KEY=s3kr1t-value\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfig(t, histCfg)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Secrets.Track("s3kr1t-value")
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1", Output: "done", Workdir: wd}, nil
	}

	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, agent: fixer, prompt: "fix" }
  - { id: tell, uses: svc.post, options: { text: "diff is {{.fix.diff}}" } }
`)
	run := store.WorkflowRun{ID: "ping:d", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}

	// The later step saw the diff through scope — scrubbed.
	calls := fake.snapshot()
	text, _ := calls[len(calls)-1].Opts["text"].(string)
	if !strings.Contains(text, "app.env") || !strings.Contains(text, "uncommitted") {
		t.Fatalf("diff in scope: %q", clipText(text, 300))
	}
	if strings.Contains(text, "s3kr1t-value") {
		t.Fatal("tracked secret leaked into the captured diff")
	}
	// And the run record persisted it with the step (timeline + diff + cost).
	rec, ok := rig.Store.lastHistory()
	if !ok {
		t.Fatal("no history")
	}
	step, _ := rec.Step("fix")
	diff, _ := step.Outputs["diff"].(string)
	if !strings.Contains(diff, "app.env") || strings.Contains(diff, "s3kr1t-value") {
		t.Fatalf("history diff: %q", clipText(diff, 200))
	}
}
