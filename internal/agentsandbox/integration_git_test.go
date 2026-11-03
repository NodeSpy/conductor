//go:build linux || darwin

package agentsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
)

// The agent's attempts on a linked worktree's shared git dir, in the real
// sandbox: nothing the operator's own git obeys (a submodule's config and
// hooks, the common config, hooks, info, alternates, replace refs, the
// shallow file, the worktree's own pointers) changes on the host — a write
// is refused, or lands in the launch's private view of the common dir — and
// the agent can still commit.
const sharedGitAttempts = `
c=$COMMON; gd=$GITDIR
try() { if sh -c "$2" 2>/dev/null; then echo "written (private view): $1"; else echo "denied: $1"; fi; }
try modules-config "printf '[core]\n\tfsmonitor = touch /tmp/pwned\n' >>$c/modules/sub/config"
try modules-hook "echo '#!/bin/sh' >$c/modules/sub/hooks/post-checkout"
try new-module "mkdir -p $c/modules/x/hooks"
try config "printf '[core]\n\tfsmonitor = x\n' >>$c/config"
try hook "echo '#!/bin/sh' >$c/hooks/pre-commit"
try info "echo '* filter=x' >>$c/info/attributes"
try alternates "echo /etc >$c/objects/info/alternates"
try shallow "echo 0000000000000000000000000000000000000000 >$c/shallow"
try replace-ref "git replace -f HEAD:README $(echo EVIL | git hash-object -w --stdin)"
try pointer "echo 'gitdir: /etc' >.git"
try commondir "echo /etc >$gd/commondir"
try gitdir-file "echo /etc >$gd/gitdir"
echo agent >>README
git -c user.name=agent -c user.email=a@x commit -qam work && echo "commit ok"
`

// linkedRepo makes a base repository with a submodule and a packed branch,
// and a linked worktree of it; it returns the worktree and the common dir.
func linkedRepo(t *testing.T, root string) (wt, common string) {
	t.Helper()
	sub, base := filepath.Join(root, "sub"), filepath.Join(root, "base")
	wt = filepath.Join(root, "wt")
	git(t, root, "init", "-q", "-b", "main", sub)
	git(t, sub, "commit", "-q", "--allow-empty", "-m", "s")
	git(t, root, "init", "-q", "-b", "main", base)
	os.WriteFile(filepath.Join(base, "README"), []byte("hello\n"), 0o644)
	git(t, base, "add", "README")
	git(t, base, "commit", "-q", "-m", "init")
	git(t, base, "-c", "protocol.file.allow=always", "submodule", "-q", "add", sub, "sub")
	git(t, base, "commit", "-q", "-m", "sub")
	git(t, base, "branch", "old")
	git(t, base, "pack-refs", "--all")
	git(t, base, "worktree", "add", "-q", "-b", "fix", wt)
	return wt, filepath.Join(base, ".git")
}

// runInSandbox runs script in the real agent sandbox in wt (its git layout
// resolved as a dispatch's is) and returns its output.
func runInSandbox(t *testing.T, root, wt, script string, env ...string) string {
	t.Helper()
	layout, err := ResolveGitLayout(wt)
	if err != nil {
		t.Fatal(err)
	}
	home, logins, err := PrepareHome(HomeSpec{Root: filepath.Join(root, "st"), Key: "agent:git", Tool: "claude-code", GOOS: runtime.GOOS, GitName: "agent", GitEmail: "a@x"})
	if err != nil {
		t.Fatal(err)
	}
	pol := Policy{
		Identity: "agent:git", Tool: "claude-code", Workspace: wt, Home: home, Logins: logins,
		ReadOnly: layout.ReadOnly, ReadWrite: layout.ReadWrite, Hide: layout.Hide,
		GitCommon: layout.Common, GitWorktree: layout.HeadDir,
		// The mounts alone: the git guard would otherwise undo (and kill
		// on) the one write that lands.
		Detect: DetectSpec{Action: DetectOff}, Limits: LimitSpec{Off: true},
	}
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	l, err := Wrap(pol, []string{"/bin/sh", "-c", script}, Deps{
		SelfExe: selfConductor(t), EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
		LookPath: exec.LookPath, RealHome: filepath.Join(root, "real"), Inherited: os.Environ(),
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = append(l.Env, env...)
	out, err := cmd.CombinedOutput()
	l.Cleanup()
	t.Logf("in the sandbox (%s):\n%s", runtime.GOOS, out)
	if err != nil {
		t.Errorf("in the sandbox: %v", err)
	}
	return string(out)
}

func requireSandbox(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short")
	}
	if why := Probe(); why != "" {
		if os.Getenv("CONDUCTOR_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("the agent sandbox cannot be built here: %s", why)
		}
		t.Skipf("no sandbox here: %s", why)
	}
	root, err := SocketDir("csg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	return root
}

func TestSandboxProtectsTheSharedGitDir(t *testing.T) {
	root := requireSandbox(t)
	wt, common := linkedRepo(t, root)
	gitdir := filepath.Join(common, "worktrees", "wt")
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	before := map[string]string{}
	for _, p := range []string{filepath.Join(common, "config"), filepath.Join(wt, ".git"), filepath.Join(gitdir, "commondir"), filepath.Join(gitdir, "gitdir"), filepath.Join(common, "modules", "sub", "config")} {
		before[p] = read(p)
	}
	out := runInSandbox(t, root, wt, sharedGitAttempts, "COMMON="+common, "GITDIR="+gitdir)
	if !strings.Contains(out, "commit ok") {
		t.Fatal("the agent cannot commit")
	}
	// The operator's git, run afterwards, finds nothing planted.
	for p, b := range before {
		if read(p) != b {
			t.Errorf("%s changed:\n%s", p, read(p))
		}
	}
	for _, p := range []string{"hooks/pre-commit", "modules/sub/hooks/post-checkout", "modules/x", "objects/info/alternates", "shallow"} {
		if _, err := os.Lstat(filepath.Join(common, p)); err == nil {
			t.Errorf("%s was planted", p)
		}
	}
	if strings.Contains(read(filepath.Join(common, "info", "attributes")), "filter") {
		t.Error("an attribute was planted")
	}
	if ents, _ := os.ReadDir(filepath.Join(common, "refs", "replace")); len(ents) != 0 {
		t.Error("a replace ref was planted")
	}
	for _, what := range []string{"modules-config", "modules-hook", "new-module", "hook", "info", "alternates", "replace-ref", "pointer", "commondir", "gitdir-file"} {
		if !strings.Contains(out, "denied: "+what) {
			t.Errorf("%s was not refused", what)
		}
	}
}

// The everyday git an agent and its tools run works in a linked worktree,
// with clean stderr: committing, deleting a packed branch, stash, local
// config, gc, pack-refs, rebase. The operator's config is untouched and the
// agent's branches are in the repository afterwards, packed or not.
const everydayGit = `
set -e
exec 2>>"$ERRLOG"
git commit -qam work
git branch -d old
echo more >>README
git stash -q
git stash pop -q
git commit -qam more
git config --local user.email dev@local
test "$(git config --local --get user.email)" = dev@local
git branch keep
git gc -q
git pack-refs --all
git rebase -q main
git log --oneline -1 >/dev/null
echo "everyday git ok"
`

func TestSandboxAgentGitWorks(t *testing.T) {
	root := requireSandbox(t)
	wt, common := linkedRepo(t, root)
	cfg, _ := os.ReadFile(filepath.Join(common, "config"))
	os.WriteFile(filepath.Join(wt, "README"), []byte("changed\n"), 0o644)
	errlog := filepath.Join(wt, ".git-stderr")
	out := runInSandbox(t, root, wt, everydayGit, "ERRLOG="+errlog)
	stderr, _ := os.ReadFile(errlog)
	if !strings.Contains(out, "everyday git ok") || len(stderr) != 0 {
		t.Fatalf("git in the sandbox failed or complained:\n%s\nstderr:\n%s", out, stderr)
	}
	if b, _ := os.ReadFile(filepath.Join(common, "config")); string(b) != string(cfg) {
		t.Fatalf("the operator's config changed:\n%s", b)
	}
	head := strings.TrimSpace(gitOut(t, wt, "rev-parse", "HEAD"))
	base := filepath.Dir(common)
	for _, ref := range []string{"fix", "keep"} {
		if got := strings.TrimSpace(gitOut(t, base, "rev-parse", "refs/heads/"+ref)); got != head && ref == "fix" {
			t.Fatalf("the repository's %s is %s, the agent's work %s", ref, got, head)
		} else if got == "" {
			t.Fatalf("the agent's branch %s is gone from the repository", ref)
		}
	}
}

// gitOut is git's output in dir ("" when it fails).
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}
