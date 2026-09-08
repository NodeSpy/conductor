package config

import "testing"

func TestSkillDelivery(t *testing.T) {
	cases := []struct {
		name     string
		runtimes map[string]RuntimeConfig
		profile  AgentProfile
		remote   *SkillConfig // a configured remote HTTP endpoint, or nil
		want     string
	}{
		{"built-in paseo (no runtimes) → cli", nil, AgentProfile{Provider: "claude"}, nil, SkillModeCLI},
		{"explicit paseo local → cli (any provider)", map[string]RuntimeConfig{"paseo": {Type: "paseo", Default: true}}, AgentProfile{Provider: "codex"}, nil, SkillModeCLI},
		{"agent-deck local → cli", map[string]RuntimeConfig{"ad": {Type: "agent-deck", Default: true}}, AgentProfile{}, nil, SkillModeCLI},
		{"opencode → mcp", map[string]RuntimeConfig{"oc": {Type: "opencode", Default: true}}, AgentProfile{}, nil, SkillModeMCP},
		{"acp agent runtime → mcp", map[string]RuntimeConfig{"gem": {Agent: "gemini", Default: true}}, AgentProfile{}, nil, SkillModeMCP},
		{"remote paseo, no endpoint → none", map[string]RuntimeConfig{"paseo": {Type: "paseo", Host: "build-box", Default: true}}, AgentProfile{Provider: "claude"}, nil, SkillModeNone},
		{"remote via profile host, no endpoint → none", map[string]RuntimeConfig{"paseo": {Type: "paseo", Default: true}}, AgentProfile{Provider: "claude", Host: "build-box"}, nil, SkillModeNone},
		{"remote paseo WITH endpoint → cli", map[string]RuntimeConfig{"paseo": {Type: "paseo", Host: "build-box", Default: true}}, AgentProfile{Provider: "claude"}, &SkillConfig{Listen: ":8098", BaseURL: "https://c.example.com"}, SkillModeCLI},
		{"remote via profile host WITH endpoint → cli", map[string]RuntimeConfig{"paseo": {Type: "paseo", Default: true}}, AgentProfile{Provider: "claude", Host: "build-box"}, &SkillConfig{Listen: ":8098", BaseURL: "https://c.example.com"}, SkillModeCLI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Runtimes: tc.runtimes, Skill: tc.remote}
			_, mode := cfg.SkillDelivery(tc.profile)
			if mode != tc.want {
				t.Fatalf("SkillDelivery = %q, want %q", mode, tc.want)
			}
			_, ok := cfg.SkillToolsSupported(tc.profile)
			if ok != (tc.want != SkillModeNone) {
				t.Fatalf("SkillToolsSupported ok = %v, want %v", ok, tc.want != SkillModeNone)
			}
		})
	}
}
