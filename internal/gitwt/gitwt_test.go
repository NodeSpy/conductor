package gitwt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
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

// A trigger with no head_ref resolves the PR's branch from the remote by its
// head commit, so a push still updates the PR. It must never land on a local
// pr-<n> branch: pushing that publishes a stray branch and leaves the PR as-is.
func TestCheckoutPRWithoutHeadRefResolvesTheBranch(t *testing.T) {
	p := newProv(t, originRepo(t))
	req := prReq()
	req.Trigger.Context = nil

	_, cwd, err := p.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "--abbrev-ref", "HEAD")); got != "feature" {
		t.Fatalf("worktree branch = %q, want the PR's branch %q", got, "feature")
	}
}

// When the head commit can't be pinned to exactly one remote branch, the head
// is checked out detached: a bare push then fails loudly instead of creating a
// new branch.
func TestCheckoutPRWithAmbiguousHeadIsDetached(t *testing.T) {
	origin := originRepo(t)
	// A second branch at the same commit makes the lookup ambiguous.
	run(t, origin, "git", "branch", "feature-copy", "feature")
	p := newProv(t, origin)
	req := prReq()
	req.Trigger.Context = nil

	_, cwd, err := p.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "--abbrev-ref", "HEAD")); got != "HEAD" {
		t.Fatalf("worktree branch = %q, want a detached HEAD", got)
	}
	wantSHA := strings.TrimSpace(out(t, origin, "git", "rev-parse", "refs/pull/7/head"))
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "HEAD")); got != wantSHA {
		t.Fatalf("worktree HEAD = %s, want the PR head %s", got, wantSHA)
	}
	if b := out(t, cwd, "git", "branch", "--list", "pr-7"); strings.TrimSpace(b) != "" {
		t.Fatalf("a local pr-7 branch was created: %q", b)
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

// Concurrent dispatches on one repo share its base clone. A checkout-pr used
// to fetch the PR head into the shared FETCH_HEAD and then `worktree add` from
// it outside the repo lock, so a sibling dispatch's fetch (its own PR's, or the
// base clone refresh) rewrote FETCH_HEAD in between: the add failed with
// "invalid reference: FETCH_HEAD", or quietly checked out ANOTHER PR's head.
// Every dispatch here must succeed and land on its own PR's head.
func TestConcurrentCheckoutPRsOnOneRepoEachLandTheirOwnHead(t *testing.T) {
	origin := originRepo(t)
	// A second PR on the same repo with a different head, so a clobbered
	// FETCH_HEAD shows up as the wrong commit, not just as an error.
	tmp := filepath.Join(t.TempDir(), "pr8")
	run(t, "", "git", "clone", "-b", "main", origin, tmp)
	gitIdentity(t, tmp)
	writeFile(t, filepath.Join(tmp, "OTHER.md"), "other pr\n")
	run(t, tmp, "git", "add", "-A")
	run(t, tmp, "git", "commit", "-m", "other pr")
	run(t, tmp, "git", "push", "origin", "HEAD:refs/heads/other")
	run(t, tmp, "git", "push", "origin", "HEAD:refs/pull/8/head")
	want := map[int]string{
		7: strings.TrimSpace(out(t, origin, "git", "rev-parse", "refs/pull/7/head")),
		8: strings.TrimSpace(out(t, origin, "git", "rev-parse", "refs/pull/8/head")),
	}

	p := newProv(t, origin)
	ctx := context.Background()
	// Seed the base clone so every dispatch takes the fetch-an-existing-clone
	// path (the one the incident hit), not N racing first clones.
	if _, err := p.baseClone(ctx, "acme/web"); err != nil {
		t.Fatalf("seed base clone: %v", err)
	}

	const n = 12
	type result struct {
		pr  int
		wt  string
		err error
	}
	results := make(chan result, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		pr := 7 + i%2
		req := prReq()
		req.DispatchID = fmt.Sprintf("disp-%d", i)
		req.Trigger.Target.PR, req.Trigger.Target.Number = pr, pr
		if pr == 8 {
			req.Trigger.Context = map[string]any{"head_ref": "other"}
		}
		go func() {
			<-start
			wt, _, err := p.ProvisionWorktree(ctx, req)
			results <- result{pr, wt, err}
		}()
	}
	close(start)
	for i := 0; i < n; i++ {
		r := <-results
		if r.err != nil {
			t.Errorf("PR %d: ProvisionWorktree: %v", r.pr, r.err)
			continue
		}
		if got := strings.TrimSpace(out(t, r.wt, "git", "rev-parse", "HEAD")); got != want[r.pr] {
			t.Errorf("PR %d worktree %s is at %s, want its own head %s", r.pr, r.wt, got, want[r.pr])
		}
	}
	// The per-dispatch fetch refs are scaffolding: none may outlive the add.
	base := filepath.Join(p.CheckoutsDir(), "acme__web")
	if left := strings.TrimSpace(out(t, base, "git", "for-each-ref", "refs/conductor/")); left != "" {
		t.Errorf("temporary fetch refs left behind:\n%s", left)
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
		if got := prBranch(req); got != "" {
			t.Fatalf("prBranch with head_ref %q = %q, want it rejected", ref, got)
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

// A connector that declares where its targets' code lives (checkout.remote)
// is cloned from there — not the default host — for its own target only;
// an unsafe transport is never used, and the harness override still wins.
func TestDeclaredRemote(t *testing.T) {
	req := func(remote, project string) dispatch.Request {
		return dispatch.Request{Trigger: core.Trigger{
			Sem:           &sdk.EventSemantics{Checkout: &sdk.CheckoutSemantics{Remote: remote}},
			Target:        core.Target{Repo: "o/r", Number: 1, Project: project},
			TargetTrusted: true,
		}}
	}
	if got := declaredRemote(req("ssh://git.example.org/{{.repo}}.git", ""), "o/r"); got != "ssh://git.example.org/o/r.git" {
		t.Fatalf("declared remote = %q", got)
	}
	if got := declaredRemote(req("ssh://git.example.org/{{.repo}}.git", "other/repo"), "other/repo"); got != "" {
		t.Fatalf("a step repo: override used the event's remote: %q", got)
	}
	if got := declaredRemote(req("ext::sh -c touch% /tmp/pwned", ""), "o/r"); got != "" {
		t.Fatalf("an unsafe transport was used: %q", got)
	}
	// A sender-chosen target never picks the host: the remote renders over
	// the event's facts.
	forged := req("https://{{.clone_host}}/{{.repo}}.git", "")
	forged.Trigger.TargetTrusted = false
	forged.Trigger.Context = map[string]any{"clone_host": "evil.example"}
	if got := declaredRemote(forged, "o/r"); got != "" {
		t.Fatalf("a sender-chosen target picked the remote: %q", got)
	}
	p := &Provisioner{}
	t.Setenv("CONDUCTOR_GIT_REMOTE_BASE", "")
	if got := p.remoteURL("o/r", "ssh://git.example.org/o/r.git"); got != "ssh://git.example.org/o/r.git" {
		t.Fatalf("remoteURL = %q", got)
	}
	if got := p.remoteURL("o/r", ""); got != DefaultRemoteURL("o/r") {
		t.Fatalf("no declaration: %q", got)
	}
	t.Setenv("CONDUCTOR_GIT_REMOTE_BASE", "git://forge/")
	if got := p.remoteURL("o/r", "ssh://git.example.org/o/r.git"); got != "git://forge/o/r.git" {
		t.Fatalf("the harness override must win: %q", got)
	}
}
