//go:build linux

package jail

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/hostcmd"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// This file proves the jail end to end on a real kernel: a real conductor
// binary, a real worktree of a local bare remote, a throwaway signing key,
// and a fake "operator home" — never the machine owner's own.

var buildOnce sync.Once
var builtConductor string

func conductorBinary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "conductor-jailtest-bin")
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, "conductor")
		cmd := exec.Command("go", "build", "-o", out, "github.com/NodeSpy/conductor/cmd/conductor")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build conductor: %v\n%s", err, b)
		}
		builtConductor = out
	})
	if builtConductor == "" {
		t.Fatal("conductor binary not built")
	}
	return builtConductor
}

func needJailKernel(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: builds conductor")
	}
	if os.Geteuid() == 0 {
		t.Skip("the jail refuses to run as root")
	}
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare missing")
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skipf("unprivileged user namespaces unavailable: %v", err)
	}
	for _, b := range []string{"git", "ssh-keygen"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skip(b + " missing")
		}
	}
}

func run(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type fixture struct {
	root, home, remote, base, ws, bin, allowed string
	events                                     []Event
	mu                                         sync.Mutex
}

// newFixture builds: a fake operator home (with a "secret" ~/.ssh, a signing
// key, ~/.config/gh/hosts.yml, ~/.claude), a bare remote with main + fix/42,
// a base clone and a worktree on fix/42, and a fake `gh` on a host PATH dir.
func newFixture(t *testing.T) *fixture {
	// Outside /tmp: a host command's view replaces /tmp with the dispatch's
	// own, and a real operator home is never under it.
	base := filepath.Join(os.Getenv("HOME"), ".cache")
	_ = os.MkdirAll(base, 0o700)
	root, err := os.MkdirTemp(base, "conductor-jailtest-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeScratch(root) })
	f := &fixture{root: root, home: filepath.Join(root, "home"), remote: filepath.Join(root, "remote.git"),
		base: filepath.Join(root, "state", "checkouts", "acme__app"), ws: filepath.Join(root, "state", "worktrees", "d1"),
		bin: filepath.Join(root, "hostbin")}
	for _, d := range []string{f.home + "/.ssh", f.home + "/.config/gh", f.home + "/.claude", f.bin, filepath.Dir(f.ws)} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(f.home+"/.ssh/id_secret", []byte("NOT-A-REAL-KEY-BUT-SECRET\n"), 0o600)
	os.WriteFile(f.home+"/.config/gh/hosts.yml", []byte("github.com:\n  user: op\n"), 0o600)
	os.WriteFile(f.home+"/.claude/settings.json", []byte("{}\n"), 0o600)
	// Throwaway signing key.
	run(t, root, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "throwaway", "-f", f.home+"/.ssh/signing")
	pub, _ := os.ReadFile(f.home + "/.ssh/signing.pub")
	f.allowed = filepath.Join(root, "allowed_signers")
	os.WriteFile(f.allowed, []byte("op@example.test "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	gitcfg := "[user]\n\tname = Op\n\temail = op@example.test\n\tsigningkey = " + f.home + "/.ssh/signing.pub\n" +
		"[gpg]\n\tformat = ssh\n[commit]\n\tgpgsign = true\n[init]\n\tdefaultBranch = main\n"
	os.WriteFile(f.home+"/.gitconfig", []byte(gitcfg), 0o644)
	t.Setenv("HOME", f.home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	run(t, root, nil, "git", "init", "-q", "--bare", f.remote)
	seed := filepath.Join(root, "seed")
	run(t, root, nil, "git", "init", "-q", seed)
	os.WriteFile(seed+"/README", []byte("hello\n"), 0o644)
	run(t, seed, nil, "git", "add", ".")
	run(t, seed, nil, "git", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")
	run(t, seed, nil, "git", "push", "-q", f.remote, "HEAD:refs/heads/main", "HEAD:refs/heads/fix/42")
	run(t, root, nil, "git", "clone", "-q", f.remote, f.base)
	run(t, f.base, nil, "git", "worktree", "add", "-q", "-B", "fix/42", f.ws, "origin/fix/42")

	// The fake gh records its view of the world, and tries to write its
	// config (the copy-on-write home must throw that away).
	gh := "#!/bin/sh\necho \"fake-gh args=$* repo=$GH_REPO\"\n" +
		"test -e \"$HOME/.ssh/id_secret\" && echo \"gh-sees-ssh\"\n" +
		"test -e \"$HOME/.config/gh/hosts.yml\" && echo \"gh-sees-login\"\n" +
		"echo tampered >> \"$HOME/.config/gh/hosts.yml\"\n"
	os.WriteFile(f.bin+"/gh", []byte(gh), 0o755)
	return f
}

func (f *fixture) manager(t *testing.T) *Manager {
	self := conductorBinary(t)
	look := func(n string) (string, error) {
		if n == "gh" {
			return f.bin + "/gh", nil
		}
		return "", exec.ErrNotFound
	}
	return &Manager{
		Root:      filepath.Join(f.root, "state", "jails"),
		SelfExe:   func() (string, error) { return self, nil },
		LookPath:  look,
		Home:      f.home,
		Sensitive: []string{filepath.Join(f.root, "state", "config")},
		Emit: func(e Event) {
			f.mu.Lock()
			f.events = append(f.events, e)
			f.mu.Unlock()
		},
		CheckWrite: func(d *Dispatch, w hostcmd.Write) string {
			if w.Kind == "create_pr" {
				return "target: opening a PR is refused (writes are bound to the dispatch's own target)"
			}
			if w.Number != d.Number {
				return "target: write to another target"
			}
			return ""
		},
		CheckPush: func(d *Dispatch, branch string, force, del bool) string {
			switch {
			case force:
				return "target: force-push refused"
			case del:
				return "target: branch deletion refused"
			case branch != d.HeadBranch:
				return "target: push to " + branch + " but the dispatch's branch is " + d.HeadBranch
			}
			return ""
		},
	}
}

func (f *fixture) runJailed(t *testing.T, m *Manager, script string) (string, error) {
	l, err := m.Prepare(context.Background(), LaunchSpec{
		DispatchID: "d1", Tool: "claude-code", Workspace: f.ws, Repo: "acme/app", Number: 42, IsPR: true,
		HeadBranch: "fix/42", Label: "fix acme/app#42",
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer l.Close()
	spec := sandbox.FromConfig(&config.IsolationConfig{Mode: "namespace"})
	binds := l.Binds
	// A read-only bind from a nosuid,nodev mount (/dev/shm everywhere; a
	// container's tmpfs state dir in practice): its locked flags must be
	// repeated on the remount or the whole jail fails with EPERM.
	if f, err := os.CreateTemp("/dev/shm", "conductor-jailtest-"); err == nil {
		f.WriteString("shm-ok\n")
		f.Close()
		t.Cleanup(func() { os.Remove(f.Name()) })
		binds = append(binds, sandbox.BindMount{Path: f.Name(), RO: true})
		script = "cat " + f.Name() + "\n" + script
	}
	deps := sandbox.LocalWrapDeps{SelfExe: m.SelfExe, Confine: true, ExtraBinds: binds}
	env := append(sandbox.MinimalEnv(), l.Env...)
	env = append(env, "GH_TOKEN=must-not-reach-the-jail")
	env = stripCreds(env)
	argv, outEnv, cleanup, err := sandbox.WrapLocalCommand(spec, []string{"/bin/sh", "-c", script}, f.ws, env, deps)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	defer cleanup()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = f.ws
	cmd.Env = outEnv
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func stripCreds(env []string) []string {
	var out []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "GH_TOKEN=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func TestJailEndToEnd(t *testing.T) {
	needJailKernel(t)
	f := newFixture(t)
	m := f.manager(t)
	script := `
echo "uid=$(id -u)"
echo "home-list:$(ls -A "$HOME" | tr '\n' ' ')"
cat "$HOME/.ssh/id_secret" 2>/dev/null && echo LEAK-ssh
test -e "` + f.root + `/state/config" && echo LEAK-config
echo "token:$(env | grep -c TOKEN= | tr -d ' ')"
ls "` + f.bin + `" 2>/dev/null && echo LEAK-hostbin
gh pr view 42; echo "gh-view-exit=$?"
gh pr create -t x -b y; echo "gh-create-exit=$?"
gh auth logout; echo "gh-logout-exit=$?"
` + f.bin + `/gh pr view 42 2>/dev/null; echo "abs-exit=$?"
echo work > file.txt
git add file.txt && git commit -q -m "jailed commit" && echo committed
git push -q origin HEAD:refs/heads/fix/42; echo "push-own-exit=$?"
git push -q origin HEAD:refs/heads/new-branch; echo "push-new-exit=$?"
git push -q --force origin HEAD:refs/heads/main; echo "push-force-exit=$?"
git push -q origin :refs/heads/fix/42; echo "push-delete-exit=$?"
echo x > .git/../x2 && git -C . status --short | head -3
echo "cfg-write:$(sh -c 'echo "[core]" >> "$(git rev-parse --git-common-dir)/config"' 2>&1 | head -1)"
`
	out, err := f.runJailed(t, m, script)
	t.Logf("jailed output:\n%s", out)
	if err != nil {
		t.Fatalf("jailed script: %v", err)
	}
	for _, want := range []string{
		"uid=" + itoa(os.Getuid()),
		"token:0",
		"fake-gh args=pr view 42 repo=acme/app", "gh-view-exit=0",
		"gh-create-exit=126", "gh-logout-exit=126",
		"committed", "push-own-exit=0", "shm-ok",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"LEAK-", "gh-sees-ssh", "push-new-exit=0", "push-force-exit=0", "push-delete-exit=0", "abs-exit=0"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected %q", bad)
		}
	}
	if !strings.Contains(out, "gh-sees-login") {
		t.Error("host gh must see its own login (used in place)")
	}
	if !strings.Contains(out, "cfg-write:") || strings.Contains(out, "cfg-write:\n") {
		// the write must fail with an error message
	}
	// The home listing shows the scratch home: only the tool state and git
	// config, never .ssh.
	if strings.Contains(out, ".ssh") {
		t.Error("the jail's home must not show .ssh")
	}
	// Host-side effects: the push landed on fix/42 of the bare remote,
	// signed with the throwaway key; the copy-on-write home threw away gh's
	// config write.
	head := run(t, f.remote, nil, "git", "log", "-1", "--format=%s", "refs/heads/fix/42")
	if head != "jailed commit" {
		t.Fatalf("remote fix/42 head: %q", head)
	}
	run(t, f.remote, nil, "git", "-c", "gpg.ssh.allowedSignersFile="+f.allowed, "verify-commit", "refs/heads/fix/42")
	if b, _ := os.ReadFile(f.home + "/.config/gh/hosts.yml"); strings.Contains(string(b), "tampered") {
		t.Fatal("a host command's write to the operator's config must be discarded")
	}
	if _, err := exec.Command("git", "-C", f.remote, "rev-parse", "--verify", "-q", "refs/heads/new-branch").Output(); err == nil {
		t.Fatal("new-branch must not exist on the remote")
	}
	cfg, _ := os.ReadFile(filepath.Join(f.base, ".git", "config"))
	if strings.Count(string(cfg), "[core]") > 1 {
		t.Fatal("the jail must not be able to write the common dir's config")
	}
	// Audit: every boundary crossing recorded, refusals with a reason.
	f.mu.Lock()
	defer f.mu.Unlock()
	var sawCreate, sawLogout, sawPush, sawRefusedPush, sawSign, sawDiscard bool
	for _, e := range f.events {
		t.Logf("event: %s %s %s %q %v", e.Type, e.Status, e.Detail, e.Reason, e.Fields)
		switch {
		case e.Type == "host_command" && e.Status == "refused" && strings.Contains(e.Detail, "pr create"):
			sawCreate = strings.Contains(e.Reason, "opening a PR")
		case e.Type == "host_command" && e.Status == "refused" && strings.Contains(e.Detail, "auth logout"):
			sawLogout = strings.Contains(e.Reason, "built-in: gh auth")
		case e.Type == "git_push" && e.Status == "ok":
			sawPush = true
		case e.Type == "git_push" && e.Status == "refused":
			sawRefusedPush = true
		case e.Type == "sign" && e.Status == "ok":
			sawSign = true
		case e.Type == "host_command" && e.Fields != nil:
			sawDiscard = true
		}
	}
	if !sawCreate || !sawLogout || !sawPush || !sawRefusedPush || !sawSign || !sawDiscard {
		t.Fatalf("audit trail incomplete: create=%v logout=%v push=%v refusedPush=%v sign=%v discard=%v", sawCreate, sawLogout, sawPush, sawRefusedPush, sawSign, sawDiscard)
	}
}
