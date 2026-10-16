//go:build linux || darwin

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SeedHome copies what the volume lacks — dirs, files, links as links,
// never a fifo — keeps what it has, and gives it all to the agent's user.
func TestSeedHome(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(src, ".claude"), 0o755)
	_ = os.WriteFile(filepath.Join(src, ".claude", "settings.json"), []byte("seed"), 0o600)
	_ = os.WriteFile(filepath.Join(src, ".claude.json"), []byte("seed"), 0o644)
	_ = os.Symlink("/nonexistent/target", filepath.Join(src, "link"))
	_ = syscall.Mkfifo(filepath.Join(src, "fifo"), 0o600)
	_ = os.WriteFile(filepath.Join(dst, ".claude.json"), []byte("the volume's own"), 0o600)
	uid, gid := os.Getuid(), os.Getgid()
	if uid == 0 {
		t.Skip("runs as a non-root user")
	}
	if err := SeedHome(src, dst, uid, gid); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dst, ".claude", "settings.json")); string(b) != "seed" {
		t.Error("a missing file was not seeded")
	}
	if b, _ := os.ReadFile(filepath.Join(dst, ".claude.json")); string(b) != "the volume's own" {
		t.Error("the volume's own file was overwritten")
	}
	if l, err := os.Readlink(filepath.Join(dst, "link")); err != nil || l != "/nonexistent/target" {
		t.Errorf("a link was not copied as a link: %q %v", l, err)
	}
	if _, err := os.Lstat(filepath.Join(dst, "fifo")); err == nil {
		t.Error("a fifo was seeded")
	}
	fi, err := os.Stat(filepath.Join(dst, ".claude", "settings.json"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode not kept: %v", fi)
	}
	if err := SeedHome(src, dst, 0, 0); err == nil || !strings.Contains(err.Error(), "uid 0") {
		t.Errorf("a HOME for root: %v", err)
	}
	if err := SeedHome(filepath.Join(src, "missing"), dst, uid, gid); err == nil {
		t.Error("a missing seed")
	}
}

// A launch runs in its own group, recorded by id; the host's signal reaches
// the whole group; its status is the launch's; its pid file goes with it.
func TestContainerLaunchAndSignal(t *testing.T) {
	old := launchDir
	launchDir = t.TempDir()
	t.Cleanup(func() { launchDir = old })
	done := make(chan int, 1)
	child := filepath.Join(launchDir, "child.pid")
	go func() {
		done <- RunContainerLaunch("L1", []string{"/bin/sh", "-c", "sleep 60 & echo $! > " + child + "; wait"})
	}()
	pidf := filepath.Join(launchDir, "run-L1.pid")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(child); err == nil {
			if _, err := os.Stat(pidf); err == nil {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the launch never recorded itself")
		}
		time.Sleep(20 * time.Millisecond)
	}
	b, _ := os.ReadFile(child)
	grandchild := strings.TrimSpace(string(b))
	if rc := SignalContainerLaunch("L1", "SIGTERM"); rc != 0 {
		t.Fatalf("signal: %d", rc)
	}
	select {
	case rc := <-done:
		if rc != 128+int(syscall.SIGTERM) {
			t.Errorf("status %d, want %d", rc, 128+int(syscall.SIGTERM))
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the launch outlived its TERM")
	}
	if _, err := os.Stat(pidf); err == nil {
		t.Error("the pid file outlived the launch")
	}
	gone := func() bool {
		st, err := os.ReadFile("/proc/" + grandchild + "/stat")
		return err != nil || strings.Contains(string(st), ") Z ")
	}
	if _, err := os.Stat("/proc/self"); err == nil {
		for end := time.Now().Add(5 * time.Second); !gone(); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(end) {
				t.Error("a process the launch started outlived it")
				break
			}
		}
	}
	if rc := RunContainerLaunch("L2", []string{"/bin/sh", "-c", "exit 7"}); rc != 7 {
		t.Errorf("exit status %d", rc)
	}
	if rc := RunContainerLaunch("L3", []string{"/nonexistent-agent"}); rc != 127 {
		t.Errorf("a missing command: %d", rc)
	}
	for _, bad := range []string{"", "../x", "a/b", strings.Repeat("a", 65), "a b"} {
		if rc := RunContainerLaunch(bad, []string{"true"}); rc != 2 {
			t.Errorf("launch id %q accepted (%d)", bad, rc)
		}
		if rc := SignalContainerLaunch(bad, "TERM"); rc != 2 {
			t.Errorf("signal id %q accepted (%d)", bad, rc)
		}
	}
	if rc := RunContainerLaunch("L4", nil); rc != 2 {
		t.Error("nothing to run")
	}
	if rc := SignalContainerLaunch("gone", "KILL"); rc != 0 {
		t.Error("signalling a finished launch is not an error")
	}
	_ = os.WriteFile(filepath.Join(launchDir, "run-L5.pid"), []byte("1"), 0o600)
	if rc := SignalContainerLaunch("L5", "KILL"); rc != 2 {
		t.Error("signalled pid 1")
	}
	_ = os.WriteFile(filepath.Join(launchDir, "run-L6.pid"), []byte("12345"), 0o600)
	if rc := SignalContainerLaunch("L6", "BOGUS"); rc != 2 {
		t.Error("an unknown signal")
	}
}
