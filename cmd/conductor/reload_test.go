package main

import (
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// signalReload can't be exercised end-to-end without a running daemon, but its
// "no daemon" path — no pidfile at the expected sibling of the state file — must
// fail with a clear, actionable message rather than a bare ENOENT.
func TestSignalReloadNoDaemon(t *testing.T) {
	cfg := &config.Config{Store: config.Store{StateFile: t.TempDir() + "/state.json"}}
	err := signalReload(cfg)
	if err == nil {
		t.Fatal("signalReload with no running daemon should error")
	}
	if !strings.Contains(err.Error(), "is the daemon running") {
		t.Fatalf("error should hint the daemon isn't running, got: %v", err)
	}
}

// A pidfile holding a pid <= 1 (or garbage) must never reach syscall.Kill:
// kill(-1) would signal every process the operator owns. No real signal is
// sent here; signalProcess is replaced.
func TestSignalDaemonRefusesBadPids(t *testing.T) {
	var sent []int
	old, oldIs := signalProcess, isConductorProcess
	signalProcess = func(pid int, sig syscall.Signal) error { sent = append(sent, pid); return nil }
	// Every pid "is conductor" here: the pid's own rule must still refuse.
	isConductorProcess = func(int) bool { return true }
	t.Cleanup(func() { signalProcess, isConductorProcess = old, oldIs })
	dir := t.TempDir()
	cfg := &config.Config{Store: config.Store{StateFile: dir + "/state.json"}}
	for _, bad := range []string{"-1", "0", "-4242", "abc", ""} {
		if err := os.WriteFile(pidPath(cfg), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := signalDaemon(cfg, syscall.SIGHUP); err == nil {
			t.Errorf("pidfile %q accepted", bad)
		}
	}
	if len(sent) != 0 {
		t.Fatalf("signalled %v from a bad pidfile", sent)
	}
	if err := os.WriteFile(pidPath(cfg), []byte("4242\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, err := signalDaemon(cfg, syscall.SIGUSR1); err != nil || pid != 4242 || len(sent) != 1 || sent[0] != 4242 {
		t.Fatalf("good pidfile: pid=%d err=%v sent=%v", pid, err, sent)
	}
}

// A pidfile's pid is signalled only when it is a conductor process: a stale
// pidfile pointing at a reused pid (or at the host's init) is refused, and
// pid 1 is signalled when it is conductor (a container's init).
func TestSignalDaemonOnlyAConductor(t *testing.T) {
	var sent []int
	old, oldIs := signalProcess, isConductorProcess
	signalProcess = func(pid int, sig syscall.Signal) error { sent = append(sent, pid); return nil }
	conductors := map[int]bool{1: false, 4242: false}
	isConductorProcess = func(pid int) bool { return conductors[pid] }
	t.Cleanup(func() { signalProcess, isConductorProcess = old, oldIs })
	cfg := &config.Config{Store: config.Store{StateFile: t.TempDir() + "/state.json"}}
	for _, pid := range []string{"1", "4242"} {
		_ = os.WriteFile(pidPath(cfg), []byte(pid), 0o600)
		if _, err := signalDaemon(cfg, syscall.SIGHUP); err == nil || !strings.Contains(err.Error(), "not a conductor process") {
			t.Fatalf("pid %s, not conductor: %v", pid, err)
		}
	}
	if len(sent) != 0 {
		t.Fatalf("signalled %v", sent)
	}
	conductors[1] = true // conductor is the container's init
	_ = os.WriteFile(pidPath(cfg), []byte("1"), 0o600)
	if pid, err := signalDaemon(cfg, syscall.SIGHUP); err != nil || pid != 1 || len(sent) != 1 {
		t.Fatalf("a container's conductor at pid 1: %d %v %v", pid, err, sent)
	}
}

// The real check: this test binary is not conductor; a pid that does not
// exist is not either.
func TestIsConductorProcess(t *testing.T) {
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("no /proc")
	}
	if isConductorProcess(os.Getpid()) {
		t.Fatal("the test binary counted as conductor")
	}
	if isConductorProcess(1 << 30) {
		t.Fatal("a pid that does not exist counted as conductor")
	}
}
