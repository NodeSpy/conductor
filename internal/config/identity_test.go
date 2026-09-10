package config

import (
	"strings"
	"testing"
)

// Step identity is the ONE key memory, sessions, and outcomes default to
// (docs/design/agents-removal.md §5). Its hard requirement is stability
// across restarts, so every case here is about what does and does not
// rotate it.

func TestIdentityLadder(t *testing.T) {
	trig := TriggerScope("github.pull_request")
	tests := []struct {
		name  string
		step  Step
		scope IdentityScope
		slot  int
		want  string
	}{
		{
			name: "1. an explicit name: pins it",
			step: Step{Name: "reviewer", ID: "sec"}, scope: trig, slot: 3,
			want: "reviewer",
		},
		{
			name: "2. structural: enclosing scope + the step's id",
			step: Step{ID: "security"}, scope: trig, slot: 1,
			want: "github.pull_request/security",
		},
		{
			name: "2. structural: enclosing scope + the ordinal when there is no id",
			step: Step{}, scope: trig, slot: 2,
			want: "github.pull_request/2",
		},
		{
			name: "2. a workflow scope is prefixed (a trigger's key is already qualified)",
			step: Step{ID: "audit"}, scope: WorkflowScope("nightly"), slot: 0,
			want: "workflow:nightly/audit",
		},
		{
			name: "a steps: template IS its key",
			step: Step{}, scope: IdentityScope{Kind: "step", Name: "fixer"}, slot: 0,
			want: "fixer",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.step.Identity(tc.scope, tc.slot); got != tc.want {
				t.Fatalf("identity = %q, want %q", got, tc.want)
			}
		})
	}
}

// 3. With no enclosing context at all, identity falls to a deterministic
// fingerprint of the definition — never a random value.
func TestIdentityFingerprintFloor(t *testing.T) {
	s := Step{Type: "agent", Prompt: "review the diff"}
	got := s.Identity(IdentityScope{}, 0)
	if !strings.HasPrefix(got, "step:") {
		t.Fatalf("want a fingerprint identity, got %q", got)
	}
	// Deterministic: the same definition fingerprints identically, every
	// call and every boot.
	for i := 0; i < 5; i++ {
		if again := s.Identity(IdentityScope{}, 0); again != got {
			t.Fatalf("fingerprint is not stable: %q vs %q", got, again)
		}
	}
	// Key order within a map field must not move it either.
	a := Step{Type: "agent", Labels: map[string]string{"x": "1", "y": "2"}}
	b := Step{Type: "agent", Labels: map[string]string{"y": "2", "x": "1"}}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("fingerprint must not depend on map iteration order")
	}
}

// Editing a step's PROMPT must not rotate its identity — that would wipe its
// track record and orphan its memories on a routine edit.
func TestStructuralIdentitySurvivesAPromptEdit(t *testing.T) {
	scope := TriggerScope("github.pull_request")
	before := Step{ID: "security", Type: "agent", Prompt: "look for injection"}
	after := Step{ID: "security", Type: "agent", Prompt: "look for injection AND SSRF"}
	if before.Identity(scope, 0) != after.Identity(scope, 0) {
		t.Fatalf("a prompt edit rotated the identity: %q -> %q",
			before.Identity(scope, 0), after.Identity(scope, 0))
	}
}

// A pinned name: survives a REORDER; an un-named, un-id'd step does not
// (which §5 explicitly permits).
func TestNamePinSurvivesReorder(t *testing.T) {
	scope := TriggerScope("github.pull_request")
	named := Step{Name: "reviewer", Type: "agent"}
	if named.Identity(scope, 0) != named.Identity(scope, 7) {
		t.Fatal("a pinned name must not depend on position")
	}
	// An `id:` also pins the slot, so reordering is safe there too.
	withID := Step{ID: "security", Type: "agent"}
	if withID.Identity(scope, 0) != withID.Identity(scope, 7) {
		t.Fatal("an id: should make the structural slot position-independent")
	}
	bare := Step{Type: "agent"}
	if bare.Identity(scope, 0) == bare.Identity(scope, 7) {
		t.Fatal("an un-named, un-id'd step is positional by construction")
	}
}

// Two steps sharing a name share one identity — the reuse a shared
// `agent: fixer` used to give, and what the migration relies on.
func TestSharedNameSharesIdentity(t *testing.T) {
	a := Step{Name: "fixer", ID: "one"}.Identity(TriggerScope("github.pull_request"), 0)
	b := Step{Name: "fixer", ID: "two"}.Identity(TriggerScope("gitlab.merge_request"), 5)
	if a != b {
		t.Fatalf("a shared name must share identity: %q vs %q", a, b)
	}
}

// Identity must never contain a run id, a timestamp, or anything else that
// changes between boots. This is a blunt guard against a regression that
// would be invisible until track records silently reset.
func TestIdentityIsPureConfig(t *testing.T) {
	s := Step{ID: "x", Type: "agent", Prompt: "p"}
	scope := TriggerScope("t")
	first := s.Identity(scope, 0)
	for i := 0; i < 100; i++ {
		if s.Identity(scope, 0) != first {
			t.Fatal("identity varies between calls — it must be a pure function of config")
		}
	}
	if strings.ContainsAny(first, " :") && !strings.HasPrefix(first, "workflow:") && !strings.HasPrefix(first, "check:") {
		t.Fatalf("unexpected identity shape %q", first)
	}
}

// --- step templates and reuse ---------------------------------------------

func TestStepTemplateExtendsSharesIdentityAndBehavior(t *testing.T) {
	src := `
connectors:
  gh: { use: github }
steps:
  fixer:
    type: agent
    workspace: worktree
    archive_when_done: true
    model: claude-opus-5
triggers:
  github.pull_request:
    steps:
      - { id: a, extends: fixer, prompt: "one" }
  gitlab.merge_request:
    steps:
      - { id: b, extends: fixer, prompt: "two" }
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if err := c.resolveExtends(); err != nil {
		t.Fatal(err)
	}
	for i, tr := range c.Triggers {
		s := tr.Steps[0]
		if s.Workspace != "worktree" || !s.ArchiveWhenDone || s.Model.Ref != "claude-opus-5" {
			t.Fatalf("trigger %d: template behavior not inherited: %+v", i, s)
		}
		// Both steps take the TEMPLATE's name as their identity, so they
		// share one memory namespace, session pool, and track record.
		if got := s.Identity(ScopeForTrigger(tr, i), 0); got != "fixer" {
			t.Fatalf("trigger %d: identity = %q, want fixer", i, got)
		}
	}
}

func TestStepTemplateChildNamePinWins(t *testing.T) {
	c := &Config{
		Steps: map[string]Step{"fixer": {Type: "agent", Workspace: "worktree"}},
		Triggers: TriggerList{{Name: "t", On: "gh.pr", Steps: []Step{
			{ID: "a", Extends: "fixer", Name: "special"},
		}}},
	}
	if err := c.resolveExtends(); err != nil {
		t.Fatal(err)
	}
	s := c.Triggers[0].Steps[0]
	if s.Workspace != "worktree" {
		t.Fatal("behavior should still be inherited")
	}
	if got := s.Identity(TriggerScope("t"), 0); got != "special" {
		t.Fatalf("an explicit name must win over the template's, got %q", got)
	}
}

func TestStepTemplateUnknownTargetIsALoadError(t *testing.T) {
	c := &Config{Triggers: TriggerList{{Name: "t", On: "gh.pr", Steps: []Step{{ID: "a", Extends: "ghost"}}}}}
	err := c.resolveExtends()
	if err == nil || !strings.Contains(err.Error(), `unknown step template "ghost"`) {
		t.Fatalf("want an unknown-template error, got %v", err)
	}
}

// Guidance STACKS rather than fills, so applying a template twice would
// duplicate its tone. ApplyStepTemplates must be idempotent — the runtime
// paths (team roles, plan sub-steps) call it on already-loaded steps.
func TestApplyStepTemplatesIsIdempotent(t *testing.T) {
	c := &Config{Steps: map[string]Step{
		"fixer": {Type: "agent", Guidance: &GuidanceSpec{Parts: []string{"house tone"}}},
	}}
	steps := []Step{{ID: "a", Extends: "fixer", Guidance: &GuidanceSpec{Parts: []string{"mine"}}}}
	for i := 0; i < 3; i++ {
		if err := c.ApplyStepTemplates("t", steps); err != nil {
			t.Fatal(err)
		}
	}
	got := steps[0].Guidance.Parts
	if len(got) != 2 || got[0] != "house tone" || got[1] != "mine" {
		t.Fatalf("guidance stacked more than once: %v", got)
	}
}
