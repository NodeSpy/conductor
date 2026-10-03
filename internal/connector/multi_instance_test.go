package connector

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
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

// TestTwoConnectorInstancesGetOnlyTheirOwnGrant is finding 1's end-to-end
// proof: two `connectors:` entries naming the SAME external plugin, each
// with its OWN allow_env/network, must each see only its own through the
// REAL spawn env (grantedEnv/withDeclaredEnv) and the real egress manifest —
// never a sibling instance's. acme-echo declares two env vars
// (ACME_ECHO_VAR_A, ACME_ECHO_VAR_B); instance "one" is granted only the
// first, instance "two" only the second.
func TestTwoConnectorInstancesGetOnlyTheirOwnGrant(t *testing.T) {
	bin, sum := buildEcho(t)
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	t.Setenv("ACME_ECHO_VAR_A", "value-a")
	t.Setenv("ACME_ECHO_VAR_B", "value-b")

	// A REMOTE-shaped `use:` (not a local path): grantedEnv/EffectiveManifest
	// read the plugin's declared capabilities off the INSTALLED record's
	// Manifest (SpecFromRef), which only a remote-origin reference gets —
	// a local `use: <path>` dev binary never records one (it is the
	// operator's own build, resolved fresh every time, docs/wiki/Plugins.md
	// "Local builds are snapshotted"). So this test seeds install state
	// directly (as cmd/conductor's own plugin tests do) rather than really
	// fetching, pointing the recorded Path at the real acme-echo binary
	// built above.
	cfg := mustDecodeConfig(t, `
connectors:
  one:
    use: acme/plugins/acme-echo
    allow_env: [ACME_ECHO_VAR_A]
    network: [one.example:443]
  two:
    use: acme/plugins/acme-echo
    allow_env: [ACME_ECHO_VAR_B]
    network: [two.example:443]
`)

	refs := cfg.PluginRefs()
	if len(refs) != 1 {
		t.Fatalf("both instances must resolve to ONE plugin reference (one binary): %v", refs)
	}
	var key string
	for k := range refs {
		key = k
	}

	state := plugin.LoadInstallState(t.TempDir())
	state.Put(plugin.Installed{
		Key: key, Kind: "connector", Name: "acme-echo",
		Resolved: "v1.0.0", Sha256: sum, Path: bin,
		// Matches acme-echo's declared Capabilities.Env exactly.
		Manifest: plugin.Manifest{Env: []string{"ACME_ECHO_VAR_A", "ACME_ECHO_VAR_B"}},
	})
	mgr := plugin.NewManager(refs, "", state, plugin.Deps{})
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
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

	// Instance "one" sees its OWN granted var, and nothing of "two"'s.
	outOwn, err := one.Impl.Invoke(context.Background(), "echo", map[string]any{"message": "x", "env_var": "ACME_ECHO_VAR_A"})
	if err != nil {
		t.Fatalf("instance one invoke: %v", err)
	}
	if outOwn["env_value"] != "value-a" {
		t.Fatalf("instance \"one\" must see its OWN granted env var: got %+v", outOwn)
	}
	outSibling, err := one.Impl.Invoke(context.Background(), "echo", map[string]any{"message": "x", "env_var": "ACME_ECHO_VAR_B"})
	if err != nil {
		t.Fatalf("instance one invoke: %v", err)
	}
	if outSibling["env_value"] != "" {
		t.Fatalf("instance \"one\" must NOT see sibling instance \"two\"'s granted env var: got %+v", outSibling)
	}

	// Instance "two", symmetrically.
	twoOwn, err := two.Impl.Invoke(context.Background(), "echo", map[string]any{"message": "x", "env_var": "ACME_ECHO_VAR_B"})
	if err != nil {
		t.Fatalf("instance two invoke: %v", err)
	}
	if twoOwn["env_value"] != "value-b" {
		t.Fatalf("instance \"two\" must see its OWN granted env var: got %+v", twoOwn)
	}
	twoSibling, err := two.Impl.Invoke(context.Background(), "echo", map[string]any{"message": "x", "env_var": "ACME_ECHO_VAR_A"})
	if err != nil {
		t.Fatalf("instance two invoke: %v", err)
	}
	if twoSibling["env_value"] != "" {
		t.Fatalf("instance \"two\" must NOT see sibling instance \"one\"'s granted env var: got %+v", twoSibling)
	}

	// And the egress manifest: each instance's own process is confined to
	// its OWN narrowed network, never the sibling's.
	instClients := mgr.InstanceClients(key)
	ca, ok := instClients["one"]
	if !ok {
		t.Fatal("instance \"one\" has no live client")
	}
	cb, ok := instClients["two"]
	if !ok {
		t.Fatal("instance \"two\" has no live client")
	}
	if got := ca.EffectiveManifest().Egress; len(got) != 1 || got[0] != "one.example:443" {
		t.Fatalf("instance \"one\" effective egress = %v, want only its own narrowed network", got)
	}
	if got := cb.EffectiveManifest().Egress; len(got) != 1 || got[0] != "two.example:443" {
		t.Fatalf("instance \"two\" effective egress = %v, want only its own narrowed network", got)
	}
}

// TestTwoConnectorInstancesAllowSecretsNotUnioned proves the other half of
// finding 1: `allow_secrets` is one connector entry's OWN restriction, never
// a sibling instance's. Before the fix, RegisterExternalConnector built one
// `allow` map from the plugin-wide UNION of every instance's allow_secrets
// and reused it for every instance's resolveConnection — so instance "two"
// here (which declares NO allow_secrets restriction of its own) would have
// been wrongly restricted to instance "one"'s narrower list, and its own
// secret reference refused.
func TestTwoConnectorInstancesAllowSecretsNotUnioned(t *testing.T) {
	bin := buildAcmeEchoForConnectorTest(t)
	config.SetStateDir(t.TempDir())
	t.Cleanup(func() { config.SetStateDir("") })

	t.Setenv("ONE_TOKEN", "tok-one")
	t.Setenv("TWO_TOKEN", "tok-two")

	cfg := mustDecodeConfig(t, fmt.Sprintf(`
connectors:
  one:
    use: %s
    allow_secrets: [env:ONE_TOKEN]
    token: env:ONE_TOKEN
  two:
    use: %s
    token: env:TWO_TOKEN
  three:
    use: %s
    allow_secrets: [env:ONE_TOKEN]
    token: env:ONE_TOKEN
    forbidden: env:TWO_TOKEN
`, bin, bin, bin))

	refs := cfg.PluginRefs()
	if len(refs) != 1 {
		t.Fatalf("both instances must resolve to ONE plugin reference: %v", refs)
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
	if !ok {
		t.Fatal("connector \"one\" missing from registry")
	}
	if one.DisabledReason != "" {
		t.Fatalf("connector \"one\" (using its OWN allowed secret) must build cleanly: %+v", one)
	}
	two, ok := reg.Get("two")
	if !ok {
		t.Fatal("connector \"two\" missing from registry")
	}
	if two.DisabledReason != "" {
		t.Fatalf("connector \"two\" (no allow_secrets of its own) must NOT be restricted by instance \"one\"'s allow_secrets: %+v", two)
	}
	// "three" shares "one"'s own allow_secrets list (ONE_TOKEN only) but
	// references a THIRD field outside it — its own restriction must still
	// be enforced (not silently dropped because some sibling's allow map
	// was computed once for the whole plugin type).
	three, ok := reg.Get("three")
	if !ok {
		t.Fatal("connector \"three\" missing from registry")
	}
	if three.DisabledReason == "" || !strings.Contains(three.DisabledReason, "allow_secrets") {
		t.Fatalf("connector \"three\" must enforce its OWN allow_secrets against its \"forbidden\" field: %+v", three)
	}
}
