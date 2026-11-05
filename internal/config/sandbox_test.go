package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

// sandbox.prefetch was removed: any value still loads, is named in one
// warning and is never written back.
func TestSandboxPrefetchRemoved(t *testing.T) {
	const base = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"
	for _, body := range []string{
		base + "sandbox:\n  prefetch: [go]\n",
		base + "sandbox:\n  prefetch: [npm, pip]\n  egress: [proxy.golang.org]\n",
		base + "sandbox: {prefetch: go}\n",
	} {
		cfg, err := loadCfg(t, body)
		if err != nil {
			t.Fatalf("Load %q: %v", body, err)
		}
		if got := cfg.RemovedFieldWarnings(); len(got) != 1 || got[0] != "sandbox.prefetch: "+PrefetchRemoved {
			t.Fatalf("%q: warnings %q", body, got)
		}
		out, err := yaml.Marshal(cfg.Sandbox)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "prefetch") {
			t.Fatalf("prefetch written back:\n%s", out)
		}
	}
	cfg, err := loadCfg(t, base+"sandbox:\n  egress: []\n")
	if err != nil {
		t.Fatal(err)
	}
	if w := cfg.RemovedFieldWarnings(); len(w) != 0 {
		t.Fatalf("warnings without prefetch: %q", w)
	}
}

// Agents reach any public host by default; egress_open: false leaves only
// the listed patterns.
func TestSandboxAgentEgress(t *testing.T) {
	const base = "connectors:\n  gh: { use: github, token: x, repos: [\"acme/*\"] }\n"
	for body, want := range map[string]string{
		base: "*",
		base + "sandbox:\n  egress: [registry.npmjs.org]\n":                       "registry.npmjs.org,*",
		base + "sandbox:\n  egress_open: false\n":                                 "",
		base + "sandbox:\n  egress_open: false\n  egress: [registry.npmjs.org]\n": "registry.npmjs.org",
		base + "sandbox:\n  egress_open: true\n":                                  "*",
	} {
		cfg, err := loadCfg(t, body)
		if err != nil {
			t.Fatalf("Load %q: %v", body, err)
		}
		if got := strings.Join(cfg.Sandbox.AgentEgress(), ","); got != want {
			t.Fatalf("%q: AgentEgress() = %q, want %q", body, got, want)
		}
	}
	var none *SandboxConfig
	if got := strings.Join(none.AgentEgress(), ","); got != "*" {
		t.Fatalf("no sandbox block: %q", got)
	}
}
