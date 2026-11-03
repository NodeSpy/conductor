package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadCfg writes body to a temp config and loads it through the real loader.
func loadCfg(t *testing.T, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	SetStateDir(filepath.Join(dir, "state"))
	t.Cleanup(func() { SetStateDir("") })
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

// Config-compat: bots are confined by default. An existing config with no
// sandbox stanza — the common case — loads and resolves to sandbox-on; a
// sandbox stanza that omits agents is still on; the explicit opt-out is off.
func TestAgentSandboxConfigCompat(t *testing.T) {
	const base = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"
	cases := []struct {
		name, body string
		wantOn     bool
	}{
		{"no sandbox stanza", base, true},
		{"stanza without agents", base + "sandbox:\n  egress: []\n", true},
		{"explicit on", base + "sandbox:\n  agents: on\n", true},
		{"explicit opt-out", base + "sandbox:\n  agents: off\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := loadCfg(t, tc.body)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := cfg.AgentSandboxOn(); got != tc.wantOn {
				t.Fatalf("AgentSandboxOn() = %v, want %v", got, tc.wantOn)
			}
		})
	}

	// A nil config and a config with no Sandbox both default to on — a config
	// that predates the sandbox block can never resolve to unconfined.
	if !(*Config)(nil).AgentSandboxOn() {
		t.Fatal("a nil config must resolve to sandbox-on")
	}
	if !(&Config{}).AgentSandboxOn() {
		t.Fatal("a config with no Sandbox must resolve to sandbox-on")
	}

	// An invalid agents value is rejected at load, not silently treated as on.
	if _, err := loadCfg(t, base+"sandbox:\n  agents: maybe\n"); err == nil || !strings.Contains(err.Error(), "sandbox.agents must be on|off") {
		t.Fatalf("invalid sandbox.agents accepted or wrong error: %v", err)
	}
}

// sandbox.push.unlanded: fail by default (no stanza, no push block, empty),
// warn when said, and anything else refused at load.
func TestSandboxPushUnlanded(t *testing.T) {
	const base = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"
	for body, want := range map[string]bool{
		base:                              true,
		base + "sandbox:\n  egress: []\n": true,
		base + "sandbox:\n  push: { sign: false }\n":    true,
		base + "sandbox:\n  push: { unlanded: fail }\n": true,
		base + "sandbox:\n  push: { unlanded: warn }\n": false,
	} {
		cfg, err := loadCfg(t, body)
		if err != nil {
			t.Fatalf("Load %q: %v", body, err)
		}
		if got := cfg.Sandbox.UnlandedFails(); got != want {
			t.Fatalf("%q: UnlandedFails() = %v, want %v", body, got, want)
		}
	}
	if _, err := loadCfg(t, base+"sandbox:\n  push: { unlanded: ignore }\n"); err == nil || !strings.Contains(err.Error(), "sandbox.push.unlanded must be fail|warn") {
		t.Fatalf("invalid unlanded accepted or wrong error: %v", err)
	}
}

// sandbox.prefetch: off by default, go when named, any other ecosystem
// refused at load.
func TestSandboxPrefetch(t *testing.T) {
	const base = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"
	for body, want := range map[string]bool{
		base:                                  false,
		base + "sandbox:\n  egress: []\n":     false,
		base + "sandbox:\n  prefetch: [go]\n": true,
	} {
		cfg, err := loadCfg(t, body)
		if err != nil {
			t.Fatalf("Load %q: %v", body, err)
		}
		if got := cfg.Sandbox.PrefetchGo(); got != want {
			t.Fatalf("%q: PrefetchGo() = %v, want %v", body, got, want)
		}
	}
	if _, err := loadCfg(t, base+"sandbox:\n  prefetch: [npm]\n"); err == nil || !strings.Contains(err.Error(), `sandbox.prefetch: unknown ecosystem "npm"`) {
		t.Fatalf("unknown ecosystem accepted or wrong error: %v", err)
	}
}

// The Go package proxy is on with the sandbox by default; enabled: false
// turns it off, and it is never on when the sandbox is off.
func TestGoPackageProxyDefaults(t *testing.T) {
	const base = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"
	for body, want := range map[string]bool{
		base:                              true,
		base + "sandbox:\n  egress: []\n": true,
		base + "sandbox:\n  packages: { go: { enabled: true } }\n":                true,
		base + "sandbox:\n  packages: { go: { enabled: false } }\n":               false,
		base + "sandbox:\n  agents: off\n":                                        false,
		base + "sandbox:\n  agents: off\n  packages: { go: { enabled: true } }\n": false,
	} {
		cfg, err := loadCfg(t, body)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if got := cfg.GoPackageProxyOn(); got != want {
			t.Errorf("%q: GoPackageProxyOn = %v, want %v", body, got, want)
		}
	}
	cfg, err := loadCfg(t, base+"sandbox:\n  packages: { cache_max: 2GB, go: { upstream: \"https://goproxy.internal.test\", private: [\"git.internal.test/*\"] } }\n")
	if err != nil {
		t.Fatal(err)
	}
	g := cfg.Sandbox.GoPackages()
	if g.Upstream != "https://goproxy.internal.test" || g.SumDB != DefaultGoSumDB || cfg.Sandbox.PackageCacheMax() != 2<<30 {
		t.Fatalf("settings: %+v max %d", g, cfg.Sandbox.PackageCacheMax())
	}
	var nilSB *SandboxConfig
	if g := nilSB.GoPackages(); g.Upstream != DefaultGoUpstream || g.SumDB != DefaultGoSumDB || nilSB.PackageCacheMax() != DefaultPackageCacheMax.Bytes() {
		t.Fatalf("defaults: %+v", g)
	}
}

// Checksum verification cannot be turned off, the upstream is one fixed
// URL, and a private pattern is never sent to the public proxy.
func TestGoPackageProxyValidation(t *testing.T) {
	const base = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\nsandbox:\n  packages:\n"
	for body, want := range map[string]string{
		"    go: { sumdb: \"off\" }\n":                                   "cannot be turned off",
		"    go: { sumdb: \"off+x\" }\n":                                 "cannot be turned off",
		"    go: { upstream: \"direct\" }\n":                             "one http(s) GOPROXY URL",
		"    go: { upstream: \"https://a.test,direct\" }\n":              "one http(s) GOPROXY URL",
		"    go: { upstream: \"https://u:p@a.test\" }\n":                 "one http(s) GOPROXY URL",
		"    go: { upstream: \"file:///srv/mods\" }\n":                   "one http(s) GOPROXY URL",
		"    go: { private: [\"git.internal.test/*\"] }\n":               "never sent to",
		"    go: { upstream: \"https://a.test\", private: [\"a,b\"] }\n": "one module path pattern",
		"    cache_max: -5\n":                                            "cache_max",
	} {
		if _, err := loadCfg(t, base+body); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", body, err, want)
		}
	}
}
