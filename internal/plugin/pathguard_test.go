//go:build linux || darwin

package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/sandbox"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// guardedPATH leaves out a relative or empty entry and every directory that
// lies inside, or holds, a path a plugin may write; the rest keep their
// order.
func TestGuardedPATH(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := func(p string) string { return filepath.Join(dir, p) }
	path := strings.Join([]string{"/usr/bin", "rel/bin", "", d("w/bin"), d("tools"), d("home/bin"), "/bin"}, ":")
	kept, dropped := guardedPATH(path, []string{d("w"), d("home/bin/one"), "relative/write"})
	if kept != "/usr/bin:"+d("tools")+":/bin" {
		t.Fatalf("kept %q", kept)
	}
	if strings.Join(dropped, ",") != d("w/bin")+","+d("home/bin") {
		t.Fatalf("dropped %v", dropped)
	}
}

// A Manager that guards PATH keeps every directory its plugins may write
// off the process's PATH, which conductor runs its own tools from: a tool
// a plugin planted there is not what conductor runs. Recomputed as its
// plugins change; restored when it closes; a PATH set since by anything
// else is the one guarded.
func TestManagerKeepsPluginWritesOffPATH(t *testing.T) {
	home, sd := jailTestDirs(t)
	safe, planted := filepath.Join(home, "safe"), filepath.Join(home, "w", "bin")
	for _, p := range []string{safe, planted} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "ps"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	orig := planted + ":" + safe + ":/usr/bin:/bin"
	t.Setenv("PATH", orig)
	m := NewManager(nil, "", stateAt(t), Deps{Sandbox: sd, GuardPATH: true})
	if got := os.Getenv("PATH"); got != orig {
		t.Fatalf("no plugin writes there yet: PATH %q", got)
	}
	m.specs["b"] = Spec{Name: "b", Manifest: Manifest{Write: []string{"~/w"}}}
	m.order = append(m.order, "b")
	m.publishSpecs()
	if got := os.Getenv("PATH"); got != safe+":/usr/bin:/bin" {
		t.Fatalf("PATH %q, want the plugin-writable dir left out", got)
	}
	if p, err := exec.LookPath("ps"); err != nil || p != filepath.Join(safe, "ps") {
		t.Fatalf("conductor's ps is %q (%v), want %s", p, err, filepath.Join(safe, "ps"))
	}
	// Another Manager's plugins count as long as it runs.
	other := NewManager(nil, "", stateAt(t), Deps{Sandbox: sd, GuardPATH: true})
	other.specs["c"] = Spec{Name: "c", Manifest: Manifest{Write: []string{"~/safe"}}}
	other.order = append(other.order, "c")
	other.publishSpecs()
	if got := os.Getenv("PATH"); got != "/usr/bin:/bin" {
		t.Fatalf("PATH %q, want both Managers' writes left out", got)
	}
	other.Close()
	if got := os.Getenv("PATH"); got != safe+":/usr/bin:/bin" {
		t.Fatalf("PATH %q after the other Manager closed", got)
	}
	// Set since by something else: that PATH is the one guarded.
	t.Setenv("PATH", planted+":/usr/bin")
	m.publishSpecs()
	if got := os.Getenv("PATH"); got != "/usr/bin" {
		t.Fatalf("PATH %q, want the new PATH guarded", got)
	}
	t.Setenv("PATH", orig)
	m.publishSpecs()
	m.Close()
	if got := os.Getenv("PATH"); got != orig {
		t.Fatalf("PATH %q after Close, want %q", got, orig)
	}
}

// A declared command is looked up on CONDUCTOR_COMMAND_PATH (the
// installing shell's order) when set: absolute entries only, executable
// files only; unset, on PATH.
func TestCommandLookPath(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	user, noexec := filepath.Join(dir, "user"), filepath.Join(dir, "noexec")
	for _, p := range []string{user, noexec} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(user, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noexec, "tool"), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv(CommandPathEnv, "user:"+noexec+":"+user+":/usr/bin")
	if p, err := commandLookPath("tool"); err != nil || p != filepath.Join(user, "tool") {
		t.Fatalf("tool is %q (%v), want %s", p, err, filepath.Join(user, "tool"))
	}
	found, err := findCommands(Spec{Name: "p"}, newGrantChecks(SandboxDeps{}, sdk.Grant{}), []string{"tool"})
	if err != nil || found[0].path != filepath.Join(user, "tool") {
		t.Fatalf("a declared command is found on %s: %+v %v", CommandPathEnv, found, err)
	}
	t.Setenv(CommandPathEnv, "")
	if p, err := commandLookPath("tool"); err == nil {
		t.Fatalf("unset, PATH is used: found %s", p)
	}
}

// Ported from the review: a write grant made through a symlinked ancestor
// whose last directory does not exist yet names the same place as the
// resolved path, both on conductor's PATH and in the program checks.
func TestWriteThroughSymlinkedAncestorOfMissingDir(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "srv")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "work")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	pathDir := filepath.Join(real, "out", "bin")
	kept, dropped := guardedPATH(pathDir+":/usr/bin", []string{filepath.Join(link, "out")})
	if kept != "/usr/bin" || len(dropped) != 1 || dropped[0] != pathDir {
		t.Fatalf("kept %q dropped %v: %s is where %s/out is", kept, dropped, pathDir, link)
	}
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pathDir, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDir+":/usr/bin:/bin")
	t.Setenv(CommandPathEnv, "")
	for _, sd := range []SandboxDeps{
		{PeerWritable: func() []string { return []string{filepath.Join(link, "out")} }},
	} {
		if found, err := findCommands(Spec{Name: "p"}, newGrantChecks(sd, sdk.Grant{}), []string{"tool"}); err == nil {
			t.Fatalf("found %+v; want a program inside %s/out refused", found, link)
		}
	}
	if found, err := findCommands(Spec{Name: "p"}, newGrantChecks(SandboxDeps{}, sdk.Grant{Write: []string{filepath.Join(link, "out", "bin", "x", "..")}}), []string{"tool"}); err == nil {
		t.Fatalf("found %+v; want a program inside the plugin's own write refused", found)
	}
}

// What any sandboxed launch was let write counts like a plugin's write: a
// code step confined to fs: [~/go] could plant ~/go/bin/tool, so a
// declared command found there is refused, and ~/go/bin leaves
// conductor's own PATH.
func TestSandboxedLaunchWritesAreRefused(t *testing.T) {
	home, sd := jailTestDirs(t)
	t.Cleanup(sandbox.ResetWriteLedger)
	if err := sandbox.UseWriteLedger(""); err != nil {
		t.Fatal(err)
	}
	gobin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(gobin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gobin, "tool"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv(CommandPathEnv, gobin+":/usr/bin:/bin")
	m := NewManager(nil, "", stateAt(t), Deps{Sandbox: sd, GuardPATH: true})
	defer m.Close()
	t.Setenv("PATH", gobin+":/usr/bin:/bin")
	m.publishSpecs()
	a := Spec{Name: "a", Manifest: Manifest{Commands: []string{"tool"}}}
	if _, err := findTool(t, a, m.deps.Sandbox); err != nil {
		t.Fatalf("before any sandboxed launch may write ~/go: %v", err)
	}
	if _, err := (&sandbox.Spec{Mode: "container", Image: "img", FS: []string{filepath.Join(home, "go")}}).WrapLocal([]string{"true"}, "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if cmds, err := findTool(t, a, m.deps.Sandbox); err == nil || !strings.Contains(err.Error(), "a plugin conductor runs may write") {
		t.Fatalf("commands %+v, err %v; want a program a code step may write refused", cmds, err)
	}
	if got := os.Getenv("PATH"); got != "/usr/bin:/bin" {
		t.Fatalf("PATH %q, want ~/go/bin left out once a sandboxed launch may write it", got)
	}
}

// A probe Manager (one plugin) is given what the rest of the configured
// plugins may write: it checks against them as the daemon does.
func TestProbeSeesConfiguredPeers(t *testing.T) {
	home, sd := jailTestDirs(t)
	r := newBrewRig(t, home)
	sd.PeerWritable = func() []string { return []string{r.prefix} }
	m := NewManager(nil, "", stateAt(t), Deps{Sandbox: sd})
	defer m.Close()
	a := Spec{Name: "a", Manifest: Manifest{Commands: []string{"tool"}}}
	if cmds, err := findTool(t, a, m.deps.Sandbox); err == nil || !strings.Contains(err.Error(), "a plugin conductor runs may write") {
		t.Fatalf("commands %+v, err %v; want the configured peers' writes counted", cmds, err)
	}
}

// A launch that would be let write over what a running jail has bound to
// run — a code step's fs:, another plugin's write, an agent's workspace —
// is refused while that jail runs, whichever launched first; and a jailed
// launch registers what it binds.
func TestWritingOverABoundProgramIsRefused(t *testing.T) {
	sd := testJailDeps(t)
	tool := fakeTool(t, "go/bin/tool")
	gohome := filepath.Dir(filepath.Dir(tool))
	t.Setenv(CommandPathEnv, "")
	ctr := func() error {
		_, err := (&sandbox.Spec{Mode: "container", Image: "img", FS: []string{gohome}}).WrapLocal([]string{"true"}, "", nil, nil)
		return err
	}
	if err := ctr(); err != nil {
		t.Fatalf("nothing runs from there yet: %v", err)
	}
	bin := writeBin(t, t.TempDir(), "b", []byte("x"), 0o755)
	s := Spec{Name: "gh", Kind: KindConnector, BinPath: bin, Manifest: Manifest{Commands: []string{"tool"}}}
	_, cleanup, _, err := buildCommand(s, sd)
	if err != nil {
		t.Fatal(err)
	}
	if err := ctr(); err == nil || !strings.Contains(err.Error(), "plugin gh runs "+tool) {
		t.Fatalf("a code step let write %s while plugin gh runs %s: %v", gohome, tool, err)
	}
	if err := sandbox.AdmitWrites(filepath.Join(gohome, "bin", "tool")); err == nil {
		t.Fatal("the program file itself was admitted")
	}
	if err := sandbox.AdmitWrites(filepath.Join(gohome, "src")); err != nil {
		t.Fatalf("beside it: %v", err)
	}
	cleanup()
	if err := ctr(); err != nil {
		t.Fatalf("once the jail is gone: %v", err)
	}
	// A write inside a directory a jail loads from (a keg it reads) is
	// refused too.
	keg := filepath.Join(gohome, "Cellar", "tool", "1.0")
	unbind := registerBound("gh", []jailCommand{{name: "tool", path: tool, real: tool, reads: []string{keg}}})
	if err := sandbox.AdmitWrites(filepath.Join(keg, "lib", "x")); err == nil {
		t.Fatal("a write inside a keg a jail loads from was admitted")
	}
	unbind()
}

// `plugin show` says what a launch would: a command another configured
// plugin, or a sandboxed launch, may write is refused there too.
func TestHereLinesSeeThePeers(t *testing.T) {
	home, _ := jailTestDirs(t)
	r := newBrewRig(t, home)
	t.Cleanup(func() { HerePeers = nil })
	s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}}}
	if here := strings.Join(HereLines(s), "\n"); !strings.Contains(here, r.real+" (homebrew)") {
		t.Fatalf("alone:\n%s", here)
	}
	HerePeers = func() []string { return []string{r.prefix} }
	if here := strings.Join(HereLines(s), "\n"); !strings.Contains(here, "a plugin conductor runs may write") {
		t.Fatalf("with a peer writing the prefix:\n%s", here)
	}
	HerePeers = nil
	t.Cleanup(sandbox.ResetWriteLedger)
	if err := sandbox.UseWriteLedger(""); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.AdmitWrites(r.lib); err != nil {
		t.Fatal(err)
	}
	if here := strings.Join(HereLines(s), "\n"); !strings.Contains(here, "a plugin conductor runs may write") {
		t.Fatalf("with a sandboxed launch writing a keg:\n%s", here)
	}
}
