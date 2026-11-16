package gitwt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoWithConductorFiles is originRepo with a .conductor.yaml on main and a
// different one on the PR branch (and its refs/pull/7/head).
func repoWithConductorFiles(t *testing.T) (bare, work string) {
	t.Helper()
	bare = originRepo(t)
	work = filepath.Join(t.TempDir(), "w")
	run(t, "", "git", "clone", bare, work)
	gitIdentity(t, work)
	writeFile(t, filepath.Join(work, ".conductor.yaml"), "version: 1 # main\n")
	writeFile(t, filepath.Join(work, "go.sum"), "main deps\n")
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-m", "main conductor file")
	run(t, work, "git", "push", "origin", "main")
	run(t, work, "git", "checkout", "feature")
	writeFile(t, filepath.Join(work, ".conductor.yaml"), "version: 1 # PR HEAD\n")
	writeFile(t, filepath.Join(work, "go.sum"), "PR deps\n")
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-m", "pr conductor file")
	run(t, work, "git", "push", "-f", "origin", "feature")
	run(t, work, "git", "push", "-f", "origin", "feature:refs/pull/7/head")
	return bare, work
}

func TestDefaultBranchFileIsTheDefaultBranchsOnly(t *testing.T) {
	bare, _ := repoWithConductorFiles(t)
	p := newProv(t, bare)
	ctx := context.Background()
	// A PR checkout exists for the same repo (its base clone has the PR
	// head fetched); the file still comes from main.
	if _, _, err := p.ProvisionWorktree(ctx, prReq()); err != nil {
		t.Fatal(err)
	}
	b, commit, err := p.DefaultBranchFile(ctx, "acme/web", ".conductor.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "version: 1 # main\n" {
		t.Fatalf("read %q, want main's", b)
	}
	want := strings.TrimSpace(out(t, bare, "git", "rev-parse", "main"))
	if commit != want {
		t.Fatalf("commit %s, want main %s", commit, want)
	}
	if b, _, err := p.DefaultBranchFile(ctx, "acme/web", "absent.yaml"); err != nil || b != nil {
		t.Fatalf("absent file: %q %v", b, err)
	}
	for _, bad := range []string{"", "a/b", "../x", "-x"} {
		if _, _, err := p.DefaultBranchFile(ctx, "acme/web", bad); err == nil {
			t.Errorf("read %q", bad)
		}
	}
}

func TestDefaultBranchFileRefusesASymlink(t *testing.T) {
	bare := originRepo(t)
	work := filepath.Join(t.TempDir(), "w")
	run(t, "", "git", "clone", bare, work)
	gitIdentity(t, work)
	if err := os.Symlink("/etc/passwd", filepath.Join(work, ".conductor.yaml")); err != nil {
		t.Fatal(err)
	}
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-m", "link")
	run(t, work, "git", "push", "origin", "main")
	p := newProv(t, bare)
	if b, _, err := p.DefaultBranchFile(context.Background(), "acme/web", ".conductor.yaml"); err == nil {
		t.Fatalf("read a symlink: %q", b)
	}
}

func TestDefaultBranchCheckoutIsMainWithoutGit(t *testing.T) {
	bare, work := repoWithConductorFiles(t)
	p := newProv(t, bare)
	ctx := context.Background()
	if _, _, err := p.ProvisionWorktree(ctx, prReq()); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "tree")
	commit, err := p.DefaultBranchCheckout(ctx, "acme/web", dst)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "go.sum")); string(b) != "main deps\n" {
		t.Fatalf("go.sum %q, want main's", b)
	}
	if _, err := os.Stat(filepath.Join(dst, "FEATURE.md")); err == nil {
		t.Fatal("the PR's file is in the default-branch checkout")
	}
	if _, err := os.Lstat(filepath.Join(dst, ".git")); err == nil {
		t.Fatal("the checkout has a git link")
	}
	if commit != strings.TrimSpace(out(t, work, "git", "rev-parse", "origin/main")) {
		t.Fatalf("commit %s", commit)
	}
	// Its bookkeeping is gone from the base clone.
	base := filepath.Join(p.CheckoutsDir(), repoSlug("acme/web"))
	if wl := out(t, base, "git", "worktree", "list", "--porcelain"); strings.Contains(wl, dst) {
		t.Fatalf("worktree still registered:\n%s", wl)
	}
}
