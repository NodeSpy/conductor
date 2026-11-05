package gitwt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// tarDir archives the tree at src (tar -C src -cf - .) into a read-only
// file, as the blob store holds an artifact, and returns its path.
func tarDir(t *testing.T, src string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tree.tar")
	out(t, src, "tar", "-C", src, "-cf", p, ".")
	if err := os.Chmod(p, 0o444); err != nil {
		t.Fatal(err)
	}
	return p
}

// archiveOf stores a small tree as an archive file and returns its path.
func archiveOf(t *testing.T) string {
	t.Helper()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "README.md"), "snapshot\n")
	writeFile(t, filepath.Join(src, "pkg", "a.go"), "package pkg\n")
	return tarDir(t, src)
}

// Every dispatch that starts from an artifact gets its own fresh copy in a
// worktree dir of its own — no checkout, no fetch — so one agent's edits
// never reach another's, nor the artifact. Removal and the reaper treat it
// like any worktree.
func TestWorkspaceFromArchiveIsAFreshCopyPerDispatch(t *testing.T) {
	archive := archiveOf(t)
	// No remote at all: an artifact workspace must never need one.
	p := New(t.TempDir())
	p.RemoteURL = func(string) string { return filepath.Join(t.TempDir(), "nope.git") }
	ctx := context.Background()

	var cwds []string
	for _, id := range []string{"lens-1", "lens-2", "lens-3"} {
		req := prReq()
		req.DispatchID = id
		req.WorkspaceArchive = archive
		wsID, cwd, err := p.ProvisionWorktree(ctx, req)
		if err != nil {
			t.Fatalf("%s: ProvisionWorktree: %v", id, err)
		}
		if wsID != cwd || filepath.Dir(cwd) != p.WorktreesDir() {
			t.Fatalf("%s: got %q/%q, want a worktree dir of its own under %s", id, wsID, cwd, p.WorktreesDir())
		}
		if got, err := os.ReadFile(filepath.Join(cwd, "pkg", "a.go")); err != nil || string(got) != "package pkg\n" {
			t.Fatalf("%s: unpacked tree: %q %v", id, got, err)
		}
		cwds = append(cwds, cwd)
	}
	if _, err := os.Stat(p.CheckoutsDir()); !os.IsNotExist(err) {
		t.Errorf("an artifact workspace made a base clone: %v", err)
	}
	writeFile(t, filepath.Join(cwds[0], "README.md"), "edited by lens-1\n")
	for _, cwd := range cwds[1:] {
		if got, _ := os.ReadFile(filepath.Join(cwd, "README.md")); string(got) != "snapshot\n" {
			t.Errorf("%s sees another dispatch's edit: %q", cwd, got)
		}
	}

	if err := p.RemoveWorktree(ctx, cwds[0]); err != nil {
		t.Fatalf("RemoveWorktree: %v", err)
	}
	if _, err := os.Stat(cwds[0]); !os.IsNotExist(err) {
		t.Errorf("RemoveWorktree left %s", cwds[0])
	}
	// A daemon that died with one live: the reaper reclaims it once stale,
	// and leaves the one a session still holds.
	p.mu.Lock()
	delete(p.live, cwds[1])
	p.mu.Unlock()
	old := time.Now().Add(-2 * time.Hour)
	touch(t, cwds[1], old)
	touch(t, cwds[2], old)
	p.MinAge = time.Hour
	p.Reap(ctx)
	if _, err := os.Stat(cwds[1]); !os.IsNotExist(err) {
		t.Errorf("the reaper left the orphaned artifact workspace %s", cwds[1])
	}
	if _, err := os.Stat(cwds[2]); err != nil {
		t.Errorf("the reaper removed a live artifact workspace: %v", err)
	}
}

// A corrupt or hostile archive fails the dispatch Unrecoverable and leaves
// no half-unpacked dir behind.
func TestWorkspaceFromBadArchiveFails(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.tar")
	writeFile(t, bad, "not a tar archive at all, just some bytes that go on for a while...")
	p := New(t.TempDir())
	req := prReq()
	req.WorkspaceArchive = bad
	_, _, err := p.ProvisionWorktree(context.Background(), req)
	if err == nil || !dispatch.IsUnrecoverable(err) {
		t.Fatalf("ProvisionWorktree(bad archive) = %v, want an Unrecoverable error", err)
	}
	if ents, _ := os.ReadDir(p.WorktreesDir()); len(ents) != 0 {
		t.Errorf("a failed unpack left %d dir(s) behind", len(ents))
	}
}

// The snapshot a PR review shares: a self-contained shallow clone of the
// base branch plus the PR head (what a git connector's clone makes), archived
// with its .git and unpacked once per reviewer. Each copy's git diffs against
// the base on its own — no alternates into any other clone.
func TestACloneSnapshotKeepsGitWorkingInEveryCopy(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	ctx := context.Background()
	tree := filepath.Join(t.TempDir(), "tree")
	out(t, origin, "git", "clone", "-q", "--depth", "5", "--single-branch", "--branch", "main", "file://"+origin, tree)
	out(t, tree, "git", "fetch", "-q", "--depth", "5", "origin", "refs/pull/7/head")
	out(t, tree, "git", "checkout", "-q", "--detach", "FETCH_HEAD")
	if _, err := os.Stat(filepath.Join(tree, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatal("the snapshot borrows objects through alternates")
	}
	archive := tarDir(t, tree)
	if err := os.RemoveAll(tree); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(out(t, origin, "git", "rev-parse", "refs/pull/7/head"))
	for _, id := range []string{"lens-a", "lens-b"} {
		req := prReq()
		req.DispatchID = id
		req.WorkspaceArchive = archive
		_, cwd, err := p.ProvisionWorktree(ctx, req)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "HEAD")); got != head {
			t.Errorf("%s: HEAD %s, want the PR head %s", id, got, head)
		}
		if got := out(t, cwd, "git", "diff", "--name-only", "origin/main...HEAD"); strings.TrimSpace(got) != "FEATURE.md" {
			t.Errorf("%s: diff against the base: %q", id, got)
		}
		if st := strings.TrimSpace(out(t, cwd, "git", "status", "--porcelain")); st != "" {
			t.Errorf("%s: the copy is not clean: %q", id, st)
		}
	}
}
