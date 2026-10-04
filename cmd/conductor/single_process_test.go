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

// TestCheckIsolateAgainstKnownSingleProcessRefusesBoot is the HARD,
// ahead-of-time half: when a plugin's RECORDED install manifest already says
// single_process (no live describe needed to learn it), isolate: true on any
// configured instance is a config validation error that refuses boot
// outright — never a soft per-instance disable, since the conflict was
// knowable before anything spawned.
func TestCheckIsolateAgainstKnownSingleProcessRefusesBoot(t *testing.T) {
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

	err := checkIsolateAgainstKnownSingleProcess(mgr, map[string]config.PluginRef{key: ref})
	if err == nil {
		t.Fatal("expected a config validation error for isolate: true against a known single_process plugin")
	}
	if !strings.Contains(err.Error(), "isolated") || !strings.Contains(err.Error(), "tailscale") {
		t.Fatalf("error must name the isolated connector and the plugin, got: %v", err)
	}

	// A config with no isolated instance of the same known single_process
	// plugin is unaffected.
	ref2 := ref
	ref2.Instances = map[string]config.ConnectorGrant{"shared": {}, "shared2": {}}
	mgr2 := plugin.NewManager(map[string]config.PluginRef{key: ref2}, "", state, plugin.Deps{})
	defer mgr2.Close()
	if err := checkIsolateAgainstKnownSingleProcess(mgr2, map[string]config.PluginRef{key: ref2}); err != nil {
		t.Fatalf("no isolated instance must pass: %v", err)
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
