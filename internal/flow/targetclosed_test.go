package flow

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// closedSpec is stopSpec with a step after the agent: nothing may run once
// the target closed.
const closedSpec = `
on: %[1]s.ping
hooks:
  - { at: done, uses: %[1]s.post, options: { text: "done" } }
  - { at: fail, uses: %[1]s.post, options: { text: "fail" } }
  - { at: stop, uses: %[1]s.post, options: { text: "stop {{.hook.status}}" } }
steps:
  - id: fix
    type: agent
    prompt: p
    hooks:
      - { at: done, uses: %[1]s.post, options: { text: "step-done" } }
      - { at: fail, uses: %[1]s.post, options: { text: "step-fail" } }
      - { at: stop, uses: %[1]s.post, options: { text: "step-stop" } }
  - id: after
    uses: %[1]s.post
    options: { text: "after" }
`

// The live sequence: the run dispatches its fixer and, in the same instant,
// the PR merges — the stop reaches the run (its context is cancelled with
// ErrTargetClosed), but the agent itself escaped it (a runtime with nothing
// to kill yet) and returns later with committed work in its workspace.
// Nothing is pushed, no later step runs, the run ends stopped, and only the
// stop hooks fire.
func TestTargetClosedMidDispatchPushesNothing(t *testing.T) {
	rig, l := headRig(t, "tcm", "aaaaaaa1111")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		cancel(dispatch.ErrTargetClosed) // the PR closes as the agent starts
		return dispatch.RunRef{Output: "{}", AgentID: "a1", Workdir: t.TempDir()}, nil
	}
	var pushes atomic.Int32
	rig.Runner.PushWork = func(context.Context, string) (map[string]any, error) {
		pushes.Add(1)
		return map[string]any{"pushed": true}, nil
	}
	runTriggerCtx(ctx, rig, headTrigger("tcm"), mustSpec(t, fmt.Sprintf(closedSpec, "tcm")))
	if n := pushes.Load(); n != 0 {
		t.Fatalf("pushed %d time(s) for a closed target", n)
	}
	if got := strings.Join(l.posts(), " | "); got != "step-stop | stop stopped" {
		t.Fatalf("hooks/steps after the close: %q, want only the stop hooks", got)
	}
	if len(rig.Store.auditsWithEvent("workflow_stopped")) != 1 {
		t.Fatal("the run wasn't recorded stopped")
	}
	if failed, e := rig.workflowFailed(); failed {
		t.Fatalf("a stopped run recorded as failed: %s", e)
	}
}

// The close lands after the agent returned, before the next step: the step
// that finished stands, the next one never starts, and the run ends stopped.
func TestTargetClosedBetweenStepsRunsNoFurtherStep(t *testing.T) {
	rig, l := headRig(t, "tcb", "aaaaaaa1111")
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		cancel(dispatch.ErrTargetClosed)
		return dispatch.RunRef{Output: "{}", AgentID: "a1"}, nil // no workspace: nothing to push
	}
	runTriggerCtx(ctx, rig, headTrigger("tcb"), mustSpec(t, fmt.Sprintf(closedSpec, "tcb")))
	if got := strings.Join(l.posts(), " | "); got != "step-done | stop stopped" {
		t.Fatalf("got %q, want the finished step's done hook, then only the run's stop hook", got)
	}
	if len(rig.Store.auditsWithEvent("workflow_stopped")) != 1 {
		t.Fatal("the run wasn't recorded stopped")
	}
}

// The push gate refuses (the target closed between the agent's return and
// the push, with the run not yet told): the step stops, it does not fail and
// is not judged as work left behind.
func TestPushGateRefusalStopsTheStep(t *testing.T) {
	rig, l := headRig(t, "tcg", "aaaaaaa1111")
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Output: "{}", AgentID: "a1", Workdir: t.TempDir()}, nil
	}
	rig.Runner.TargetClosed = func(string) bool { return true }
	var pushes atomic.Int32
	rig.Runner.PushWork = func(_ context.Context, id string) (map[string]any, error) {
		if err := rig.Runner.PushRefusal(id); err != nil {
			return nil, err
		}
		pushes.Add(1)
		return map[string]any{"pushed": true}, nil
	}
	runTrigger(rig, headTrigger("tcg"), mustSpec(t, fmt.Sprintf(closedSpec, "tcg")))
	if n := pushes.Load(); n != 0 {
		t.Fatalf("the gate let %d push(es) through", n)
	}
	if got := strings.Join(l.posts(), " | "); got != "step-stop | stop stopped" {
		t.Fatalf("got %q, want only the stop hooks", got)
	}
}

// PushRefusal: nothing wired, or a target still open — no refusal.
func TestPushRefusal(t *testing.T) {
	var r *Runner
	if r.PushRefusal("d") != nil {
		t.Fatal("a nil runner refused")
	}
	r = &Runner{}
	if r.PushRefusal("d") != nil {
		t.Fatal("no gate wired, yet refused")
	}
	r.TargetClosed = func(id string) bool { return id == "closed" }
	if r.PushRefusal("open") != nil {
		t.Fatal("refused an open target's push")
	}
	if err := r.PushRefusal("closed"); err == nil || !strings.Contains(err.Error(), dispatch.ErrTargetClosed.Error()) {
		t.Fatalf("closed target: %v", err)
	}
}
