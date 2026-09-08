package dispatch

import (
	"os"
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

	// Local skill profile → endpoint + a minted session token in env.
	env := SkillEnv(req, "")
	if env["CONDUCTOR_ENDPOINT"] != "unix:///run/c/memory.sock" {
		t.Errorf("endpoint = %q, want unix:///run/c/memory.sock", env["CONDUCTOR_ENDPOINT"])
	}
	if env["CONDUCTOR_SKILL_TOKEN"] == "" {
		t.Errorf("expected a session token in env, got none")
	}

	// A remote launch (profile host:) has no local socket → nothing injected.
	remote := req
	remote.Profile.Host = "build-box"
	if SkillEnv(remote, "") != nil {
		t.Errorf("remote (host:) launch must not inject a local endpoint")
	}
	// A runtime-level host: likewise.
	if SkillEnv(req, "build-box") != nil {
		t.Errorf("runtime host: must not inject a local endpoint")
	}

	// No skill: block → nothing.
	noskill := req
	noskill.Profile.Skill = nil
	if SkillEnv(noskill, "") != nil {
		t.Errorf("a non-skill profile must get no skill env")
	}

	// No tool server published at boot → nothing.
	memory.SetToolCommand(nil)
	if SkillEnv(req, "") != nil {
		t.Errorf("no tool command published → no skill env")
	}
	_ = os.Getuid
}
