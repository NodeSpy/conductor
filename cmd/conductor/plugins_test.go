package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

func tempExecutable(t *testing.T) (path, sum string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "rt-plugin")
	data := []byte("#!/bin/true\n")
	if err := os.WriteFile(p, data, 0o755); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	return p, hex.EncodeToString(h[:])
}

func TestPluginRuntimeControllers(t *testing.T) {
	bin, sum := tempExecutable(t)

	t.Run("verified runtime becomes an acp controller", func(t *testing.T) {
		cfg := &config.Config{Plugins: map[string]config.PluginRef{
			"myrt": {Source: bin, Kind: config.PluginKindRuntime, Provides: "my-runtime", Sha256: sum, Version: "1.0"},
		}}
		merged, err := mergedControllersWithPlugins(cfg)
		if err != nil {
			t.Fatal(err)
		}
		cc, ok := merged["my-runtime"]
		if !ok {
			t.Fatalf("runtime not registered: %+v", merged)
		}
		if cc.Transport != "acp" || len(cc.Command) == 0 || cc.Command[0] != bin {
			t.Fatalf("unexpected controller config: %+v", cc)
		}
	})

	t.Run("bad sha refuses (fail-closed)", func(t *testing.T) {
		cfg := &config.Config{Plugins: map[string]config.PluginRef{
			"myrt": {Source: bin, Kind: config.PluginKindRuntime, Provides: "my-runtime", Sha256: strings.Repeat("0", 64)},
		}}
		if _, err := mergedControllersWithPlugins(cfg); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
			t.Fatalf("want sha256 refusal, got %v", err)
		}
	})

	t.Run("collision with configured runtime refused", func(t *testing.T) {
		cfg := &config.Config{
			Runtimes: map[string]config.RuntimeConfig{"my-runtime": {Type: "paseo"}},
			Plugins: map[string]config.PluginRef{
				"myrt": {Source: bin, Kind: config.PluginKindRuntime, Provides: "my-runtime", Sha256: sum},
			},
		}
		if _, err := mergedControllersWithPlugins(cfg); err == nil || !strings.Contains(err.Error(), "collides") {
			t.Fatalf("want collision refusal, got %v", err)
		}
	})

	t.Run("connector plugins are ignored here", func(t *testing.T) {
		cfg := &config.Config{Plugins: map[string]config.PluginRef{
			"conn": {Source: bin, Kind: config.PluginKindConnector, Provides: "x", Sha256: sum},
		}}
		merged, err := mergedControllersWithPlugins(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := merged["x"]; ok {
			t.Fatal("connector plugin must not register a runtime")
		}
	})
}
