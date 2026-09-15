package gitwt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// These tests drive the REAL git binary against real repositories in t.TempDir():
// the whole point of the package is the sequence of git commands it issues, and a
// mocked git would assert the mock rather than the behavior.

// originRepo builds a bare repo that stands in for the forge: a `main` branch, a
// `feature` branch one commit ahead, and refs/pull/7/head pointing at it — the
// exact refs a checkout-pr dispatch fetches. It returns the bare repo's path.
func originRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "origin.git")

	run(t, "", "git", "init", "--bare", "--initial-branch=main", bare)
	run(t, "", "git", "init", "--initial-branch=main", work)
	gitIdentity(t, work)
	writeFile(t, filepath.Join(work, "README.md"), "base\n")
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-m", "initial")
	run(t, work, "git", "remote", "add", "origin", bare)
	run(t, work, "git", "push", "-u", "origin", "main")

	// The PR: a branch off main with one extra commit, published both as a
	// branch and under refs/pull/7/head the way a forge exposes it.
	run(t, work, "git", "checkout", "-b", "feature")
	writeFile(t, filepath.Join(work, "FEATURE.md"), "pr work\n")
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-m", "pr change")
	run(t, work, "git", "push", "origin", "feature")
	run(t, work, "git", "push", "origin", "feature:refs/pull/7/head")
	return bare
}

// newProv builds a Provisioner rooted in a temp state dir that clones from the
// given bare repo for any repo name.
func newProv(t *testing.T, origin string) *Provisioner {
	t.Helper()
	p := New(t.TempDir())
	p.RemoteURL = func(string) string { return origin }
	return p
}

func prReq() dispatch.Request {
	return dispatch.Request{
		DispatchID: "disp-1",
		Trigger: core.Trigger{
			Kind:    "merge_conflict",
			Target:  core.Target{Repo: "acme/web", PR: 7, Number: 7, BaseRef: "main"},
			Context: map[string]any{"head_ref": "feature"},
		},
	}
}

func TestBaseCloneIsCreatedThenRefetched(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	ctx := context.Background()

	base, err := p.baseClone(ctx, "acme/web")
	if err != nil {
		t.Fatalf("first baseClone: %v", err)
	}
	if want := filepath.Join(p.CheckoutsDir(), "acme__web"); base != want {
		t.Fatalf("base clone at %q, want %q", base, want)
	}
	if !isGitDir(base) {
		t.Fatalf("%s is not a git checkout", base)
	}

	// A new commit lands on the forge; the second call must FETCH it, not
	// silently reuse the stale clone.
	pushExtraCommit(t, origin, "main", "LATER.md")
	want := strings.TrimSpace(out(t, origin, "git", "rev-parse", "main"))

	base2, err := p.baseClone(ctx, "acme/web")
	if err != nil {
		t.Fatalf("second baseClone: %v", err)
	}
	if base2 != base {
		t.Fatalf("second baseClone returned %q, want the same dir %q", base2, base)
	}
	if got := strings.TrimSpace(out(t, base, "git", "rev-parse", "origin/main")); got != want {
		t.Fatalf("origin/main = %s after re-fetch, want the new tip %s", got, want)
	}
}

func TestCheckoutPRLandsThePRHeadOnItsBranch(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	ctx := context.Background()

	id, cwd, err := p.ProvisionWorktree(ctx, prReq())
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	if id == "" || id != cwd {
		t.Fatalf("got (id=%q, cwd=%q), want both the worktree path", id, cwd)
	}
	if got, want := filepath.Dir(id), p.WorktreesDir(); got != want {
		t.Fatalf("worktree at %q, want a child of %q", id, want)
	}
	if _, err := os.Stat(filepath.Join(cwd, "FEATURE.md")); err != nil {
		t.Fatalf("PR head not checked out (no FEATURE.md): %v", err)
	}
	// The PR head commit, on a branch named for the PR's head ref — an agent's
	// `git push origin HEAD` must target the PR branch, not a detached head.
	wantSHA := strings.TrimSpace(out(t, origin, "git", "rev-parse", "refs/pull/7/head"))
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "HEAD")); got != wantSHA {
		t.Fatalf("worktree HEAD = %s, want the PR head %s", got, wantSHA)
	}
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "--abbrev-ref", "HEAD")); got != "feature" {
		t.Fatalf("worktree branch = %q, want the PR head ref %q", got, "feature")
	}
}

// A trigger with no head_ref still lands on a named branch, so the push target
// is deterministic rather than a detached HEAD.
func TestCheckoutPRWithoutHeadRefUsesPRNumberBranch(t *testing.T) {
	p := newProv(t, originRepo(t))
	req := prReq()
	req.Trigger.Context = nil

	_, cwd, err := p.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "--abbrev-ref", "HEAD")); got != "pr-7" {
		t.Fatalf("worktree branch = %q, want %q", got, "pr-7")
	}
}

func TestBranchOffCutsTheConductorBranchFromTheBaseRef(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)

	req := prReq()
	req.Action.Checkout = "branch-off"

	_, cwd, err := p.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	if got, want := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "--abbrev-ref", "HEAD")),
		"conductor/merge_conflict-7"; got != want {
		t.Fatalf("branch = %q, want %q", got, want)
	}
	// Branched off `main` (the trigger's base ref), NOT the PR head — so the
	// PR's file must be absent and the tip must be main's.
	wantSHA := strings.TrimSpace(out(t, origin, "git", "rev-parse", "main"))
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "HEAD")); got != wantSHA {
		t.Fatalf("HEAD = %s, want main's tip %s", got, wantSHA)
	}
	if _, err := os.Stat(filepath.Join(cwd, "FEATURE.md")); err == nil {
		t.Fatal("branch-off worktree contains the PR's file; it branched off the wrong ref")
	}
}

func TestCheckoutNoneProvisionsNothing(t *testing.T) {
	p := newProv(t, originRepo(t))
	req := prReq()
	req.Action.Checkout = "none"

	id, cwd, err := p.ProvisionWorktree(context.Background(), req)
	if err != nil || id != "" || cwd != "" {
		t.Fatalf("got (%q, %q, %v), want empty/empty/nil for checkout:none", id, cwd, err)
	}
	if _, err := os.Stat(p.WorktreesDir()); err == nil {
		t.Fatal("checkout:none created a worktrees dir")
	}
}

func TestExplicitWorkDirIsNotOurs(t *testing.T) {
	p := newProv(t, originRepo(t))
	req := prReq()
	req.Action.WorkDir = "/srv/checkout"

	id, cwd, err := p.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	// No id: an operator-named directory is never removed on close.
	if id != "" || cwd != "/srv/checkout" {
		t.Fatalf("got (%q, %q), want (\"\", \"/srv/checkout\")", id, cwd)
	}
}

// TestRemoveWorktreeLeavesNothingBehind is the LEAK test: provision, prove the
// checkout and git's own record of it exist, remove, and prove BOTH are gone.
// This is precisely what cliSession.Close never did before.
func TestRemoveWorktreeLeavesNothingBehind(t *testing.T) {
	p := newProv(t, originRepo(t))
	ctx := context.Background()

	id, cwd, err := p.ProvisionWorktree(ctx, prReq())
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	base := filepath.Join(p.CheckoutsDir(), "acme__web")
	if !strings.Contains(out(t, base, "git", "worktree", "list"), cwd) {
		t.Fatalf("git does not list the worktree %s it just created", cwd)
	}

	if err := p.RemoveWorktree(ctx, id); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatalf("worktree dir %s still present after removal (stat err: %v)", cwd, err)
	}
	if listing := out(t, base, "git", "worktree", "list"); strings.Contains(listing, cwd) {
		t.Fatalf("git still lists the removed worktree:\n%s", listing)
	}
	// And the provisioner no longer claims it, so the reaper isn't blocked.
	p.mu.Lock()
	n := len(p.live)
	p.mu.Unlock()
	if n != 0 {
		t.Fatalf("live set still holds %d worktree(s) after removal", n)
	}

	// Idempotent: closing twice (Close then a reaper pass) must not error.
	if err := p.RemoveWorktree(ctx, id); err != nil {
		t.Fatalf("second RemoveWorktree: %v", err)
	}
}

// A worktree left by a PREVIOUS daemon life has no entry in the live map; it
// must still be removed through git (via its .git pointer), not just unlinked.
func TestRemoveWorktreeRecoversTheBaseCloneAfterRestart(t *testing.T) {
	p := newProv(t, originRepo(t))
	ctx := context.Background()
	id, cwd, err := p.ProvisionWorktree(ctx, prReq())
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	if got := baseOf(cwd); got != filepath.Join(p.CheckoutsDir(), "acme__web") {
		t.Fatalf("baseOf(%s) = %q", cwd, got)
	}

	// Simulate the restart: a fresh provisioner over the same state dir.
	p2 := New(p.root)
	if err := p2.RemoveWorktree(ctx, id); err != nil {
		t.Fatalf("RemoveWorktree after restart: %v", err)
	}
	base := filepath.Join(p.CheckoutsDir(), "acme__web")
	if listing := out(t, base, "git", "worktree", "list"); strings.Contains(listing, cwd) {
		t.Fatalf("git still lists the worktree after a post-restart removal:\n%s", listing)
	}
}

// RemoveWorktree must be inert for anything that is not one of ours — a paseo
// workspace id from the fallback path, or any other path on the box.
func TestRemoveWorktreeIgnoresForeignPaths(t *testing.T) {
	p := newProv(t, originRepo(t))
	ctx := context.Background()

	outside := filepath.Join(t.TempDir(), "precious")
	writeFile(t, filepath.Join(outside, "keep.txt"), "do not delete\n")
	// A nested path UNDER the worktrees dir is not a direct child either.
	nested := filepath.Join(p.WorktreesDir(), "wt", "inner")
	writeFile(t, filepath.Join(nested, "keep.txt"), "do not delete\n")

	for _, id := range []string{"", "ws-42", outside, nested, filepath.Dir(p.WorktreesDir())} {
		if err := p.RemoveWorktree(ctx, id); err != nil {
			t.Fatalf("RemoveWorktree(%q) = %v, want nil", id, err)
		}
	}
	for _, path := range []string{outside, nested} {
		if _, err := os.Stat(filepath.Join(path, "keep.txt")); err != nil {
			t.Fatalf("RemoveWorktree deleted %s: %v", path, err)
		}
	}
}

// TestReapRemovesOrphansButNeverALiveWorktree covers both halves of the reaper
// contract in one run: an abandoned checkout is reclaimed, and one a session is
// still working in is untouched — however old it looks.
func TestReapRemovesOrphansButNeverALiveWorktree(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	ctx := context.Background()

	liveID, liveCwd, err := p.ProvisionWorktree(ctx, prReq())
	if err != nil {
		t.Fatalf("provision live: %v", err)
	}
	orphanReq := prReq()
	orphanReq.DispatchID = "disp-2"
	orphanID, orphanCwd, err := p.ProvisionWorktree(ctx, orphanReq)
	if err != nil {
		t.Fatalf("provision orphan: %v", err)
	}
	if orphanID == liveID {
		t.Fatal("two dispatches got the same worktree path")
	}
	// The orphan's owning session died without closing (a killed daemon):
	// nothing claims it any more.
	p.mu.Lock()
	delete(p.live, orphanID)
	p.mu.Unlock()

	// Both dirs look equally old; only the live set distinguishes them.
	old := time.Now().Add(-2 * time.Hour)
	touch(t, liveCwd, old)
	touch(t, orphanCwd, old)
	p.MinAge = time.Hour

	p.Reap(ctx)

	if _, err := os.Stat(orphanCwd); !os.IsNotExist(err) {
		t.Fatalf("reaper left the orphan %s behind (stat err: %v)", orphanCwd, err)
	}
	if _, err := os.Stat(liveCwd); err != nil {
		t.Fatalf("reaper removed a LIVE worktree %s: %v", liveCwd, err)
	}
	base := filepath.Join(p.CheckoutsDir(), "acme__web")
	listing := out(t, base, "git", "worktree", "list")
	if strings.Contains(listing, orphanCwd) {
		t.Fatalf("reaper did not prune git's record of the orphan:\n%s", listing)
	}
	if !strings.Contains(listing, liveCwd) {
		t.Fatalf("reaper pruned git's record of the LIVE worktree:\n%s", listing)
	}
}

// A dir younger than MinAge is left alone even with nothing claiming it — the
// grace period is what keeps a just-provisioned checkout from being reaped out
// from under a session that has not registered yet.
func TestReapRespectsTheGracePeriod(t *testing.T) {
	p := newProv(t, originRepo(t))
	ctx := context.Background()
	id, cwd, err := p.ProvisionWorktree(ctx, prReq())
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	p.mu.Lock()
	delete(p.live, id)
	p.mu.Unlock()

	p.MinAge = time.Hour
	p.Reap(ctx)

	if _, err := os.Stat(cwd); err != nil {
		t.Fatalf("reaper removed a fresh worktree inside the grace period: %v", err)
	}
}

// The reaper must never look outside <state>/worktrees, even when a stray file
// or dir sits next to it in the state dir.
func TestReapOnlyTouchesTheWorktreesDir(t *testing.T) {
	p := newProv(t, originRepo(t))
	ctx := context.Background()
	if _, _, err := p.ProvisionWorktree(ctx, prReq()); err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	neighbour := filepath.Join(p.root, "state.json")
	writeFile(t, neighbour, "{}\n")
	touch(t, neighbour, time.Now().Add(-48*time.Hour))
	base := filepath.Join(p.CheckoutsDir(), "acme__web")
	touch(t, base, time.Now().Add(-48*time.Hour))

	p.MinAge = time.Minute
	p.Reap(ctx)

	if _, err := os.Stat(neighbour); err != nil {
		t.Fatalf("reaper removed %s: %v", neighbour, err)
	}
	if !isGitDir(base) {
		t.Fatalf("reaper removed the base clone %s", base)
	}
}

// A dispatch whose git work fails must escalate (Unrecoverable), exactly like a
// failed `paseo workspace create`, and leave no half-made directory behind.
func TestProvisionFailureIsUnrecoverable(t *testing.T) {
	p := New(t.TempDir())
	p.RemoteURL = func(string) string { return filepath.Join(t.TempDir(), "nope.git") }

	_, _, err := p.ProvisionWorktree(context.Background(), prReq())
	if err == nil {
		t.Fatal("ProvisionWorktree succeeded against a missing remote")
	}
	if !dispatch.IsUnrecoverable(err) {
		t.Fatalf("error %v is not Unrecoverable; the engine would retry it as an ordinary step failure", err)
	}
}

func TestProvisionWithoutRepoIsUnrecoverable(t *testing.T) {
	p := newProv(t, originRepo(t))
	req := prReq()
	req.Trigger.Target = core.Target{PR: 7, Number: 7}
	req.Action.Checkout = "checkout-pr"

	if _, _, err := p.ProvisionWorktree(context.Background(), req); err == nil || !dispatch.IsUnrecoverable(err) {
		t.Fatalf("err = %v, want an Unrecoverable error", err)
	}
}

// Two dispatches carrying the same id must not collide on one directory — the
// second would otherwise fail `git worktree add` or, worse, share a checkout.
func TestWorktreePathsAreUniquePerDispatch(t *testing.T) {
	p := newProv(t, originRepo(t))
	ctx := context.Background()
	a, _, err := p.ProvisionWorktree(ctx, prReq())
	if err != nil {
		t.Fatalf("first provision: %v", err)
	}
	b, _, err := p.ProvisionWorktree(ctx, prReq())
	if err != nil {
		t.Fatalf("second provision: %v", err)
	}
	if a == b {
		t.Fatalf("both dispatches got %s", a)
	}
}

// New("") falls back to the state dir the store uses, so the checkouts and
// worktrees land where the reaper (and an operator) expect them.
func TestNewDefaultsToTheStateDir(t *testing.T) {
	config.SetStateDir(filepath.Join(t.TempDir(), "state"))
	t.Cleanup(func() { config.SetStateDir("") })
	p := New("")
	if want := filepath.Join(config.StateDir(), "worktrees"); p.WorktreesDir() != want {
		t.Fatalf("WorktreesDir() = %q, want %q", p.WorktreesDir(), want)
	}
}

func TestDefaultRemoteURLIsSSHUnlessOverridden(t *testing.T) {
	t.Setenv("CONDUCTOR_GIT_REMOTE_BASE", "")
	if got, want := DefaultRemoteURL("acme/web"), "git@github.com:acme/web.git"; got != want {
		t.Fatalf("DefaultRemoteURL = %q, want %q", got, want)
	}
	t.Setenv("CONDUCTOR_GIT_REMOTE_BASE", "git://forge/")
	if got, want := DefaultRemoteURL("acme/web"), "git://forge/acme/web.git"; got != want {
		t.Fatalf("DefaultRemoteURL = %q, want %q", got, want)
	}
}

// A head_ref comes off event data the PR author controls; it may name the
// branch, never an option or a path traversal.
func TestPRBranchRejectsAnUnsafeHeadRef(t *testing.T) {
	for _, ref := range []string{"--upload-pack=touch /tmp/x", "../evil", "a b", "", "-x", "feat.lock", "x/"} {
		req := prReq()
		req.Trigger.Context = map[string]any{"head_ref": ref}
		if got := prBranch(req); got != "pr-7" {
			t.Fatalf("prBranch with head_ref %q = %q, want the pr-7 fallback", ref, got)
		}
	}
	req := prReq()
	req.Trigger.Context = map[string]any{"head_ref": "users/me/fix-1"}
	if got := prBranch(req); got != "users/me/fix-1" {
		t.Fatalf("prBranch rejected a legitimate ref: %q", got)
	}
}

// ---- helpers -------------------------------------------------------------

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func run(t *testing.T, dir string, argv ...string) {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v: %s", strings.Join(argv, " "), err, b)
	}
}

func out(t *testing.T, dir string, argv ...string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s in %s: %v", strings.Join(argv, " "), dir, err)
	}
	return string(b)
}

func gitIdentity(t *testing.T, dir string) {
	t.Helper()
	run(t, dir, "git", "config", "user.name", "Test")
	run(t, dir, "git", "config", "user.email", "test@example.test")
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

// pushExtraCommit adds a commit to branch on the bare repo, via a throwaway clone.
func pushExtraCommit(t *testing.T, bare, branch, file string) {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "push")
	run(t, "", "git", "clone", "-b", branch, bare, tmp)
	gitIdentity(t, tmp)
	writeFile(t, filepath.Join(tmp, file), "more\n")
	run(t, tmp, "git", "add", "-A")
	run(t, tmp, "git", "commit", "-m", "more")
	run(t, tmp, "git", "push", "origin", branch)
}
