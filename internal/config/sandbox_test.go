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
