package flow

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/gitpush"
)

// conflictPushErr is the end-of-step push error a branch that moved under
// the agent produces when its commits conflict with the new head: what the
// daemon's push wiring returns (PushError → KeptError → DivergedError).
func conflictPushErr() error {
	de := &gitpush.DivergedError{Branch: "fix/1", Remote: "remotehead000", Head: "agenthead000",
		Attempted: true, Commit: "agenthead000", Files: []string{"app.go"}}
	return &PushError{Since: "starthead000",
		Err: &gitpush.KeptError{Err: de, Ref: gitpush.UnpushedRef("fix/1", "agenthead000"), Head: "agenthead000", Commits: 1}}
}

type conflictRig struct {
	rig      *testRig
	st       *fakeState
	mu       sync.Mutex
	pushes   int
	followUp []string
}

// runConflict runs one agent step whose successive end-of-step pushes answer
// pushes[i] (the last repeats), with or without a runtime that takes a
// follow-up turn.
func runConflict(t *testing.T, followUp bool, pushes ...func() (map[string]any, error)) *conflictRig {
	t.Helper()
	cfg := loadConfig(t, "connectors: { svc: { use: fake } }\n")
	reg := buildRegistry(t, cfg)
	c := &conflictRig{st: newFakeState(t, "svc")}
	c.rig = newTestRunner(t, cfg, reg)
	ws := cloneWithCommits(t, 1)
	c.rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Workdir: ws, Output: "done", AgentID: "agent-" + req.DispatchID}, nil
	}
	c.rig.Runner.PushWork = func(context.Context, string) (map[string]any, error) {
		c.mu.Lock()
		i := c.pushes
		c.pushes++
		c.mu.Unlock()
		if i >= len(pushes) {
			i = len(pushes) - 1
		}
		return pushes[i]()
	}
	if followUp {
		c.rig.Runner.Agents.FollowUp = func(_ context.Context, _, _ string, _ core.Trigger, prompt string) (string, bool, error) {
			c.mu.Lock()
			c.followUp = append(c.followUp, prompt)
			c.mu.Unlock()
			return "rebased", true, nil
		}
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: fix
    type: agent
    prompt: address the review
  - id: after
    uses: svc.post
    options: { text: "AFTER pushed={{.fix.push.pushed}}" }
`)
	runTrigger(c.rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	return c
}

func conflictThenOK() []func() (map[string]any, error) {
	return []func() (map[string]any, error){
		func() (map[string]any, error) { return nil, conflictPushErr() },
		func() (map[string]any, error) { return map[string]any{"pushed": true, "commits": 1}, nil },
	}
}

// A runtime that takes a follow-up turn: the live agent is asked to rebase
// onto the moved head and resolve, and the push is tried again — one
// dispatch, the work lands.
func TestPushConflictResolvedByTheLiveAgent(t *testing.T) {
	c := runConflict(t, true, conflictThenOK()...)
	if failed, e := c.rig.workflowFailed(); failed {
		t.Fatalf("the resolved push must not fail the run: %s", e)
	}
	if n := len(c.rig.Agents.requests()); n != 1 {
		t.Fatalf("resolved in place: %d dispatches, want 1", n)
	}
	if len(c.followUp) != 1 || !strings.Contains(c.followUp[0], "git rebase origin/fix/1") || !strings.Contains(c.followUp[0], "app.go") {
		t.Fatalf("the resolve turn names the rebase and the conflict: %q", c.followUp)
	}
	if c.pushes != 2 || !hasPost(c.st, "AFTER pushed=true") {
		t.Fatalf("pushed again after the resolve turn: pushes=%d posts=%v", c.pushes, postTexts(c.st))
	}
}

// No follow-up turn on this runtime (the cli runtime): the step is
// dispatched once more on the new head, told where the first attempt's
// commits are kept, under its own dispatch id — and the work lands.
func TestPushConflictRedispatchesOnceOnTheNewHead(t *testing.T) {
	c := runConflict(t, false, conflictThenOK()...)
	if failed, e := c.rig.workflowFailed(); failed {
		t.Fatalf("the re-dispatched step lands its work: %s", e)
	}
	reqs := c.rig.Agents.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d dispatches, want 2", len(reqs))
	}
	if strings.Contains(reqs[0].Action.Prompt, "NOTE FROM CONDUCTOR") {
		t.Fatal("the first attempt carries no note")
	}
	p := reqs[1].Action.Prompt
	for _, want := range []string{"NOTE FROM CONDUCTOR", "app.go", gitpush.UnpushedRef("fix/1", "agenthead000"), "git cherry-pick starthead000..agenthead000"} {
		if !strings.Contains(p, want) {
			t.Fatalf("the re-dispatch prompt names %q: %s", want, p)
		}
	}
	if reqs[1].DispatchID == reqs[0].DispatchID || !strings.HasSuffix(reqs[1].DispatchID, ":rebase") {
		t.Fatalf("the re-dispatch has its own dispatch id: %q vs %q", reqs[1].DispatchID, reqs[0].DispatchID)
	}
	if !hasPost(c.st, "AFTER pushed=true") {
		t.Fatalf("the run went on with the work pushed: %v", postTexts(c.st))
	}
	rows := c.rig.Store.auditsWithEvent("push_conflict")
	if len(rows) != 1 || rows[0]["resolve"] != "redispatch" || rows[0]["kept_ref"] != gitpush.UnpushedRef("fix/1", "agenthead000") {
		t.Fatalf("one audited re-dispatch: %v", rows)
	}
	// The first attempt's agent is released: its work is kept under the ref.
	if a := c.rig.Agents.archived; len(a) == 0 || a[0] != "agent-"+reqs[0].DispatchID {
		t.Fatalf("first agent archived: %v", a)
	}
}

// The conflict persists: exactly one re-dispatch, then the step fails, the
// error naming where the commits are kept.
func TestPushConflictThatPersistsFailsAfterOneRedispatch(t *testing.T) {
	c := runConflict(t, false, func() (map[string]any, error) { return nil, conflictPushErr() })
	failed, e := c.rig.workflowFailed()
	if !failed || !strings.Contains(e, gitpush.UnpushedRef("fix/1", "agenthead000")) {
		t.Fatalf("fails naming the kept ref: failed=%v %q", failed, e)
	}
	if n := len(c.rig.Agents.requests()); n != 2 {
		t.Fatalf("%d dispatches, want 2 (one re-dispatch, never a loop)", n)
	}
	if hasPost(c.st, "AFTER") {
		t.Fatal("nothing runs after unlanded work")
	}
}

// A push that went out rebased onto the moved branch landed the agent's
// work: its original commits (not on any remote ref) are not unlanded work,
// so an expect_push step succeeds.
func TestRebasedPushIsNotNoProgress(t *testing.T) {
	cfg := loadConfig(t, "connectors: { svc: { use: fake } }\n")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	ws := cloneWithCommits(t, 1)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Workdir: ws, Output: "done", AgentID: "a1"}, nil
	}
	rig.Runner.PushWork = func(context.Context, string) (map[string]any, error) {
		return map[string]any{"pushed": true, "commits": 1, "rebased": true, "onto": "remotehead000"}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: fix
    type: agent
    prompt: p
    expect_push: true
  - id: after
    uses: svc.post
    options: { text: "AFTER rebased={{.fix.push.rebased}}" }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	if failed, e := rig.workflowFailed(); failed {
		t.Fatalf("a rebased push is progress: %s", e)
	}
	if !hasPost(st, "AFTER rebased=true") {
		t.Fatalf("posts: %v", postTexts(st))
	}
}
