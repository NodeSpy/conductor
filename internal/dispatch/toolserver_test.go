package dispatch

import (
	"slices"
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
		Step:    config.Step{Skill: &config.SkillPolicy{Verbs: []string{"gh.*"}}},
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
	noskill.Step.Skill = nil
	if SkillEnv(noskill, "unix:///run/c/memory.sock") != nil {
		t.Errorf("a non-skill profile must get no skill env")
	}

	// No tool server published at boot → LocalSkillEndpoint is empty.
	memory.SetToolCommand(nil)
	if LocalSkillEndpoint() != "" {
		t.Errorf("no tool command published → empty local endpoint")
	}
}

// ROUND-10 #1, the argv half. The memory MCP subprocess authorizes scopes
// against the dispatch it was launched for, so it needs the target's
// PROVENANCE as well as its repo. Without the flag the subprocess has to
// assume — and either assumption is wrong: assume trusted and a webhook-forged
// repo gets implicit own-scope (the bug), assume untrusted and every
// legitimate dispatch is over-refused.
func TestToolServerCarriesTargetProvenance(t *testing.T) {
	memory.SetToolCommand([]string{"conductor", "mcp", "memory"})
	t.Cleanup(func() { memory.SetToolCommand(nil) })

	for _, tc := range []struct {
		name    string
		trusted bool
	}{
		{"a platform-assigned target", true},
		{"a target the request body chose", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := BuildToolServer(Request{
				Trigger: core.Trigger{
					Kind: "delivery", TargetTrusted: tc.trusted,
					Target: core.Target{Repo: "acme/app", Number: 7},
				},
				Action: config.Action{Agent: "probe"},
				Step:   config.Step{},
			}, "")
			if spec == nil {
				t.Fatal("no tool server built")
			}
			got := slices.Contains(spec.Args, "--target-trusted")
			if got != tc.trusted {
				t.Fatalf("--target-trusted present=%v, want %v (args=%v)", got, tc.trusted, spec.Args)
			}
			// The repo travels either way — provenance qualifies it, it does
			// not replace it.
			if !slices.Contains(spec.Args, "acme/app") {
				t.Errorf("the repo must still be passed: %v", spec.Args)
			}
		})
	}
}
