package gitpush

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moveRemote pushes a commit writing name=body onto the remote's fix/1 from
// another clone — the PR's author pushing while the agent works — and
// returns the remote's new head.
func (f fixture) moveRemote(t *testing.T, name, body string) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "o")
	run(t, "", "git", "clone", "-q", "-b", "fix/1", f.remote, other)
	os.WriteFile(filepath.Join(other, name), []byte(body), 0o644)
	run(t, other, "git", "add", ".")
	run(t, other, "git", "commit", "-q", "-m", "author: "+name)
	run(t, other, "git", "push", "-q", "origin", "fix/1")
	return run(t, f.remote, "git", "rev-parse", "fix/1")
}

func (f fixture) rebaseReq() Request {
	r := f.req()
	r.Rebase = true
	return r
}

// The remote branch moved while the agent worked and the agent's commits
// replay cleanly onto it: they are rebased onto the new head and pushed
// fast-forward — the author's commit kept, the agent's work on top, signed.
func TestPushRebasesOntoAMovedRemote(t *testing.T) {
	f := newFixture(t)
	f.commit(t, "b.txt", "b\n")
	agentHead := f.commit(t, "c.txt", "c\n")
	theirs := f.moveRemote(t, "x.txt", "x\n")

	res, err := Push(context.Background(), f.rebaseReq())
	if err != nil {
		t.Fatalf("a cleanly rebasing push goes through: %v", err)
	}
	if !res.Rebased || res.Onto != theirs || res.Commits != 2 || res.Old != theirs {
		t.Fatalf("result: %+v", res)
	}
	now := run(t, f.remote, "git", "rev-parse", "fix/1")
	if now != res.New {
		t.Fatalf("remote fix/1 = %s, want %s", now, res.New)
	}
	// A fast-forward of the author's head: their commit is never rewritten.
	run(t, f.remote, "git", "merge-base", "--is-ancestor", theirs, now)
	for _, p := range []string{"a.txt", "b.txt", "c.txt", "x.txt"} {
		run(t, f.remote, "git", "cat-file", "-e", now+":"+p)
	}
	if subj := run(t, f.remote, "git", "log", "--format=%s", theirs+".."+now); subj != "agent: c.txt\nagent: b.txt" {
		t.Fatalf("the agent's commits, in order, with their messages: %q", subj)
	}
	f.verifyRange(t, theirs, now)
	// The workspace keeps its own commits; its origin/fix/1 says what landed.
	if got := run(t, f.ws, "git", "rev-parse", "HEAD"); got != agentHead {
		t.Fatalf("the workspace branch moved: %s", got)
	}
	if got := run(t, f.ws, "git", "rev-parse", "refs/remotes/origin/fix/1"); got != now {
		t.Fatalf("workspace origin/fix/1 = %s, want %s", got, now)
	}
}

// A commit whose change the moved remote already carries is dropped, not
// pushed again as an empty commit.
func TestPushRebaseDropsAChangeAlreadyOnTheRemote(t *testing.T) {
	f := newFixture(t)
	f.commit(t, "x.txt", "x\n")
	f.commit(t, "b.txt", "b\n")
	theirs := f.moveRemote(t, "x.txt", "x\n")
	res, err := Push(context.Background(), f.rebaseReq())
	if err != nil {
		t.Fatal(err)
	}
	if res.Commits != 1 {
		t.Fatalf("only b.txt is new: %+v", res)
	}
	if n := run(t, f.remote, "git", "rev-list", "--count", theirs+"..fix/1"); n != "1" {
		t.Fatalf("%s commits pushed, want 1", n)
	}
}

// The replay conflicts: nothing is pushed, the error names the commit and
// the path, the agent's commits are kept under a ref in the trusted git
// dir, and the workspace's origin/fix/1 is the remote's new head — what the
// agent resolves against.
func TestPushRebaseConflictKeepsTheCommits(t *testing.T) {
	f := newFixture(t)
	agentHead := f.commit(t, "a.txt", "agent\n")
	theirs := f.moveRemote(t, "a.txt", "author\n")

	_, err := Push(context.Background(), f.rebaseReq())
	if !errors.Is(err, ErrDiverged) {
		t.Fatalf("a conflicting rebase is a diverged push: %v", err)
	}
	var de *DivergedError
	if !errors.As(err, &de) || !de.Conflicted() || de.Commit != agentHead || strings.Join(de.Files, ",") != "a.txt" || de.Remote != theirs {
		t.Fatalf("conflict detail: %+v", de)
	}
	var kept *KeptError
	if !errors.As(err, &kept) || kept.Ref != UnpushedRef("fix/1", agentHead) || kept.Commits != 1 {
		t.Fatalf("kept: %+v (%v)", kept, err)
	}
	if !strings.Contains(err.Error(), kept.Ref) || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("the error names the ref and the conflict: %v", err)
	}
	if got := run(t, f.base, "git", "rev-parse", kept.Ref); got != agentHead {
		t.Fatalf("kept ref = %s, want %s", got, agentHead)
	}
	if got := run(t, f.remote, "git", "rev-parse", "fix/1"); got != theirs {
		t.Fatal("nothing was pushed")
	}
	if got := run(t, f.ws, "git", "rev-parse", "refs/remotes/origin/fix/1"); got != theirs {
		t.Fatalf("workspace origin/fix/1 = %s, want the remote's new head", got)
	}
	// The workspace can now resolve: the remote's commit is readable there.
	run(t, f.ws, "git", "cat-file", "-e", theirs+":a.txt")
}

// Rebasing off: the push is refused as before — and the commits are kept.
func TestPushWithRebaseOffRefusesButKeeps(t *testing.T) {
	f := newFixture(t)
	agentHead := f.commit(t, "b.txt", "b\n")
	f.moveRemote(t, "x.txt", "x\n")
	_, err := Push(context.Background(), f.req())
	var de *DivergedError
	if !errors.Is(err, ErrDiverged) || !errors.As(err, &de) || de.Attempted {
		t.Fatalf("refused without a rebase attempt: %v", err)
	}
	if got := run(t, f.base, "git", "rev-parse", UnpushedRef("fix/1", agentHead)); got != agentHead {
		t.Fatalf("kept ref = %s", got)
	}
}

// The cli runtime's layout: a linked worktree of conductor's base clone on
// the PR branch. The rebased push lands, and the objects the workspace's
// origin/fix/1 names are in the shared git dir.
func TestLinkedPushRebasesOntoAMovedRemote(t *testing.T) {
	f := newFixture(t)
	ws := filepath.Join(filepath.Dir(f.ws), "linkedpr")
	run(t, f.base, "git", "worktree", "add", "-q", "-B", "fix/1", ws, "origin/fix/1")
	l := linkedFixture{fixture: f, repo: f.base, headDir: filepath.Join(f.base, ".git", "worktrees", "linkedpr")}
	l.ws = ws
	l.commit(t, "b.txt", "b\n")
	theirs := l.moveRemote(t, "x.txt", "x\n")
	r := l.fixture.req()
	r.Workspace, r.HeadDir, r.Rebase = ws, l.headDir, true
	res, err := Push(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rebased || res.Commits != 1 {
		t.Fatalf("result: %+v", res)
	}
	run(t, f.remote, "git", "merge-base", "--is-ancestor", theirs, "fix/1")
	if got := run(t, f.base, "git", "rev-parse", "refs/remotes/origin/fix/1"); got != res.New {
		t.Fatalf("origin/fix/1 = %s, want %s", got, res.New)
	}
	run(t, ws, "git", "cat-file", "-e", res.New+":x.txt")
}

// A linked worktree's conflicting push keeps the commits too.
func TestLinkedPushRebaseConflictKeepsTheCommits(t *testing.T) {
	f := newFixture(t)
	ws := filepath.Join(filepath.Dir(f.ws), "linkedpr")
	run(t, f.base, "git", "worktree", "add", "-q", "-B", "fix/1", ws, "origin/fix/1")
	l := linkedFixture{fixture: f, repo: f.base, headDir: filepath.Join(f.base, ".git", "worktrees", "linkedpr")}
	l.ws = ws
	agentHead := l.commit(t, "a.txt", "agent\n")
	theirs := l.moveRemote(t, "a.txt", "author\n")
	r := l.fixture.req()
	r.Workspace, r.HeadDir, r.Rebase = ws, l.headDir, true
	_, err := Push(context.Background(), r)
	var de *DivergedError
	if !errors.As(err, &de) || !de.Conflicted() {
		t.Fatalf("conflict: %v", err)
	}
	if got := run(t, f.base, "git", "rev-parse", UnpushedRef("fix/1", agentHead)); got != agentHead {
		t.Fatalf("kept ref = %s", got)
	}
	if got := run(t, ws, "git", "rev-parse", "origin/fix/1"); got != theirs {
		t.Fatalf("origin/fix/1 = %s, want %s", got, theirs)
	}
}
