//go:build linux

package jail

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/gitwt"
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
	prov                                       *gitwt.Provisioner
	events                                     []Event
	mu                                         sync.Mutex
}

// provision makes a dispatch's checkout the way the daemon does: its own
// clone (gitwt), borrowing the shared base clone's objects.
func (f *fixture) provision(t *testing.T, id string, pr int, branch string) string {
	t.Helper()
	_, ws, err := f.prov.ProvisionWorktree(context.Background(), dispatch.Request{
		DispatchID: id,
		Trigger: core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/app", PR: pr, Number: pr, BaseRef: "main"},
			Context: map[string]any{"head_ref": branch}},
	})
	if err != nil {
		t.Fatalf("provision %s: %v", id, err)
	}
	return ws
}

// newFixture builds: a fake operator home (with a "secret" ~/.ssh, a signing
// key, ~/.config/gh/hosts.yml, ~/.claude), a bare remote (filtering on, so
// the base clone is a blob:none partial clone) with main, fix/42 and fix/43
// whose history holds a blob only the remote has (OLD.txt), the dispatch
// checkout for #42 provisioned by gitwt, and a fake `gh` on a host PATH dir.
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
	run(t, f.remote, nil, "git", "config", "uploadpack.allowFilter", "true")
	run(t, f.remote, nil, "git", "config", "uploadpack.allowAnySHA1InWant", "true")
	seed := filepath.Join(root, "seed")
	run(t, root, nil, "git", "init", "-q", seed)
	os.WriteFile(seed+"/OLD.txt", []byte("ancient history\n"), 0o644)
	run(t, seed, nil, "git", "add", ".")
	run(t, seed, nil, "git", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "old")
	os.Remove(seed + "/OLD.txt")
	os.WriteFile(seed+"/README", []byte("hello\n"), 0o644)
	run(t, seed, nil, "git", "add", "-A", ".")
	run(t, seed, nil, "git", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")
	run(t, seed, nil, "git", "push", "-q", f.remote, "HEAD:refs/heads/main", "HEAD:refs/heads/fix/42",
		"HEAD:refs/pull/42/head", "HEAD:refs/heads/fix/43", "HEAD:refs/pull/43/head")
	f.prov = gitwt.New(filepath.Join(root, "state"))
	f.prov.RemoteURL = func(string) string { return "file://" + f.remote }
	if ws := f.provision(t, "d1", 42, "fix/42"); ws != f.ws {
		t.Fatalf("checkout at %s, want %s", ws, f.ws)
	}

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
	cmd, done := f.startJailed(t, m, "d1", f.ws, 42, "fix/42", script)
	defer done()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// startJailed prepares a jailed launch of script for one dispatch and
// returns the (unstarted) command and its teardown.
func (f *fixture) startJailed(t *testing.T, m *Manager, id, ws string, pr int, branch, script string) (*exec.Cmd, func()) {
	t.Helper()
	l, err := m.Prepare(context.Background(), LaunchSpec{
		DispatchID: id, Tool: "claude-code", Workspace: ws, Repo: "acme/app", Number: pr, IsPR: true,
		HeadBranch: branch, Label: "fix acme/app#" + itoa(pr),
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
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
	argv, outEnv, cleanup, err := sandbox.WrapLocalCommand(spec, []string{"/bin/sh", "-c", script}, ws, env, deps)
	if err != nil {
		l.Close()
		t.Fatalf("wrap: %v", err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = ws
	cmd.Env = outEnv
	return cmd, func() { cleanup(); l.Close() }
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
git show origin/main~1:OLD.txt; echo "lazy-exit=$?"
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
	// A lazy blob fetch (the base is blob:none) is brokered into the base's
	// store and read through the clone's alternates.
	if !strings.Contains(out, "ancient history") || !strings.Contains(out, "lazy-exit=0") {
		t.Error("a lazy blob fetch must work from inside the jail")
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

// Two jailed dispatches on the same repository, both live at once: A attacks
// everything B depends on — B's branch (by path and through the remote), the
// base clone's packed-refs and object store, and a gc of what it can reach —
// and every attempt fails or stays inside A's own clone. B then pushes and
// lazily fetches a blob the partial base never had.
func TestJailConcurrentDispatchesCannotTouchEachOther(t *testing.T) {
	needJailKernel(t)
	f := newFixture(t)
	m := f.manager(t)
	wsB := f.provision(t, "d2", 43, "fix/43")
	baseGit := filepath.Join(f.base, ".git")
	packedBefore, _ := os.ReadFile(filepath.Join(baseGit, "packed-refs"))
	objsBefore := run(t, f.base, nil, "git", "count-objects", "-v")
	// A private repository elsewhere on the machine (never visible in a
	// jail), with a blob whose id the attacker knows.
	private := filepath.Join(f.home, "private")
	run(t, "", nil, "git", "init", "-q", private)
	os.WriteFile(filepath.Join(private, "packed.txt"), []byte("TOP-SECRET-PACKED\n"), 0o600)
	run(t, private, nil, "git", "add", "packed.txt")
	run(t, private, nil, "git", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "private")
	run(t, private, nil, "git", "gc", "-q")
	packed := run(t, private, nil, "git", "rev-parse", "HEAD:packed.txt")
	os.WriteFile(filepath.Join(private, "loose.txt"), []byte("TOP-SECRET-LOOSE\n"), 0o600)
	loose := run(t, private, nil, "git", "hash-object", "-w", "loose.txt")
	privObjs := filepath.Join(private, ".git", "objects")
	privPack := strings.TrimSuffix(run(t, filepath.Join(privObjs, "pack"), nil, "sh", "-c", "ls pack-*.pack"), ".pack")

	// B's jail comes up first and waits, live, until A is done.
	cmdB, doneB := f.startJailed(t, m, "d2", wsB, 43, "fix/43", `read go
echo b > b.txt && git add b.txt && git commit -q -m "b work" && echo b-committed
git push -q origin HEAD:refs/heads/fix/43; echo "b-push-exit=$?"
git show origin/main~1:OLD.txt; echo "b-lazy-exit=$?"
git rev-parse --verify -q refs/heads/fix/43 >/dev/null && echo b-branch-intact
`)
	defer doneB()
	stdinB, err := cmdB.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var outB strings.Builder
	cmdB.Stdout, cmdB.Stderr = &outB, &outB
	if err := cmdB.Start(); err != nil {
		t.Fatal(err)
	}

	outA, err := f.runJailed(t, m, `
# Steer conductor's git through the clone's own config: keys only a git
# run on the host with this config would honor (the in-jail git's own
# signing and transport are fixed by conductor's GIT_CONFIG_* env and its
# remote helper) — a signing program, and the receive/upload-pack command a
# push or fetch to the file:// origin runs.
printf '#!/bin/sh\ntouch "`+f.ws+`/PWNED-sign"\nexit 1\n' > evil-sign.sh && chmod +x evil-sign.sh
git config gpg.ssh.program "`+f.ws+`/evil-sign.sh"
printf '#!/bin/sh\ntouch "`+f.ws+`/PWNED-pack"\nexec git-receive-pack "$@"\n' > evil-rp.sh && chmod +x evil-rp.sh
printf '#!/bin/sh\ntouch "`+f.ws+`/PWNED-pack"\nexec git-upload-pack "$@"\n' > evil-up.sh && chmod +x evil-up.sh
git config remote.origin.receivepack "`+f.ws+`/evil-rp.sh"
git config remote.origin.uploadpack "`+f.ws+`/evil-up.sh"
echo a > a.txt && git add a.txt && git commit -q -m "a work" && echo a-committed
git push -q origin HEAD:refs/heads/fix/42; echo "a-own-push-exit=$?"
test -e "`+wsB+`" && echo A-SEES-B
git -C "`+wsB+`" branch -D fix/43 >/dev/null 2>&1; echo "a-branch-other-exit=$?"
git push -q origin :refs/heads/fix/43; echo "a-delete-b-exit=$?"
git push -q origin HEAD:refs/heads/fix/43; echo "a-move-b-exit=$?"
echo junk > "`+baseGit+`/packed-refs"; echo "a-packed-refs-exit=$?"
test -e "`+baseGit+`/config" && echo A-SEES-BASE-CONFIG
touch "`+baseGit+`/objects/planted"; echo "a-objects-exit=$?"
p=$(ls "`+baseGit+`"/objects/pack/*.pack | head -1); rm -f "$p"; echo "a-rm-pack-exit=$?"
git update-ref -d refs/remotes/origin/fix/43; git branch -D fix/42 >/dev/null 2>&1
git gc -q --prune=now; echo "a-gc-exit=$?"
# Point conductor's git at the private repo — through the clone's
# alternates, a symlinked loose object and a symlinked pack (all resolve on
# the host, not in the jail) — then push a commit whose tree names both
# private blobs.
echo "`+privObjs+`" >> .git/objects/info/alternates; echo "a-alt-write-exit=$?"
mkdir -p .git/objects/`+loose[:2]+` && ln -s "`+privObjs+`/`+loose[:2]+`/`+loose[2:]+`" .git/objects/`+loose[:2]+`/`+loose[2:]+`
ln -s "`+privObjs+`/pack/`+privPack+`.pack" .git/objects/pack/`+privPack+`.pack
ln -s "`+privObjs+`/pack/`+privPack+`.idx" .git/objects/pack/`+privPack+`.idx
t=$(printf '100644 blob %s\tpacked.txt\n100644 blob %s\tloose.txt\n' `+packed+` `+loose+` | git mktree --missing)
c=$(git commit-tree "$t" -p HEAD -m leak) && echo a-leak-commit
git push -q origin "$c:refs/heads/fix/42"; echo "a-leak-push-exit=$?"
`)
	t.Logf("A:\n%s", outA)
	if err != nil {
		t.Fatalf("A: %v", err)
	}
	stdinB.Write([]byte("go\n"))
	stdinB.Close()
	errB := cmdB.Wait()
	t.Logf("B:\n%s", outB.String())
	if errB != nil {
		t.Fatalf("B: %v", errB)
	}

	for _, bad := range []string{"A-SEES-B", "A-SEES-BASE-CONFIG", "a-branch-other-exit=0", "a-delete-b-exit=0",
		"a-move-b-exit=0", "a-packed-refs-exit=0", "a-objects-exit=0", "a-rm-pack-exit=0", "a-leak-push-exit=0"} {
		if strings.Contains(outA, bad) {
			t.Errorf("A: %s", bad)
		}
	}
	for _, p := range []string{"PWNED-sign", "PWNED-pack"} {
		if _, err := os.Stat(filepath.Join(f.ws, p)); err == nil {
			t.Errorf("conductor ran a program the agent named in its clone's config (%s)", p)
		}
	}
	if head := run(t, f.remote, nil, "git", "log", "-1", "--format=%s", "refs/heads/fix/42"); head != "a work" {
		t.Errorf("A's own push must land on the real remote, signed with the operator's key: head %q", head)
	}
	for _, want := range []string{"a-committed", "a-own-push-exit=0", "a-gc-exit=0", "a-alt-write-exit=0", "a-leak-commit"} {
		if !strings.Contains(outA, want) {
			t.Errorf("A: missing %q (the attack did not get as far as the push)", want)
		}
	}
	for _, secret := range []string{packed, loose} {
		if err := exec.Command("git", "--git-dir="+f.remote, "cat-file", "-e", secret).Run(); err == nil {
			t.Fatalf("private blob %s reached the remote: conductor's git followed the agent's alternates/symlinks", secret)
		}
	}
	for _, want := range []string{"b-committed", "b-push-exit=0", "ancient history", "b-lazy-exit=0", "b-branch-intact"} {
		if !strings.Contains(outB.String(), want) {
			t.Errorf("B: missing %q", want)
		}
	}
	// On the host: the base is exactly as it was (bar the lazily fetched
	// blob), B's clone is whole, and B's push landed.
	if after, _ := os.ReadFile(filepath.Join(baseGit, "packed-refs")); string(after) != string(packedBefore) {
		t.Error("the base clone's packed-refs changed")
	}
	if _, err := os.Stat(filepath.Join(baseGit, "objects", "planted")); err == nil {
		t.Error("A wrote into the base's object store")
	}
	t.Logf("base objects before:\n%s\nafter:\n%s", objsBefore, run(t, f.base, nil, "git", "count-objects", "-v"))
	run(t, f.base, nil, "git", "fsck", "--connectivity-only", "--no-dangling")
	run(t, wsB, nil, "git", "fsck", "--connectivity-only", "--no-dangling")
	if head := run(t, f.remote, nil, "git", "log", "-1", "--format=%s", "refs/heads/fix/43"); head != "b work" {
		t.Fatalf("remote fix/43 head: %q", head)
	}
	run(t, f.remote, nil, "git", "-c", "gpg.ssh.allowedSignersFile="+f.allowed, "verify-commit", "refs/heads/fix/43")
}

// A content-executing host command (a terraform plan runs the workspace's
// providers and data sources): refused by default; allowed by the operator's
// config, it runs in the host-side jail — its own config readable, the rest
// of the home and the operator's session absent, the workspace read-only
// (its writes kept for the dispatch's next run, never in the workspace), no
// stdin, and no network but the tool's endpoints through the egress proxy.
func TestJailContentExecutingHostCommandRunsConfined(t *testing.T) {
	needJailKernel(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl missing")
	}
	f := newFixture(t)
	m := f.manager(t)
	os.MkdirAll(f.home+"/.terraform.d", 0o700)
	os.WriteFile(f.home+"/.terraform.d/credentials.tfrc.json", []byte("{}\n"), 0o600)
	// What an `external` data source would do, as the "provider" runs it.
	tf := "#!/bin/sh\necho \"tf-args=$*\"\n" +
		"[ -r \"$HOME/.terraform.d/credentials.tfrc.json\" ] && echo tf-sees-own-config\n" +
		"cat \"$HOME/.ssh/id_secret\" 2>/dev/null && echo LEAK-home-ssh\n" +
		"cat \"$HOME/.config/gh/hosts.yml\" 2>/dev/null && echo LEAK-other-tool-config\n" +
		"ls /run/user/$(id -u) >/dev/null 2>&1 && echo LEAK-session\n" +
		"test -e \"" + f.root + "/state/checkouts\" && echo LEAK-state\n" +
		"test -e ./earlier.txt && echo sees-earlier-write\n" +
		"echo planted > ./earlier.txt && echo ws-write-ok\n" +
		"curl -s -m 5 -o /dev/null -w 'proxy-code=%{http_connect}\\n' https://example.com; echo \"proxy-rc=$?\"\n" +
		"curl -s -m 5 --noproxy '*' -o /dev/null https://example.com; echo \"direct-rc=$?\"\n" +
		"if read -r line; then echo \"STDIN=$line\"; else echo no-stdin; fi\n"
	// Installed the way version managers do: a symlink on PATH into a
	// versions dir (only the resolved install is a BinRoot; the confined
	// root must recreate the hop).
	versions := filepath.Join(f.root, "tf-versions")
	os.MkdirAll(versions, 0o755)
	os.WriteFile(filepath.Join(versions, "terraform_9.9.9"), []byte(tf), 0o755)
	os.Symlink(filepath.Join(versions, "terraform_9.9.9"), f.bin+"/terraform")
	look := m.LookPath
	m.LookPath = func(n string) (string, error) {
		if n == "terraform" {
			return f.bin + "/terraform", nil
		}
		return look(n)
	}
	var denied []string
	pm := sandbox.NewProxyManager(func(_, hostport string) { denied = append(denied, hostport) })
	defer pm.Close()
	m.HostEgress = pm.UnixEndpointLabeled

	run := func(layers []*config.IsolationConfig, script string) string {
		l, err := m.Prepare(context.Background(), LaunchSpec{
			DispatchID: "d1", Tool: "claude-code", Workspace: f.ws, Repo: "acme/app", Number: 42, IsPR: true,
			HeadBranch: "fix/42", Label: "fix acme/app#42", Layers: layers,
		})
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		defer l.Close()
		spec := sandbox.FromConfig(&config.IsolationConfig{Mode: "namespace"})
		deps := sandbox.LocalWrapDeps{SelfExe: m.SelfExe, Confine: true, ExtraBinds: l.Binds}
		argv, env, cleanup, err := sandbox.WrapLocalCommand(spec, []string{"/bin/sh", "-c", script}, f.ws, append(sandbox.MinimalEnv(), l.Env...), deps)
		if err != nil {
			t.Fatalf("wrap: %v", err)
		}
		defer cleanup()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir, cmd.Env = f.ws, env
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	// Refused by default, with the reason and the knob.
	out := run(nil, `terraform plan; echo "exit=$?"`)
	t.Logf("default:\n%s", out)
	if !strings.Contains(out, "exit=126") || !strings.Contains(out, `isolation.host.terraform.allow: ["plan *"]`) {
		t.Fatalf("terraform plan must be refused by default, naming the knob:\n%s", out)
	}
	if strings.Contains(out, "tf-args=") {
		t.Fatal("the refused command ran")
	}
	// Allowed by the operator: runs confined, twice (the second sees the
	// first's write — the dispatch's layer — while the workspace never does).
	allow := []*config.IsolationConfig{{Host: map[string]*config.HostCommand{"terraform": {Allow: []string{"plan *"}}}}}
	out = run(allow, `echo from-the-agent | terraform plan -out=x -; echo "exit1=$?"
test -e earlier.txt && echo WS-CHANGED || echo ws-untouched
terraform -chdir=. plan; echo "exit2=$?"`)
	t.Logf("allowed:\n%s", out)
	for _, want := range []string{"tf-args=plan -out=x", "tf-sees-own-config", "ws-write-ok", "ws-untouched",
		"sees-earlier-write", "no-stdin", "proxy-code=403", "exit1=0", "exit2=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"LEAK-", "WS-CHANGED", "STDIN=", "direct-rc=0", "proxy-rc=0"} {
		if strings.Contains(out, bad) {
			t.Errorf("unexpected %q", bad)
		}
	}
	if _, err := os.Stat(filepath.Join(f.ws, "earlier.txt")); err == nil {
		t.Error("the confined run wrote into the real workspace")
	}
	if len(denied) == 0 || !strings.Contains(strings.Join(denied, " "), "example.com") {
		t.Errorf("the proxy must have refused example.com: %v", denied)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var sawConfined bool
	for _, e := range f.events {
		if e.Type == "host_command" && e.Fields != nil && e.Fields["confined"] == true {
			sawConfined = true
		}
	}
	if !sawConfined {
		t.Error("the confined run must be audited as confined")
	}
}

// TestJailLiveTerraformExternalDataSource (CONDUCTOR_LIVE_TERRAFORM=1): the
// real terraform, run by a jailed agent shell through the shim, broker and
// host-side jail, with the real egress proxy and network: init downloads the
// external provider from the registry (the tool's endpoints), and the plan
// runs an external data source whose program tries to read ~/.ssh and reach
// an outside host — both fail while the plan runs.
func TestJailLiveTerraformExternalDataSource(t *testing.T) {
	if os.Getenv("CONDUCTOR_LIVE_TERRAFORM") == "" {
		t.Skip("live: CONDUCTOR_LIVE_TERRAFORM=1")
	}
	needJailKernel(t)
	tfBin, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform missing")
	}
	f := newFixture(t)
	m := f.manager(t)
	for _, n := range []string{"main.tf", "probe.sh"} {
		b, _ := os.ReadFile(filepath.Join("testdata", "tfprobe", n))
		os.WriteFile(filepath.Join(f.ws, n), b, 0o755)
	}
	look := m.LookPath
	m.LookPath = func(n string) (string, error) {
		if n == "terraform" {
			return tfBin, nil
		}
		return look(n)
	}
	pm := sandbox.NewProxyManager(nil)
	defer pm.Close()
	m.HostEgress = pm.UnixEndpointLabeled
	allow := []*config.IsolationConfig{{Host: map[string]*config.HostCommand{"terraform": {Allow: []string{"init *", "plan *"}}}}}
	l, err := m.Prepare(context.Background(), LaunchSpec{
		DispatchID: "d1", Tool: "claude-code", Workspace: f.ws, Repo: "acme/app", Number: 42, IsPR: true,
		HeadBranch: "fix/42", Label: "fix acme/app#42", Layers: allow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	spec := sandbox.FromConfig(&config.IsolationConfig{Mode: "namespace"})
	deps := sandbox.LocalWrapDeps{SelfExe: m.SelfExe, Confine: true, ExtraBinds: l.Binds}
	script := `terraform apply -auto-approve -no-color >/dev/null 2>&1; echo "apply-exit=$?"
terraform init -input=false -no-color | tail -3; echo "init-exit=$?"
terraform plan -input=false -no-color; echo "plan-exit=$?"
test -e written-by-probe.txt && echo WS-CHANGED || echo ws-untouched
test -e .terraform && echo WS-HAS-DOT-TERRAFORM || echo ws-has-no-dot-terraform`
	argv, env, cleanup, err := sandbox.WrapLocalCommand(spec, []string{"/bin/sh", "-c", script}, f.ws, append(sandbox.MinimalEnv(), l.Env...), deps)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = f.ws, env
	out, _ := cmd.CombinedOutput()
	t.Logf("jailed agent shell:\n%s", out)
	s := string(out)
	for _, want := range []string{"apply-exit=126", "init-exit=0", "plan-exit=0", "ws-untouched", "ws-has-no-dot-terraform"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	out1 := func(k string) string { // a plan output value: + "k" = "v"
		m := regexp.MustCompile(`"?` + k + `"?\s*=\s*"([^"]*)"`).FindStringSubmatch(s)
		if m == nil {
			return ""
		}
		return m[1]
	}
	if out1("ssh") != "blocked" || out1("via_proxy") != "403" || out1("workspace") != "written-copy-on-write" {
		t.Errorf("probe: ssh=%q via_proxy=%q workspace=%q", out1("ssh"), out1("via_proxy"), out1("workspace"))
	}
	if rc := out1("direct_rc"); rc == "" || rc == "0" {
		t.Errorf("the external program reached the network directly (rc %q)", rc)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e.Type == "host_command" || e.Type == "egress" {
			t.Logf("event: %s %s %s %q %v", e.Type, e.Status, e.Detail, e.Reason, e.Fields)
		}
	}
}
