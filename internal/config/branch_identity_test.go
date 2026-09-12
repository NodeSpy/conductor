package config

import (
	"strings"
	"testing"
)

// H7: same-position steps in different parallel branches must not share an
// identity — they are different work, and a shared identity means a shared
// session pool, memory namespace, and track record.
func TestParallelBranchesGetDistinctIdentities(t *testing.T) {
	var c Config
	if err := strictUnmarshal([]byte(`
workflows:
  w:
    steps:
      - id: fan
        parallel:
          - [{ type: agent, prompt: left }]
          - [{ type: agent, prompt: right }]
`), &c); err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	c.WalkSteps(func(scope IdentityScope, slot int, s *Step) {
		if s.Prompt == "" {
			return
		}
		id := s.Identity(scope, slot)
		if prev, dup := seen[id]; dup {
			t.Fatalf("branch steps %q and %q share identity %q", prev, s.Prompt, id)
		}
		seen[id] = s.Prompt
	})
	if len(seen) != 2 {
		t.Fatalf("want 2 branch identities, got %v", seen)
	}
}

// L6: reuse copies fields, `id:` among them. Two steps merging one base
// that sets an `id:` land on the SAME structural identity and silently
// share a memory namespace, session pool, and track record.
func TestSharedIDInOneScopeIsWarned(t *testing.T) {
	var c Config
	if err := strictUnmarshal([]byte(`
x-t:
  base: &base { type: agent, id: review, workspace: worktree }
connectors:
  gh: { use: github }
workflows:
  w:
    steps:
      - { <<: *base, prompt: one }
      - { <<: *base, prompt: two }
`), &c); err != nil {
		t.Fatal(err)
	}
	c.warnSharedAnchorIDs()
	w := strings.Join(c.PackWarnings(), "\n")
	if !strings.Contains(w, "share the identity") || !strings.Contains(w, "review") {
		t.Fatalf("a shared id: in one scope should be surfaced: %q", w)
	}
}

// Distinct ids say nothing, and an explicit `name:` means the author
// already decided.
func TestDistinctIDsAndPinnedNamesAreQuiet(t *testing.T) {
	var c Config
	if err := strictUnmarshal([]byte(`
connectors:
  gh: { use: github }
workflows:
  w:
    steps:
      - { id: a, type: agent, prompt: one }
      - { id: b, type: agent, prompt: two }
      - { id: c, type: agent, name: shared, prompt: three }
      - { id: d, type: agent, name: shared, prompt: four }
`), &c); err != nil {
		t.Fatal(err)
	}
	c.warnSharedAnchorIDs()
	if w := c.PackWarnings(); len(w) != 0 {
		t.Fatalf("nothing to warn about here: %v", w)
	}
}

// §3: a step's `compensate:` undo is different work from the step it
// undoes — its own prompt, its own outcome. Both were walked with the
// parent's scope AND slot, so they were literally one identity: one
// memory namespace, one session pool, one track record for doing a thing
// and undoing it.
func TestCompensateGetsItsOwnIdentity(t *testing.T) {
	var c Config
	if err := strictUnmarshal([]byte(`
workflows:
  w:
    steps:
      - id: deploy
        type: agent
        prompt: deploy it
        compensate: { type: agent, prompt: roll it back }
`), &c); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	c.WalkSteps(func(scope IdentityScope, slot int, s *Step) {
		ids[s.Prompt] = s.Identity(scope, slot)
	})
	if len(ids) != 2 {
		t.Fatalf("both steps should be walked: %v", ids)
	}
	if ids["deploy it"] == ids["roll it back"] {
		t.Fatalf("a step and its undo must not share an identity: %q", ids["deploy it"])
	}
	if !strings.HasSuffix(ids["roll it back"], "/0") || !strings.Contains(ids["roll it back"], "deploy.compensate") {
		t.Fatalf("the undo should be scoped under its parent's slot: %q", ids["roll it back"])
	}
}
