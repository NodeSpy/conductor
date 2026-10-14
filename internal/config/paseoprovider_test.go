package config

import (
	"strings"
	"testing"
	"time"
)

const paseoProviderBase = `
runtimes:
  acp_claude: { use: acp, agent: claude-code, command: [claude-agent-acp] }
  cli_claude: { use: cli, tool: claude-code }
  remote_acp: { use: acp, agent: claude-code, host: box }
  unjailed: { use: acp, agent: claude-code, isolation: { mode: none } }
hosts:
  box: { host: example.test }
`

// paseo_provider names an acp runtime conductor launches, jailed, on this
// box; anything else is refused at load.
func TestPaseoProviderValidation(t *testing.T) {
	for runtime, want := range map[string]string{
		"acp_claude": "",
		"":           "runtime is required",
		"nope":       "not a configured runtime",
		"cli_claude": "must be an acp runtime",
		"remote_acp": "runs on host",
		"unjailed":   "opts out of the workspace jail",
	} {
		_, err := loadYAML(t, paseoProviderBase+"paseo_provider: { runtime: \""+runtime+"\" }\n")
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", runtime, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: want %q, got %v", runtime, want, err)
		}
	}
	cfg, err := loadYAML(t, paseoProviderBase+"paseo_provider: { runtime: acp_claude, mcp_servers: [docs] }\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PaseoProvider.Detached() != time.Hour || len(cfg.PaseoProvider.MCPServers) != 1 {
		t.Fatalf("defaults: %+v", cfg.PaseoProvider)
	}
	if _, err := loadYAML(t, paseoProviderBase+"paseo_provider: { runtime: acp_claude, paseo_tools: true }\n"); err == nil {
		t.Fatal("unknown keys are refused")
	}
}
