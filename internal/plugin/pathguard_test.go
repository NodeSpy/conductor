//go:build linux || darwin

package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
