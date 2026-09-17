package main

import (
	"os"
	"testing"
)

// TestMain isolates the plugin/pack install-state directory for the WHOLE
// cmd/conductor test package. Several tests drive real CLI commands — `conductor
// init`, `pack update`, `plugin update` — which RECONCILE install state to the
// config they are handed and prune records that config does not reference. Run
// against a developer's or CI runner's actual ~/.local/state/conductor, a test
// whose fixture config references (say) no `js` engine would delete the real js
// plugin from the live manifest — silently breaking the running daemon on its
// next restart. That is exactly the recurring "js not installed" wipe.
//
// config.StateDir() resolves override → XDG_STATE_HOME → ~/.local/state, so
// pointing XDG_STATE_HOME at a throwaway dir here guarantees no test in this
// package can touch the real manifest, regardless of whether the individual test
// remembered to pass --state-dir. Tests that set their own state dir still win.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "conductor-cmd-teststate")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", tmp)
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}
