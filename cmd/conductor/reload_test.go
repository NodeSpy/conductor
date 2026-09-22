package main

import (
	"strings"
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
