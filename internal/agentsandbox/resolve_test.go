package agentsandbox

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/skill"
)

func TestIdentityKey(t *testing.T) {
	if k := IdentityKey("fixer"); k != "fixer" {
		t.Fatalf("a clean name is its own key: %q", k)
	}
	a, b := IdentityKey("agent:fixer@acme/app"), IdentityKey("agent:fixer@acme_app")
	if a == b || strings.ContainsAny(a, "/:@") {
		t.Fatalf("cleaned names never collide or escape: %q %q", a, b)
	}
	for _, n := range []string{"..", ".", "/", "", "../../etc", strings.Repeat("x", 200)} {
		k := IdentityKey(n)
		if k == "" || k == "." || k == ".." || strings.Contains(k, "/") || len(k) > 64 {
			t.Errorf("IdentityKey(%q) = %q", n, k)
		}
	}
}

func realHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	os.MkdirAll(filepath.Join(h, ".claude"), 0o700)
	os.WriteFile(filepath.Join(h, ".claude.json"), []byte(`{"userID":"u","projects":{"/secret/proj":{"history":["x"]}}}`), 0o600)
	os.WriteFile(filepath.Join(h, ".claude", "settings.json"), []byte(`{"model":"opus"}`), 0o600)
	os.WriteFile(filepath.Join(h, ".claude", ".credentials.json"), []byte(`{"login":1}`), 0o600)
	os.MkdirAll(filepath.Join(h, ".claude", "projects", "other"), 0o700)
	os.WriteFile(filepath.Join(h, ".claude", "projects", "other", "t.jsonl"), []byte("transcript"), 0o600)
	return h
}

func TestPrepareHomeSeedsOnceWithoutHistory(t *testing.T) {
	real := realHome(t)
	root := t.TempDir()
	spec := HomeSpec{Root: root, Key: "agent:fixer@acme/app", Tool: "claude-code", RealHome: real, GitName: `Op "Q" Erator`, GitEmail: "op@example.test", GOOS: "linux"}
	home, logins, err := PrepareHome(spec)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(home) != root || filepath.Base(home) != IdentityKey(spec.Key) {
		t.Fatalf("home %s", home)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".claude.json"))
	var m map[string]any
	json.Unmarshal(b, &m)
	if m["userID"] != "u" || m["projects"] != nil {
		t.Fatalf("seeded account state minus every project's history: %s", b)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); string(b) != `{"model":"opus"}` {
		t.Fatalf("settings seeded: %s", b)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects", "other")); err == nil {
		t.Fatal("another project's transcripts are never copied")
	}
	if fi, err := os.Stat(filepath.Join(home, ".config", "claude")); err != nil || !fi.IsDir() {
		t.Fatal("every state path exists before the launch (a missing bind source loses the agent's writes)")
	}
	if len(logins) != 1 || logins[0].Src != filepath.Join(real, ".claude", ".credentials.json") || logins[0].Path != filepath.Join(home, ".claude", ".credentials.json") {
		t.Fatalf("logins: %+v", logins)
	}
	if fi, err := os.Lstat(logins[0].Path); err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
		t.Fatal("linux: an empty placeholder the login is bound over (never a copy of it)")
	}
	gc, _ := os.ReadFile(filepath.Join(home, ".gitconfig"))
	if !strings.Contains(string(gc), `name = "Op \"Q\" Erator"`) || !strings.Contains(string(gc), "gpgsign = false") {
		t.Fatalf("gitconfig: %s", gc)
	}
	// Seeded once: the agent's own changes survive the next launch.
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"model":"mine"}`), 0o600)
	if _, _, err := PrepareHome(spec); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); string(b) != `{"model":"mine"}` {
		t.Fatal("never reseeded over the identity's own state")
	}
	// darwin shares the login by symlink (Seatbelt cannot mount over a path).
	spec.Key, spec.GOOS = "mac", "darwin"
	h2, l2, err := PrepareHome(spec)
	if err != nil {
		t.Fatal(err)
	}
	if dst, err := os.Readlink(filepath.Join(h2, ".claude", ".credentials.json")); err != nil || dst != l2[0].Src {
		t.Fatalf("darwin login link: %q %v", dst, err)
	}
	if _, _, err := PrepareHome(HomeSpec{Key: "x"}); err == nil {
		t.Fatal("no root, no home")
	}
}

func TestPrepareHomeReplacesAPlantedGitconfigSymlink(t *testing.T) {
	root := t.TempDir()
	home, _, err := PrepareHome(HomeSpec{Root: root, Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, []byte("keep"), 0o600)
	gc := filepath.Join(home, ".gitconfig")
	os.Remove(gc)
	os.Symlink(victim, gc)
	if _, _, err := PrepareHome(HomeSpec{Root: root, Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Fatal("conductor never writes through a symlink the agent left in its home")
	}
}

func TestResolverDefaultPolicy(t *testing.T) {
	real := realHome(t)
	state := t.TempDir()
	ws := t.TempDir()
	var minted []skill.Identity
	r := &Resolver{
		Registry: &Registry{}, StateRoot: filepath.Join(state, "agent-state"), RealHome: real,
		Protected: []string{state}, Egress: []string{"registry.npmjs.org"}, Socket: "/s/memory.sock",
		Mint: func(id skill.Identity) (string, error) { minted = append(minted, id); return "fresh", nil },
		GOOS: "linux",
	}
	p, err := r.PolicyFor(ws, "claude", []string{"ANTHROPIC_BASE_URL=http://127.0.0.1:3456"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Identity != "dir:"+canon(ws) || p.Workspace != canon(ws) || p.Tool != "claude-code" {
		t.Fatalf("an unknown directory is its own identity: %+v", p)
	}
	if !contains(p.Egress, "api.anthropic.com") || !contains(p.Egress, "registry.npmjs.org") || len(p.Loopback) != 1 {
		t.Fatalf("model endpoints + sandbox.egress, the loopback router relayed: %+v", p)
	}
	if p.Token != "fresh" || p.Socket != "/s/memory.sock" || len(minted) != 1 || len(minted[0].Policy.Verbs) != 0 {
		t.Fatalf("a fresh token that grants no verb: %+v %+v", p, minted)
	}
	if _, err := r.PolicyFor(ws, "claude", nil, false); err != nil || len(minted) != 2 {
		t.Fatal("every launch mints its own token")
	}
	if !within(p.Home, r.StateRoot) {
		t.Fatalf("home %s outside %s", p.Home, r.StateRoot)
	}
	for _, bad := range []string{"/", real, filepath.Dir(real), state, filepath.Join(state, "worktrees", "x"), "relative"} {
		os.MkdirAll(filepath.Join(state, "worktrees", "x"), 0o700)
		if _, err := r.PolicyFor(bad, "claude", nil, false); err == nil {
			t.Errorf("default workspace %q must be refused", bad)
		}
	}
	// A launcher's utility process with no agent workspace (paseo listing a
	// provider's models from its own cwd): the tool's empty scratch
	// workspace — never the refused directory itself.
	for _, bad := range []string{"/", real} {
		p, err := r.PolicyFor(bad, "codex", nil, true)
		if err != nil || p.Identity != "scratch:codex" || !within(p.Workspace, r.StateRoot) || p.Workspace == canon(bad) {
			t.Fatalf("scratch for %s: %+v %v", bad, p, err)
		}
	}
	if p, _ := r.PolicyFor(ws, "codex", nil, true); p.Identity != "dir:"+canon(ws) {
		t.Fatalf("an acceptable directory is still its own workspace: %+v", p)
	}
	// No socket or no mint: no token, still a policy.
	r.Socket = ""
	if p, err := r.PolicyFor(ws, "codex", nil, false); err != nil || p.Token != "" || p.Socket != "" {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestResolverRegisteredWorkspace(t *testing.T) {
	reg := &Registry{}
	ws := t.TempDir()
	sub := filepath.Join(ws, "pkg")
	os.MkdirAll(sub, 0o700)
	id := skill.Identity{Agent: "fixer", Dispatch: "d-1"}
	unreg := reg.Register(Entry{Workspace: ws, Identity: "agent:fixer@acme/app", Tool: "codex", Dispatch: "d-1", Skill: &id,
		Egress: []string{"proxy.golang.org"}, ReadOnly: []string{"/base/objects"}, Env: []string{"GIT_AUTHOR_NAME=Op"}})
	r := &Resolver{Registry: reg, StateRoot: t.TempDir(), Socket: "/s", Mint: func(i skill.Identity) (string, error) {
		if i.Dispatch != "d-1" {
			t.Fatalf("the token stands for the workspace's own dispatch: %+v", i)
		}
		return "tok", nil
	}}
	p, err := r.PolicyFor(sub, "claude", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if p.Identity != "agent:fixer@acme/app" || p.Tool != "codex" || p.Workspace != canon(ws) || p.Cwd != canon(sub) ||
		!contains(p.Egress, "proxy.golang.org") || !contains(p.Egress, "api.openai.com") || p.ReadOnly[0] != "/base/objects" || p.Token != "tok" {
		t.Fatalf("the registered identity wins: %+v", p)
	}
	if e, ok := reg.ForDispatch("d-1"); !ok || e.Identity != p.Identity {
		t.Fatal("ForDispatch")
	}
	unreg()
	if _, ok := reg.Lookup(sub); ok {
		t.Fatal("unregistered")
	}
	if _, err := r.PolicyForEntry(Entry{Workspace: ws}, "claude", nil); err == nil {
		t.Fatal("an entry with no identity is refused")
	}
}

func TestRegistryDeepestAndForget(t *testing.T) {
	reg := &Registry{}
	outer := t.TempDir()
	inner := filepath.Join(outer, "inner")
	os.MkdirAll(inner, 0o700)
	reg.Register(Entry{Workspace: outer, Identity: "outer"})
	reg.Register(Entry{Workspace: inner, Identity: "inner"})
	if e, _ := reg.Lookup(filepath.Join(inner, "x")); e.Identity != "inner" {
		t.Fatal("the deepest workspace wins")
	}
	if e, _ := reg.Lookup(outer); e.Identity != "outer" {
		t.Fatal("outer")
	}
	if _, ok := reg.Lookup(outer + "-sibling"); ok {
		t.Fatal("a sibling path with the same prefix is not inside")
	}
	reg.Forget(inner)
	if e, _ := reg.Lookup(inner); e.Identity != "outer" {
		t.Fatal("forgotten")
	}
	if _, ok := reg.ForDispatch(""); ok {
		t.Fatal("no dispatch, no entry")
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@x")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestResolveGitLayout(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	git(t, root, "init", "-q", "-b", "main", base)
	git(t, base, "commit", "-q", "--allow-empty", "-m", "x")
	// A per-dispatch clone borrowing the base's objects.
	clone := filepath.Join(root, "clone")
	git(t, root, "init", "-q", clone)
	os.MkdirAll(filepath.Join(clone, ".git", "objects", "info"), 0o755)
	os.WriteFile(filepath.Join(clone, ".git", "objects", "info", "alternates"), []byte(filepath.Join(base, ".git", "objects")+"\n"), 0o644)
	l, err := ResolveGitLayout(clone)
	if err != nil || !l.Clone || len(l.ReadOnly) != 1 || l.ReadOnly[0] != filepath.Join(base, ".git", "objects") || l.GitDir != filepath.Join(base, ".git") || len(l.ReadWrite) != 0 {
		t.Fatalf("clone: %+v %v", l, err)
	}
	// A linked worktree of the base.
	wt := filepath.Join(root, "wt")
	git(t, base, "worktree", "add", "-q", "-b", "fix", wt)
	l, err = ResolveGitLayout(wt)
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(base, ".git")
	if l.GitDir != common || !contains(l.ReadWrite, common) || !contains(l.ReadWrite, filepath.Join(common, "worktrees", "wt")) ||
		!contains(l.ReadOnly, filepath.Join(common, "config")) || !contains(l.ReadOnly, filepath.Join(common, "hooks")) ||
		!contains(l.ReadOnly, filepath.Join(common, "objects", "info")) || !contains(l.Hide, filepath.Join(common, "worktrees")) {
		t.Fatalf("linked worktree: %+v", l)
	}
	// Not a checkout: nothing.
	if l, err := ResolveGitLayout(t.TempDir()); err != nil || len(l.ReadOnly)+len(l.ReadWrite) != 0 {
		t.Fatalf("%+v %v", l, err)
	}
	// An alternate that is not a directory is refused.
	os.WriteFile(filepath.Join(clone, ".git", "objects", "info", "alternates"), []byte("/nonexistent/objects\n"), 0o644)
	if _, err := ResolveGitLayout(clone); err == nil {
		t.Fatal("a missing alternate is refused")
	}
}

func TestRelayPipesOnlyToLoopback(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b := make([]byte, 4)
		io.ReadFull(c, b)
		c.Write(append([]byte("echo:"), b...))
	}()
	rel, err := NewRelayer()
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Close()
	if len(rel.dir) > 40 {
		t.Fatalf("relay sockets live under a short dir (macOS sun_path): %s", rel.dir)
	}
	sock, closeFn, err := rel.Relay(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("ping"))
	got, _ := io.ReadAll(c)
	if string(got) != "echo:ping" {
		t.Fatalf("relayed %q", got)
	}
	if _, _, err := rel.Relay("10.1.2.3:443"); err == nil {
		t.Fatal("only loopback targets are relayed")
	}
}

func TestLaunchBindsShowAnInstallNotTheHome(t *testing.T) {
	withPlatform(t, "linux")
	home := t.TempDir()
	// The native installer's shape: ~/.local/bin/claude → ~/.local/share/claude/versions/1/claude
	ver := filepath.Join(home, ".local", "share", "claude", "versions", "1")
	os.MkdirAll(ver, 0o755)
	os.WriteFile(filepath.Join(ver, "claude"), []byte("\x7fELF"), 0o755)
	bin := filepath.Join(home, ".local", "bin")
	os.MkdirAll(bin, 0o755)
	os.Symlink(filepath.Join(ver, "claude"), filepath.Join(bin, "claude"))
	// A binary directly in the home dir: shown alone, never its dir.
	os.WriteFile(filepath.Join(home, "tool"), []byte("\x7fELF"), 0o755)
	bs := launchBinds(filepath.Join(bin, "claude"), exectLook, home)
	if !hasBind(bs, sandbox.BindMount{Path: ver, RO: true, Optional: true}) || !hasBind(bs, sandbox.BindMount{Path: filepath.Join(bin, "claude"), Link: filepath.Join(ver, "claude")}) {
		t.Fatalf("install + recreated symlink: %+v", bs)
	}
	for _, b := range launchBinds(filepath.Join(home, "tool"), exectLook, home) {
		if b.Path == home {
			t.Fatalf("the home is never bound whole: %+v", b)
		}
	}
	if bs := launchBinds("/usr/bin/env", exectLook, home); len(bs) != 0 {
		t.Fatalf("a base-system binary needs nothing: %+v", bs)
	}
}

func exectLook(b string) (string, error) { return exec.LookPath(b) }
