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

	// No skill: block → still a CREDENTIAL, because that is how the socket
	// authenticates a tool request's provenance rather than believing the
	// Source in it (round-12 #1). What it carries is an empty grant.
	noskill := req
	noskill.Step.Skill = nil
	env := SkillEnv(noskill, "unix:///run/c/memory.sock")
	if env == nil || env["CONDUCTOR_SKILL_TOKEN"] == "" {
		t.Fatalf("a non-skill dispatch must still get a credential: %+v", env)
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

// ROUND-12 #4. The argv path passes --dispatch, and skill.Identity has the
// field, but neither mint site populated it — so a skill-enabled dispatch
// with an UNTRUSTED target lost run_step's per-dispatch anchor and reverted
// to the shared literal namespace (round-11 #2, reopened on the skill path).
//
// Both paths must carry the same daemon-assigned value: they are two ways
// into the same socket for the same dispatch.
func TestBothToolPathsCarryTheDispatchAnchor(t *testing.T) {
	b := skill.NewBroker(func(string) (string, bool) { return "", false }, nil)
	skill.SetActive(b)
	t.Cleanup(func() { skill.SetActive(nil) })
	memory.SetToolCommand([]string{"conductor", "mcp", "memory"})
	t.Cleanup(func() { memory.SetToolCommand(nil) })

	req := Request{
		Trigger:    core.Trigger{Kind: "delivery", Target: core.Target{Repo: "victim/repo"}},
		Action:     config.Action{Agent: "probe"},
		Step:       config.Step{Skill: &config.SkillPolicy{Verbs: []string{"gh.comment"}}},
		DispatchID: "run-abc:step1",
	}

	// ARGV path: the flag is on the subprocess's command line.
	spec := BuildToolServer(req, "")
	if spec == nil {
		t.Fatal("no tool server built")
	}
	if !slices.Contains(spec.Args, "--dispatch") || !slices.Contains(spec.Args, "run-abc:step1") {
		t.Errorf("the argv path lost the anchor: %v", spec.Args)
	}
	// …and the SKILL path: the claim it minted resolves to an identity
	// carrying the same value.
	code := spec.Env["CONDUCTOR_SKILL_CLAIM"]
	if code == "" {
		t.Fatal("no claim minted")
	}
	tok, err := b.ClaimSession(code, skill.Peer{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := b.Authorize(tok, skill.Peer{})
	if err != nil {
		t.Fatal(err)
	}
	if id.Dispatch != "run-abc:step1" {
		t.Errorf("the skill path lost the anchor: Dispatch=%q — a skill dispatch with an "+
			"untrusted target would fall back to the namespace every run_step shares", id.Dispatch)
	}

	// The env/session path (paseo, CLI) carries it too.
	env := SkillEnv(req, "unix:///tmp/x.sock")
	if env == nil {
		t.Fatal("no skill env built")
	}
	sid, err := b.Authorize(env["CONDUCTOR_SKILL_TOKEN"], skill.Peer{})
	if err != nil {
		t.Fatal(err)
	}
	if sid.Dispatch != "run-abc:step1" {
		t.Errorf("the session path lost the anchor: Dispatch=%q", sid.Dispatch)
	}
}
