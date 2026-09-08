package config

import (
	"strings"
	"testing"
)

func TestValidateSkill(t *testing.T) {
	// An agent with a skill: block so SkillEnabled() is true where needed.
	withSkill := map[string]AgentProfile{"a": {Skill: &SkillPolicy{Verbs: []string{"gh.*"}}}}

	cases := []struct {
		name    string
		skill   *SkillConfig
		agents  map[string]AgentProfile
		wantErr string // substring; "" = no error
	}{
		{"no skill block", nil, withSkill, ""},
		{"empty skill block", &SkillConfig{}, withSkill, ""},
		{"listen without base_url", &SkillConfig{Listen: ":8098"}, withSkill, "base_url is empty"},
		{"base_url without listen", &SkillConfig{BaseURL: "https://c.example.com"}, withSkill, "listen is empty"},
		{"base_url wrong scheme", &SkillConfig{Listen: ":8098", BaseURL: "c.example.com"}, withSkill, "must be an http"},
		{"valid but no skill agent", &SkillConfig{Listen: ":8098", BaseURL: "https://c.example.com"}, nil, "no agent has a skill"},
		{"valid", &SkillConfig{Listen: ":8098", BaseURL: "https://c.example.com/"}, withSkill, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Skill: tc.skill, Agents: tc.agents}
			err := c.validateSkill()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSkill() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateSkill() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}

	// The endpoint helper trims a trailing slash and appends /skill.
	c := &Config{Skill: &SkillConfig{BaseURL: "https://c.example.com/"}}
	if got := c.SkillRemoteEndpoint(); got != "https://c.example.com/skill" {
		t.Fatalf("SkillRemoteEndpoint() = %q, want https://c.example.com/skill", got)
	}
	if (&Config{}).SkillRemoteEndpoint() != "" {
		t.Fatal("no skill block → empty remote endpoint")
	}
	// RemoteSkillEnabled keys off Listen, not BaseURL.
	if !(&Config{Skill: &SkillConfig{Listen: ":1"}}).RemoteSkillEnabled() {
		t.Fatal("listen set → RemoteSkillEnabled true")
	}
	if (&Config{Skill: &SkillConfig{BaseURL: "https://x"}}).RemoteSkillEnabled() {
		t.Fatal("base_url without listen → RemoteSkillEnabled false")
	}
}
