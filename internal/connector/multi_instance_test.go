package connector

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// buildAcmeEchoForConnectorTest compiles the reference acme-echo plugin
// (test/plugins/acme-echo, also used by internal/plugin's own integration
// tests) so this package can prove multi-instance isolation through the REAL
// registration path — RegisterExternalConnector + connector.Build — rather
// than the fake single-client seam instance_refine_test.go uses for the Q6
// refinement checks.
func buildAcmeEchoForConnectorTest(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "acme-echo")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/NodeSpy/conductor/test/plugins/acme-echo")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build acme-echo: %v\n%s", err, out)
	}
	return bin
}

// TestTwoConnectorInstancesOfOnePluginGetDistinctProcesses is the end-to-end
// multi-instance isolation proof (docs/wiki/Plugins.md, docs/design/
// plugin-contract.md): two `connectors:` entries naming the SAME external
// plugin resolve to ONE plugin.PluginRef (config.PluginRefs folds them), but
// — registered the real way, through RegisterExternalConnector and
// plugin.Manager.InstanceClientFactory, exactly as cmd/conductor's
// loadConnectorPlugins does — each configured instance ends up driven by its
// OWN subprocess, not a process the two share.
func TestTwoConnectorInstancesOfOnePluginGetDistinctProcesses(t *testing.T) {
	bin := buildAcmeEchoForConnectorTest(t)
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	cfg := mustDecodeConfig(t, fmt.Sprintf(`
connectors:
  one:
    use: %s
  two:
    use: %s
`, bin, bin))

	refs := cfg.PluginRefs()
	if len(refs) != 1 {
		t.Fatalf("both instances must resolve to ONE plugin reference (one binary): %v", refs)
	}
	var key string
	for k := range refs {
		key = k
	}

	state := plugin.LoadInstallState(plugin.InstallDir())
	mgr := plugin.NewManager(refs, "", state, plugin.Deps{})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// The type-level probe (what loadConnectorPlugins calls ProbeDescribe for)
	// — a throwaway process, never reused by either instance below.
	typeDecl, err := mgr.ProbeDescribe(ctx, key)
	if err != nil {
		t.Fatalf("probe describe: %v", err)
	}
	spec, ok := mgr.Spec(key)
	if !ok {
		t.Fatal("spec not found")
	}
	if _, err := RegisterExternalConnector(mgr.InstanceClientFactory(key), spec, typeDecl); err != nil {
		t.Fatalf("register: %v", err)
	}
	defer UnregisterExternalType(spec.Provides)

	reg, err := Build(cfg, Deps{Secrets: secrets.New(), Log: t.Logf})
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	one, ok := reg.Get("one")
	if !ok || one.DisabledReason != "" {
		t.Fatalf("connector \"one\" not built cleanly: %+v", one)
	}
	two, ok := reg.Get("two")
	if !ok || two.DisabledReason != "" {
		t.Fatalf("connector \"two\" not built cleanly: %+v", two)
	}

	pid1, ok1 := InstancePID(one)
	pid2, ok2 := InstancePID(two)
	if !ok1 || !ok2 {
		t.Fatalf("expected both instances to be live processes: one=(%d,%v) two=(%d,%v)", pid1, ok1, pid2, ok2)
	}
	if pid1 == pid2 {
		t.Fatalf("two configured instances of one plugin must run as two DIFFERENT processes; both reported pid %d", pid1)
	}

	// And the two never share a *plugin.Client: killing one's process via the
	// Manager must leave the other reachable.
	instClients := mgr.InstanceClients(key)
	if len(instClients) != 2 {
		t.Fatalf("expected 2 live per-instance clients, got %d: %v", len(instClients), instClients)
	}
	if ca, ok := instClients["one"]; ok {
		_ = ca.Close()
	}
	out, err := two.Impl.Invoke(context.Background(), "echo", map[string]any{"message": "still here"})
	if err != nil {
		t.Fatalf("instance \"two\" must survive instance \"one\"'s teardown: %v", err)
	}
	if out["message"] != "still here" {
		t.Fatalf("unexpected echo output: %+v", out)
	}
}
