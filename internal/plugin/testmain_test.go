package plugin

import (
	"os"
	"testing"
)

// TestMain isolates the plugin state directory for the whole package. A
// local-build plugin is snapshotted by content hash under
// config.PluginLocalSnapshotDir() (<state>/plugins/local) before it ever
// runs, and several tests here resolve local builds (acme-echo, widget) —
// run against a developer's real ~/.local/state/conductor they left a
// snapshot dir in the live daemon's plugin state on every run.
// config.StateDir() resolves override → XDG_STATE_HOME → ~/.local/state, so
// pointing XDG_STATE_HOME at a throwaway dir keeps every test off the real
// state; tests that set their own state dir still win.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "conductor-plugin-teststate")
	if err != nil {
		panic(err)
	}
	os.Setenv("XDG_STATE_HOME", tmp)
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}
