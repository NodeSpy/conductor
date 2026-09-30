package hostcmd

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func ghRule(layers ...*config.IsolationConfig) Rule { return Resolve("gh", layers, false) }

func hostLayer(tool string, h *config.HostCommand) *config.IsolationConfig {
	return &config.IsolationConfig{Host: map[string]*config.HostCommand{tool: h}}
}

func ghDecide(rule Rule, ctx Context, args ...string) Decision {
	return Decide(Request{Tool: "gh", Args: args, Cwd: "/state/worktrees/d1"}, rule, ctx)
}

// The gh profile's write binding: its own PR by default; opening a PR, a
// merge, another target or another repository only when the operator's own
// gh allow list names the command — and a blanket or step-level allow never
// does.
func TestGHBindingDefaultsAndTheAllowList(t *testing.T) {
	ctx := testCtx()
	create := []string{"pr", "create", "-t", "x", "-b", "y"}

	if d := ghDecide(ghRule(), ctx, create...); d.Allow || !strings.Contains(d.Reason, "opening a PR is refused") ||
		!strings.Contains(d.Reason, `isolation.host.gh.allow: ["*", "pr create *"]`) {
		t.Fatalf("gh pr create is refused by default, naming the knob: %+v", d)
	}
	opened := ghRule(hostLayer("gh", &config.HostCommand{Allow: []string{"*", "pr create *"}}))
	if d := ghDecide(opened, ctx, create...); !d.Allow {
		t.Fatalf("allowed by host.gh.allow: %q", d.Reason)
	}
	if d := ghDecide(opened, ctx, "pr", "view", "42"); !d.Allow {
		t.Fatalf(`"*" keeps every other gh command available: %q`, d.Reason)
	}
	if d := ghDecide(opened, ctx, "pr", "merge", "42"); d.Allow {
		t.Fatal("naming pr create does not open pr merge")
	}
	for _, blanket := range [][]string{{"*"}, {"pr *"}, {"*", "* create *"}} {
		if d := ghDecide(ghRule(hostLayer("gh", &config.HostCommand{Allow: blanket})), ctx, create...); d.Allow {
			t.Errorf("allow %q must not name pr create", blanket)
		}
	}
	// A step's own allow list narrows, never widens.
	step := Resolve("gh", []*config.IsolationConfig{nil, nil, hostLayer("gh", &config.HostCommand{Allow: []string{"*", "pr create *"}})}, true)
	if d := ghDecide(step, ctx, create...); d.Allow {
		t.Fatal("a step's allow list must not open pr create")
	}
	// Other targets and repositories.
	if d := ghDecide(ghRule(), ctx, "pr", "comment", "43", "-b", "x"); d.Allow {
		t.Fatal("a comment on another PR is refused by default")
	}
	if d := ghDecide(ghRule(hostLayer("gh", &config.HostCommand{Allow: []string{"*", "pr comment *"}})), ctx, "pr", "comment", "43", "-b", "x"); !d.Allow {
		t.Fatalf("named, a comment on another PR is allowed: %q", d.Reason)
	}
	// A thread on another repository's PR #42 is not the dispatch's own #42.
	if d := ghDecide(ghRule(), ctx, "api", "graphql", "-f", `query=mutation { resolveReviewThread(input: {threadId: "PRRT_otherrepo"}) { thread { id } } }`); d.Allow {
		t.Fatal("resolving a thread on another repository's same-numbered PR must be refused")
	}
	if d := ghDecide(ghRule(), ctx, "-R", "other/repo", "pr", "view", "1"); d.Allow || !strings.Contains(d.Reason, "other/repo") {
		t.Fatalf("another repository is refused by default: %+v", d)
	}
	other := ghRule(hostLayer("gh", &config.HostCommand{Allow: []string{"*", "pr view * --repo=other/*"}}))
	if d := ghDecide(other, ctx, "-R", "other/repo", "pr", "view", "1"); !d.Allow {
		t.Fatalf("named with its --repo, another repository's read is allowed: %q", d.Reason)
	}
	if d := ghDecide(other, ctx, "-R", "evil/repo", "pr", "view", "1"); d.Allow {
		t.Fatal("…and only that one")
	}
	// An allow list without "*" is still an allow list: only those commands.
	if d := ghDecide(ghRule(hostLayer("gh", &config.HostCommand{Allow: []string{"pr create *"}})), ctx, "pr", "view", "42"); d.Allow {
		t.Fatal("an allow list without \"*\" permits only what it lists")
	}
}

// A review step's gh writes nothing (unless the operator names the write);
// a closed target takes no gh write at all, allowed or not.
func TestGHBindingReviewStepAndClosedTarget(t *testing.T) {
	ctx := testCtx()
	ctx.ReadOnly = true
	if d := ghDecide(ghRule(), ctx, "pr", "comment", "42", "-b", "x"); d.Allow || !strings.Contains(d.Reason, "review step") {
		t.Fatalf("a review step's gh comment is refused: %+v", d)
	}
	if d := ghDecide(ghRule(), ctx, "pr", "diff", "42"); !d.Allow {
		t.Fatalf("a review step still reads: %q", d.Reason)
	}
	if d := ghDecide(ghRule(hostLayer("gh", &config.HostCommand{Allow: []string{"*", "pr review *"}})), ctx, "pr", "review", "42", "--comment", "-b", "x"); !d.Allow {
		t.Fatalf("the operator can name a review step's write: %q", d.Reason)
	}
	ctx = testCtx()
	ctx.TargetClosed = func() string { return "target: acme/app#42 is merged — writes refused" }
	if d := ghDecide(ghRule(hostLayer("gh", &config.HostCommand{Allow: []string{"*", "pr comment *"}})), ctx, "pr", "comment", "42", "-b", "x"); d.Allow || !strings.Contains(d.Reason, "is merged") {
		t.Fatalf("a closed target takes no gh write, even a named one: %+v", d)
	}
	if d := ghDecide(ghRule(), ctx, "pr", "view", "42"); !d.Allow {
		t.Fatalf("reads of a closed target still work: %q", d.Reason)
	}
}

// The git profile's push binding.
func TestGitPushBinding(t *testing.T) {
	ctx := testCtx()
	git := func(layers ...*config.IsolationConfig) Rule { return Resolve("git", layers, false) }
	if r := GitPush(git(), ctx, "fix/42", false, false); r != "" {
		t.Fatalf("the dispatch's own branch: %q", r)
	}
	if r := GitPush(git(), ctx, "new-branch", false, false); !strings.Contains(r, "not the dispatch's own branch") ||
		!strings.Contains(r, `isolation.host.git.allow: ["*", "push new-branch"]`) {
		t.Fatalf("another branch is refused, naming the knob: %q", r)
	}
	rel := git(hostLayer("git", &config.HostCommand{Allow: []string{"*", "push release/*"}}))
	if r := GitPush(rel, ctx, "release/1.2", false, false); r != "" {
		t.Fatalf("host.git.allow opens release branches: %q", r)
	}
	if r := GitPush(rel, ctx, "main", false, false); r == "" {
		t.Fatal("…and only those")
	}
	any := git(hostLayer("git", &config.HostCommand{Allow: []string{"push *"}}))
	for _, c := range []struct {
		force, del bool
		want       string
	}{{true, false, "force push is refused"}, {false, true, "branch delete is refused"}} {
		if r := GitPush(any, ctx, "fix/42", c.force, c.del); !strings.Contains(r, c.want) {
			t.Errorf("force/delete are always refused: %q", r)
		}
	}
	if r := GitPush(git(), Context{Repo: "", Number: 0}, "fix/42", false, false); r == "" {
		t.Fatal("no dispatch target: no push")
	}
	if r := GitPush(git(hostLayer("git", &config.HostCommand{Deny: []string{"push fix/*"}})), ctx, "fix/42", false, false); r == "" {
		t.Fatal("host.git.deny narrows even the own branch")
	}
	step := Resolve("git", []*config.IsolationConfig{nil, nil, hostLayer("git", &config.HostCommand{Allow: []string{"*", "push release/*"}})}, true)
	if r := GitPush(step, ctx, "release/1", false, false); r == "" {
		t.Fatal("a step cannot widen pushes")
	}
	ro := testCtx()
	ro.ReadOnly = true
	if r := GitPush(git(), ro, "fix/42", false, false); !strings.Contains(r, "review step") {
		t.Fatalf("a review step pushes nothing: %q", r)
	}
	cl := testCtx()
	cl.TargetClosed = func() string { return "target: acme/app#42 is closed — writes refused" }
	if r := GitPush(rel, cl, "fix/42", false, false); !strings.Contains(r, "is closed") {
		t.Fatalf("a closed target takes no push: %q", r)
	}
}

// git is never a shim: naming it in isolation.host configures its push
// binding, it does not make git a host command.
func TestGitIsNeverAHostCommand(t *testing.T) {
	look := func(n string) (string, error) { return "/usr/bin/" + n, nil }
	en, _ := HostSet([]*config.IsolationConfig{hostLayer("git", &config.HostCommand{Allow: []string{"*"}})}, false, look)
	for _, n := range en {
		if n == "git" {
			t.Fatal("git must not be a host command")
		}
	}
}
