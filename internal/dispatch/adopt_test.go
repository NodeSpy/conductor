package dispatch

import (
	"context"
	"os/exec"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// Feedback is the event's declaration (feedback: true), not its name: a
// trigger of ANY kind that declares it is eligible for open-workspace
// adoption, and one that does not is not.
func TestFeedbackIsDeclared(t *testing.T) {
	for _, k := range []string{"new_comment", "changes_requested"} {
		if !(core.Trigger{Kind: k}).Feedback() {
			t.Fatalf("%q declares feedback", k)
		}
	}
	for _, k := range []string{"merge_conflict", "issue_matched", "release", "review_requested"} {
		if (core.Trigger{Kind: k}).Feedback() {
			t.Fatalf("%q declares no feedback", k)
		}
	}
	if !(core.Trigger{Kind: "anything", Sem: &sdk.EventSemantics{Feedback: true}}).Feedback() {
		t.Fatal("any event declaring feedback is feedback")
	}
}

func TestPickAdoptTarget(t *testing.T) {
	if pickAdoptTarget(nil) != "" {
		t.Fatal("no candidates → empty")
	}
	// Most-recently-active wins (RFC3339 sorts lexically).
	cands := []adoptCand{
		{id: "old", active: "2026-08-20T10:00:00Z"},
		{id: "new", active: "2026-08-21T09:00:00Z"},
		{id: "mid", active: "2026-08-21T08:00:00Z"},
	}
	if got := pickAdoptTarget(cands); got != "new" {
		t.Fatalf("expected most-recent 'new', got %q", got)
	}
	// A single candidate with no timestamp is still chosen.
	if got := pickAdoptTarget([]adoptCand{{id: "solo"}}); got != "solo" {
		t.Fatalf("single candidate should win, got %q", got)
	}
}

func TestAdoptNoHeadRefIsNoop(t *testing.T) {
	d := &Dispatcher{PaseoBin: "paseo", AdoptOpenWorkspaces: true}
	// No head_ref in Context → returns "" without listing agents (no shelling).
	req := Request{Trigger: core.Trigger{Kind: "new_comment", Target: core.Target{Repo: "a/w", Number: 1}}}
	if id := d.adoptAgentForBranch(context.Background(), req); id != "" {
		t.Fatalf("no head_ref should yield no adoption, got %q", id)
	}
}

func TestGitBranchAndRepoMatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	ctx := context.Background()
	run := func(args ...string) {
		c := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "t")
	run("commit", "--allow-empty", "-q", "-m", "init")
	run("checkout", "-q", "-b", "fix/streamer")
	run("remote", "add", "origin", "git@github.com:AcmeCorp/Widget.git")

	d := &Dispatcher{PaseoBin: "paseo"}
	if b := d.gitBranch(ctx, dir); b != "fix/streamer" {
		t.Fatalf("gitBranch = %q, want fix/streamer", b)
	}
	if !gitRepoMatches(ctx, dir, "AcmeCorp/Widget") {
		t.Fatal("origin should match the repo (case-insensitive)")
	}
	if !gitRepoMatches(ctx, dir, "acmecorp/widget") {
		t.Fatal("repo match should be case-insensitive")
	}
	if gitRepoMatches(ctx, dir, "SomeoneElse/Other") {
		t.Fatal("a different repo must not match")
	}
}

// Checkout is the event's declaration: a fetch ref checks the target's code
// out (with the runtime hints passed through unread), a bare remote branches
// off, and no checkout runs in the base workspace — whatever forge it is.
func TestCheckoutIsDeclared(t *testing.T) {
	mk := func(co *sdk.CheckoutSemantics) Request {
		return Request{Trigger: core.Trigger{Kind: "mr_opened", Context: map[string]any{"project": "grp/app", "iid": 12},
			Sem: &sdk.EventSemantics{Checkout: co, Labels: "labels"}}}
	}
	pr := mk(&sdk.CheckoutSemantics{Remote: "git@forge.example:{{.project}}.git", FetchRef: "refs/merge-requests/{{.iid}}/head",
		RuntimeHints: map[string]string{"forge": "forgeworks", "pr_number": "{{.iid}}"}})
	if s := repoStrategy(pr); s != "checkout-pr" {
		t.Fatalf("strategy = %s", s)
	}
	if n, forge := prHints(pr); n != "12" || forge != "forgeworks" {
		t.Fatalf("hints = %q %q", n, forge)
	}
	if s := repoStrategy(mk(&sdk.CheckoutSemantics{Remote: "git@forge.example:grp/app.git"})); s != "branch-off" {
		t.Fatalf("bare remote: %s", s)
	}
	if s := repoStrategy(mk(nil)); s != "none" {
		t.Fatalf("no checkout declared: %s", s)
	}
}
