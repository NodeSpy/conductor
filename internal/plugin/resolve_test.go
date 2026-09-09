package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// TestResolvePluginsVendorsAndLoadRewrites proves the whole remote-plugin path:
// ResolvePlugins fetches + verifies + vendors a remote plugin and writes the
// lockfile, then config.Load rewrites that plugin's Source to the vendored
// binary and fills in the locked sha — so the daemon runs it like a local,
// sha-pinned plugin with no network at boot.
func TestResolvePluginsVendorsAndLoadRewrites(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := `connectors:
  gh:
    type: github
plugins:
  jira:
    source: github.com/acme/conductor-plugins//jira
    kind: connector
    version: "~> 1.0"
    allow_unsandboxed: true
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	bin := []byte("#!/bin/sh\necho conductor-jira\n")
	api := stubAPI{
		tags:      []string{"jira/v1.0.0", "jira/v1.2.0", "jira/v2.0.0"},
		bin:       bin,
		assetName: RemoteSource{Component: "jira"}.AssetName(),
	}

	plugins, trust, err := config.LoadPluginsBlock(cfgPath)
	if err != nil {
		t.Fatalf("LoadPluginsBlock: %v", err)
	}
	res, err := ResolvePlugins(dir, plugins, trust, false, api)
	if err != nil {
		t.Fatalf("ResolvePlugins: %v", err)
	}
	if len(res) != 1 || res[0].Action != "fetched" || res[0].Tag != "jira/v1.2.0" {
		t.Fatalf("resolution = %+v, want one fetched jira/v1.2.0 (highest 1.x)", res)
	}
	vendored := filepath.Join(dir, res[0].Path)
	if _, err := os.Stat(vendored); err != nil {
		t.Fatalf("vendored binary missing: %v", err)
	}

	// The lockfile carries the plugin entry.
	lock, err := config.ReadLockfile(dir)
	if err != nil || lock == nil || len(lock.Plugins) != 1 {
		t.Fatalf("lockfile plugins = %+v (err %v), want 1", lock, err)
	}
	if lock.Plugins[0].Sha256 != res[0].Sha {
		t.Fatalf("lock sha %q != resolved sha %q", lock.Plugins[0].Sha256, res[0].Sha)
	}

	// Load rewrites Source -> vendored path and fills the sha, offline.
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("Load after resolve: %v", err)
	}
	ref := loaded.Plugins["jira"]
	if !filepath.IsAbs(ref.Source) || !strings.HasSuffix(ref.Source, res[0].Path) {
		t.Fatalf("Source not rewritten to vendored path: %q", ref.Source)
	}
	if ref.Sha256 != res[0].Sha {
		t.Fatalf("Sha256 not filled from lock: %q want %q", ref.Sha256, res[0].Sha)
	}

	// SpecFromRef now points at the vendored binary (no URL).
	spec := SpecFromRef("jira", ref, dir)
	if spec.BinPath != ref.Source {
		t.Fatalf("SpecFromRef BinPath %q != %q", spec.BinPath, ref.Source)
	}
}

// TestResolvePluginsTrustGate proves plugin_trust refuses an unlisted remote
// source (and --allow-unlisted overrides it).
func TestResolvePluginsTrustGate(t *testing.T) {
	dir := t.TempDir()
	plugins := map[string]config.PluginRef{
		"jira": {Source: "github.com/acme/conductor-plugins//jira", Kind: "connector"},
	}
	trust := &config.PackTrustConfig{Allow: []string{"github.com/trusted/*"}}
	api := stubAPI{tags: []string{"jira/v1.0.0"}, bin: []byte("x"), assetName: RemoteSource{Component: "jira"}.AssetName()}

	if _, err := ResolvePlugins(dir, plugins, trust, false, api); err == nil {
		t.Fatal("untrusted remote plugin source must be refused")
	}
	if _, err := ResolvePlugins(dir, plugins, trust, true, api); err != nil {
		t.Fatalf("--allow-unlisted must override trust: %v", err)
	}
}
