package config

import "testing"

func TestSkillDelivery(t *testing.T) {
	cases := []struct {
		name     string
		runtimes map[string]RuntimeConfig
		profile  Step
		want     string
	}{
		{"built-in paseo (no runtimes) → cli", nil, Step{}, SkillModeCLI},
		{"explicit paseo local → cli (any provider)", map[string]RuntimeConfig{"paseo": {Use: "paseo", Default: true}}, Step{}, SkillModeCLI},
		{"agent-deck local → cli", map[string]RuntimeConfig{"ad": {Use: "agent-deck", Default: true}}, Step{}, SkillModeCLI},
		{"opencode → mcp", map[string]RuntimeConfig{"oc": {Use: "opencode", Default: true}}, Step{}, SkillModeMCP},
		{"acp agent runtime → mcp", map[string]RuntimeConfig{"gem": {Agent: "gemini", Default: true}}, Step{}, SkillModeMCP},
		// A remote runtime reaches the CLI face over the SSH reverse tunnel the
		// dispatch path opens — same delivery mode as a local shell runtime.
		{"remote paseo (runtime host) → cli", map[string]RuntimeConfig{"paseo": {Use: "paseo", Host: "build-box", Default: true}}, Step{}, SkillModeCLI},
		{"remote via profile host → cli", map[string]RuntimeConfig{"paseo": {Use: "paseo", Default: true}}, Step{Host: "build-box"}, SkillModeCLI},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Runtimes: tc.runtimes}
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
