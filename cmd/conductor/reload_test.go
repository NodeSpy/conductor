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
	old := signalProcess
	signalProcess = func(pid int, sig syscall.Signal) error { sent = append(sent, pid); return nil }
	t.Cleanup(func() { signalProcess = old })
	dir := t.TempDir()
	cfg := &config.Config{Store: config.Store{StateFile: dir + "/state.json"}}
	for _, bad := range []string{"-1", "0", "1", "-4242", "abc", ""} {
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
