package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// buildSingleProcessEcho compiles test/plugins/acme-echo with its
// singleProcess build flag set — the single_process capability's fixture
// (pkg/plugin/wire.go Capabilities.SingleProcess). Since sharing one process
// is now the DEFAULT (docs/wiki/Plugins.md "Multi-instance isolation"), a
// single_process plugin configured with no isolate: true needs no special
// handling to end up on one process — these tests instead prove that, and
// that isolate: true is refused for it (loadConnectorPlugins,
// Manager.ForbidIsolated / checkIsolateAgainstKnownSingleProcess).
func buildSingleProcessEcho(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	bin := filepath.Join(t.TempDir(), "acme-echo")
	cmd := exec.Command("go", "build",
		"-ldflags", "-X main.singleProcess=true",
		"-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-echo")
	cmd.Env = os.Environ()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build single_process acme-echo: %v\n%s", err, out)
	}
	return bin
}

// captureLogf redirects os.Stderr (what logf writes to) for the duration of
// fn and returns everything logged.
func captureLogf(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return string(out)
}

// TestLoadConnectorPluginsSharesSingleProcessPluginByDefault is the
// single_process capability's end-to-end proof at the cmd/conductor layer
// under the NEW default: TWO configured instances of a plugin that declares
// Capabilities.SingleProcess, with no isolate: true anywhere, are registered
// successfully and served by the SAME underlying *plugin.Client — exactly
// the shape every other plugin's non-isolated instances already get, with
// no special-case "promotion" needed.
func TestLoadConnectorPluginsSharesSingleProcessPluginByDefault(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin := buildSingleProcessEcho(t)
	t.Cleanup(func() { connector.UnregisterExternalType("acme-echo") })

	doc := fmt.Sprintf("connectors:\n  a:\n    use: %s\n  b:\n    use: %s\n", bin, bin)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadConfig([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}

	sec := secrets.New()
	var mgr *plugin.Manager
	logged := captureLogf(t, func() {
		mgr, err = loadConnectorPlugins(cfg, sec, func(map[string]any) {}, nil)
	})
	if err != nil {
		t.Fatalf("loadConnectorPlugins: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })

	if !strings.Contains(logged, "acme-echo") || !strings.Contains(logged, "single_process") {
		t.Fatalf("expected a log line naming single_process for visibility, got:\n%s", logged)
	}

	key := "connectors/acme-echo"
	spec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("spec not found")
	}
	if !spec.Shared {
		t.Fatal("a connector with no isolated instance must be Shared = true, single_process or not")
	}

	factory := mgr.InstanceClientFactory(key)
	ca, err := factory("a")
	if err != nil {
		t.Fatal(err)
	}
	cb, err := factory("b")
	if err != nil {
		t.Fatal(err)
	}
	if ca != cb {
		t.Fatal("both configured instances must resolve to the SAME *Client")
	}

	// And both instances build cleanly through the real connector registry —
	// "both instances served", not just "the factory agrees".
	deps := connector.Deps{Secrets: sec, Log: func(string, ...any) {}, Config: cfg, Auth: connector.NewAuthRegistry()}
	reg, err := connector.Build(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		in, ok := reg.Get(n)
		if !ok || in.DisabledReason != "" {
			t.Fatalf("instance %s did not build cleanly: ok=%v disabled=%q", n, ok, in.DisabledReason)
		}
		out, err := in.Invoke(context.Background(), "echo", map[string]any{"message": "hi-" + n})
		if err != nil {
			t.Fatalf("instance %s: invoke: %v", n, err)
		}
		if out["message"] != "hi-"+n {
			t.Fatalf("instance %s: echo mismatch: %+v", n, out)
		}
	}
}

// TestLoadConnectorPluginsDisablesIsolatedInstanceOfSingleProcessPlugin is the
// late-discovery refusal end to end: one shared instance plus one isolate:
// true instance of a plugin whose single_process capability is learned only
// at this describe (no recorded manifest said so ahead of time). The shared
// instance must build and work normally; the isolated one must be disabled,
// never silently folded into the shared process.
func TestLoadConnectorPluginsDisablesIsolatedInstanceOfSingleProcessPlugin(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin := buildSingleProcessEcho(t)
	t.Cleanup(func() { connector.UnregisterExternalType("acme-echo") })

	doc := fmt.Sprintf("connectors:\n  a:\n    use: %s\n  b:\n    use: %s\n    isolate: true\n", bin, bin)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := loadConfig([]string{"--config", path})
	if err != nil {
		t.Fatal(err)
	}

	sec := secrets.New()
	var mgr *plugin.Manager
	logged := captureLogf(t, func() {
		mgr, err = loadConnectorPlugins(cfg, sec, func(map[string]any) {}, nil)
	})
	if err != nil {
		t.Fatalf("loadConnectorPlugins: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	if !strings.Contains(logged, "will be disabled") {
		t.Fatalf("expected a log line naming the disabled isolated instance, got:\n%s", logged)
	}

	deps := connector.Deps{Secrets: sec, Log: func(string, ...any) {}, Config: cfg, Auth: connector.NewAuthRegistry()}
	reg, err := connector.Build(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := reg.Get("a")
	if !ok || a.DisabledReason != "" {
		t.Fatalf("shared instance a must build cleanly: ok=%v disabled=%q", ok, a.DisabledReason)
	}
	if _, err := a.Invoke(context.Background(), "echo", map[string]any{"message": "hi"}); err != nil {
		t.Fatalf("instance a: invoke: %v", err)
	}
	b, ok := reg.Get("b")
	if !ok || b.DisabledReason == "" {
		t.Fatalf("isolated instance b (single_process plugin) must be disabled, got ok=%v disabled=%q", ok, b.DisabledReason)
	}
}

// TestSingleProcessConflictDegradesIsolatedInstanceNeverBootFatal is finding
// 3 (MEDIUM): a plugin's RECORDED install manifest already saying
// single_process, with isolate: true on one configured instance, used to be
// a HARD, boot-refusing config validation error (checkIsolateAgainstKnownSingleProcess).
// That is resolution-dependent — an auto-update can create or remove this
// shape with no config edit — so it must never take the whole daemon down:
// applySingleProcessConflicts (the boot path's degrade half) disables only
// the conflicting instance (via Manager.ForbidIsolated, the SAME mechanism
// the late-discovery path already used), while the shared instance of the
// same group is completely unaffected and reports no disabled group at all.
func TestSingleProcessConflictDegradesIsolatedInstanceNeverBootFatal(t *testing.T) {
	ref := config.PluginRef{
		Name: "tailscale",
		Use:  config.Use{Kind: config.UseKindConnector, Name: "tailscale", Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins"},
		Instances: map[string]config.ConnectorGrant{
			"shared":   {},
			"isolated": {Isolate: true},
		},
	}
	key := ref.Key()
	state := plugin.LoadInstallState(t.TempDir())
	state.Put(plugin.Installed{Key: key, Manifest: plugin.Manifest{SingleProcess: true}})
	mgr := plugin.NewManager(map[string]config.PluginRef{key: ref}, "", state, plugin.Deps{})
	defer mgr.Close()

	conflicts := singleProcessConflicts(mgr)
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict, got %d: %+v", len(conflicts), conflicts)
	}
	if !strings.Contains(conflicts[0].reason, "isolated") || !strings.Contains(conflicts[0].reason, "tailscale") {
		t.Fatalf("reason must name the isolated connector and the plugin, got: %v", conflicts[0].reason)
	}

	disabledGroups := applySingleProcessConflicts(mgr)
	if len(disabledGroups) != 0 {
		t.Fatalf("an isolate-vs-single_process conflict must never disable the whole GROUP (the shared instance is fine), got %+v", disabledGroups)
	}
	if _, err := mgr.InstanceClient(key, "isolated"); err == nil {
		t.Fatal("the isolated instance must be refused (ForbidIsolated) after applySingleProcessConflicts")
	}

	// A config with no isolated instance of the same known single_process
	// plugin is unaffected.
	ref2 := ref
	ref2.Instances = map[string]config.ConnectorGrant{"shared": {}, "shared2": {}}
	mgr2 := plugin.NewManager(map[string]config.PluginRef{key: ref2}, "", state, plugin.Deps{})
	defer mgr2.Close()
	if c := singleProcessConflicts(mgr2); len(c) != 0 {
		t.Fatalf("no isolated instance must report no conflicts, got: %+v", c)
	}
}

// TestSingleProcessConflictDisablesBothSideBySideVersions is the
// side-by-side-versions half of the same guard (docs/wiki/Plugins.md "Side-
// by-side versions"): a single_process plugin keeps one box-global resource
// only ONE process can own (the design doc's example is a tailscale funnel),
// so two connectors configured to run two DIFFERENT resolved versions of it
// side by side — two groups sharing one install key — has no partial fix;
// applySingleProcessConflicts disables BOTH groups (never the whole daemon),
// naming every connector involved in the reason.
func TestSingleProcessConflictDisablesBothSideBySideVersions(t *testing.T) {
	installKey := "connectors/tailscale"
	refV1 := config.PluginRef{
		Name: "tailscale",
		Use:  config.Use{Kind: config.UseKindConnector, Name: "tailscale", Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins", Version: "=1.0.0"},
		Instances: map[string]config.ConnectorGrant{
			"a": {Use: config.Use{Kind: config.UseKindConnector, Name: "tailscale", Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins", Version: "=1.0.0"}},
		},
	}
	refV2 := config.PluginRef{
		Name: "tailscale",
		Use:  config.Use{Kind: config.UseKindConnector, Name: "tailscale", Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins", Version: "=2.0.0"},
		Instances: map[string]config.ConnectorGrant{
			"b": {Use: config.Use{Kind: config.UseKindConnector, Name: "tailscale", Origin: config.OriginGitHub, Host: "github.com", Repo: "acme/plugins", Version: "=2.0.0"}},
		},
	}
	state := plugin.LoadInstallState(t.TempDir())
	// Both versions installed, both declaring single_process (recorded, as a
	// real describe at install time would have).
	state.Put(plugin.Installed{Key: installKey, Resolved: "v1.0.0", Manifest: plugin.Manifest{SingleProcess: true}})
	state.Put(plugin.Installed{Key: installKey, Resolved: "v2.0.0", Manifest: plugin.Manifest{SingleProcess: true}})

	// Pre-exploded (two groups sharing one install key) — exactly what
	// pluginManagerForStack feeds NewManager once two versions are
	// configured side by side.
	gk1, gk2 := installKey+"@v1.0.0", installKey+"@v2.0.0"
	mgr := plugin.NewManager(map[string]config.PluginRef{
		gk1: refV1,
		gk2: refV2,
	}, "", state, plugin.Deps{})
	defer mgr.Close()

	conflicts := singleProcessConflicts(mgr)
	if len(conflicts) != 1 {
		t.Fatalf("expected exactly 1 conflict, got %d: %+v", len(conflicts), conflicts)
	}
	reason := conflicts[0].reason
	if !strings.Contains(reason, "tailscale") || !strings.Contains(reason, "a") || !strings.Contains(reason, "b") {
		t.Fatalf("reason must name the plugin and both connectors, got: %v", reason)
	}
	if !strings.Contains(reason, "single_process") {
		t.Fatalf("reason must explain single_process is the cause, got: %v", reason)
	}

	disabledGroups := applySingleProcessConflicts(mgr)
	if _, ok := disabledGroups[gk1]; !ok {
		t.Errorf("expected group %s disabled, got %+v", gk1, disabledGroups)
	}
	if _, ok := disabledGroups[gk2]; !ok {
		t.Errorf("expected group %s disabled, got %+v", gk2, disabledGroups)
	}
}

// TestPermissionLinesShowsSingleProcessReason is permissionLines'
// (cmd/conductor/plugins.go) display half: a plugin whose declared manifest
// says single_process, with one shared instance and one isolate: true
// instance, must show the shared process's union grant labelled with its
// serving instance(s), a line explaining single_process, AND the isolated
// instance's own grant on its own line.
func TestPermissionLinesShowsSingleProcessReason(t *testing.T) {
	ref := config.PluginRef{
		Name:    "tailscale",
		Use:     config.Use{Kind: config.UseKindConnector, Name: "tailscale"},
		Network: []string{"controlplane.tailscale.com:443"},
		Instances: map[string]config.ConnectorGrant{
			"ts":         {Network: []string{"controlplane.tailscale.com:443"}},
			"ts-isolate": {Network: []string{"other.tailscale.com:443"}, Isolate: true},
		},
	}
	spec := plugin.Spec{
		Name: "tailscale", Kind: plugin.KindConnector, Provides: "tailscale",
		Shared:   true,
		Manifest: plugin.Manifest{SingleProcess: true},
	}
	lines := permissionLines(ref, spec)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "shared process") {
		t.Fatalf("expected the shared-process grant line, got:\n%s", joined)
	}
	if !strings.Contains(joined, "single_process") {
		t.Fatalf("expected a line explaining the single_process override, got:\n%s", joined)
	}
	if !strings.Contains(joined, "controlplane.tailscale.com:443") {
		t.Fatalf("expected the union network on the shared grant line, got:\n%s", joined)
	}
	if !strings.Contains(joined, "ts-isolate") || !strings.Contains(joined, "other.tailscale.com:443") {
		t.Fatalf("expected the isolated instance's own grant line, got:\n%s", joined)
	}

	// Without the capability, the ordinary display stands, with NO
	// single_process line at all.
	plainRef := config.PluginRef{
		Name: "tailscale",
		Use:  config.Use{Kind: config.UseKindConnector, Name: "tailscale"},
		Instances: map[string]config.ConnectorGrant{
			"ts": {},
		},
	}
	plainSpec := plugin.Spec{Name: "tailscale", Kind: plugin.KindConnector, Provides: "tailscale", Shared: true}
	plainLines := strings.Join(permissionLines(plainRef, plainSpec), "\n")
	if strings.Contains(plainLines, "single_process") {
		t.Fatalf("a plugin that does not declare single_process must show no such line:\n%s", plainLines)
	}
}

// TestLoadConnectorPluginsRegistersBothSideBySideSingleProcessGroupsUnavailable
// is a TEST GAP at internal/connector/external.go ~919
// (RegisterUnavailableType, and the RegisterExternalTypeGroup groupKey/
// installKey collision logic it drives): this is
// TestSingleProcessConflictDisablesBothSideBySideVersions driven all the
// way through loadConnectorPlugins and the real connector registry, not
// just applySingleProcessConflicts' returned map.
//
// Both disabled groups call connector.RegisterUnavailableType with the SAME
// installKey (one plugin) but DIFFERENT groupKey (two resolved versions) —
// RegisterExternalTypeGroup's own collision guard ("two plugins cannot
// provide the same type") must recognize them as sibling groups of ONE
// plugin and allow BOTH registrations, never let the second one refuse
// because the first already claimed the type name. If the groupKey/
// installKey arguments were ever swapped, or RegisterExternalTypeGroup's
// installKey comparison broke, the second RegisterUnavailableType call
// would be refused, and reg.Get would show that instance not registered as
// disabled at all — an un-degraded hole where a connector should have come
// up loudly disabled.
func TestLoadConnectorPluginsRegistersBothSideBySideSingleProcessGroupsUnavailable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Cleanup(func() { connector.UnregisterExternalType("tailscale") })

	key := "connectors/tailscale"
	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{
		Key: key, Kind: config.PluginKindConnector, Name: "tailscale", Resolved: "v1.0.0",
		Path: "/bin/true", Manifest: plugin.Manifest{SingleProcess: true},
	})
	state.Put(plugin.Installed{
		Key: key, Kind: config.PluginKindConnector, Name: "tailscale", Resolved: "v2.0.0",
		Path: "/bin/true", Manifest: plugin.Manifest{SingleProcess: true},
	})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{ConnectorsMap: map[string]config.ConnectorRef{
		// A single-plugin repo (no monorepo component subpath), so its
		// release tags carry no prefix — matching the bare "v1.0.0"/
		// "v2.0.0" Resolved values recorded above.
		"a": {Use: "acme/tailscale@=1.0.0"},
		"b": {Use: "acme/tailscale@=2.0.0"},
	}}

	sec := secrets.New()
	var mgr *plugin.Manager
	var err error
	logged := captureLogf(t, func() {
		mgr, err = loadConnectorPlugins(cfg, sec, func(map[string]any) {}, nil)
	})
	if err != nil {
		t.Fatalf("loadConnectorPlugins: %v", err)
	}
	t.Cleanup(func() { mgr.Close() })
	if !strings.Contains(logged, "single_process") {
		t.Fatalf("expected a log line naming single_process, got:\n%s", logged)
	}

	// Both groups must have ACTUALLY registered under their OWN group key —
	// not just "some instance shows a disabled-looking reason", which a
	// groupKey/installKey argument swap can still produce by accident (the
	// second, refused registration leaves declFor's unbound-instance
	// fallback reusing the FIRST group's representative TypeDecl, which
	// also happens to be an unavailable one here and would otherwise make
	// this assertion pass for the wrong reason).
	groups := connector.TypeDeclsFor("tailscale")
	for _, gk := range []string{key + "@v1.0.0", key + "@v2.0.0"} {
		if _, ok := groups[gk]; !ok {
			t.Fatalf("expected group %q registered under its OWN key, got groups: %v", gk, keysOfTypeDecls(groups))
		}
	}
	if len(groups) != 2 {
		t.Fatalf("expected exactly 2 distinct registered groups, got %d: %v", len(groups), keysOfTypeDecls(groups))
	}

	deps := connector.Deps{Secrets: sec, Log: func(string, ...any) {}, Config: cfg, Auth: connector.NewAuthRegistry()}
	reg, err := connector.Build(cfg, deps)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		in, ok := reg.Get(n)
		if !ok {
			t.Fatalf("instance %s: not registered at all — the second side-by-side group's RegisterUnavailableType call was likely refused as a type collision", n)
		}
		if in.DisabledReason == "" {
			t.Fatalf("instance %s must be disabled (side-by-side single_process conflict), got no reason", n)
		}
		if !strings.Contains(in.DisabledReason, "single_process") {
			t.Fatalf("instance %s disabled reason must mention single_process, got %q", n, in.DisabledReason)
		}
	}
}

func keysOfTypeDecls(m map[string]*connector.TypeDecl) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
