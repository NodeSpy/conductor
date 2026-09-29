package gitwt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/gitdiff"
)

// These tests drive the go-git FALLBACK path: forceNoGit makes hasGit()
// report false, so every gitwt operation below goes through gogit.go instead
// of shelling out to `git`. Repos are still built with the real git binary in
// setup (originRepo, from gitwt_test.go) — the point of these tests is gitwt's
// own behavior without git, not re-testing git itself.

// forceNoGit makes hasGit() report false for the duration of the test.
func forceNoGit(t *testing.T) {
	t.Helper()
	prev := LookPath
	LookPath = func(string) (string, error) { return "", errors.New("git not found (forced for test)") }
	t.Cleanup(func() { LookPath = prev })
}

func TestGoGitCheckoutPRLandsThePRHeadOnItsBranch(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	forceNoGit(t)
	ctx := context.Background()

	id, cwd, err := p.ProvisionWorktree(ctx, prReq())
	if err != nil {
		t.Fatalf("ProvisionWorktree (go-git): %v", err)
	}
	if id == "" || id != cwd {
		t.Fatalf("got (id=%q, cwd=%q), want both the worktree path", id, cwd)
	}
	if _, err := os.Stat(filepath.Join(cwd, "FEATURE.md")); err != nil {
		t.Fatalf("PR head not checked out (no FEATURE.md): %v", err)
	}
	wantSHA := strings.TrimSpace(out(t, origin, "git", "rev-parse", "refs/pull/7/head"))
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "HEAD")); got != wantSHA {
		t.Fatalf("worktree HEAD = %s, want the PR head %s", got, wantSHA)
	}
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "--abbrev-ref", "HEAD")); got != "feature" {
		t.Fatalf("worktree branch = %q, want %q", got, "feature")
	}
	// origin must be re-pointed at the real remote, not the local base clone.
	originURL := strings.TrimSpace(out(t, cwd, "git", "remote", "get-url", "origin"))
	if originURL != origin {
		t.Fatalf("origin = %q, want the real remote %q", originURL, origin)
	}
}

func TestGoGitBranchOffCutsTheConductorBranchFromTheBaseRef(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	forceNoGit(t)

	req := prReq()
	req.Action.Checkout = "branch-off"
	_, cwd, err := p.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionWorktree (go-git): %v", err)
	}
	if got, want := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "--abbrev-ref", "HEAD")),
		"conductor/merge_conflict-7"; got != want {
		t.Fatalf("branch = %q, want %q", got, want)
	}
	wantSHA := strings.TrimSpace(out(t, origin, "git", "rev-parse", "main"))
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "HEAD")); got != wantSHA {
		t.Fatalf("HEAD = %s, want main's tip %s", got, wantSHA)
	}
	if _, err := os.Stat(filepath.Join(cwd, "FEATURE.md")); err == nil {
		t.Fatal("branch-off worktree contains the PR's file; it branched off the wrong ref")
	}
}

// A PR checkout in fallback mode must leave a reviewable diff behind for a
// box with no git at all: <worktree>/.conductor/pr.diff, and .conductor/
// excluded from the checkout's own git status.
func TestGoGitWritesPRDiffAndExcludesConductorDir(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	forceNoGit(t)

	_, cwd, err := p.ProvisionWorktree(context.Background(), prReq())
	if err != nil {
		t.Fatalf("ProvisionWorktree (go-git): %v", err)
	}
	diff, err := os.ReadFile(filepath.Join(cwd, ".conductor", "pr.diff"))
	if err != nil {
		t.Fatalf("pr.diff not written: %v", err)
	}
	if !strings.Contains(string(diff), "FEATURE.md") {
		t.Fatalf("pr.diff does not mention the PR's own file:\n%s", diff)
	}
	exclude, err := os.ReadFile(filepath.Join(cwd, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("info/exclude not written: %v", err)
	}
	if !strings.Contains(string(exclude), ".conductor/") {
		t.Fatalf("info/exclude does not list .conductor/: %s", exclude)
	}
	status := out(t, cwd, "git", "status", "--porcelain")
	if strings.Contains(status, ".conductor") {
		t.Fatalf("git status still reports .conductor/ as untracked:\n%s", status)
	}
}

// gitdiff.Proposed must work against a fallback-provisioned worktree exactly
// as it does against a git one (it shells to `git` itself when available,
// independent of how the worktree was made — this proves the two packages'
// fallbacks compose).
func TestGoGitProvisionedWorktreeWorksWithGitdiffProposed(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	forceNoGit(t)
	req := prReq()
	req.Action.Checkout = "branch-off"
	_, cwd, err := p.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionWorktree (go-git): %v", err)
	}
	// README.md is already tracked (originRepo commits it on main); `git diff
	// HEAD` never shows untracked files, so the edit must land on a tracked one.
	if err := os.WriteFile(filepath.Join(cwd, "README.md"), []byte("base\nhello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	diff, err := gitdiff.Proposed(context.Background(), cwd, 0)
	if err != nil {
		t.Fatalf("gitdiff.Proposed: %v", err)
	}
	if !strings.Contains(diff, "+hello") {
		t.Fatalf("gitdiff.Proposed missed the uncommitted change:\n%s", diff)
	}
}

func TestGoGitRemoveWorktreeLeavesNothingBehind(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	forceNoGit(t)
	ctx := context.Background()

	id, cwd, err := p.ProvisionWorktree(ctx, prReq())
	if err != nil {
		t.Fatalf("ProvisionWorktree (go-git): %v", err)
	}
	if err := p.RemoveWorktree(ctx, id); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatalf("worktree dir %s still present after removal (stat err: %v)", cwd, err)
	}
	// Idempotent.
	if err := p.RemoveWorktree(ctx, id); err != nil {
		t.Fatalf("second RemoveWorktree: %v", err)
	}
}

func TestGoGitBaseCloneForExportsTheLiveMap(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	forceNoGit(t)
	id, _, err := p.ProvisionWorktree(context.Background(), prReq())
	if err != nil {
		t.Fatalf("ProvisionWorktree (go-git): %v", err)
	}
	base, ok := p.BaseCloneFor(id)
	if !ok {
		t.Fatal("BaseCloneFor reported not-live for a just-provisioned worktree")
	}
	if want := filepath.Join(p.CheckoutsDir(), "acme__web"); base != want {
		t.Fatalf("BaseCloneFor = %q, want %q", base, want)
	}
	if _, ok := p.BaseCloneFor("/nonexistent"); ok {
		t.Fatal("BaseCloneFor reported live for an unknown path")
	}
}

// Push (go-git path): a fast-forward push to the real bare origin must land,
// and a non-fast-forward push must fail rather than silently force.
func TestGoGitPushFastForwardAndRejectsDiverged(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	forceNoGit(t)
	ctx := context.Background()

	req := prReq()
	req.Action.Checkout = "branch-off"
	_, cwd, err := p.ProvisionWorktree(ctx, req)
	if err != nil {
		t.Fatalf("ProvisionWorktree (go-git): %v", err)
	}
	branch := "conductor/merge_conflict-7"
	if err := os.WriteFile(filepath.Join(cwd, "PUSHED.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, cwd, "add pushed file")

	remoteBranch := "conductor-push-test"
	if err := p.Push(ctx, cwd, branch, remoteBranch, false); err != nil {
		t.Fatalf("Push (fast-forward): %v", err)
	}
	gotSHA := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "HEAD"))
	wantSHA := strings.TrimSpace(out(t, origin, "git", "rev-parse", "refs/heads/"+remoteBranch))
	if gotSHA != wantSHA {
		t.Fatalf("origin's %s = %s, want the pushed tip %s", remoteBranch, wantSHA, gotSHA)
	}

	// Diverge: push something else to the same remote branch from a second
	// clone, then try (non-force) to push the FIRST worktree's now-stale
	// history over it — must fail.
	pushExtraCommit(t, origin, remoteBranch, "DIVERGED.md")
	if err := os.WriteFile(filepath.Join(cwd, "ALSO.md"), []byte("also\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, cwd, "diverge locally")
	if err := p.Push(ctx, cwd, branch, remoteBranch, false); err == nil {
		t.Fatal("non-fast-forward Push succeeded; it must fail without force")
	}
	// force:true must still be able to land it.
	if err := p.Push(ctx, cwd, branch, remoteBranch, true); err != nil {
		t.Fatalf("forced Push: %v", err)
	}
}

// commitAll stages and commits everything in dir with the real git binary
// (unaffected by forceNoGit, which only overrides gitwt's own LookPath var).
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	run(t, dir, "git", "add", "-A")
	run(t, dir, "git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", msg)
}
