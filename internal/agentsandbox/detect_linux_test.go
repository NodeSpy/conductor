//go:build linux

package agentsandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A launch process that starts its own group and leaves a daemon behind —
// its environment dropped, reparented out of the tree — still has the
// daemon killed at the launch's end.
func TestDetectorKillsADaemonLeftInTheLaunchsGroup(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid")
	}
	marker := newMarker()
	pidFile := filepath.Join(t.TempDir(), "pid")
	c := exec.Command("setsid", "sh", "-c", "env -i sleep 60 & echo $! > "+pidFile+"; sleep 1")
	c.Env = append(os.Environ(), markerEnv+"="+marker)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	d := &detector{spec: DetectSpec{Action: DetectAlert}, allow: map[string]bool{}, marker: marker,
		signal: signalProc, reported: map[string]bool{}}
	d.list = listProcs(marker, d.isRoot)
	time.Sleep(300 * time.Millisecond)
	d.scan()
	waitOnce(c) // the group's leader exits; the daemon is now outside the tree
	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if p, ok := procStat(pid); !ok || p.comm != "sleep" {
		t.Fatalf("daemon %d not running: %+v", pid, p)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if d.killAll() == 0 {
		t.Fatal("nothing killed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := procStat(pid); !ok || isZombie(pid) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon left in the launch's group survived")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func isZombie(pid int) bool {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	i := strings.LastIndexByte(string(b), ')')
	return i > 0 && strings.HasPrefix(strings.TrimSpace(string(b[i+1:])), "Z")
}

// safeToSignal refuses the pids killAll must never touch: nothing, init,
// this process, and its parent.
func TestSafeToSignalRefusesPid1SelfAndParent(t *testing.T) {
	if safeToSignal(0) {
		t.Error("pid 0 safe to signal")
	}
	if safeToSignal(1) {
		t.Error("pid 1 (init) safe to signal")
	}
	if safeToSignal(os.Getpid()) {
		t.Error("this process safe to signal")
	}
	if safeToSignal(os.Getppid()) {
		t.Error("the parent process safe to signal")
	}
	if !safeToSignal(os.Getpid() + 1) {
		t.Error("an ordinary other pid refused")
	}
}

// signalProc refuses to kill a pid whose kernel start time no longer
// matches what the scan saw — the process now at that pid is not the one
// being hunted (a reused pid after the original already exited).
func TestSignalProcRefusesAPidWhoseStartTimeChanged(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	c := exec.Command(sleep, "5")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); waitOnce(c) })
	pid := c.Process.Pid
	start, ok := procStart(pid)
	if !ok {
		t.Fatal("no start time")
	}
	if err := signalProc(pid, start+1); err == nil {
		t.Fatal("signalled a pid whose claimed start time did not match")
	}
	if p, ok := procStat(pid); !ok || p.comm != "sleep" {
		t.Fatal("the process was killed despite the start-time mismatch")
	}
}
