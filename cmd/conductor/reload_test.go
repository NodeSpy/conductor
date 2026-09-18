package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// TestReloadMoved drives the orchestrator end to end against a real Backend-RPC
// plugin subprocess: a same-surface move reloads in place (no restart), and a
// moved pack (not a live plugin) forces a restart. XDG_STATE_HOME is isolated by
// TestMain, so seeding install state here can't touch the real installed.yaml.
func TestReloadMoved(t *testing.T) {
	bin := buildTestPlugin(t, "acme-runtime")
	sum := sha256Of(t, bin)
	// A REMOTE-shaped runtime plugin so SpecFromRef resolves the binary from
	// install state (and it gets a live Backend-RPC client to reload).
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
		"rt": {Use: "acme/plugins/acme-runtime"},
	}}
	var key, name string
	for k, ref := range cfg.PluginRefs() {
		key, name = k, ref.Name
	}
	if key == "" {
		t.Fatal("no plugin ref derived from config")
	}

	st := plugin.LoadInstallState(plugin.InstallDir())
	st.Put(plugin.Installed{Key: key, Kind: "runtime", Name: name,
		Use: "acme/plugins/acme-runtime", Path: bin, Sha256: sum, Resolved: "v1.0.0"})
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	mgr := pluginManagerFor(cfg, secrets.New(), func(map[string]any) {})
	t.Cleanup(func() { _ = mgr.Close() })
	if _, err := loadRuntimePlugins(mgr, cfg, config.Retry{}); err != nil {
		t.Fatalf("loadRuntimePlugins: %v", err)
	}

	moved := []plugin.Resolution{{Key: key, Name: name, Action: plugin.ActionUpdated, Path: bin, Sha: sum}}
	if !reloadMoved(cfg, mgr, moved) {
		t.Fatal("a same-surface plugin move must reload in place (return true)")
	}
	pack := []plugin.Resolution{{Key: "packs/whatever", Name: "whatever", Action: plugin.ActionUpdated}}
	if reloadMoved(cfg, mgr, pack) {
		t.Fatal("a moved pack (not a live plugin) must force a restart (return false)")
	}
}
