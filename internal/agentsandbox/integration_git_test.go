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
// sandbox: what the operator's own git obeys (a submodule's config and
// hooks, the common config, hooks, info, alternates, replace refs, the
// shallow file, the worktree's own pointers) cannot be written; the agent
// can still commit.
const sharedGitAttempts = `
c=$COMMON; gd=$GITDIR
try() { if sh -c "$2" 2>/dev/null; then echo "WRITABLE: $1"; else echo "denied: $1"; fi; }
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

func TestSandboxProtectsTheSharedGitDir(t *testing.T) {
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
	sub, base, wt := filepath.Join(root, "sub"), filepath.Join(root, "base"), filepath.Join(root, "wt")
	git(t, root, "init", "-q", "-b", "main", sub)
	git(t, sub, "commit", "-q", "--allow-empty", "-m", "s")
	git(t, root, "init", "-q", "-b", "main", base)
	os.WriteFile(filepath.Join(base, "README"), []byte("hello\n"), 0o644)
	git(t, base, "add", "README")
	git(t, base, "commit", "-q", "-m", "init")
	git(t, base, "-c", "protocol.file.allow=always", "submodule", "-q", "add", sub, "sub")
	git(t, base, "commit", "-q", "-m", "sub")
	git(t, base, "worktree", "add", "-q", "-b", "fix", wt)
	common := filepath.Join(base, ".git")

	layout, err := ResolveGitLayout(wt)
	if err != nil {
		t.Fatal(err)
	}
	home, logins, err := PrepareHome(HomeSpec{Root: filepath.Join(root, "st"), Key: "agent:git", Tool: "claude-code", GOOS: runtime.GOOS})
	if err != nil {
		t.Fatal(err)
	}
	pol := Policy{
		Identity: "agent:git", Tool: "claude-code", Workspace: wt, Home: home, Logins: logins,
		ReadOnly: layout.ReadOnly, ReadWrite: layout.ReadWrite, Hide: layout.Hide,
		// The mounts alone: the git guard would otherwise undo (and kill
		// on) the one write that lands.
		Detect: DetectSpec{Action: DetectOff}, Limits: LimitSpec{Off: true},
	}
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	l, err := Wrap(pol, []string{"/bin/sh", "-c", sharedGitAttempts}, Deps{
		SelfExe: selfConductor(t), EgressUnix: pm.UnixEndpoint, EgressAddr: pm.Endpoint,
		LookPath: exec.LookPath, RealHome: filepath.Join(root, "real"), Inherited: os.Environ(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Cleanup()
	cmd := exec.Command(l.Argv[0], l.Argv[1:]...)
	cmd.Dir = l.Dir
	cmd.Env = append(l.Env, "COMMON="+common, "GITDIR="+filepath.Join(common, "worktrees", "wt"))
	out, err := cmd.CombinedOutput()
	t.Logf("in the sandbox (%s):\n%s", runtime.GOOS, out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "WRITABLE") || !strings.Contains(string(out), "commit ok") || strings.Count(string(out), "denied: ") != 12 {
		t.Fatal("the shared git dir is not protected, or the agent cannot commit")
	}
	// The operator's git, run afterwards, finds nothing planted.
	if b, _ := os.ReadFile(filepath.Join(common, "modules", "sub", "config")); strings.Contains(string(b), "fsmonitor") {
		t.Fatal("a submodule's config was written")
	}
	if ents, _ := os.ReadDir(filepath.Join(common, "refs", "replace")); len(ents) != 0 {
		t.Fatal("a replace ref was planted")
	}
}
