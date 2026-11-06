//go:build linux

package plugin

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for the third adversarial review of the plugin jail:
// paths inside runtime dirs, and the programs a declared command runs.

// noGrantExempt makes the test's temp dir a runtime dir again (TestMain
// exempts it), for a test of what is refused inside one.
func noGrantExempt(t *testing.T) {
	was := grantExemptRoots
	grantExemptRoots = nil
	t.Cleanup(func() { grantExemptRoots = was })
}

// listenUnix serves a unix socket at p for the test.
func listenUnix(t *testing.T, p string) {
	t.Helper()
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
}

// Anything inside a runtime dir is refused, not only the dir and what
// contains it: a directory one level below $XDG_RUNTIME_DIR (systemd
// --user's, holding its private socket) or the session bus socket, read.
// Only an exact socket file declared under write: is granted there, and
// consent names it as a socket.
func TestNothingInsideARuntimeDirButOneSocketUnderWrite(t *testing.T) {
	_, sd := jailTestDirs(t)
	noGrantExempt(t)
	runtime, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", runtime)
	systemd := filepath.Join(runtime, "systemd")
	if err := os.MkdirAll(systemd, 0o700); err != nil {
		t.Fatal(err)
	}
	listenUnix(t, filepath.Join(systemd, "private"))
	bus := filepath.Join(runtime, "bus")
	listenUnix(t, bus)
	data := filepath.Join(runtime, "data")
	if err := os.WriteFile(data, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	uid := fmt.Sprint(os.Getuid())
	for _, m := range []Manifest{
		{Read: []string{systemd}},
		{Read: []string{systemd + "/"}},
		{Write: []string{systemd}},
		{Exec: []string{systemd}},
		{Env: []string{"XDG_RUNTIME_DIR"}, Read: []string{"env:XDG_RUNTIME_DIR/systemd"}},
		{Read: []string{bus}},
		{Exec: []string{bus}},
		{Write: []string{data}},
		{Read: []string{"/run/user/" + uid + "/systemd"}},
		{Read: []string{"/run/user/" + uid + "/bus/"}},
		{Read: []string{"/run/dbus"}},
		{Read: []string{"/run/dbus/system_bus_socket"}},
		{Read: []string{"/var/run/dbus"}},
	} {
		s := Spec{Name: "p", Manifest: m}
		p, err := grantFor(t, s, sd)
		if err != nil {
			if !strings.Contains(err.Error(), "sockets") {
				t.Errorf("%v: refused, but not for its sockets: %v", m.Lines(), err)
			}
			continue
		}
		if n := len(p.read) + len(p.write) + len(p.exec); n != 0 {
			t.Errorf("%v granted: %+v", m.Lines(), p)
		}
	}

	m := Manifest{Env: []string{"XDG_RUNTIME_DIR"}, Write: []string{"env:XDG_RUNTIME_DIR/bus"}}
	p, err := grantFor(t, Spec{Name: "p", Manifest: m}, sd)
	if err != nil || len(p.write) != 1 || p.write[0].real != bus || !p.write[0].socket {
		t.Fatalf("one socket under write: %+v %v", p, err)
	}
	lines := strings.Join(m.Lines(), "\n")
	if !strings.Contains(lines, socketLabel+": $XDG_RUNTIME_DIR/bus") || strings.Contains(lines, "reads and writes") {
		t.Errorf("consent must name the socket as one:\n%s", lines)
	}
	if w := strings.Join(Manifest{}.Widens(m), "\n"); !strings.Contains(w, socketLabel+" $XDG_RUNTIME_DIR/bus") {
		t.Errorf("widens must name the socket as one: %s", w)
	}
	if here := strings.Join(HereLines(Spec{Name: "p", Manifest: m}), "\n"); !strings.Contains(here, socketLabel+": "+bus) {
		t.Errorf("here lines must name the socket as one: %s", here)
	}
}

// A script command's #! interpreter is a program the jail binds: it is
// checked like a declared path. A script inside a path the plugin may write
// is refused outright (the plugin could rewrite its #! to any path, the
// session bus's directory, the home dir); an interpreter that is a
// directory, goes through a symlink root does not own, or lies inside
// conductor's own dirs or the plugin's staging dir is refused, and the
// launch fails naming it.
func TestShebangInterpreterIsCheckedLikeADeclaredPath(t *testing.T) {
	home, sd := jailTestDirs(t)
	tools := filepath.Join(home, "tools")
	secret := filepath.Join(home, "secret")
	for _, d := range []string{tools, secret} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := filepath.Join(tools, "mytool")
	program := func(p string) {
		t.Helper()
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	state := sd.MaskPaths[0]
	program(filepath.Join(state, "sh"))
	if err := os.MkdirAll(filepath.Join(sd.StagingRoot, "p"), 0o700); err != nil {
		t.Fatal(err)
	}
	program(filepath.Join(sd.StagingRoot, "p", "sh"))
	sh, err := spawnLookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	if err := os.Symlink(sh, filepath.Join(tools, "sh")); err != nil {
		t.Fatal(err)
	}

	cmdOnly := Manifest{Commands: []string{"mytool"}}
	for _, c := range []struct {
		name    string
		shebang string
		m       Manifest
		want    string
	}{
		{"script inside a write grant", "/bin/sh", Manifest{Commands: []string{"mytool"}, Write: []string{"~/tools"}}, "which it may write"},
		{"directory", secret, cmdOnly, "not an executable file"},
		{"user-owned symlink", filepath.Join(tools, "sh"), cmdOnly, "a symlink to " + sh + " that root does not own"},
		{"inside conductor's state", filepath.Join(state, "sh"), cmdOnly, "conductor's own"},
		{"inside the staging dir", filepath.Join(sd.StagingRoot, "p", "sh"), cmdOnly, "which it may write"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if err := os.WriteFile(script, []byte("#!"+c.shebang+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			s := Spec{Name: "p", Manifest: c.m}
			_, _, err := buildJailed(s, sd, []string{"true"}, nil)
			if err == nil || !strings.Contains(err.Error(), "command mytool") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("launch: %v, want it refused naming the command and %q", err, c.want)
			}
		})
	}

	// The program itself: a declared command inside the staging dir.
	staged := filepath.Join(sd.StagingRoot, "p", "sh")
	t.Setenv("PATH", filepath.Dir(staged)+string(os.PathListSeparator)+os.Getenv("PATH"))
	g, err := pluginGrant(Spec{Name: "p", Manifest: Manifest{Commands: []string{"sh"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := findCommands(Spec{Name: "p"}, newGrantChecks(sd, g), []string{"sh"}); err == nil || !strings.Contains(err.Error(), "which it may write") {
		t.Fatalf("a command inside the staging dir: %v", err)
	}
}

// On macOS a path compares without case, as its file systems resolve it: a
// case-variant spelling of a runtime or protected dir is still inside it.
func TestMacOSPathsCompareWithoutCase(t *testing.T) {
	was := spawnGOOS
	spawnGOOS = "darwin"
	t.Cleanup(func() { spawnGOOS = was })
	if !within("/private/tmp", "/Private/Tmp/x") || !within("/Users/u/Library", "/users/U/library") {
		t.Fatal("a case-variant spelling escaped within")
	}
	spawnGOOS = "linux"
	if within("/private/tmp", "/Private/Tmp/x") {
		t.Fatal("linux paths are case-sensitive")
	}
}
