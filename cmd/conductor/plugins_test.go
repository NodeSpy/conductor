package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
	"github.com/NodeSpy/conductor/internal/secrets"
)

func tempExecutable(t *testing.T) (path, sum string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "conductor-my-runtime")
	data := []byte("#!/bin/true\n")
	if err := os.WriteFile(p, data, 0o755); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	return p, hex.EncodeToString(h[:])
}

// runtimeCfg declares a plugin runtime the app-extension way: one `use:` on a
// runtimes: entry, pointing at a local development binary.
func runtimeCfg(bin string) *config.Config {
	return &config.Config{
		Runtimes: map[string]config.RuntimeConfig{
			"my-runtime": {Use: bin},
		},
	}
}

func TestPluginRuntimeControllers(t *testing.T) {
	bin, sum := tempExecutable(t)

	t.Run("a use: runtime becomes an acp controller", func(t *testing.T) {
		merged, err := mergedControllersWithPlugins(runtimeCfg(bin), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		cc, ok := merged["my-runtime"]
		if !ok {
			t.Fatalf("runtime not registered: %+v", merged)
		}
		// The command routes through the plugin-exec re-verify wrapper and ends
		// at the real binary.
		if cc.Transport != "acp" || len(cc.Command) == 0 || cc.Command[len(cc.Command)-1] != bin {
			t.Fatalf("unexpected controller config: %+v", cc)
		}
		if !cc.ScrubEnv {
			t.Fatal("a runtime plugin must not inherit the daemon's environment")
		}
	})

	t.Run("the runtimes: name wins over the reference leaf", func(t *testing.T) {
		cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
			"gpu": {Use: bin},
		}}
		merged, err := mergedControllersWithPlugins(cfg, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := merged["gpu"]; !ok {
			t.Fatalf("runtime not keyed by its runtimes: name: %+v", merged)
		}
	})

	t.Run("runtime command routes through the re-verify wrapper", func(t *testing.T) {
		merged, err := mergedControllersWithPlugins(runtimeCfg(bin), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		cc := merged["my-runtime"]
		if len(cc.Command) < 4 || cc.Command[1] != "plugin-exec" || cc.Command[2] != "--sha" {
			t.Fatalf("expected plugin-exec re-verify wrapper, got %v", cc.Command)
		}
		if cc.Command[len(cc.Command)-1] != bin {
			t.Fatalf("wrapper must exec the real binary, got %v", cc.Command)
		}
	})

	t.Run("plugin-exec re-verify refuses a tampered binary before exec", func(t *testing.T) {
		err := cmdPluginExec([]string{"--sha", strings.Repeat("0", 64), "--", bin})
		if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
			t.Fatalf("want re-verify refusal, got %v", err)
		}
		// The real sha passes verification (exec is not reached here because
		// VerifyOnly is what we are exercising).
		if err := plugin.VerifyOnly(plugin.Spec{Name: "r", BinPath: bin, Sha256: sum}); err != nil {
			t.Fatalf("the correct sha must verify: %v", err)
		}
	})

	t.Run("connector plugins are ignored here", func(t *testing.T) {
		cfg := &config.Config{ConnectorsMap: map[string]config.ConnectorRef{
			"conn": {Use: bin},
		}}
		merged, err := mergedControllersWithPlugins(cfg, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := merged["my-runtime"]; ok {
			t.Fatal("a connector plugin must not register a runtime")
		}
	})

	t.Run("builtin runtimes need no plugin", func(t *testing.T) {
		cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
			"local": {Use: "paseo"},
		}}
		merged, err := mergedControllersWithPlugins(cfg, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if merged["local"].Type != "paseo" {
			t.Fatalf("builtin paseo runtime lost: %+v", merged["local"])
		}
		if len(merged["local"].Command) != 0 {
			t.Fatalf("a builtin runtime must not be wrapped: %+v", merged["local"])
		}
	})
}

// TestLoadEnginePluginsDegradesOnFailedEngine: an engine plugin that is
// installed but fails to start/describe (corrupt binary) must not fail the
// whole boot — Q12's posture generalizes to engines, since a code step using
// a missing engine already has its own clear runtime error
// (internal/code/engineplugin.go's execPluginEngine).
func TestLoadEnginePluginsDegradesOnFailedEngine(t *testing.T) {
	badBin, badSum := tempExecutable(t) // executable, verifies, speaks no protocol
	u, err := config.ParseUse(config.UseKindEngine, "acme/widget")
	if err != nil {
		t.Fatal(err)
	}
	state := &plugin.InstallState{Plugins: []plugin.Installed{{
		Key: u.InstallKey(), Kind: config.PluginKindEngine, Name: u.Name,
		Path: badBin, Sha256: badSum,
	}}}
	refs := map[string]config.PluginRef{u.InstallKey(): {Name: u.Name, Instance: u.Name, Use: u}}
	mgr := plugin.NewManager(refs, t.TempDir(), state, pluginDeps(secrets.New(), func(map[string]any) {}))
	defer mgr.Close()

	lookup, err := loadEnginePlugins(mgr)
	if err != nil {
		t.Fatalf("a broken engine plugin must not fail the boot: %v", err)
	}
	if lookup != nil {
		if _, ok := lookup(u.Name); ok {
			t.Fatal("a broken engine plugin must not be wired as usable")
		}
	}
}

// A referenced-but-uninstalled plugin runtime reports a DIRECTION, not a crash.
func TestPluginRuntimeNotInstalled(t *testing.T) {
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
		"modal": {Use: "acme/plugins/modal"},
	}}
	_, err := mergedControllersWithPlugins(cfg, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "conductor init") {
		t.Fatalf("want a not-installed direction, got %v", err)
	}
}

// TestPendingPluginBackoffCapsAtOneHour: a still-missing plugin's retry wait
// doubles each time, from the 5m default up to the 1h cap, instead of
// retrying every 5 minutes forever.
func TestPendingPluginBackoffCapsAtOneHour(t *testing.T) {
	oldCap := pendingPluginBackoffCap
	pendingPluginBackoffCap = time.Hour
	t.Cleanup(func() { pendingPluginBackoffCap = oldCap })

	wait := 5 * time.Minute
	want := []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour, time.Hour}
	for i, w := range want {
		wait = nextPendingPluginWait(wait)
		if wait != w {
			t.Fatalf("step %d: got %s, want %s", i, wait, w)
		}
	}
}

// TestPendingPluginRetryWontRestartOnBadConfig: once a pending plugin is
// installed, pendingPluginRetry must validate the config against what the
// plugin ACTUALLY declares before asking the daemon to restart into it — its
// triggers/options were never checked while the type was Unavailable (which
// accepts any verb/event name, unchecked, so boot could proceed with no
// Decl at all). A restart straight into a config that fails flow.Validate
// would crash-loop the daemon on every subsequent boot, which is the exact
// failure this whole mechanism exists to prevent.
func TestPendingPluginRetryWontRestartOnBadConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin := buildTestPlugin(t, "acme-ticker") // declares ONE event: "tick"; no verbs

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	doc := fmt.Sprintf(`
connectors:
  forge:
    use: %s
triggers:
  - on: forge.not_a_real_event
    steps: [{ id: hi, uses: forge.run, options: {} }]
`, bin)
	if err := os.WriteFile(cfgPath, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate "this boot found it missing" — a local `use:` path is always
	// Installed() (it is a path to a binary already on disk), so the very
	// first retry tick sees missing == 0 and goes straight to the new
	// validate step; the test cares about that step, not the backoff path.
	oldPending := pendingPlugins
	pendingPlugins = []string{"acme-ticker"}
	t.Cleanup(func() { pendingPlugins = oldPending })

	oldInterval, oldCap := pendingPluginInterval, pendingPluginBackoffCap
	pendingPluginInterval = 10 * time.Millisecond
	t.Cleanup(func() { pendingPluginInterval, pendingPluginBackoffCap = oldInterval, oldCap })

	stopped := make(chan struct{})
	stop := func() { close(stopped) }

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		pendingPluginRetry(ctx, cfg, cfgPath, stop)
		close(done)
	}()

	select {
	case <-stopped:
		t.Fatal("must not restart into a plugin the config does not validate against")
	case <-done:
		// returned without restarting — correct: give up rather than loop.
	case <-time.After(4 * time.Second):
		t.Fatal("pendingPluginRetry neither restarted nor returned")
	}
}
