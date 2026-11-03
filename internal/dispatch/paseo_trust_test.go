package dispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
)

// An agent's registered workspace is described by what conductor recorded
// when it registered it — never by the workspace's own git config, which
// the agent writes.
func TestAgentWorkspaceFactsComeFromTheRegistration(t *testing.T) {
	ws := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "evil"}, {"config", "remote.origin.url", "git@forge.example:evil/repo.git"},
		{"-c", "user.name=a", "-c", "user.email=a@x", "commit", "-q", "--allow-empty", "-m", "x"}} {
		if out, err := exec.Command("git", append([]string{"-C", ws}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	ctx := context.Background()
	d := &Dispatcher{}
	// Unregistered: the checkout's own answers.
	if !gitRepoMatches(ctx, ws, "evil/repo") || d.gitBranch(ctx, ws) != "evil" {
		t.Fatal("unregistered: the checkout's own config")
	}
	unreg := agentsandbox.Default.Register(agentsandbox.Entry{Workspace: ws, Identity: "agent:t", Dispatch: "d1",
		Push: &agentsandbox.PushTarget{Repo: "own/repo", Branch: "conductor/fix-1", Remote: "git@forge.example:own/repo.git", GitDir: filepath.Join(ws, "nope")}})
	defer unreg()
	if gitRepoMatches(ctx, ws, "evil/repo") || !gitRepoMatches(ctx, ws, "own/repo") {
		t.Fatal("registered: the remote conductor recorded decides, not the workspace's config")
	}
	if got := d.gitBranch(ctx, ws); got != "conductor/fix-1" {
		t.Fatalf("registered: the recorded branch, got %q", got)
	}
	// A substring is not a match.
	if gitRepoMatches(ctx, ws, "own/re") {
		t.Fatal("a substring of the slug is not the repo")
	}
}

// The stale-lock sweep removes config.lock only from the git dir conductor
// recorded — never from wherever an agent's .git points.
func TestClearStaleGitLockIgnoresAnAgentRepointedGitDir(t *testing.T) {
	root := t.TempDir()
	victim := filepath.Join(root, "victim")
	os.MkdirAll(filepath.Join(victim, "objects"), 0o755)
	os.WriteFile(filepath.Join(victim, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644)
	lock := filepath.Join(victim, "config.lock")
	os.WriteFile(lock, nil, 0o644)
	old := time.Now().Add(-time.Hour)
	os.Chtimes(lock, old, old)
	ws := filepath.Join(root, "ws")
	os.MkdirAll(ws, 0o755)
	os.WriteFile(filepath.Join(ws, ".git"), []byte("gitdir: "+victim+"\n"), 0o644)
	unreg := agentsandbox.Default.Register(agentsandbox.Entry{Workspace: ws, Identity: "agent:t"})
	clearStaleGitLock(context.Background(), "", ws)
	unreg()
	if _, err := os.Stat(lock); err != nil {
		t.Fatal("a lock outside the recorded git dir was removed")
	}
}
