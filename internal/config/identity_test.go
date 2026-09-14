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
			// The registry rung is gone with the registry: a step in a
			// parallel branch gets its scope from BranchScope, and there
			// is no scope kind that bypasses the ladder any more.
			name: "a branch scope is prefixed like any other",
			step: Step{}, scope: BranchScope(WorkflowScope("w"), "fan", 1), slot: 0,
			want: "workflow:w/fan[1]/0",
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

// --- anchors, reuse, and identity -----------------------------------------
//
// An anchor shares CONFIGURATION. It does not share identity: a merged step
// is still identified by where it sits, which is what keeps a step's memory
// and track record attached to its job rather than to its wording.

func TestAnchoredStepsKeepStructuralIdentity(t *testing.T) {
	src := `
x-templates:
  fixer: &fixer
    type: agent
    workspace: worktree
    archive_when_done: true
    model: claude-opus-5
connectors:
  gh: { use: github }
triggers:
  github.pull_request:
    steps:
      - <<: *fixer
        id: a
        prompt: "one"
  gitlab.merge_request:
    steps:
      - <<: *fixer
        id: b
        prompt: "two"
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, tr := range c.Triggers {
		s := tr.Steps[0]
		if s.Workspace != "worktree" || !s.ArchiveWhenDone || s.Model.Ref != "claude-opus-5" {
			t.Fatalf("trigger %d: anchored behavior not merged: %+v", i, s)
		}
		ids = append(ids, s.Identity(ScopeForTrigger(tr, i), 0))
	}
	// Two steps sharing an anchor are still two identities — the anchor
	// copied fields, it did not merge the steps.
	if ids[0] == ids[1] {
		t.Fatalf("an anchor must not collapse identity, both are %q", ids[0])
	}
	for _, id := range ids {
		if id == "fixer" {
			t.Fatalf("identity leaked the anchor label: %q", id)
		}
	}
}

// `name:` is the separate, deliberate opt-in to SHARING identity across
// workflows — the thing an anchor does not do.
func TestNameSharesIdentityAcrossTriggers(t *testing.T) {
	src := `
x-templates:
  fixer: &fixer
    type: agent
    name: fixer
    workspace: worktree
connectors:
  gh: { use: github }
triggers:
  github.pull_request:
    steps: [{ <<: *fixer, id: a, prompt: "one" }]
  gitlab.merge_request:
    steps: [{ <<: *fixer, id: b, prompt: "two" }]
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	for i, tr := range c.Triggers {
		if got := tr.Steps[0].Identity(ScopeForTrigger(tr, i), 0); got != "fixer" {
			t.Fatalf("trigger %d: identity = %q, want the shared name fixer", i, got)
		}
	}
}

// A step's own `name:` wins over one merged in, same as any other key.
func TestMergedNameIsOverridable(t *testing.T) {
	src := `
x-templates:
  fixer: &fixer { type: agent, name: fixer, workspace: worktree }
connectors:
  gh: { use: github }
triggers:
  github.pull_request:
    steps: [{ <<: *fixer, id: a, name: special, prompt: p }]
`
	var c Config
	if err := strictUnmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	s := c.Triggers[0].Steps[0]
	if s.Workspace != "worktree" {
		t.Fatal("the rest of the merge should still apply")
	}
	if got := s.Identity(TriggerScope("t"), 0); got != "special" {
		t.Fatalf("an explicit name must win, got %q", got)
	}
}
