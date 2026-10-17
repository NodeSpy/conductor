//go:build linux || darwin

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	guardSignals(t)
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

// guardSignals makes every group signal sent during the test either reach a
// process group made only of this test's descendants (or an empty group,
// where it is a no-op) or fail the test unsent. A test must never be able to
// signal anything else: a mutated guard then fails here instead of taking out
// the operator's session.
func guardSignals(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	seen := map[int]bool{} // groups already verified as ours: a pgid isn't reused while any member lives
	old := rawKill
	rawKill = func(pid int, sig syscall.Signal) error {
		mu.Lock()
		defer mu.Unlock()
		if pgid := -pid; pgid > 1 && (seen[pgid] || groupIsOurs(pgid)) {
			seen[pgid] = true
			return old(pid, sig)
		}
		t.Errorf("refused to send signal %v to %d: not a process group this test started", sig, pid)
		return syscall.EPERM
	}
	t.Cleanup(func() { rawKill = old })
}

// groupIsOurs reports whether every process in group pgid descends from the
// test process. An empty group counts as ours (signalling it is a no-op);
// anything it can't determine does not.
func groupIsOurs(pgid int) bool {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,pgid=").Output()
	if err != nil {
		return false
	}
	parent := map[int]int{}
	var members []int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		pg, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		parent[pid] = ppid
		if pg == pgid {
			members = append(members, pid)
		}
	}
	self := os.Getpid()
	for _, m := range members {
		ours := false
		for p, hops := m, 0; p > 1 && hops < 64; p, hops = parent[p], hops+1 {
			if parent[p] == self {
				ours = true
				break
			}
		}
		if !ours {
			return false
		}
	}
	return true
}

func TestSignalGroupRefusesLowPGIDs(t *testing.T) {
	var got []int
	old := rawKill
	rawKill = func(pid int, sig syscall.Signal) error { got = append(got, pid); return nil }
	t.Cleanup(func() { rawKill = old })
	for _, pgid := range []int{1, 0, -1, -7} {
		if err := signalGroup(pgid, syscall.SIGKILL); err != syscall.EINVAL {
			t.Errorf("signalGroup(%d): %v, want EINVAL", pgid, err)
		}
	}
	if len(got) != 0 {
		t.Fatalf("signalGroup sent %v for a pgid <= 1", got)
	}
	if err := signalGroup(4242, syscall.SIGTERM); err != nil || len(got) != 1 || got[0] != -4242 {
		t.Fatalf("signalGroup(4242): err=%v sent=%v, want one signal to -4242", err, got)
	}
}
