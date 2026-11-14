//go:build linux || darwin

package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// Regression tests for the second adversarial review of the plugin jail:
// what a declared path may be before anything is bound.

// A directory of other programs' sockets, or one above it, is refused for
// every kind of grant: a read-only bind does not stop connect(). A file
// inside one (a single socket) is fine.
func TestSocketDirsAreRefused(t *testing.T) {
	_, sd := jailTestDirs(t)
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/4242")
	for _, kind := range []string{"read", "write", "exec"} {
		for _, p := range []string{"/run", "/var/run", "/run/user", "/run/user/1000", "/run/user/4242", "/tmp", "/var/tmp", "/dev/shm", "/dev"} {
			m := Manifest{}
			switch kind {
			case "read":
				m.Read = []string{p}
			case "write":
				m.Write = []string{p}
			default:
				m.Exec = []string{p}
			}
			if _, err := os.Stat(p); err != nil {
				continue
			}
			if _, err := grantFor(t, Spec{Name: "p", Manifest: m}, sd); err == nil {
				t.Errorf("%s %s was granted", kind, p)
			}
		}
	}
	if _, err := os.Stat("/run/docker.sock"); err == nil {
		if _, err := grantFor(t, Spec{Name: "p", Manifest: Manifest{Write: []string{"/run/docker.sock"}}}, sd); err != nil {
			t.Errorf("one socket file: %v", err)
		}
	}
	lines := strings.Join(Manifest{Read: []string{"~/.config/tool"}}.Lines(), "\n")
	if !strings.Contains(lines, "connecting to any socket in it") {
		t.Fatalf("consent must say a directory grant includes its sockets:\n%s", lines)
	}
}

// Nothing inside conductor's own state or config dir may be written, or run;
// it may be read only as an exact file a config: value names (an App key),
// never because the plugin declared it.
func TestNothingInsideConductorsDirsUnlessConfigured(t *testing.T) {
	home, sd := jailTestDirs(t)
	state, cfg := sd.MaskPaths[0], sd.MaskPaths[1]
	for _, f := range []string{filepath.Join(state, "plugins", "installed.yaml"), filepath.Join(cfg, "config.yaml")} {
		if err := os.MkdirAll(filepath.Dir(f), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []Manifest{
		{Write: []string{"~/.local/state/conductor/plugins/installed.yaml"}},
		{Write: []string{"~/.config/conductor/config.yaml"}},
		{Write: []string{"~/.local/state/conductor/plugins"}},
		{Read: []string{"~/.config/conductor/config.yaml"}},
		{Read: []string{"~/.config/conductor/app.pem"}},
		{Exec: []string{"~/.local/state/conductor/plugins"}},
	} {
		if _, err := grantFor(t, Spec{Name: "p", Manifest: m}, sd); err == nil || !strings.Contains(err.Error(), "conductor's own") {
			t.Errorf("%v: %v, want refused", m.Lines()[0], err)
		}
	}
	conf := func(v string, m Manifest) Spec {
		return Spec{Name: "p", Conns: []map[string]any{{"key": v}}, Manifest: m}
	}
	if p, err := grantFor(t, conf("~/.config/conductor/app.pem", Manifest{Read: []string{"config:key"}}), sd); err != nil || len(p.read) != 1 {
		t.Fatalf("a configured key file: %+v %v", p, err)
	}
	for _, s := range []Spec{
		conf("~/.config/conductor/app.pem", Manifest{Write: []string{"config:key"}}),
		conf(filepath.Join(state, "plugins"), Manifest{Read: []string{"config:key"}}),
	} {
		if _, err := grantFor(t, s, sd); err == nil {
			t.Errorf("%+v was granted", s.Manifest)
		}
	}
	_ = home
}

// What a plugin runs is named, never swept: an exec path that is, or holds,
// the system's or the user's program directories is refused; one relative
// to a command stays in that command's install tree.
func TestExecPathsAreNarrow(t *testing.T) {
	home, sd := jailTestDirs(t)
	for _, d := range []string{filepath.Join(home, "bin"), filepath.Join(home, ".local", "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{"/usr", "/usr/bin", "/usr/lib", "/etc", "/proc", "/var", "/opt", "~/bin", "~/.local/bin", "~/.local"} {
		if _, err := grantFor(t, Spec{Name: "p", Manifest: Manifest{Exec: []string{p}}}, sd); err == nil {
			if _, serr := os.Stat(strings.Replace(p, "~", home, 1)); serr == nil {
				t.Errorf("exec %s was granted", p)
			}
		}
	}
	tool := fakeTool(t, "usr/bin/tool")
	prefix := filepath.Dir(filepath.Dir(tool))
	if err := os.MkdirAll(filepath.Join(prefix, "lib", "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	for decl, want := range map[string]string{
		"command:tool/../../lib/tool": "",
		"command:tool/../../..":       "outside tool's install tree",
	} {
		s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}, Exec: []string{decl}}}
		_, err := grantFor(t, s, sd)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", decl, err, want)
		}
	}
	// A command directly in the system's bin dir: its "command:sh/.." is
	// that bin dir, and "command:sh/../.." the prefix above it. Required,
	// the launch fails naming the entry; optional, it is skipped.
	if _, err := spawnLookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	for _, decl := range []string{"command:sh/..", "command:sh/../.."} {
		s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"sh"}, Exec: []string{decl}}}
		if _, err := grantFor(t, s, sd); err == nil || !strings.Contains(err.Error(), "(declared "+decl+")") {
			t.Fatalf("required %s: %v", decl, err)
		}
		s.Manifest.Exec = []string{"?" + decl}
		if p, err := grantFor(t, s, sd); err != nil || len(p.exec) != 0 {
			t.Fatalf("optional %s: %+v %v, want skipped", decl, p, err)
		}
	}
	here := strings.Join(HereLines(Spec{Name: "p", Manifest: Manifest{Commands: []string{"tool"}, Exec: []string{"command:tool/../../lib/tool"}}}), "\n")
	if !strings.Contains(here, filepath.Join(prefix, "lib", "tool")) {
		t.Fatalf("consent must show where the exec path is here:\n%s", here)
	}
}

// A declared path through a symlink the user owns (a dotfiles-managed
// config dir) fails with the path, the symlink and the fix; marked
// optional, it is skipped instead.
func TestSymlinkedDeclaredPathNamesTheFix(t *testing.T) {
	home, sd := jailTestDirs(t)
	real := filepath.Join(home, "dotfiles", "tool")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(home, ".config", "tool")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".config", "tool")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	_, err := grantFor(t, Spec{Name: "p", Manifest: Manifest{Read: []string{"~/.config/tool"}}}, sd)
	if err == nil {
		t.Fatal("granted through a symlink")
	}
	for _, want := range []string{"(declared ~/.config/tool)", fmt.Sprintf("goes through %s, a symlink to %s", link, real), "replace the symlink", "trust: full"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if p, err := grantFor(t, Spec{Name: "p", Manifest: Manifest{Read: []string{"?~/.config/tool"}}}, sd); err != nil || len(p.read) != 0 {
		t.Fatalf("optional: %+v %v", p, err)
	}
}

// A script command runs with its interpreter: both are bound.
func TestScriptCommandBindsItsInterpreter(t *testing.T) {
	sh, err := spawnLookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	tool := fakeTool(t, "bin/tool")
	if err := os.WriteFile(tool, []byte("#!"+sh+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(sh)
	found, err := findCommands(Spec{Name: "p"}, newGrantChecks(SandboxDeps{}, sdk.Grant{}), []string{"tool"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range found {
		got = append(got, c.real)
	}
	if len(got) != 2 || got[1] != real {
		t.Fatalf("commands %v, want the tool and %s", got, real)
	}
}

// A declared command the daemon's PATH finds through a symlink the operator
// owns (Homebrew's bin/tool -> ../Cellar/tool/1.0/bin/tool) is followed to
// the program and runs from there: the jail's PATH carries the program's
// own directory, never the symlink's. A symlink in a path the plugin may
// write is one it could have made, and is still refused.
func TestCommandThroughAnOperatorSymlinkIsFollowed(t *testing.T) {
	home, sd := jailTestDirs(t)
	bin := filepath.Join(home, "prefix", "bin")
	cellar := filepath.Join(home, "prefix", "Cellar", "mytool", "1.0", "bin")
	for _, d := range []string{bin, cellar} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	real := filepath.Join(cellar, "mytool")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../Cellar/mytool/1.0/bin/mytool", filepath.Join(bin, "mytool")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	find := func(m Manifest) ([]jailCommand, error) {
		t.Helper()
		s := Spec{Name: "p", Manifest: m}
		g, err := pluginGrant(s)
		if err != nil {
			t.Fatal(err)
		}
		return findCommands(s, newGrantChecks(sd, g), []string{"mytool"})
	}

	cmds, err := find(Manifest{Commands: []string{"mytool"}})
	if err != nil {
		t.Fatalf("a command through the operator's symlink: %v", err)
	}
	if len(cmds) == 0 || cmds[0].path != real || cmds[0].real != real {
		t.Fatalf("commands = %+v, want mytool at %s", cmds, real)
	}
	if path := jailPATH(cmds); !strings.Contains(path, cellar) || strings.Contains(path, bin) {
		t.Fatalf("jail PATH = %s, want the program's directory %s and not the symlink's", path, cellar)
	}

	// The symlink's directory is one the plugin may write.
	if _, err := find(Manifest{Commands: []string{"mytool"}, Write: []string{"~/prefix/bin"}}); err == nil || !strings.Contains(err.Error(), "command mytool") || !strings.Contains(err.Error(), "it could have made it") {
		t.Fatalf("a symlink inside a write grant: %v, want it refused", err)
	}
	// So is the program's.
	if _, err := find(Manifest{Commands: []string{"mytool"}, Write: []string{"~/prefix/Cellar"}}); err == nil || !strings.Contains(err.Error(), "which it may write") {
		t.Fatalf("a program inside a write grant: %v, want it refused", err)
	}
}
