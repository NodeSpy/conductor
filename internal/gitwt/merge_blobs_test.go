package gitwt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// conflictOrigin is a partial-clone origin where the PR and main both changed
// README.md since they diverged, and main also added a directory the PR has
// never seen — a merge needs blobs the PR head's checkout never fetched.
func conflictOrigin(t *testing.T) (url, bare string) {
	t.Helper()
	bare = originRepo(t)
	run(t, "", "git", "--git-dir="+bare, "config", "uploadpack.allowFilter", "true")
	run(t, "", "git", "--git-dir="+bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	work := t.TempDir()
	run(t, "", "git", "clone", "-q", bare, work)
	gitIdentity(t, work)
	writeFile(t, filepath.Join(work, "README.md"), "main's readme\n")
	writeFile(t, filepath.Join(work, "apis", "client.go"), "package apis\n")
	run(t, work, "git", "add", "-A")
	run(t, work, "git", "commit", "-qm", "main moves on")
	run(t, work, "git", "push", "-q", "origin", "main")
	run(t, work, "git", "checkout", "-q", "feature")
	writeFile(t, filepath.Join(work, "README.md"), "the PR's readme\n")
	run(t, work, "git", "commit", "-qam", "pr edits readme")
	run(t, work, "git", "push", "-q", "origin", "feature", "feature:refs/pull/7/head")
	return "file://" + bare, bare
}

// A merge-conflict fixer runs sandboxed: once its checkout exists it has no
// network, so its git cannot lazily fetch a blob the partial base clone
// lacks. The checkout must already hold everything merging the PR with its
// base needs — the base branch's tree and the merge base's — or the fixer is
// blocked ("blobs missing from partial clone, no network to fetch").
func TestPRCheckoutCanMergeItsBaseWithoutNetwork(t *testing.T) {
	url, bare := conflictOrigin(t)
	p := newProv(t, url)
	_, wt, err := p.ProvisionWorktree(context.Background(), prReq())
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(p.CheckoutsDir(), "acme__web")
	if strings.TrimSpace(out(t, base, "git", "config", "--get", "remote.origin.promisor")) != "true" {
		t.Fatal("the base clone is not a partial clone — this test needs one")
	}
	// The agent's sandbox: no route to the origin.
	if err := os.Rename(bare, bare+".gone"); err != nil {
		t.Fatal(err)
	}
	gitIdentity(t, wt)
	merge := exec.Command("git", "merge", "--no-edit", "origin/main")
	merge.Dir = wt
	merge.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	got, _ := merge.CombinedOutput()
	if !strings.Contains(string(got), "CONFLICT (content): Merge conflict in README.md") {
		t.Fatalf("merge of the base did not reach the conflict:\n%s", got)
	}
	for _, f := range []string{"apis/client.go", "README.md"} {
		if _, err := os.Stat(filepath.Join(wt, f)); err != nil {
			t.Fatalf("%s not written by the merge: %v", f, err)
		}
	}
	// Resolving and committing needs no further object either.
	writeFile(t, filepath.Join(wt, "README.md"), "resolved\n")
	run(t, wt, "git", "add", "README.md")
	run(t, wt, "git", "commit", "-qm", "merge main")
	// So does a rebase of the PR's commits onto the base instead.
	run(t, wt, "git", "reset", "-q", "--hard", "HEAD~1")
	rebase := exec.Command("git", "rebase", "origin/main")
	rebase.Dir = wt
	rebase.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	got, _ = rebase.CombinedOutput()
	if !strings.Contains(string(got), "CONFLICT (content): Merge conflict in README.md") {
		t.Fatalf("rebase onto the base did not reach the conflict:\n%s", got)
	}
}
