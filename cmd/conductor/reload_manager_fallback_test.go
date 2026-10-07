package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// setupMultiInstanceReloadFixture is setupReloadFixture (reload_inplace_test.go)
// generalized to N configured instances of the SAME acme-instance plugin, all
// on the SAME variant for "old" and "new" — every reloadMoved pre-check
// (describe, SameReloadSurface, per-instance Q6) therefore passes cleanly,
// isolating the test to the final apply loop's call into mgr.Reload.
func setupMultiInstanceReloadFixture(t *testing.T, variant string, instanceNames ...string) (cfg *config.Config, mgr *plugin.Manager, reg *connector.Registry, key string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	u, err := config.ParseUse(config.UseKindConnector, "acme/acme-instance")
	if err != nil {
		t.Fatal(err)
	}
	key = u.InstallKey()
	t.Cleanup(func() { connector.UnregisterExternalType(u.Name) })

	bin, sum := buildAcmeInstance(t, variant)
	putInstalled(t, key, bin, sum)

	// isolate: true on every instance: this test exercises Manager.Reload's
	// serial swap across more than one LIVE per-instance client, which under
	// the new default (one shared process) would otherwise collapse to a
	// single client — isolate: true keeps each instance on its own process,
	// exactly as multi-instance isolation's opt-in path.
	refs := map[string]config.ConnectorRef{}
	for _, n := range instanceNames {
		refs[n] = config.ConnectorRef{Use: "acme/acme-instance", Isolate: true}
	}
	cfg = &config.Config{ConnectorsMap: refs}

	sec := secrets.New()
	mgr, err = loadConnectorPlugins(cfg, sec, func(map[string]any) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })

	deps := connector.Deps{Secrets: sec, Log: func(string, ...any) {}, Config: cfg, Auth: connector.NewAuthRegistry()}
	reg, err = connector.Build(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range instanceNames {
		if in, ok := reg.Get(n); !ok || in.DisabledReason != "" {
			t.Fatalf("instance %s did not build cleanly: ok=%v disabled=%q", n, ok, in.DisabledReason)
		}
	}
	// Point install state at a freshly built copy of the SAME variant — the
	// "new build" a dependency refresh would have fetched just before
	// reloadMoved runs, same as setupReloadFixture. Same variant means the
	// moved plugin's declared surface is unchanged, so only the final apply
	// loop (mgr.Reload) is exercised, not the describe/SameReloadSurface/Q6
	// pre-checks above it.
	newBin, newSum := buildAcmeInstance(t, variant)
	putInstalled(t, key, newBin, newSum)

	return cfg, mgr, reg, key
}

// TestReloadMovedFallsBackToRestartOnManagerReloadError is the item-8
// regression. Manager.Reload (internal/plugin/manager.go) swaps every live
// per-instance client SERIALLY with NO rollback — its own doc comment says a
// per-instance key with more than one live client "can therefore end up
// PARTIALLY swapped on error: some instances already on the new build,
// others not" — and calls that "safe only because the caller's one response
// to ANY Reload error is a full daemon restart (reloadMoved), which
// re-resolves every instance fresh."
//
// This proves that guarantee holds at the actual call site: force ONE of
// several live per-instance clients to refuse Reload (Client.Reload returns
// ErrReloadUnsupported for a client with an active source stream — onEvent
// set) and confirm reloadMoved returns false (the caller's signal to
// restart) regardless of which instance failed or where in Manager.Reload's
// (map-ordered, so unspecified) iteration it sits.
//
// The onEvent side-channel is reached through the public API alone:
// Client.StartSource (internal/plugin/client.go startSource) records the
// emit callback into c.onEvent BEFORE it ever calls the plugin, so even
// though acme-instance does not implement start_source at all (the call
// itself fails, method not found), onEvent is left set regardless — exactly
// the condition Client.Reload's ErrReloadUnsupported check reads.
func TestReloadMovedFallsBackToRestartOnManagerReloadError(t *testing.T) {
	cfg, mgr, reg, key := setupMultiInstanceReloadFixture(t, "v1", "a", "b")

	clients := mgr.InstanceClients(key)
	if len(clients) != 2 {
		t.Fatalf("expected 2 per-instance clients, got %d: %+v", len(clients), clients)
	}
	var forced string
	for name, cl := range clients {
		forced = name
		_ = cl.StartSource(context.Background(), plugin.StartSourceRequest{Instance: name}, func(json.RawMessage) {})
		break // exactly one forced instance is enough to prove the guarantee
	}

	moved := []plugin.Resolution{{Key: key, GroupKey: key, Name: "acme-instance", Action: plugin.ActionUpdated}}
	if ok := reloadMoved(cfg, mgr, reg, moved); ok {
		t.Fatalf("a mid-loop Manager.Reload failure (forced on instance %q) must fall back to a restart (reloadMoved returning false), got an in-place success", forced)
	}
}
