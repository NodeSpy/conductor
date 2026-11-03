package dispatch

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// A workspace its owner registered (a member, under its own identity) keeps
// that identity when a dispatch launches into it; a dispatch's own
// registration is reused on its resume.
func TestRegisterSandboxWorkspaceRespectsTheOwner(t *testing.T) {
	ws := t.TempDir()
	unreg := agentsandbox.Default.Register(agentsandbox.Entry{Workspace: ws, Identity: "member:alice"})
	defer unreg()
	e, err := RegisterSandboxWorkspace(ws, "agent:fixer@a/b", "claude-code", &Request{DispatchID: "d1"})
	if err != nil || e.Identity != "member:alice" {
		t.Fatalf("owner identity: %+v %v", e, err)
	}
	ws2 := t.TempDir()
	e1, _ := RegisterSandboxWorkspace(ws2, "agent:fixer@a/b", "claude-code", &Request{DispatchID: "d2"})
	defer agentsandbox.Default.Forget(ws2)
	e2, _ := RegisterSandboxWorkspace(ws2, "agent:fixer@a/b", "claude-code", nil)
	if e1.Dispatch != "d2" || e2.Dispatch != "d2" {
		t.Fatalf("resume reuses the dispatch's registration: %+v %+v", e1, e2)
	}
	e3, _ := RegisterSandboxWorkspace(ws2, "agent:other@a/b", "codex", &Request{DispatchID: "d3"})
	if e3.Dispatch != "d3" || e3.Identity != "agent:other@a/b" {
		t.Fatalf("another dispatch re-registers a dispatch's workspace: %+v", e3)
	}
	// The same identity's NEXT dispatch into the workspace re-registers too:
	// the end-of-step push and its unlanded-work check look it up by the
	// dispatch id, and a stale one reads as "unsandboxed".
	e4, _ := RegisterSandboxWorkspace(ws2, "agent:other@a/b", "codex", &Request{DispatchID: "d4"})
	if got, ok := agentsandbox.Default.ForDispatch("d4"); e4.Dispatch != "d4" || !ok || got.Dispatch != "d4" {
		t.Fatalf("a new dispatch of the same identity must be found by its id: %+v %+v %v", e4, got, ok)
	}
	// …while a member's workspace keeps its owner whoever launches into it.
	if e, _ := RegisterSandboxWorkspace(ws, "agent:fixer@a/b", "claude-code", &Request{DispatchID: "d5"}); e.Identity != "member:alice" {
		t.Fatalf("member workspace re-keyed: %+v", e)
	}
}

// linkedWorktree is the workspace gitwt provisions for a PR: a linked
// worktree of a base clone (whose origin is a bare remote) on branch pr-1.
func linkedWorktree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	remote, base, ws := filepath.Join(root, "remote.git"), filepath.Join(root, "base"), filepath.Join(root, "ws")
	run(root, "init", "-q", "--bare", remote)
	run(root, "clone", "-q", remote, base)
	run(base, "commit", "-q", "--allow-empty", "-m", "init")
	run(base, "push", "-q", "origin", "HEAD:main")
	run(base, "worktree", "add", "-q", "-b", "pr-1", ws)
	return ws
}

// The live bug's shape: a fixer step that reports a structured result
// (output_schema) read as a review and got no push target. An output schema
// alone still makes a review (a reviewer's commits are never pushed) —
// expect_push: true is how a fixer with a structured result says it fixes,
// and then conductor pushes its PR branch.
func TestOutputSchemaStepPushTarget(t *testing.T) {
	schema := map[string]any{"type": "object"}
	for _, tc := range []struct {
		name       string
		step       config.Step
		wantTarget bool
	}{
		{"plain fixer", config.Step{}, true},
		{"output_schema alone", config.Step{OutputSchema: schema}, false},
		{"output_schema + expect_push", config.Step{OutputSchema: schema, ExpectPush: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := linkedWorktree(t)
			defer agentsandbox.Default.Forget(ws)
			e, err := RegisterSandboxWorkspace(ws, "agent:fixer@a/b", "claude-code", &Request{DispatchID: "run1:address", Step: tc.step, Trigger: core.Trigger{TargetTrusted: true}})
			if err != nil {
				t.Fatal(err)
			}
			if got := e.Push != nil; got != tc.wantTarget {
				t.Fatalf("push target = %v, want %v (%+v)", got, tc.wantTarget, e.Push)
			}
			if tc.wantTarget && e.Push.Branch != "pr-1" {
				t.Fatalf("pushes branch %q, want pr-1", e.Push.Branch)
			}
			head, err := exec.Command("git", "-C", ws, "rev-parse", "HEAD").Output()
			if err != nil || e.StartHead != strings.TrimSpace(string(head)) {
				t.Fatalf("StartHead = %q, want the provisioned HEAD %s (%v)", e.StartHead, head, err)
			}
			if got, ok := agentsandbox.Default.ForDispatch("run1:address"); !ok || got.Workspace != e.Workspace {
				t.Fatalf("ForDispatch(run1:address) = %+v %v", got, ok)
			}
		})
	}
}

// ReviewBy names output_schema only when it is the sole reason; the explicit
// reasons win, and expect_push clears every one.
func TestReviewBy(t *testing.T) {
	schema := map[string]any{"type": "object"}
	for _, tc := range []struct {
		req  Request
		want string
	}{
		{Request{}, ""},
		{Request{Step: config.Step{OutputSchema: schema}}, ReviewByOutputSchema},
		{Request{Step: config.Step{OutputSchema: schema, ExpectPush: true}}, ""},
		{Request{Step: config.Step{OutputSchema: schema, Checkout: "none"}}, ReviewByCheckoutNone},
		{Request{Step: config.Step{OutputSchema: schema}, Trigger: core.Trigger{Kind: "review_requested"}}, ReviewByTrigger},
		{Request{Trigger: core.Trigger{Kind: "self_review"}}, ReviewByTrigger},
		{Request{Trigger: core.Trigger{Kind: "changes_requested"}}, ""},
		{Request{Step: config.Step{Checkout: "none", ExpectPush: true}}, ""},
		{Request{Step: config.Step{DecisionLaunch: &config.DecisionLaunch{}, OutputSchema: schema}}, ReviewByDecide},
	} {
		if got := ReviewBy(tc.req); got != tc.want {
			t.Errorf("ReviewBy(kind=%q schema=%v checkout=%q expect_push=%v) = %q, want %q",
				tc.req.Trigger.Kind, tc.req.Step.OutputSchema != nil, tc.req.Step.Checkout, tc.req.Step.ExpectPush, got, tc.want)
		}
		if ReviewRole(tc.req) != (tc.want != "") {
			t.Errorf("ReviewRole disagrees with ReviewBy for %+v", tc.req.Step)
		}
	}
}

// A dispatch's first registration prefetches its checkout — a trusted
// target only; a resume, a profile-less launch, or a target the sender
// chose does not.
func TestRegisterSandboxWorkspacePrefetches(t *testing.T) {
	var got []string
	SandboxPrefetch = func(dir string) { got = append(got, dir) }
	defer func() { SandboxPrefetch = nil }()
	trusted := &Request{DispatchID: "p1", Trigger: core.Trigger{TargetTrusted: true}}
	ws := t.TempDir()
	defer agentsandbox.Default.Forget(ws)
	RegisterSandboxWorkspace(ws, "agent:fixer@a/b", "claude-code", trusted)
	RegisterSandboxWorkspace(ws, "agent:fixer@a/b", "claude-code", trusted)
	RegisterSandboxWorkspace(ws, "agent:fixer@a/b", "claude-code", nil)
	if len(got) != 1 || got[0] != ws {
		t.Fatalf("prefetched %v, want once for %s", got, ws)
	}
	got = nil
	ws2 := t.TempDir()
	defer agentsandbox.Default.Forget(ws2)
	RegisterSandboxWorkspace(ws2, "agent:fixer@dispatch-p2", "claude-code", &Request{DispatchID: "p2"})
	ws3 := t.TempDir()
	defer agentsandbox.Default.Forget(ws3)
	RegisterSandboxWorkspace(ws3, "member:alice", "claude-code", nil)
	if len(got) != 0 {
		t.Fatalf("an untrusted target or a member's workspace is prefetched: %v", got)
	}
}
