package main

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/skill"
)

// TestMayRunStep pins the run_step capability gate: an authentication-only
// identity (no dispatch provenance, no grant — the sandbox's ad-hoc directory /
// scratch sessions) is refused; a real dispatch or a configured skill agent is
// allowed, so agent-authored workflows keep working.
func TestMayRunStep(t *testing.T) {
	cases := []struct {
		name string
		id   skill.Identity
		want bool
	}{
		{"auth-only (ad-hoc sandbox) refused", skill.Identity{Agent: "sandbox:proj"}, false},
		{"empty identity refused", skill.Identity{}, false},
		{"dispatch provenance allowed", skill.Identity{Agent: "a", Dispatch: "d-1"}, true},
		{"skill grant allowed", skill.Identity{Agent: "a", Policy: config.SkillPolicy{Verbs: []string{"gh.comment"}}}, true},
		{"both allowed", skill.Identity{Agent: "a", Dispatch: "d-1", Policy: config.SkillPolicy{Verbs: []string{"*"}}}, true},
	}
	for _, c := range cases {
		got, reason := mayRunStep(c.id)
		if got != c.want {
			t.Errorf("%s: mayRunStep = %v, want %v (reason %q)", c.name, got, c.want, reason)
		}
		if !got && reason == "" {
			t.Errorf("%s: a refusal must carry a reason", c.name)
		}
	}
}
