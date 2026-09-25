package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	agentmodels "github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// buildTestPlugin compiles one in-repo reference plugin (built against pkg/plugin)
// into a temp dir and returns its path.
func buildTestPlugin(t *testing.T, pkg string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, pkg)
	build := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/test/plugins/"+pkg)
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}
	return bin
}

func runtimePluginCfg(bin string) *config.Config {
	return &config.Config{Runtimes: map[string]config.RuntimeConfig{"rt": {Use: bin}}}
}

func loadOne(t *testing.T, cfg *config.Config) (map[string]runtimePluginBackend, error) {
	t.Helper()
	mgr := pluginManagerFor(cfg, secrets.New(), func(map[string]any) {})
	t.Cleanup(func() { _ = mgr.Close() })
	b, _, err := loadRuntimePlugins(mgr, cfg, config.Retry{}, secrets.New())
	return b, err
}

// A Backend-RPC runtime plugin (acme-runtime: full verb set, no declared kind)
// is adopted — kept running, wrapped as a dispatch.Backend that round-trips to
// the subprocess — and mergedControllersWithPlugins gives it a type:paseo slot,
// not an ACP command.
func TestLoadRuntimePlugins_BackendRPCAdopted(t *testing.T) {
	cfg := runtimePluginCfg(buildTestPlugin(t, "acme-runtime"))
	backends, err := loadOne(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	rb, ok := backends["rt"]
	if !ok {
		t.Fatalf("acme-runtime should be adopted as a Backend-RPC runtime, got %v", backends)
	}
	if rb.Backend == nil {
		t.Fatal("adopted runtime must carry a non-nil Backend")
	}
	// The end-to-end RPC round-trip over a real subprocess is proven by
	// internal/dispatch's rpc_backend_integration_test; here we assert
	// classification + wiring. (The boot-ctx dial is torn down when this func
	// returns, exactly like the connector path — the first real dispatch/reaper
	// tick re-dials, so an immediate call here would race that async teardown.)
	merged, err := mergedControllersWithPlugins(cfg, backends, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cc := merged["rt"]; cc.Type != "paseo" || len(cc.Command) != 0 {
		t.Fatalf("adopted runtime must be a type:paseo slot with no ACP command, got %+v", cc)
	}
}

// An engine plugin (declares Kind:step) referenced under runtimes: is refused —
// the running binary's declared kind must match the block.
func TestLoadRuntimePlugins_KindMismatchRefused(t *testing.T) {
	_, err := loadOne(t, runtimePluginCfg(buildTestPlugin(t, "acme-engine")))
	if err == nil || !strings.Contains(err.Error(), "describes itself as") {
		t.Fatalf("an engine under runtimes: must be refused, got %v", err)
	}
}

// An ACP-dialect runtime plugin (acme-echo: no Backend verbs) is neither adopted
// nor refused — it falls through to the ACP controller path unchanged.
func TestLoadRuntimePlugins_ACPFallsThrough(t *testing.T) {
	cfg := runtimePluginCfg(buildTestPlugin(t, "acme-echo"))
	backends, err := loadOne(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backends["rt"]; ok {
		t.Fatal("acme-echo (no Backend verbs) must not be adopted as Backend-RPC")
	}
	merged, err := mergedControllersWithPlugins(cfg, backends, nil)
	if err != nil {
		t.Fatal(err)
	}
	if merged["rt"].Transport != "acp" {
		t.Fatalf("acme-echo should be wrapped as an ACP controller, got %+v", merged["rt"])
	}
}

// host: on a Backend-RPC runtime plugin is refused for v1 (no remoting story for
// a local-stdio plugin.Client).
func TestLoadRuntimePlugins_HostRefused(t *testing.T) {
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
		"rt": {Use: buildTestPlugin(t, "acme-runtime"), Host: "somebox"},
	}}
	_, err := loadOne(t, cfg)
	if err == nil || !strings.Contains(err.Error(), "host:") {
		t.Fatalf("host: on a Backend-RPC runtime must be refused, got %v", err)
	}
}

// A decision runtime (acme-decider: declares system_one/v1 and serves
// decide) is classified as neither a dispatcher nor an ACP session: it lands
// in the decider set with its connection's secret references resolved, its
// roster is registered for fleets, and it gets NO controller — nothing can
// select it to launch an agent.
func TestLoadRuntimePlugins_DecisionRuntime(t *testing.T) {
	t.Setenv("ACME_DECIDER_KEY", "k-from-env")
	logPath := filepath.Join(t.TempDir(), "calls.log")
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
		"rt": {Use: buildTestPlugin(t, "acme-decider"), Connection: map[string]any{
			"api_key": "env:ACME_DECIDER_KEY", "calls_log": logPath,
		}},
	}}
	mgr := pluginManagerFor(cfg, secrets.New(), func(map[string]any) {})
	t.Cleanup(func() { _ = mgr.Close() })
	backends, deciders, err := loadRuntimePlugins(mgr, cfg, config.Retry{}, secrets.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backends["rt"]; ok {
		t.Fatal("a decision runtime must not be adopted as a Backend-RPC dispatcher")
	}
	d, ok := deciders["rt"]
	if !ok || !d.Speaks("system_one/v1") {
		t.Fatalf("acme-decider must be registered as a v1 decision runtime, got %v", deciders.Names())
	}
	merged, err := mergedControllersWithPlugins(cfg, backends, deciders)
	if err != nil {
		t.Fatal(err)
	}
	if cc, ok := merged["rt"]; ok {
		t.Fatalf("a decision runtime gets no agent controller, got %+v", cc)
	}

	// The roster is discoverable through the model resolver, so a fleet can
	// name its models.
	res := agentmodels.NewResolver(cfg, nil)
	if ids := res.Rosters(context.Background())["rt"].IDs(); strings.Join(ids, ",") != "acme-2,acme-1" {
		t.Fatalf("the decision runtime's roster must be discoverable, got %v", ids)
	}
	// The env: reference was resolved at boot and delivered on the call.
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "key=k-from-env") {
		t.Fatalf("the connection's secret reference must reach the plugin resolved:\n%s", raw)
	}
}
