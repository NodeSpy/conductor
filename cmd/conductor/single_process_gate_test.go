package main

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// TestSingleProcessConflictsForCatchesSideBySideVersions is finding 3's
// pre-restart-gate half: singleProcessConflictsFor — the exact function both
// `conductor validate` (main.go) and the auto-update pre-restart gate
// (refreshDeps, update.go) call — must report a side-by-side single_process
// conflict against a REAL *config.Config and the actual on-disk install
// state (plugin.InstallDir(), via XDG_STATE_HOME), with no network needed:
// the conflict is a property of what's already resolved/installed, exactly
// what refreshDeps checks AFTER a plugin refresh and BEFORE deciding to
// restart into it.
func TestSingleProcessConflictsForCatchesSideBySideVersions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	cfg := &config.Config{
		ConnectorsMap: map[string]config.ConnectorRef{
			"a": {Use: "acme/plugins/tailscale@~>1.0.0"},
			"b": {Use: "acme/plugins/tailscale@~>2.0.0"},
		},
	}

	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{Key: "connectors/tailscale", Resolved: "tailscale/1.0.0", Path: "/bin/true", Manifest: plugin.Manifest{SingleProcess: true}})
	state.Put(plugin.Installed{Key: "connectors/tailscale", Resolved: "tailscale/2.0.0", Path: "/bin/true", Manifest: plugin.Manifest{SingleProcess: true}})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	conflicts := singleProcessConflictsFor(cfg)
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 single_process conflict against the real install state, got %d: %+v", len(conflicts), conflicts)
	}
	if conflicts[0].reason == "" {
		t.Fatal("conflict must carry a non-empty reason naming the plugin")
	}

	// Once the operator pins both connectors to the SAME version (the fix
	// the reason suggests), the exact same gate reports no conflict.
	cfg2 := &config.Config{
		ConnectorsMap: map[string]config.ConnectorRef{
			"a": {Use: "acme/plugins/tailscale@~>1.0.0"},
			"b": {Use: "acme/plugins/tailscale@~>1.0.0"},
		},
	}
	if c := singleProcessConflictsFor(cfg2); len(c) != 0 {
		t.Fatalf("both connectors pinned to the SAME version must report no conflict, got %+v", c)
	}
}
