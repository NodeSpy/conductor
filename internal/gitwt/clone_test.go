package gitwt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// partialOrigin is originRepo served over file:// with filtering on, so the
// base clone is a real blob:none partial clone, plus a main history whose
// first README blob only the origin has.
func partialOrigin(t *testing.T) string {
	t.Helper()
	bare := originRepo(t)
	run(t, "", "git", "--git-dir="+bare, "config", "uploadpack.allowFilter", "true")
	run(t, "", "git", "--git-dir="+bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	work := t.TempDir()
	run(t, "", "git", "clone", "-q", bare, work)
	gitIdentity(t, work)
	writeFile(t, filepath.Join(work, "README.md"), "base v2\n")
	run(t, work, "git", "commit", "-qam", "readme v2")
	run(t, work, "git", "push", "-q", "origin", "main")
	return "file://" + bare
}

func objectCount(t *testing.T, gitDir string) string {
	t.Helper()
	var loose, packs int
	_ = filepath.WalkDir(filepath.Join(gitDir, "objects"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(p, ".pack"):
			packs++
		case len(filepath.Base(filepath.Dir(p))) == 2:
			loose++
		}
		return nil
	})
	return strings.Repeat("l", loose) + strings.Repeat("p", packs)
}

// Two dispatches on the same repo get their own clones: objects borrowed
// from the base (none copied), refs and config their own — so one cannot
// move, delete, repack or corrupt the other's — and a blob the partial base
// lacks is still fetched lazily in a dispatch clone.
func TestDispatchClonesAreIsolatedFromEachOther(t *testing.T) {
	p := newProv(t, partialOrigin(t))
	ctx := context.Background()
	reqA, reqB := prReq(), prReq()
	reqA.DispatchID, reqB.DispatchID = "disp-a", "disp-b"
	_, a, err := p.ProvisionWorktree(ctx, reqA)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := p.ProvisionWorktree(ctx, reqB)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(p.CheckoutsDir(), "acme__web")
	for _, wt := range []string{a, b} {
		if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || !fi.IsDir() {
			t.Fatalf("%s: a dispatch gets its own clone (.git dir), not a linked worktree", wt)
		}
		alt, _ := os.ReadFile(filepath.Join(wt, ".git", "objects", "info", "alternates"))
		if strings.TrimSpace(string(alt)) != filepath.Join(base, ".git", "objects") {
			t.Fatalf("%s borrows %q, want the base's objects", wt, alt)
		}
		if n := objectCount(t, filepath.Join(wt, ".git")); n != "" {
			t.Fatalf("%s copied objects (%s) — it must borrow them", wt, n)
		}
		if got := strings.TrimSpace(out(t, wt, "git", "status", "--porcelain")); got != "" {
			t.Fatalf("%s is not a clean checkout:\n%s", wt, got)
		}
		if br := strings.TrimSpace(out(t, wt, "git", "symbolic-ref", "--short", "HEAD")); br != "feature" {
			t.Fatalf("%s is on %q, want the PR branch", wt, br)
		}
		out(t, wt, "git", "rev-parse", "--verify", "origin/main")
	}
	if strings.TrimSpace(out(t, base, "git", "config", "--get", "remote.origin.promisor")) != "true" {
		t.Fatal("the base clone is not a partial clone — this test needs one")
	}

	// A deletes "its" branch, writes packed-refs, and prunes everything.
	gitIdentity(t, a)
	run(t, a, "git", "checkout", "-q", "--detach")
	run(t, a, "git", "branch", "-D", "feature")
	writeFile(t, filepath.Join(a, ".git", "packed-refs"), "# pack-refs with: peeled\n")
	run(t, a, "git", "gc", "-q", "--prune=now")
	// B is untouched: its branch, its commit, its objects.
	if br := strings.TrimSpace(out(t, b, "git", "rev-parse", "--verify", "refs/heads/feature")); br == "" {
		t.Fatal("B lost its branch")
	}
	run(t, b, "git", "fsck", "--connectivity-only", "--no-dangling")
	// A commit in B stays B's; A cannot see it.
	gitIdentity(t, b)
	writeFile(t, filepath.Join(b, "B.md"), "b\n")
	run(t, b, "git", "add", "B.md")
	run(t, b, "git", "commit", "-qm", "b work")
	sha := strings.TrimSpace(out(t, b, "git", "rev-parse", "HEAD"))
	if err := exec.Command("git", "-C", a, "cat-file", "-e", sha).Run(); err == nil {
		t.Fatal("A sees B's new commit")
	}
	if err := exec.Command("git", "-C", base, "cat-file", "-e", sha).Run(); err == nil {
		t.Fatal("B's new object landed in the shared base")
	}
	// Lazy blob fetch: main's first README exists only on the origin.
	if got := strings.TrimSpace(out(t, b, "git", "show", "origin/main~1:README.md")); got != "base" {
		t.Fatalf("lazy blob fetch in a dispatch clone: %q", got)
	}
}
