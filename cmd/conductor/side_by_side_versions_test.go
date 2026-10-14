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

// buildAcmeEchoPing compiles test/plugins/acme-echo with pingVerb baked in
// at the given value — two "releases" of the same plugin that disagree
// about their declared verb surface, exactly like two real releases would.
func buildAcmeEchoPing(t *testing.T, ping bool) (path, sum string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "acme-echo")
	args := []string{"build", "-o", bin}
	if ping {
		args = append(args, "-ldflags", "-X main.pingVerb=true")
	}
	args = append(args, "github.com/NodeSpy/conductor/test/plugins/acme-echo")
	cmd := exec.Command("go", args...)
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build acme-echo (ping=%v): %v\n%s", ping, out, err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(data)
	return bin, hex.EncodeToString(s[:])
}

// TestTwoConnectorsPinnedToDifferentVersionsEachGetOwnDeclAndProcess is the
// registry-level half of the side-by-side-versions regression (the
// internal/plugin fetch/install path is covered by internal/plugin/
// sidebyside_test.go): two connectors pinning DIFFERENT versions of the
// SAME remote plugin name, with both versions already installed (standing
// in for what `conductor init` would have fetched), must each build through
// connector.Build with THEIR OWN pinned version's declaration — "a" (pinned
// =1.0.0) never sees "ping" (only the =2.0.0 build declares it) — and run as
// two separate subprocesses. Before the fix, config.PluginRefs folded both
// connectors into ONE PluginRef keyed by name alone, and internal/
// connector's type registry had exactly one TypeDecl/Builder per type name
// — whichever connector's Use PluginRefs happened to see first silently
// decided the version (and the live process) for BOTH.
func TestTwoConnectorsPinnedToDifferentVersionsEachGetOwnDeclAndProcess(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	binV1, sumV1 := buildAcmeEchoPing(t, false)
	binV2, sumV2 := buildAcmeEchoPing(t, true)
	t.Cleanup(func() { connector.UnregisterExternalType("acme-echo") })

	installKey := "connectors/acme-echo"
	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{Key: installKey, Kind: config.PluginKindConnector, Name: "acme-echo", Resolved: "acme-echo/v1.0.0", Sha256: sumV1, Path: binV1})
	state.Put(plugin.Installed{Key: installKey, Kind: config.PluginKindConnector, Name: "acme-echo", Resolved: "acme-echo/v2.0.0", Sha256: sumV2, Path: binV2})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	doc := "connectors:\n" +
		"  a:\n    use: acme/plugins/acme-echo@=1.0.0\n" +
		"  b:\n    use: acme/plugins/acme-echo@=2.0.0\n"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadConfig([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}

	sec := secrets.New()
	mgr, err := loadConnectorPlugins(cfg, sec, func(map[string]any) {}, nil)
	if err != nil {
		t.Fatalf("loadConnectorPlugins: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })

	deps := connector.Deps{Secrets: sec, Log: func(string, ...any) {}, Config: cfg, Auth: connector.NewAuthRegistry()}
	reg, err := connector.Build(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := reg.Get("a")
	if !ok || a.DisabledReason != "" {
		t.Fatalf("instance a did not build cleanly: ok=%v disabled=%q", ok, a.DisabledReason)
	}
	b, ok := reg.Get("b")
	if !ok || b.DisabledReason != "" {
		t.Fatalf("instance b did not build cleanly: ok=%v disabled=%q", ok, b.DisabledReason)
	}

	hasPing := func(d *connector.TypeDecl) bool {
		if d == nil {
			return false
		}
		for _, v := range d.Verbs {
			if v.Name == "ping" {
				return true
			}
		}
		return false
	}
	if hasPing(a.Decl) {
		t.Error("instance a (pinned v1.0.0) must NOT declare ping — that would be the silent-first-wins bug")
	}
	if !hasPing(b.Decl) {
		t.Error("instance b (pinned v2.0.0) must declare ping")
	}

	// And it actually WORKS, not just declares: b can call ping, a cannot.
	if _, err := b.Invoke(context.Background(), "ping", map[string]any{}); err != nil {
		t.Errorf("instance b: ping should succeed on its own pinned build: %v", err)
	}
	if _, err := a.Invoke(context.Background(), "ping", map[string]any{}); err == nil {
		t.Error("instance a: ping must fail — its pinned build never implements it")
	}

	// Two different pinned versions must run as two different processes —
	// never one silently sharing with the other.
	groupOf := map[string]string{}
	for _, spec := range mgr.ConnectorSpecs() {
		for inst := range spec.Instances {
			groupOf[inst] = spec.GroupKey
		}
	}
	if groupOf["a"] == "" || groupOf["b"] == "" {
		t.Fatalf("expected both instances bound to a group, got %+v", groupOf)
	}
	if groupOf["a"] == groupOf["b"] {
		t.Fatalf("different pinned versions must land in different groups, both got %s", groupOf["a"])
	}
	clientA, _ := mgr.InstanceClient(groupOf["a"], "a")
	clientB, _ := mgr.InstanceClient(groupOf["b"], "b")
	if clientA.PID() == clientB.PID() {
		t.Fatalf("expected two distinct subprocess pids, got the same: %d", clientA.PID())
	}
}
