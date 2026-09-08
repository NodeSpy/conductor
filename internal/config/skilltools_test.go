package config

import "testing"

func TestSkillToolsSupportedPaseo(t *testing.T) {
	cfg := &Config{
		Runtimes: map[string]RuntimeConfig{"paseo": {Type: "paseo", Default: true}},
	}
	cases := []struct {
		name     string
		provider string
		ws       string
		want     bool
	}{
		{"claude + worktree", "claude", "worktree", true},
		{"claude/model + worktree", "claude/opus", "worktree", true},
		{"claude + local (no worktree to inject into)", "claude", "local", false},
		{"non-claude provider + worktree", "codex", "worktree", false},
		{"claude + unset workspace", "claude", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := cfg.SkillToolsSupported(AgentProfile{Provider: tc.provider, Workspace: tc.ws})
			if ok != tc.want {
				t.Fatalf("SkillToolsSupported(provider=%q ws=%q) = %v, want %v", tc.provider, tc.ws, ok, tc.want)
			}
		})
	}
}
