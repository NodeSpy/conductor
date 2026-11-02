package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// cloneWithCommits is a clone of a fresh origin with n local commits no
// remote has: the workspace a sandboxed agent leaves when it committed and
// nothing pushed.
func cloneWithCommits(t *testing.T, n int) string {
	t.Helper()
	origin := gitWorktree(t, false)
	dir := filepath.Join(t.TempDir(), "ws")
	if out, err := exec.Command("git", "clone", "-q", origin, dir).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	for i := 0; i < n; i++ {
		f := fmt.Sprintf("fix%d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, f), []byte("fix\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", f}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", f}} {
			if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
	return dir
}

type unlandedCase struct {
	cfg     string // extra top-level config
	step    string // extra step keys
	kind    string // trigger kind (default ping)
	workdir string
	push    func() (map[string]any, error)
}

// runUnlanded runs the live shape: an agent step `fix` (output_schema by the
// caller's step keys) whose end-of-step push answers c.push, then `resolve`,
// gated on the push having landed, and `after`, which reports what fix's push
// output said.
func runUnlanded(t *testing.T, c unlandedCase) (*testRig, *fakeState) {
	t.Helper()
	cfg := loadConfig(t, "connectors: { svc: { use: fake } }\n"+c.cfg)
	reg := buildRegistry(t, cfg)
	st := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{Workdir: c.workdir, Output: `{"addressed_comments":[11,12]}`, AgentID: "a1"}, nil
	}
	rig.Runner.PushWork = func(context.Context, string) (map[string]any, error) { return c.push() }
	kind := c.kind
	if kind == "" {
		kind = "ping"
	}
	spec := mustSpec(t, `
on: svc.`+kind+`
steps:
  - id: fix
    type: agent
    prompt: p`+c.step+`
    hooks:
      - { at: fail, uses: svc.post, options: { text: "FAIL K={{.hook.failure.kind}}" } }
  - id: resolve
    if: "fix.push.pushed"
    uses: svc.post
    options: { text: "RESOLVED {{.fix.addressed_comments}}" }
  - id: after
    uses: svc.post
    options: { text: "AFTER {{with .fix.push}}pushed={{.pushed}} n={{.unpushed_commits}}{{end}}" }
`)
	runTrigger(rig, newTrigger(kind, map[string]any{"msg": "x"}), spec)
	return rig, st
}

const schemaStep = `
    output_schema:
      type: object
      properties:
        addressed_comments: { type: array, items: { type: integer } }`

var errNoBranch = fmt.Errorf("%w: conductor has no branch to push this workspace to", ErrNothingToPush)

func hasPost(st *fakeState, sub string) bool {
	for _, x := range postTexts(st) {
		if strings.Contains(x, sub) {
			return true
		}
	}
	return false
}

func unlandedRows(rig *testRig) []map[string]any {
	var out []map[string]any
	for _, r := range rig.Store.auditsWithEvent("git_push") {
		if r["outcome"] == "unlanded" {
			out = append(out, r)
		}
	}
	return out
}

// The live bug: an output_schema step's agent committed, conductor had no
// branch to push to (the step read as a review), and the run went on to
// resolve the threads the agent claimed — the commit lost with its worktree.
// Now the step fails as no_progress, loudly (audit + fail hook), and nothing
// after it runs.
func TestUnlandedOutputSchemaStepFails(t *testing.T) {
	rig, st := runUnlanded(t, unlandedCase{step: schemaStep, workdir: cloneWithCommits(t, 1),
		push: func() (map[string]any, error) { return nil, errNoBranch }})
	failed, e := rig.workflowFailed()
	if !failed || !strings.Contains(e, "never pushed") || !strings.Contains(e, "expect_push") {
		t.Fatalf("want the step failed naming the unpushed work and expect_push, got failed=%v %q", failed, e)
	}
	if !hasPost(st, "FAIL K=no_progress") {
		t.Fatalf("fail hook must see kind no_progress: %v", postTexts(st))
	}
	if hasPost(st, "RESOLVED") || hasPost(st, "AFTER") {
		t.Fatalf("nothing may run after unlanded work: %v", postTexts(st))
	}
	rows := unlandedRows(rig)
	if len(rows) != 1 || rows[0]["commits"] != 1 || rows[0]["fails_step"] != true || rows[0]["review"] != "output_schema" || rows[0]["step"] != "fix" {
		t.Fatalf("want one unlanded git_push audit row: %v", rows)
	}
}

// A refused push (diverged remote) is the same: the work did not land.
func TestUnlandedRefusedPushFails(t *testing.T) {
	rig, st := runUnlanded(t, unlandedCase{workdir: cloneWithCommits(t, 2),
		push: func() (map[string]any, error) { return nil, errors.New("gitpush: the remote branch has diverged") }})
	if failed, e := rig.workflowFailed(); !failed || !strings.Contains(e, "2 commit(s) left unpushed") {
		t.Fatalf("a refused push with commits left must fail the step: failed=%v %q", failed, e)
	}
	if hasPost(st, "RESOLVED") {
		t.Fatal("resolve ran after a refused push")
	}
}

// warn: recorded, never silent, the step goes on — and the push gate keeps
// the next step from trusting the agent's claim.
func TestUnlandedWarnRecordsAndGates(t *testing.T) {
	rig, st := runUnlanded(t, unlandedCase{cfg: "sandbox: { push: { unlanded: warn } }", step: schemaStep,
		workdir: cloneWithCommits(t, 1), push: func() (map[string]any, error) { return nil, errNoBranch }})
	if failed, e := rig.workflowFailed(); failed {
		t.Fatalf("unlanded: warn must not fail the step: %s", e)
	}
	if hasPost(st, "RESOLVED") {
		t.Fatal("resolve ran though nothing was pushed")
	}
	if !hasPost(st, "AFTER pushed=false n=1") {
		t.Fatalf("the step's push output must say pushed=false with the commit count: %v", postTexts(st))
	}
	if rows := unlandedRows(rig); len(rows) != 1 || rows[0]["fails_step"] != false {
		t.Fatalf("want one non-failing unlanded audit row: %v", rows)
	}
}

// An explicit review (a review trigger) keeps its commits by design, and an
// unsandboxed agent pushes for itself: recorded, not failed.
func TestUnlandedExplicitReviewAndUnsandboxedWarn(t *testing.T) {
	for name, c := range map[string]unlandedCase{
		"review trigger": {kind: "review_requested", step: schemaStep, push: func() (map[string]any, error) { return nil, errNoBranch }},
		"unsandboxed":    {push: func() (map[string]any, error) { return nil, ErrUnsandboxed }},
	} {
		t.Run(name, func(t *testing.T) {
			c.workdir = cloneWithCommits(t, 1)
			rig, st := runUnlanded(t, c)
			if failed, e := rig.workflowFailed(); failed {
				t.Fatalf("must not fail: %s", e)
			}
			if hasPost(st, "RESOLVED") || !hasPost(st, "AFTER pushed=false n=1") {
				t.Fatalf("posts: %v", postTexts(st))
			}
			if len(unlandedRows(rig)) != 1 {
				t.Fatalf("still audited: %v", rig.Store.auditsWithEvent("git_push"))
			}
		})
	}
}

// unlanded: warn loosens only the new check: an expect_push step's own
// no_progress (its proposed diff never landed) still fails it.
func TestUnlandedExpectPushStillFailsUnderWarn(t *testing.T) {
	rig, _ := runUnlanded(t, unlandedCase{cfg: "sandbox: { push: { unlanded: warn } }", step: schemaStep + "\n    expect_push: true",
		workdir: cloneWithCommits(t, 1), push: func() (map[string]any, error) { return nil, errNoBranch }})
	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("expect_push with work left unpushed fails as no_progress")
	}
}

// A fork's PR checked out detached: its own commits are on no remote of
// the clone. Measured from where the workspace started (PushError.Since),
// they are not the agent's — no failure; the agent's one commit past it is.
func TestUnlandedCountsFromTheStart(t *testing.T) {
	ws := cloneWithCommits(t, 2) // the fork's PR commits
	out, err := exec.Command("git", "-C", ws, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	start := strings.TrimSpace(string(out))
	rig, st := runUnlanded(t, unlandedCase{step: schemaStep, workdir: ws,
		push: func() (map[string]any, error) { return nil, &PushError{Err: errNoBranch, Since: start} }})
	if failed, e := rig.workflowFailed(); failed || len(unlandedRows(rig)) != 0 {
		t.Fatalf("the PR's own commits are not the agent's: failed=%v %s rows=%v", failed, e, unlandedRows(rig))
	}
	if hasPost(st, "RESOLVED") {
		t.Fatal("nothing pushed: no resolve")
	}
	if out, err := exec.Command("git", "-C", ws, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "agent").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	rig, _ = runUnlanded(t, unlandedCase{step: schemaStep, workdir: ws,
		push: func() (map[string]any, error) { return nil, &PushError{Err: errNoBranch, Since: start} }})
	if failed, e := rig.workflowFailed(); !failed || !strings.Contains(e, "1 commit(s)") {
		t.Fatalf("the agent's own commit fails the step: failed=%v %s", failed, e)
	}
	// A start commit the clone no longer has: measured against the remotes.
	rig, _ = runUnlanded(t, unlandedCase{step: schemaStep, workdir: ws,
		push: func() (map[string]any, error) {
			return nil, &PushError{Err: errNoBranch, Since: strings.Repeat("ab", 20)}
		}})
	if failed, e := rig.workflowFailed(); !failed || !strings.Contains(e, "3 commit(s)") {
		t.Fatalf("fallback to the remotes: failed=%v %s", failed, e)
	}
}

// Pushed: the gate opens. Nothing to push and nothing committed: silent,
// as before — there is nothing to lose.
func TestPushedOpensGateAndCleanIsSilent(t *testing.T) {
	rig, st := runUnlanded(t, unlandedCase{step: schemaStep, workdir: cloneWithCommits(t, 0),
		push: func() (map[string]any, error) { return map[string]any{"pushed": true, "commits": 1}, nil }})
	if failed, e := rig.workflowFailed(); failed {
		t.Fatal(e)
	}
	if !hasPost(st, "RESOLVED [11 12]") {
		t.Fatalf("resolve must run on a landed push: %v", postTexts(st))
	}
	rig2, st2 := runUnlanded(t, unlandedCase{step: schemaStep, workdir: cloneWithCommits(t, 0),
		push: func() (map[string]any, error) { return nil, errNoBranch }})
	if failed, e := rig2.workflowFailed(); failed {
		t.Fatal(e)
	}
	if hasPost(st2, "RESOLVED") || len(unlandedRows(rig2)) != 0 {
		t.Fatalf("clean workspace: no resolve, no unlanded row: %v %v", postTexts(st2), unlandedRows(rig2))
	}
}
