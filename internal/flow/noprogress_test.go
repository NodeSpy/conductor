package flow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// gitWorktree builds a temp git repo with a committed file; when dirty it also
// leaves an UNCOMMITTED edit, so gitdiff.Proposed() returns a non-empty diff.
func gitWorktree(t *testing.T, dirty bool) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "f.txt")
	git("commit", "-qm", "init")
	if dirty {
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("b\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runFixer dispatches one expect_push?/plain agent step whose worktree is `workdir`.
func runFixer(t *testing.T, expectPush bool, workdir string) (*testRig, *fakeState) {
	t.Helper()
	cfg := loadConfig(t, "connectors: { svc: { use: fake } }")
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Workdir: workdir, Output: "the PR wants closing", AgentID: "a1"}, nil
	}
	ep := ""
	if expectPush {
		ep = "\n    expect_push: true"
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: fix
    type: agent
    prompt: p`+ep+`
    hooks:
      - { at: fail, uses: svc.post, options: { text: "K={{.hook.failure.kind}} S={{.hook.failure.agent_summary}}" } }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	return rig, st
}

// TestNoProgressFailure: an expect_push agent step that leaves an unlanded diff fails as
// no_progress, and the fail hook sees the contract (kind + agent_summary). The same
// worktree WITHOUT expect_push, and expect_push with nothing proposed, both succeed.
func TestNoProgressFailure(t *testing.T) {
	// expect_push + unlanded diff → no_progress failure with the contract.
	rig, st := runFixer(t, true, gitWorktree(t, true))
	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("expect_push step with an unlanded diff should fail (no_progress)")
	}
	ok := false
	for _, x := range postTexts(st) {
		if strings.Contains(x, "K=no_progress") && strings.Contains(x, "S=the PR wants closing") {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("fail hook missing the no_progress contract: %v", postTexts(st))
	}

	// Same unlanded diff but NOT expect_push → ordinary success (a review/judge step).
	rig2, _ := runFixer(t, false, gitWorktree(t, true))
	if failed, e := rig2.workflowFailed(); failed {
		t.Fatalf("a non-expect_push step must not be flagged no_progress: %s", e)
	}

	// expect_push but a clean worktree (nothing proposed) → success.
	rig3, _ := runFixer(t, true, gitWorktree(t, false))
	if failed, e := rig3.workflowFailed(); failed {
		t.Fatalf("expect_push with an empty proposed diff must succeed: %s", e)
	}
}

// TestFailureCtxKinds: failureCtx classifies ordinary / gave_up / no_progress and
// carries the agent summary + diff for no_progress.
func TestFailureCtxKinds(t *testing.T) {
	ord := failureCtx(errors.New("boom"), "boom", "s")
	if ord["kind"] != "ordinary" || ord["gave_up"] != false {
		t.Fatalf("ordinary: %+v", ord)
	}
	gave := failureCtx(dispatch.Unrecoverable(errors.New("no runtime")), "no runtime", "s")
	if gave["kind"] != "gave_up" || gave["gave_up"] != true {
		t.Fatalf("gave_up: %+v", gave)
	}
	np := failureCtx(&noProgressError{step: "fix", summary: "needs closing", diff: "d"}, "e", "fix")
	if np["kind"] != "no_progress" || np["agent_summary"] != "needs closing" || np["diff"] != "d" {
		t.Fatalf("no_progress: %+v", np)
	}
}
