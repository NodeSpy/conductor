package config

import "testing"

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
