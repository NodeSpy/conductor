package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// buildAcmeInstanceRetagged is buildAcmeInstance with an extra, purely
// cosmetic ELF build-id note (-B) baked in via a distinct hex value — the
// SAME declared behavior and Decl (acme-instance's Desc text embeds
// `variant` verbatim, so even a differently-SPELLED but same-meaning variant
// string would change the per-instance declaration and defeat this test's
// "pure retag" premise) but a GUARANTEED different compiled binary, and so a
// different sha256, regardless of the toolchain's own build reproducibility
// (two plain `go build` runs of identical source can and did produce
// byte-identical output here, which a retag/rebuild test must not rely on
// NOT happening).
func buildAcmeInstanceRetagged(t *testing.T, buildIDHex string) (path, sum string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "acme-instance-retag-"+buildIDHex)
	cmd := exec.Command("go", "build",
		"-ldflags", "-X main.variant=v1 -B 0x"+buildIDHex,
		"-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-instance")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build acme-instance (retag %s): %v\n%s", buildIDHex, err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(data)
	return bin, hex.EncodeToString(s[:])
}

// TestReloadMovedSideBySideUsesItsOwnGroupsInstallRecord is a TEST GAP at
// reload.go ~82 (reloadMoved's `state.GetVersion(r.Key, r.Tag)` call):
// with side-by-side versions (docs/wiki/Plugins.md), TWO groups can share
// the same install Key ("connectors/acme-instance") while differing only in
// Resolved — reloading the group named by one moved Resolution must fetch
// THAT group's own (Key, Resolved) record, never a sibling group's, even
// though both live under the identical Key.
//
// Two connector instances pin DIFFERENT versions of acme-instance (1.0.0
// and 2.0.0) — two coexisting groups, two live clients. Only the 1.0.0
// group's install record is then replaced with a freshly-built binary
// UNDER THE SAME RESOLVED TAG (a retag/rebuild, not a version bump — the
// one shape that keeps a side-by-side group's own discriminator, and so
// its GroupKey, stable enough to reload in place at all; a version bump
// changes the group's own key and always forces a restart, by design).
// reloadMoved must swap the 1.0.0 group onto the new build and leave the
// 2.0.0 group's own live client completely untouched.
func TestReloadMovedSideBySideUsesItsOwnGroupsInstallRecord(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	u, err := config.ParseUse(config.UseKindConnector, "acme/acme-instance")
	if err != nil {
		t.Fatal(err)
	}
	key := u.InstallKey() // "connectors/acme-instance"
	t.Cleanup(func() { connector.UnregisterExternalType(u.Name) })

	binA1, sumA1 := buildAcmeInstanceRetagged(t, "1111111111111111")
	binB1, sumB1 := buildAcmeInstance(t, "v1")

	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{
		Key: key, Kind: config.PluginKindConnector, Name: "acme-instance",
		Use: "acme/acme-instance@1.0.0", Resolved: "1.0.0", Path: binA1, Sha256: sumA1,
	})
	state.Put(plugin.Installed{
		Key: key, Kind: config.PluginKindConnector, Name: "acme-instance",
		Use: "acme/acme-instance@2.0.0", Resolved: "2.0.0", Path: binB1, Sha256: sumB1,
	})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{ConnectorsMap: map[string]config.ConnectorRef{
		"stable": {Use: "acme/acme-instance@=1.0.0"},
		"canary": {Use: "acme/acme-instance@2.0.0"},
	}}

	sec := secrets.New()
	mgr, err := loadConnectorPlugins(cfg, sec, func(map[string]any) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mgr.Close() })

	deps := connector.Deps{Secrets: sec, Log: func(string, ...any) {}, Config: cfg, Auth: connector.NewAuthRegistry()}
	reg, err := connector.Build(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"stable", "canary"} {
		if in, ok := reg.Get(n); !ok || in.DisabledReason != "" {
			t.Fatalf("instance %s did not build cleanly: ok=%v disabled=%q", n, ok, in.DisabledReason)
		}
	}

	groupA, groupB := key+"@1.0.0", key+"@2.0.0"
	if !mgr.HasLiveClient(groupA) || !mgr.HasLiveClient(groupB) {
		t.Fatalf("expected two live side-by-side groups, got HasLiveClient(%s)=%v HasLiveClient(%s)=%v",
			groupA, mgr.HasLiveClient(groupA), groupB, mgr.HasLiveClient(groupB))
	}
	clientB, ok := mgr.Client(groupB)
	if !ok {
		t.Fatalf("expected a live shared client for %s", groupB)
	}
	if err := clientB.Start(context.Background()); err != nil {
		t.Fatalf("starting group B's client: %v", err)
	}
	shaBBefore := clientB.Digest()
	if shaBBefore != sumB1 {
		t.Fatalf("sanity: group B's client sha = %q, want %q", shaBBefore, sumB1)
	}

	// Retag/rebuild group A's version — the SAME Resolved tag "1.0.0", a NEW
	// binary (new sha, declaration-identical). Group B's own record is left
	// completely alone.
	binA2, sumA2 := buildAcmeInstanceRetagged(t, "2222222222222222")
	if sumA2 == sumA1 {
		t.Fatal("test fixture assumption violated: two separate builds must not share a sha")
	}
	state2 := plugin.LoadInstallState(plugin.InstallDir())
	state2.Put(plugin.Installed{
		Key: key, Kind: config.PluginKindConnector, Name: "acme-instance",
		Use: "acme/acme-instance@1.0.0", Resolved: "1.0.0", Path: binA2, Sha256: sumA2,
	})
	if err := state2.Save(); err != nil {
		t.Fatal(err)
	}

	moved := []plugin.Resolution{{
		Key: key, GroupKey: groupA, Name: "acme-instance",
		Tag: "1.0.0", Action: plugin.ActionUpdated,
	}}
	if ok := reloadMoved(cfg, mgr, reg, moved); !ok {
		t.Fatal("expected the retagged side-by-side group to reload in place")
	}

	clientA, ok := mgr.Client(groupA)
	if !ok {
		t.Fatalf("expected a live shared client for %s after reload", groupA)
	}
	// Reload lazily tears down the old process (digest reset); force the
	// respawn so Digest() reports the build actually running now.
	if err := clientA.Start(context.Background()); err != nil {
		t.Fatalf("starting group A's client after reload: %v", err)
	}
	if got := clientA.Digest(); got != sumA2 {
		t.Fatalf("group A must be reloaded onto ITS OWN new build: got sha %q, want %q", got, sumA2)
	}

	// Group B's client must be completely unaffected — proving
	// state.GetVersion(key, "1.0.0") never cross-read group B's (key,
	// "2.0.0") record.
	clientBAfter, ok := mgr.Client(groupB)
	if !ok {
		t.Fatalf("expected group B's live client to still exist")
	}
	if got := clientBAfter.Digest(); got != sumB1 {
		t.Fatalf("group B must be untouched by group A's reload: got sha %q, want the original %q", got, sumB1)
	}
}
