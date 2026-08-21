package dispatch

import (
	"context"
	"os/exec"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/core"
)

func TestIsFeedbackKind(t *testing.T) {
	for _, k := range []string{"new_comment", "changes_requested"} {
		if !isFeedbackKind(k) {
			t.Fatalf("%q should be a feedback kind", k)
		}
	}
	for _, k := range []string{"merge_conflict", "issue_matched", "release", "review_requested"} {
		if isFeedbackKind(k) {
			t.Fatalf("%q should NOT be a feedback kind", k)
		}
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
	run("remote", "add", "origin", "git@github.com:EdnitionCode/RosterStream.git")

	d := &Dispatcher{PaseoBin: "paseo"}
	if b := d.gitBranch(ctx, dir); b != "fix/streamer" {
		t.Fatalf("gitBranch = %q, want fix/streamer", b)
	}
	if !gitRepoMatches(ctx, dir, "EdnitionCode/RosterStream") {
		t.Fatal("origin should match the repo (case-insensitive)")
	}
	if !gitRepoMatches(ctx, dir, "ednitioncode/rosterstream") {
		t.Fatal("repo match should be case-insensitive")
	}
	if gitRepoMatches(ctx, dir, "SomeoneElse/Other") {
		t.Fatal("a different repo must not match")
	}
}
