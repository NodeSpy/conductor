package main

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// Side-by-side versions (docs/wiki/Plugins.md): `plugin list`/`plugin show`
// must show EACH resolved version of a plugin pinned side by side, and
// which configured connector(s) use it — never collapse two versions into
// one row/section that can only speak for whichever was found first.

// putTwoVersions records two installed versions of the SAME plugin key —
// standing in for what `conductor init` would have fetched for two
// connectors pinning different versions of one plugin.
func putTwoVersions(t *testing.T, key string) {
	t.Helper()
	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{Key: key, Kind: config.PluginKindConnector, Name: "widget", Resolved: "widget/v1.0.0", Sha256: "aaaa", Path: "/fake/v1/conductor-widget"})
	state.Put(plugin.Installed{Key: key, Kind: config.PluginKindConnector, Name: "widget", Resolved: "widget/v2.0.0", Sha256: "bbbb", Path: "/fake/v2/conductor-widget"})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
}

func TestCmdPluginListShowsBothSidesBySideVersions(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	args := writeCfg(t, `
connectors:
  stable: { use: acme/plugins/widget@=1.0.0 }
  canary: { use: acme/plugins/widget@=2.0.0 }
`)
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatal(err)
	}
	refs := cfg.PluginRefs()
	var key string
	for k := range refs {
		key = k
	}
	putTwoVersions(t, key)

	out, err := captureStdout(t, func() error { return cmdPluginList(args) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "widget/v1.0.0") || !strings.Contains(out, "widget/v2.0.0") {
		t.Fatalf("plugin list must show BOTH resolved versions, got:\n%s", out)
	}
	// Each row must say which connector(s) it serves, so two rows of the
	// same plugin name are tellable apart.
	if !strings.Contains(out, "stable") || !strings.Contains(out, "canary") {
		t.Fatalf("plugin list must name which connector each version serves, got:\n%s", out)
	}
}

func TestCmdPluginShowListsEachVersionGroup(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	args := writeCfg(t, `
connectors:
  stable: { use: acme/plugins/widget@=1.0.0 }
  canary: { use: acme/plugins/widget@=2.0.0 }
`)
	cfg, _, err := loadConfig(args)
	if err != nil {
		t.Fatal(err)
	}
	refs := cfg.PluginRefs()
	var key string
	for k := range refs {
		key = k
	}
	putTwoVersions(t, key)

	// The fake paths above are not real binaries, so each group's live-spawn
	// verb printing fails — that failure is expected and reported inline
	// (see cmdPluginShow), not fatal to the test: what this test checks is
	// that BOTH version groups' headers and install-state info print
	// regardless, never one hiding the other.
	out, _ := captureStdout(t, func() error { return cmdPluginShow(append(append([]string(nil), args...), "widget")) })
	if !strings.Contains(out, "version 1 of 2") || !strings.Contains(out, "version 2 of 2") {
		t.Fatalf("plugin show must present both version groups distinctly, got:\n%s", out)
	}
	if !strings.Contains(out, "widget/v1.0.0") || !strings.Contains(out, "widget/v2.0.0") {
		t.Fatalf("plugin show must print both resolved builds, got:\n%s", out)
	}
}
