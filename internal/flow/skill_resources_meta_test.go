package flow

import (
	"context"
	"os"
	"strings"
	"testing"
)

// META-TEST C. The skill surface gated only by connector.verb — it never
// looked at WHICH resource the option named. The plan surface has always
// checked that (checkVerbResources), so the two surfaces disagreed about the
// same grant: a `skill.verbs: [svc.post]` grant issued for the PR under
// review could be pointed at any repo the connector could reach by passing a
// different `repo:` option, and any store by passing a different `store:`.
//
// Both surfaces now call the same function with the same allowlists, so they
// cannot drift. This asserts the property from the skill side, over every
// resource kind the shared check covers.
func TestSkillSurfaceScopesResourcesLikeThePlanSurface(t *testing.T) {
	const cfgYAML = `
connectors:
  svc: { use: fake }
policy:
  agent_authored:
    allow: ["**"]
    allow_targets: ["allowed/repo"]
    allow_stores: ["allowed-store"]
`
	cfg := loadConfig(t, cfgYAML)
	r := newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner

	// The dispatch this grant was issued for.
	id := SkillIdentity{Agent: "probe", Verbs: []string{"svc.*"}, Repo: "trigger/repo", Number: 7}

	call := func(t *testing.T, opts map[string]any) error {
		t.Helper()
		_, err := r.RunSkillVerb(context.Background(), id, "svc.post", opts)
		return err
	}
	// scoped reports whether the call was refused by the RESOURCE check
	// specifically, not by some later failure (an unroutable fake connector).
	scoped := func(err error) bool {
		return err != nil && (strings.Contains(err.Error(), "allow_targets") ||
			strings.Contains(err.Error(), "allow_stores"))
	}

	for _, tc := range []struct {
		name    string
		opts    map[string]any
		refused bool
		why     string
	}{
		{
			name: "repo the grant was issued for", opts: map[string]any{"repo": "trigger/repo"},
			refused: false, why: "the dispatch's own target is implicitly in scope",
		},
		{
			name: "allow-listed repo", opts: map[string]any{"repo": "allowed/repo"},
			refused: false, why: "allow_targets names it",
		},
		{
			name: "a DIFFERENT repo", opts: map[string]any{"repo": "victim/repo"},
			refused: true, why: "a grant for one PR must not act on another repo",
		},
		{
			name: "allow-listed store", opts: map[string]any{"store": "allowed-store"},
			refused: false, why: "allow_stores names it",
		},
		{
			name: "a non-allow-listed store", opts: map[string]any{"store": "victim-store"},
			refused: true, why: "a kv/sql grant must not reach an unlisted store",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := call(t, tc.opts)
			if tc.refused && !scoped(err) {
				t.Fatalf("%s was NOT refused by the resource check (%s); err=%v", tc.name, tc.why, err)
			}
			if !tc.refused && scoped(err) {
				t.Fatalf("%s was refused by the resource check but %s; err=%v", tc.name, tc.why, err)
			}
		})
	}
}

// The check must be the SHARED one. A private reimplementation on the skill
// side is how the two surfaces drifted apart in the first place.
func TestSkillSurfaceUsesTheSharedResourceCheck(t *testing.T) {
	src := readSource(t, "skillverbs.go")
	if !strings.Contains(src, "r.checkVerbResources(") {
		t.Error("RunSkillVerb no longer calls checkVerbResources — the skill surface " +
			"and the plan surface must enforce resource scoping from one function, or a " +
			"grant means different things depending on which surface the agent uses")
	}
}

// A vault's verbs read and write SECRET material. A broad `["*"]` grant is
// written to mean "the ordinary connectors"; folding every vault into it hands
// the agent the secret store by accident. An explicit grant still works —
// that is the operator making a deliberate choice.
func TestAWildcardGrantDoesNotReachAVault(t *testing.T) {
	cfgYAML := `
connectors:
  svc: { use: fake }
vaults:
  kv1: { type: file, dir: ` + t.TempDir() + ` }
`
	cfg := loadConfig(t, cfgYAML)
	reg := buildRegistry(t, cfg)
	r := newTestRunner(t, cfg, reg).Runner

	has := func(patterns []string, prefix string) bool {
		for _, g := range r.GrantedVerbs(patterns) {
			if strings.HasPrefix(g.Uses, prefix) {
				return true
			}
		}
		return false
	}

	if _, ok := reg.Get("kv1"); !ok {
		t.Skip("no vault connector in this build's fixture")
	}
	if has([]string{"*"}, "kv1.") {
		t.Error(`a ["*"] grant reached the vault's verbs — a wildcard is written to mean ` +
			`"the ordinary connectors", and silently including secret read/write in it is ` +
			`a capability the operator never chose to hand over`)
	}
	if !has([]string{"kv1.read"}, "kv1.") {
		t.Error("an EXPLICIT vault grant was dropped — the exclusion is only meant to stop " +
			"wildcards, not to make vaults ungrantable")
	}
	// The wildcard still reaches ordinary connectors, so the assertion above
	// can't pass by breaking wildcards outright.
	if !has([]string{"*"}, "svc.") {
		t.Error(`the ["*"] grant stopped reaching ordinary connectors`)
	}
}

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
