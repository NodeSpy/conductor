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

// allow_scopes and the legacy allow_* lists are one map with two spellings.
func TestScopeAllowUnionsLegacyAliases(t *testing.T) {
	var cfg struct {
		Policy struct {
			AgentAuthored *AgentAuthoredPolicy `yaml:"agent_authored"`
		} `yaml:"policy"`
	}
	if err := strictUnmarshal([]byte(`
policy:
  agent_authored:
    allow: [ kv.* ]
    allow_targets: [ "legacy/*" ]
    allow_stores: [ legacy-store ]
    allow_secrets: [ house/k ]
    allow_scopes:
      repo: [ "modern/*" ]
      channel: [ "#ops" ]
`), &cfg); err != nil {
		t.Fatal(err)
	}
	got := cfg.Policy.AgentAuthored.ScopeAllow()
	for dim, want := range map[string][]string{
		DimRepo:   {"modern/*", "legacy/*"},
		DimStore:  {"legacy-store"},
		DimSecret: {"house/k"},
		"channel": {"#ops"},
	} {
		g := append([]string(nil), got[dim]...)
		sort.Strings(g)
		w := append([]string(nil), want...)
		sort.Strings(w)
		if !reflect.DeepEqual(g, w) {
			t.Errorf("dimension %q: got %v, want %v", dim, g, w)
		}
	}
	// `allow:` keeps meaning verb access — the two axes stay separate.
	if !reflect.DeepEqual(cfg.Policy.AgentAuthored.Allow, []string{"kv.*"}) {
		t.Errorf("allow: must still be the verb allowlist, got %v", cfg.Policy.AgentAuthored.Allow)
	}
}
