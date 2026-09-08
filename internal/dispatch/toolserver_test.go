package dispatch

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
)

func TestSkillEnv(t *testing.T) {
	memory.SetToolCommand([]string{"conductor", "mcp", "memory", "--socket", "/run/c/memory.sock"})
	skill.SetActive(skill.NewBroker(func(string) (string, bool) { return "", false }, nil))
	t.Cleanup(func() { memory.SetToolCommand(nil); skill.SetActive(nil) })

	req := Request{
		Profile: config.AgentProfile{Skill: &config.SkillPolicy{Verbs: []string{"gh.*"}}},
		Action:  config.Action{Agent: "fixer"},
		Trigger: core.Trigger{Kind: "review_requested", Target: core.Target{Repo: "o/r", Number: 7}},
	}

	// LocalSkillEndpoint reads the published tool socket.
	if got := LocalSkillEndpoint(); got != "unix:///run/c/memory.sock" {
		t.Fatalf("LocalSkillEndpoint = %q, want unix:///run/c/memory.sock", got)
	}

	// A skill profile + a resolved endpoint → that endpoint + a minted token.
	// The endpoint is passed in verbatim (a forwarded remote socket looks the
	// same as a local one — a plain unix:// path).
	for _, ep := range []string{"unix:///run/c/memory.sock", "unix:///tmp/conductor-skill-deadbeef.sock"} {
		env := SkillEnv(req, ep)
		if env["CONDUCTOR_ENDPOINT"] != ep {
			t.Errorf("endpoint = %q, want %q", env["CONDUCTOR_ENDPOINT"], ep)
		}
		if env["CONDUCTOR_SKILL_TOKEN"] == "" {
			t.Errorf("expected a session token in env for %q, got none", ep)
		}
	}

	// No endpoint resolved → nothing (never inject a token with nowhere to send it).
	if SkillEnv(req, "") != nil {
		t.Errorf("an empty endpoint must inject nothing")
	}

	// No skill: block → nothing, even with an endpoint.
	noskill := req
	noskill.Profile.Skill = nil
	if SkillEnv(noskill, "unix:///run/c/memory.sock") != nil {
		t.Errorf("a non-skill profile must get no skill env")
	}

	// No tool server published at boot → LocalSkillEndpoint is empty.
	memory.SetToolCommand(nil)
	if LocalSkillEndpoint() != "" {
		t.Errorf("no tool command published → empty local endpoint")
	}
}
