package main

import (
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

// buildAcmeInstance compiles the reference acme-instance plugin fixture
// (test/plugins/acme-instance) with the given per-instance-decl variant baked
// in via -ldflags, simulating "one build" of a plugin for the in-place
// hot-reload tests below (plugin-contract.md §1.4 Q6).
func buildAcmeInstance(t *testing.T, variant string) (path, sum string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "acme-instance-"+variant)
	cmd := exec.Command("go", "build",
		"-ldflags", "-X main.variant="+variant,
		"-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-instance")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build acme-instance (%s): %v\n%s", variant, err, out)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(data)
	return bin, hex.EncodeToString(s[:])
}

// putInstalled records a plugin build at key in the install state the test's
// XDG_STATE_HOME points at — standing in for what a real `conductor init`/
// dependency-refresh fetch would have written.
func putInstalled(t *testing.T, key, bin, sum string) {
	t.Helper()
	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{
		Key: key, Kind: config.PluginKindConnector, Name: "acme-instance",
		Use: "acme/acme-instance", Path: bin, Sha256: sum,
	})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
}

// setupReloadFixture boots one "acme-instance" connector instance against
// oldVariant's build (mirroring what the running daemon already has live),
// then points install state at newVariant's build (mirroring a dependency
// refresh having just fetched it) — the exact setup reloadMoved sees when a
// moved plugin is reported.
func setupReloadFixture(t *testing.T, oldVariant, newVariant string) (cfg *config.Config, mgr *plugin.Manager, reg *connector.Registry, key string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	u, err := config.ParseUse(config.UseKindConnector, "acme/acme-instance")
	if err != nil {
		t.Fatal(err)
	}
	key = u.InstallKey()
	t.Cleanup(func() { connector.UnregisterExternalType(u.Name) })

	oldBin, oldSum := buildAcmeInstance(t, oldVariant)
	putInstalled(t, key, oldBin, oldSum)

	cfg = &config.Config{ConnectorsMap: map[string]config.ConnectorRef{
		"inst1": {Use: "acme/acme-instance"},
	}}

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
	if in, ok := reg.Get("inst1"); !ok || in.DisabledReason != "" {
		t.Fatalf("instance did not build cleanly: ok=%v disabled=%q", ok, in.DisabledReason)
	}

	// Now move install state to the "new build", as a dependency refresh
	// would just before calling reloadMoved.
	newBin, newSum := buildAcmeInstance(t, newVariant)
	putInstalled(t, key, newBin, newSum)

	return cfg, mgr, reg, key
}

// TestReloadMovedSwapsUnchangedInstanceDeclInPlace: two builds that declare
// the SAME per-instance decl (Q6) for a live instance swap in place — no
// restart needed.
func TestReloadMovedSwapsUnchangedInstanceDeclInPlace(t *testing.T) {
	cfg, mgr, reg, key := setupReloadFixture(t, "v1", "v1")
	moved := []plugin.Resolution{{Key: key, Name: "acme-instance", Action: plugin.ActionUpdated}}
	if ok := reloadMoved(cfg, mgr, reg, moved); !ok {
		t.Fatal("an unchanged per-instance declaration must reload in place")
	}
}

// TestReloadMovedRestartsOnChangedInstanceDecl: this is the finding-1
// regression — the acme-instance type decl never changes (no verbs/events at
// all, the rest/graphql shape), so a type-level-only reload-surface check
// always passes; only this instance's own plugin.describe {instance} answer
// changes between the "v1" and "v2" builds (a new option on its one verb).
// reloadMoved must detect that and fall back to a restart (return false)
// rather than swap the process in place and keep serving the live instance
// under its stale cached per-instance Decl.
func TestReloadMovedRestartsOnChangedInstanceDecl(t *testing.T) {
	cfg, mgr, reg, key := setupReloadFixture(t, "v1", "v2")
	moved := []plugin.Resolution{{Key: key, Name: "acme-instance", Action: plugin.ActionUpdated}}
	if ok := reloadMoved(cfg, mgr, reg, moved); ok {
		t.Fatal("a changed per-instance declaration must force a restart, not an in-place swap")
	}
}
