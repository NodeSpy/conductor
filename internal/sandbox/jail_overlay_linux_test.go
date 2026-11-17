//go:build linux

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Exit codes of the overlay probe.
const (
	ovlOK         = 0
	ovlNoCache    = 11 // the cache's content was not visible at the target
	ovlNoWrite    = 12 // a write at the target failed
	ovlSawLower   = 13 // the lower layer's own path was visible in the jail
	ovlSawLayers  = 14 // the run's overlay layers were reachable in the jail
	ovlUnmounted  = 15 // the payload could unmount the overlay
	ovlCannot     = 42
	ovlSymlinkHit = 16 // a symlinked target was mounted through
)

// TestJailOverlayCache proves a cache overlay in the real jail: the run sees
// the cache's content at its target and writes there freely, every write
// lands in its upper layer, and the lower layer — the shared cache — is
// unchanged, never visible by its own path, and cannot be reached by
// unmounting the overlay. The upper and work directories are conductor's,
// outside the home (mount(2) follows a symlink in them): the payload can
// neither see nor reach them.
func TestJailOverlayCache(t *testing.T) {
	switch os.Getenv("OVL_ROLE") {
	case "probe":
		os.Exit(runOverlayProbe())
	case "outer":
		os.Exit(runOverlayOuter())
	}
	requireRootMappedUserns(t)
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	lower := filepath.Join(dir, "store", "data")
	layers := filepath.Join(dir, "store", ".layers")
	upper, work := filepath.Join(layers, "s", "upper"), filepath.Join(layers, "s", "work")
	// The kernel leaves an overlay's work/work mode 0.
	t.Cleanup(func() { chmodTree(work) })
	for _, d := range []string{home, lower, upper, work, filepath.Join(lower, "pkg")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(t, filepath.Join(lower, "pkg", "f"), "cached")
	mustWrite(t, filepath.Join(lower, "g"), "doomed")
	// A read-only module cache, as Go leaves one.
	if err := os.Chmod(filepath.Join(lower, "pkg", "f"), 0o444); err != nil {
		t.Fatal(err)
	}
	out, code := runOverlayJail(t, dir, map[string]string{
		"OVL_HOME": home, "OVL_LOWER": lower, "OVL_UPPER": upper, "OVL_WORK": work,
		"OVL_TARGET": filepath.Join(home, "go", "mod"), "OVL_LAYERS": layers,
	})
	if code == ovlCannot {
		t.Skipf("environment cannot mount an overlay in a user namespace:\n%s", out)
	}
	if code != ovlOK {
		t.Fatalf("overlay probe exit %d\n%s", code, out)
	}
	// The shared copy is exactly as it was.
	if b, err := os.ReadFile(filepath.Join(lower, "pkg", "f")); err != nil || string(b) != "cached" {
		t.Errorf("lower pkg/f = %q, %v; want it untouched", b, err)
	}
	if _, err := os.Stat(filepath.Join(lower, "g")); err != nil {
		t.Errorf("lower g was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lower, "new")); err == nil {
		t.Error("a write reached the lower layer")
	}
	// Every write is in the run's upper layer.
	if b, err := os.ReadFile(filepath.Join(upper, "new")); err != nil || string(b) != "run" {
		t.Errorf("upper new = %q, %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(upper, "pkg", "f")); err != nil || string(b) != "changed" {
		t.Errorf("upper pkg/f = %q, %v", b, err)
	}
	if fi, err := os.Lstat(filepath.Join(upper, "g")); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		t.Errorf("upper g should be a whiteout: %v %v", fi, err)
	}
}

// TestJailOverlayRefusesSymlinkTarget: a target reached through a symlink
// (a workspace is the agent's, and a link it left could send the mount
// anywhere) fails the jail rather than mount through it.
func TestJailOverlayRefusesSymlinkTarget(t *testing.T) {
	if os.Getenv("OVL_ROLE") != "" {
		return
	}
	requireRootMappedUserns(t)
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	lower := filepath.Join(dir, "store", "data")
	elsewhere := filepath.Join(dir, "elsewhere")
	layers := filepath.Join(dir, "store", ".layers")
	upper, work := filepath.Join(layers, "s", "upper"), filepath.Join(layers, "s", "work")
	for _, d := range []string{home, lower, upper, work, elsewhere} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(elsewhere, filepath.Join(home, "go")); err != nil {
		t.Fatal(err)
	}
	out, code := runOverlayJail(t, dir, map[string]string{
		"OVL_HOME": home, "OVL_LOWER": lower, "OVL_UPPER": upper, "OVL_WORK": work,
		"OVL_TARGET": filepath.Join(home, "go", "mod"), "OVL_LAYERS": layers,
	})
	if code == ovlOK {
		t.Fatalf("the jail mounted through a symlinked target\n%s", out)
	}
	if !strings.Contains(out, "symlink") && !strings.Contains(out, "not a directory") {
		t.Fatalf("exit %d without naming the symlink:\n%s", code, out)
	}
	if ents, _ := os.ReadDir(elsewhere); len(ents) != 0 {
		t.Fatalf("the symlink's target was written: %v", ents)
	}
}

func requireRootMappedUserns(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("unshare"); err != nil {
		t.Skip("unshare not on PATH")
	}
	if err := exec.Command("unshare", "--user", "--map-root-user", "--mount", "true").Run(); err != nil {
		t.Skipf("requires unprivileged root-mapped user namespaces: %v", err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runOverlayJail(t *testing.T, dir string, env map[string]string) (string, int) {
	t.Helper()
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("unshare", "--user", "--map-root-user", "--pid", "--fork", "--mount-proc", "--kill-child", "--",
		os.Args[0], "-test.run=^TestJailOverlayCache$", "-test.v")
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp, "OVL_ROLE=outer",
		"OVL_JAILROOT="+filepath.Join(dir, "state", "jail"), "OVL_JAILTMP="+filepath.Join(dir, "state", JailTmpDir))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	}
	if err != nil {
		t.Skipf("outer namespace launch failed: %v\n%s", err, out)
	}
	return string(out), 0
}

func runOverlayOuter() int {
	// Can this namespace mount an overlay at all? (A kernel before 5.11.)
	probe, err := os.MkdirTemp(os.Getenv("TMPDIR"), "ovlprobe")
	if err == nil {
		for _, d := range []string{"l", "u", "w", "m"} {
			_ = os.Mkdir(filepath.Join(probe, d), 0o700)
		}
		err = unix.Mount("overlay", filepath.Join(probe, "m"), "overlay", 0,
			"lowerdir="+filepath.Join(probe, "l")+",upperdir="+filepath.Join(probe, "u")+",workdir="+filepath.Join(probe, "w")+",volatile")
		if err == nil {
			_ = unix.Unmount(filepath.Join(probe, "m"), 0)
		}
		chmodTree(probe)
		_ = os.RemoveAll(probe)
	}
	if err != nil {
		return ovlCannot
	}
	os.Setenv("OVL_ROLE", "probe")
	return RunEnter(EnterOpts{
		Binds: []BindMount{
			{Path: os.Getenv("OVL_HOME")},
			{Path: os.Getenv("OVL_TARGET"), Overlay: &Overlay{Lower: os.Getenv("OVL_LOWER"), Upper: os.Getenv("OVL_UPPER"), Work: os.Getenv("OVL_WORK")}},
			{Path: filepath.Dir(os.Args[0]), RO: true},
		},
		Argv:     []string{os.Args[0], "-test.run=^TestJailOverlayCache$", "-test.v"},
		JailRoot: os.Getenv("OVL_JAILROOT"),
		JailTmp:  os.Getenv("OVL_JAILTMP"),
	})
}

func runOverlayProbe() int {
	target := os.Getenv("OVL_TARGET")
	if b, err := os.ReadFile(filepath.Join(target, "pkg", "f")); err != nil || string(b) != "cached" {
		return ovlNoCache
	}
	if err := os.WriteFile(filepath.Join(target, "new"), []byte("run"), 0o644); err != nil {
		return ovlNoWrite
	}
	// The read-only file is the run's to change (as GOFLAGS=-modcacherw
	// lets Go), in its own copy.
	if err := os.Chmod(filepath.Join(target, "pkg", "f"), 0o644); err != nil {
		return ovlNoWrite
	}
	if err := os.WriteFile(filepath.Join(target, "pkg", "f"), []byte("changed"), 0o644); err != nil {
		return ovlNoWrite
	}
	if err := os.Remove(filepath.Join(target, "g")); err != nil {
		return ovlNoWrite
	}
	if _, err := os.Stat(os.Getenv("OVL_LOWER")); err == nil {
		return ovlSawLower
	}
	for _, p := range []string{os.Getenv("OVL_LAYERS"), os.Getenv("OVL_UPPER"), os.Getenv("OVL_WORK")} {
		if _, err := os.Lstat(p); err == nil {
			return ovlSawLayers
		}
	}
	if err := unix.Unmount(target, unix.MNT_DETACH); err == nil {
		return ovlUnmounted
	}
	return ovlOK
}

// chmodTree makes a tree an overlay left removable.
func chmodTree(root string) {
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, _ error) error {
		if d != nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
}
