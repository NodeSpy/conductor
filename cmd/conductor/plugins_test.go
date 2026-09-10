package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/plugin"
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
		merged, err := mergedControllersWithPlugins(runtimeCfg(bin))
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
		merged, err := mergedControllersWithPlugins(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := merged["gpu"]; !ok {
			t.Fatalf("runtime not keyed by its runtimes: name: %+v", merged)
		}
	})

	t.Run("runtime command routes through the re-verify wrapper", func(t *testing.T) {
		merged, err := mergedControllersWithPlugins(runtimeCfg(bin))
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
		merged, err := mergedControllersWithPlugins(cfg)
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
		merged, err := mergedControllersWithPlugins(cfg)
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

// A referenced-but-uninstalled plugin runtime reports a DIRECTION, not a crash.
func TestPluginRuntimeNotInstalled(t *testing.T) {
	cfg := &config.Config{Runtimes: map[string]config.RuntimeConfig{
		"modal": {Use: "acme/plugins/modal"},
	}}
	_, err := mergedControllersWithPlugins(cfg)
	if err == nil || !strings.Contains(err.Error(), "conductor init") {
		t.Fatalf("want a not-installed direction, got %v", err)
	}
}
