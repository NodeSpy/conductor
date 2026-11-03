package dispatch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/core"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=o", "GIT_AUTHOR_EMAIL=o@x", "GIT_COMMITTER_NAME=o", "GIT_COMMITTER_EMAIL=o@x")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func mentions(paths []string, p string) bool {
	for _, x := range paths {
		if x == p || strings.HasPrefix(p, x+"/") {
			return true
		}
	}
	return false
}

// An agent rewrites its workspace's .git pointer to name the operator's
// home. A follow-up dispatch into the same workspace — and, after the
// registration is gone (a restart that lost it), a fresh one — never binds
// what the rewritten pointer names.
func TestAGitPointerRewriteNeverReachesTheNextLaunch(t *testing.T) {
	op, _ := filepath.EvalSymlinks(t.TempDir())
	os.MkdirAll(filepath.Join(op, ".ssh"), 0o700)
	os.WriteFile(filepath.Join(op, ".ssh", "id_ed25519"), []byte("DECOY"), 0o600)
	repo := filepath.Join(op, "Projects", "widget")
	wt := filepath.Join(op, "Projects", "worktrees", "wt1")
	os.MkdirAll(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "i")
	gitIn(t, repo, "remote", "add", "origin", "git@forge.example:acme/widget.git")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "pr-7", wt)
	oldTrust := agentsandbox.GitTrust
	agentsandbox.GitTrust.Home = op
	t.Cleanup(func() { agentsandbox.GitTrust = oldTrust; agentsandbox.Default.Forget(wt) })
	trusted := core.Trigger{TargetTrusted: true}
	id := "agent:fixer@acme/widget"

	e1, err := RegisterSandboxWorkspace(wt, id, "claude-code", &Request{DispatchID: "d1", Trigger: trusted})
	if err != nil {
		t.Fatal(err)
	}
	// The agent of d1 rewrites its .git pointer (in a clone it can; in a
	// linked worktree the sandbox shows it read-only).
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+op+"\n"), 0o644)

	e2, err := RegisterSandboxWorkspace(wt, id, "claude-code", &Request{DispatchID: "d2", Trigger: trusted})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(e2.ReadWrite, ",") != strings.Join(e1.ReadWrite, ",") || strings.Join(e2.ReadOnly, ",") != strings.Join(e1.ReadOnly, ",") {
		t.Fatalf("a follow-up dispatch re-read the agent's pointer:\n d1 rw=%v ro=%v\n d2 rw=%v ro=%v", e1.ReadWrite, e1.ReadOnly, e2.ReadWrite, e2.ReadOnly)
	}
	for _, p := range append(append([]string(nil), e2.ReadWrite...), e2.ReadOnly...) {
		if p == op || mentions([]string{p}, filepath.Join(op, ".ssh")) {
			t.Fatalf("d2 shows the operator's home: %s", p)
		}
	}
	if e1.Push == nil || e2.Push == nil || e2.Push.GitDir != e1.Push.GitDir {
		t.Fatalf("d2's push target is d1's recorded one: %+v vs %+v", e2.Push, e1.Push)
	}
	// The registration is gone: a fresh registration re-reads the pointer
	// and refuses it.
	agentsandbox.Default.Forget(wt)
	if _, err := RegisterSandboxWorkspace(wt, id, "claude-code", &Request{DispatchID: "d3", Trigger: trusted}); err == nil {
		t.Fatal("a .git pointer at the home was bound on a fresh registration")
	}
	if _, err := os.Stat(filepath.Join(op, "hooks")); err == nil {
		t.Fatal("conductor created paths inside an unvalidated pointer target")
	}
}
