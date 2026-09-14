package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func decodeSkill(t *testing.T, y string) (*SkillPolicy, error) {
	t.Helper()
	var got struct {
		Skill *SkillPolicy `yaml:"skill"`
	}
	if err := strictUnmarshal([]byte(y), &got); err != nil {
		return nil, err
	}
	return got.Skill, nil
}

// Both forms grant the same verbs; only the map form carries constraints.
func TestSkillVerbsPolymorphicForms(t *testing.T) {
	list, err := decodeSkill(t, `
skill:
  verbs: [slack.post, github.submit_review]
`)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(list.Verbs, []string{"slack.post", "github.submit_review"}) {
		t.Fatalf("list form: %v", list.Verbs)
	}
	if len(list.VerbScopes) != 0 {
		t.Fatalf("the list form carries no constraints, got %v", list.VerbScopes)
	}

	m, err := decodeSkill(t, `
skill:
  verbs:
    slack.post:           { channel: ["#code-reviews"] }
    github.submit_review: {}
    kv.*:                 { store: [shared-kv] }
  max_calls: 9
`)
	if err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), m.Verbs...)
	sort.Strings(got)
	if want := []string{"github.submit_review", "kv.*", "slack.post"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("map form must grant the same access as the list form: %v", m.Verbs)
	}
	if !reflect.DeepEqual(m.VerbScopes["slack.post"], map[string][]string{"channel": {"#code-reviews"}}) {
		t.Fatalf("slack.post constraints: %v", m.VerbScopes["slack.post"])
	}
	if len(m.VerbScopes["github.submit_review"]) != 0 {
		t.Fatalf("`verb: {}` is access with no widening, got %v", m.VerbScopes["github.submit_review"])
	}
	// Sibling keys keep their ordinary decode.
	if m.MaxCalls != 9 {
		t.Fatalf("max_calls: %d", m.MaxCalls)
	}

	// A bare value is the one-element list.
	one, err := decodeSkill(t, "skill:\n  verbs:\n    slack.post: { channel: \"#ops\" }\n")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(one.VerbScopes["slack.post"]["channel"], []string{"#ops"}) {
		t.Fatalf("scalar value: %v", one.VerbScopes["slack.post"])
	}
}

// Shape errors are load errors, and an unknown SIBLING key still is too — the
// custom decode must not become an escape hatch from strict decoding.
func TestSkillVerbsDecodeErrors(t *testing.T) {
	for _, tc := range []struct{ name, yaml, wantIn string }{
		{"unknown sibling key", "skill: { verbs: [a.b], secrets_vai: broker }", "secrets_vai"},
		{"verb entry is not a map", "skill:\n  verbs:\n    slack.post: [channel]\n", "skill.verbs.slack.post"},
		{"empty value", "skill:\n  verbs:\n    slack.post: { channel: [\"\"] }\n", "empty value"},
		{"empty pattern in list", "skill: { verbs: [\"\"] }", "empty verb pattern"},
		{"scalar block", "skill: nonsense", "want a block"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeSkill(t, tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("want an error mentioning %q, got %v", tc.wantIn, err)
			}
		})
	}
}

// A pack override round-trips a Step through YAML. The map form has to
// survive that, and a pattern that a narrowing pass dropped must NOT come
// back through a stale constraints entry.
func TestSkillVerbsSurviveYAMLRoundTrip(t *testing.T) {
	in := SkillPolicy{
		Verbs:      []string{"slack.post", "kv.*"},
		VerbScopes: map[string]map[string][]string{"slack.post": {"channel": {"#ops"}}, "kv.*": {"store": {"main"}}},
		MaxCalls:   3,
	}
	b, err := yaml.Marshal(map[string]any{"skill": in})
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodeSkill(t, string(b))
	if err != nil {
		t.Fatalf("round trip: %v\n%s", err, b)
	}
	if !reflect.DeepEqual(out.VerbScopes, in.VerbScopes) {
		t.Fatalf("constraints lost in the round trip: %v", out.VerbScopes)
	}
	if out.MaxCalls != 3 {
		t.Fatalf("max_calls lost: %d", out.MaxCalls)
	}

	// Narrowed: the dropped pattern must not reappear.
	narrowed := in
	narrowed.Verbs = []string{"slack.post"}
	narrowed.VerbScopes = pruneVerbScopes(narrowed.VerbScopes, narrowed.Verbs)
	b, _ = yaml.Marshal(map[string]any{"skill": narrowed})
	out, err = decodeSkill(t, string(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Verbs) != 1 || out.Verbs[0] != "slack.post" {
		t.Fatalf("a narrowing must not be undone by the round trip: %v", out.Verbs)
	}
}

// ScopesFor merges every pattern that admits a verb — listing only widens.
func TestScopesForMergesMatchingPatterns(t *testing.T) {
	sk := &SkillPolicy{
		Verbs: []string{"kv.*", "kv.set"},
		VerbScopes: map[string]map[string][]string{
			"kv.*":   {"store": {"shared"}},
			"kv.set": {"store": {"writable"}},
		},
	}
	match := func(pattern, uses string) bool {
		return pattern == uses || strings.HasSuffix(pattern, ".*") &&
			strings.HasPrefix(uses, strings.TrimSuffix(pattern, "*"))
	}
	got := sk.ScopesFor("kv.set", match)["store"]
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"shared", "writable"}) {
		t.Fatalf("both matching patterns must contribute: %v", got)
	}
	if got := sk.ScopesFor("kv.get", match)["store"]; !reflect.DeepEqual(got, []string{"shared"}) {
		t.Fatalf("kv.get sees only the pattern that admits it: %v", got)
	}
}

// The PLAN surface's `verbs:` parses through the same code as skill.verbs and
// resolves per-verb — the rename's whole point. `allow:` + a flat
// `allow_scopes:` map used to apply one scope set to every verb in the policy;
// now a scope belongs to the verb it was written under, and a verb the entry
// does not admit gets nothing from it.
func TestPolicyVerbsResolvePerVerb(t *testing.T) {
	var cfg struct {
		Policy struct {
			AgentAuthored *AgentAuthoredPolicy `yaml:"agent_authored"`
		} `yaml:"policy"`
	}
	if err := strictUnmarshal([]byte(`
policy:
  agent_authored:
    verbs:
      kv.*:      { store: [shared-kv] }
      kv.set:    { store: [writable] }
      gh.comment: { repo: ["acme/docs"] }
      code:      { store: [cache], scope: ["repo:acme/shared"] }
`), &cfg); err != nil {
		t.Fatal(err)
	}
	pol := cfg.Policy.AgentAuthored
	match := func(pattern, uses string) bool {
		return pattern == uses || strings.HasSuffix(pattern, ".*") &&
			strings.HasPrefix(uses, strings.TrimSuffix(pattern, "*"))
	}

	// Both patterns that admit kv.set contribute; kv.get sees only the one
	// that admits it.
	got := pol.ScopesFor("kv.set", match)["store"]
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"shared-kv", "writable"}) {
		t.Fatalf("kv.set must union both matching patterns: %v", got)
	}
	if got := pol.ScopesFor("kv.get", match)["store"]; !reflect.DeepEqual(got, []string{"shared-kv"}) {
		t.Fatalf("kv.get sees only kv.*: %v", got)
	}
	// THE RENAME'S POINT: a scope written under one verb does not leak to
	// another. Under the old flat allow_scopes, `repo: [acme/docs]` applied
	// to every verb with a repo: option.
	if got := pol.ScopesFor("kv.set", match)["repo"]; len(got) != 0 {
		t.Fatalf("gh.comment's repo scope must not reach kv.set: %v", got)
	}
	if got := pol.ScopesFor("gh.comment", match)["repo"]; !reflect.DeepEqual(got, []string{"acme/docs"}) {
		t.Fatalf("gh.comment keeps its own repo scope: %v", got)
	}
	// A step CLASS is a key like any other, and `code` carries the run:code
	// ctx.kv / ctx.memory data allowlists.
	code := pol.ScopesFor("code", match)
	if !reflect.DeepEqual(code["store"], []string{"cache"}) ||
		!reflect.DeepEqual(code["scope"], []string{"repo:acme/shared"}) {
		t.Fatalf("code: carries the ctx.* data scopes: %v", code)
	}
	// `verbs:` is still the ACCESS list too — the map's keys are the grant.
	want := []string{"code", "gh.comment", "kv.*", "kv.set"}
	got = append([]string(nil), pol.Verbs...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("verbs: must be the access list as well as the scope map, got %v", got)
	}
}
