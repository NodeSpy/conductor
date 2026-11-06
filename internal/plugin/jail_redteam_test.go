//go:build linux || darwin

package plugin

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Regression tests for the jail half of the plugin jail's adversarial review:
// what a declaration resolves to before anything is bound.

// jailTestDirs is a fake home, conductor state and config dirs, and a
// staging root, all under one temp dir.
func jailTestDirs(t *testing.T) (home string, sd SandboxDeps) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(dir, "home")
	state := filepath.Join(home, ".local", "state", "conductor")
	cfg := filepath.Join(home, ".config", "conductor")
	for _, d := range []string{state, cfg, filepath.Join(home, ".config", "tool"), filepath.Join(dir, "staging")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(cfg, "app.pem"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	jailHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { jailHome = os.UserHomeDir })
	return home, SandboxDeps{MaskPaths: []string{state, cfg}, StagingRoot: filepath.Join(dir, "staging")}
}

func grantFor(t *testing.T, s Spec, sd SandboxDeps) (jailPaths, error) {
	t.Helper()
	g, err := pluginGrant(s)
	if err != nil {
		return jailPaths{}, err
	}
	return pathsFor(s, sd, g)
}

// A config: path value (or a declaration) naming the root, the home dir or
// an ancestor of conductor's own state/config dirs is refused; an exact file
// inside the config dir (an App key) is fine.
func TestDeclaredPathsRefuseHomeRootAndConductorsDirs(t *testing.T) {
	home, sd := jailTestDirs(t)
	for _, v := range []string{"/", "~", home, filepath.Join(home, ".config"), filepath.Join(home, ".local"), filepath.Join(home, ".config", "conductor")} {
		s := Spec{Name: "p", Conns: []map[string]any{{"key": v}}, Manifest: Manifest{Read: []string{"config:key"}}}
		if _, err := grantFor(t, s, sd); err == nil {
			t.Errorf("read of %q was granted", v)
		}
	}
	s := Spec{Name: "p", Conns: []map[string]any{{"key": "~/.config/conductor/app.pem"}}, Manifest: Manifest{Read: []string{"config:key", "~/.config/tool"}}}
	p, err := grantFor(t, s, sd)
	if err != nil || len(p.read) != 2 {
		t.Fatalf("an exact key file and a tool's dir: %+v, %v", p, err)
	}
}

// A declared path is never bound through a symlink conductor's user owns:
// replacing a declared write dir with a symlink to the state dir (what a
// plugin's previous call could do) is refused, not followed.
func TestDeclaredPathThroughASymlinkIsRefused(t *testing.T) {
	home, sd := jailTestDirs(t)
	data := filepath.Join(home, "plugin-data")
	if err := os.Symlink(filepath.Join(home, ".config", "conductor"), data); err != nil {
		t.Fatal(err)
	}
	s := Spec{Name: "p", Manifest: Manifest{Write: []string{data}}}
	if _, err := grantFor(t, s, sd); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("a symlinked write path: %v, want refused as a symlink", err)
	}
	// Deeper: a parent dir replaced by a symlink.
	s = Spec{Name: "p", Manifest: Manifest{Read: []string{filepath.Join(data, "app.pem")}}}
	if _, err := grantFor(t, s, sd); err == nil {
		t.Fatal("a path through a symlinked parent was granted")
	}
}

// A declared path inside another declared writable path is refused: the
// plugin could replace it with a symlink before the next launch.
func TestDeclaredPathInsideAWritablePathIsRefused(t *testing.T) {
	home, sd := jailTestDirs(t)
	s := Spec{Name: "p", Manifest: Manifest{Write: []string{"~/data"}, Read: []string{"~/data/inner"}}}
	if _, err := grantFor(t, s, sd); err == nil || !strings.Contains(err.Error(), "may write") {
		t.Fatalf("read inside a write path: %v", err)
	}
	s = Spec{Name: "p", Manifest: Manifest{Read: []string{filepath.Join(sd.StagingRoot, "p", "x")}}}
	if _, err := grantFor(t, s, sd); err == nil {
		t.Fatal("a path inside the staging dir was granted")
	}
	_ = home
}

// network: and allow_env: unset grants everything declared; a set but empty
// list grants nothing.
func TestEmptyNarrowingGrantsNothing(t *testing.T) {
	m := Manifest{Egress: []string{"api.example.com:443"}, Env: []string{"TOKEN"}}
	g, err := pluginGrant(Spec{Name: "p", Manifest: m})
	if err != nil || len(g.Egress) != 1 || len(g.Env) != 1 {
		t.Fatalf("unset: %+v, %v", g, err)
	}
	g, err = pluginGrant(Spec{Name: "p", Manifest: m, Network: []string{}, AllowEnv: []string{}})
	if err != nil || len(g.Egress) != 0 || len(g.Env) != 0 {
		t.Fatalf("network: [] and allow_env: []: %+v, %v — want nothing", g, err)
	}
	if em := (Spec{Manifest: m, Network: []string{}}).EffectiveManifest(); len(em.Egress) != 0 {
		t.Fatalf("effective manifest with network: []: %+v", em)
	}
}

// A loopback destination may not be a port conductor itself serves on.
func TestLoopbackEgressToConductorsOwnPortIsRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	own, err := ownServicePort(port)
	if err != nil || !own {
		t.Fatalf("this process listens on %d: own=%v err=%v", port, own, err)
	}
	if err := refuseOwnPorts(Spec{Name: "p"}, []int{port}); err == nil {
		t.Fatal("a relay to conductor's own port was allowed")
	}
	// A plugin's own listen relay is conductor's listener, not its service.
	stop, err := serveTCPToUnix("127.0.0.1:0", filepath.Join(t.TempDir(), "s"))
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	relayed.Lock()
	var rp int
	for p := range relayed.ports {
		rp = p
	}
	relayed.Unlock()
	if own, err := ownServicePort(rp); err != nil || own {
		t.Fatalf("a plugin listen relay counted as conductor's own port: %v %v", own, err)
	}
}

// A declared exec path resolves like a read path ("command:tool/…" relative
// to the tool's file), and is granted read-only and checked like one.
func TestExecPathsResolveRelativeToACommand(t *testing.T) {
	_, sd := jailTestDirs(t)
	cat, err := spawnLookPath("cat")
	if err != nil {
		t.Skip("no cat")
	}
	real, _ := filepath.EvalSymlinks(cat)
	s := Spec{Name: "p", Manifest: Manifest{Commands: []string{"cat"}, Exec: []string{"command:cat/.."}}}
	p, err := grantFor(t, s, sd)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.exec) != 1 || p.exec[0].path != filepath.Dir(real) {
		t.Fatalf("exec = %+v, want %s", p.exec, filepath.Dir(real))
	}
}
