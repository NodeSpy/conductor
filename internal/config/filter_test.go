package config

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// decodeFilter parses a `filter:` document fragment the way a trigger does.
func decodeFilter(t *testing.T, doc string) (*Filter, error) {
	t.Helper()
	var wrap struct {
		Filter *Filter `yaml:"filter"`
	}
	if err := yaml.Unmarshal([]byte(doc), &wrap); err != nil {
		return nil, err
	}
	return wrap.Filter, nil
}

func mustDecodeFilter(t *testing.T, doc string) *Filter {
	t.Helper()
	f, err := decodeFilter(t, doc)
	if err != nil {
		t.Fatalf("decode %q: %v", doc, err)
	}
	return f
}

// testMatcher is a stand-in connector matcher: `has_<name>` is true when the
// fact of that name is truthy, `eq_<name>` compares the fact to the value, and
// `boom` always errors (so error propagation is testable).
func testMatcher(key string, val any, facts map[string]any) (bool, error) {
	switch {
	case key == "boom":
		return false, fmt.Errorf("match %q exploded", key)
	case strings.HasPrefix(key, "has_"):
		return facts[strings.TrimPrefix(key, "has_")] == true, nil
	case strings.HasPrefix(key, "eq_"):
		return facts[strings.TrimPrefix(key, "eq_")] == val, nil
	}
	return false, fmt.Errorf("unknown match key %q", key)
}

// TestFilterDecodeShapes: the YAML shape IS the boolean structure.
func TestFilterDecodeShapes(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string // Filter.String()
	}{
		{"string is an expr", `filter: "!is_draft"`, `expr("!is_draft")`},
		{"list is an OR", `filter: ["a", "b"]`, `or(expr("a"),expr("b"))`},
		{"map is an AND", `filter: {not_draft: true}`, `and(match(not_draft,true))`},
		{
			"map keys AND in SORTED order, not source order",
			`filter: {not_draft: true, authors: [dependabot], base_branches: [main]}`,
			`and(match(authors,[dependabot]),match(base_branches,[main]),match(not_draft,true))`,
		},
		{
			"the same map written in a different order decodes identically",
			`filter: {base_branches: [main], authors: [dependabot], not_draft: true}`,
			`and(match(authors,[dependabot]),match(base_branches,[main]),match(not_draft,true))`,
		},
		{
			"the reserved expr: key is an Expr AND-ed with its siblings",
			`filter: {not_draft: true, expr: "!contains(title, 'Release')"}`,
			`and(expr("!contains(title, 'Release')"),match(not_draft,true))`,
		},
		{
			"an array of objects is an OR of ANDs",
			"filter:\n  - {authors: [dependabot], not_draft: true}\n  - {labels_any: [urgent]}",
			`or(and(match(authors,[dependabot]),match(not_draft,true)),and(match(labels_any,[urgent])))`,
		},
		{
			"a string leaf sits anywhere a leaf is allowed",
			"filter:\n  - {labels_any: [urgent]}\n  - \"!is_draft\"",
			`or(and(match(labels_any,[urgent])),expr("!is_draft"))`,
		},
		{
			"an object may hold an array-valued nested filter position",
			"filter:\n  not_draft: true\n  labels_any: [a, b]",
			`and(match(labels_any,[a b]),match(not_draft,true))`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mustDecodeFilter(t, c.doc).String()
			if got != c.want {
				t.Errorf("decode:\n got %s\nwant %s", got, c.want)
			}
		})
	}
}

// TestFilterDecodeDeterministic: the same document decodes to the same IR
// every time. Map iteration is randomised in Go, so a decode that walked the
// YAML mapping directly would be stable only by luck.
func TestFilterDecodeDeterministic(t *testing.T) {
	const doc = `filter: {zeta: true, alpha: [x], mid: 3, expr: "a", beta: false}`
	first := mustDecodeFilter(t, doc).String()
	for i := 0; i < 50; i++ {
		if got := mustDecodeFilter(t, doc).String(); got != first {
			t.Fatalf("decode %d differs:\n got %s\nwant %s", i, got, first)
		}
	}
	if !strings.HasPrefix(first, `and(match(alpha,`) {
		t.Errorf("keys should be visited sorted; got %s", first)
	}
}

func TestFilterDecodeRejections(t *testing.T) {
	cases := []struct{ name, doc, wantErr string }{
		{"empty list", `filter: []`, "empty list matches nothing"},
		{"empty map", `filter: {}`, "empty map constrains nothing"},
		{"null", "filter:\n", ""}, // yaml leaves the pointer nil; see below
		{"bare bool", `filter: true`, "condition string"},
		{"bare number", `filter: 7`, "condition string"},
		{"duplicate key", "filter: {a: 1, a: 2}", ""}, // yaml itself rejects this
		{"expr: must be a string", `filter: {expr: [a, b]}`, "condition string"},
		{"nested error names its branch", `filter: ["ok", 7]`, "filter[1]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := decodeFilter(t, c.doc)
			if c.wantErr == "" {
				// Either an error or a nil filter is acceptable here; what
				// must NOT happen is a silently-accepted bogus filter.
				if err == nil && f != nil {
					t.Fatalf("want a rejection or a nil filter, got %s", f)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error containing %q, got filter %s", c.wantErr, f)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want an error containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// TestFilterDecodeDepthGuard refuses pathological nesting at decode, before it
// can recurse during evaluation.
func TestFilterDecodeDepthGuard(t *testing.T) {
	doc := "filter: " + strings.Repeat("[", 200) + `"x"` + strings.Repeat("]", 200)
	if _, err := decodeFilter(t, doc); err == nil {
		t.Fatal("want a depth error for 200-deep nesting, got nil")
	}
}

func TestFilterEval(t *testing.T) {
	facts := map[string]any{
		"is_draft":    false,
		"urgent":      true,
		"head_branch": "codex-changelog",
		"title":       "changelog: publish each release entry",
	}
	cases := []struct {
		name string
		doc  string
		want bool
	}{
		{"expr true", `filter: "!is_draft"`, true},
		{"expr false", `filter: "is_draft"`, false},
		{"match true", `filter: {has_urgent: true}`, true},
		{"match false", `filter: {has_is_draft: true}`, false},
		{"AND all true", `filter: {has_urgent: true, expr: "!is_draft"}`, true},
		{"AND one false", `filter: {has_is_draft: true, expr: "!is_draft"}`, false},
		{"OR any true", `filter: ["is_draft", "!is_draft"]`, true},
		{"OR none true", `filter: ["is_draft", "is_draft"]`, false},
		{"OR of ANDs", "filter:\n  - {has_is_draft: true}\n  - {has_urgent: true, expr: \"!is_draft\"}", true},
		{"negation inside expr", `filter: "!(is_draft || head_branch == 'staging')"`, true},
		{"eq match", `filter: {eq_head_branch: codex-changelog}`, true},
		{"eq match miss", `filter: {eq_head_branch: staging}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := mustDecodeFilter(t, c.doc).Eval(facts, testMatcher)
			if err != nil {
				t.Fatalf("Eval: %v", err)
			}
			if got != c.want {
				t.Errorf("Eval = %v, want %v", got, c.want)
			}
		})
	}
}

// TestFilterEvalEmptyIsTrue: an absent filter fires (docs/design §The Filter IR).
func TestFilterEvalEmptyIsTrue(t *testing.T) {
	var absent *Filter
	if ok, err := absent.Eval(nil, testMatcher); err != nil || !ok {
		t.Fatalf("a nil filter should evaluate true; got %v, %v", ok, err)
	}
	if ok, err := FilterAnd().Eval(nil, testMatcher); err != nil || !ok {
		t.Fatalf("And of nothing should be true; got %v, %v", ok, err)
	}
	if ok, err := FilterOr().Eval(nil, testMatcher); err != nil || ok {
		t.Fatalf("Or of nothing should be false; got %v, %v", ok, err)
	}
}

// TestFilterEvalNot covers the node the legacy exclude lowering is built from.
func TestFilterEvalNot(t *testing.T) {
	facts := map[string]any{"urgent": true}
	cases := []struct {
		f    *Filter
		want bool
	}{
		{FilterNot(FilterMatch("has_urgent", true)), false},
		{FilterNot(FilterNot(FilterMatch("has_urgent", true))), true},
		{FilterNot(FilterOr()), true},                                 // "nothing excluded" → not excluded
		{FilterNot(FilterOr(FilterMatch("has_urgent", true))), false}, // excluded
		{FilterAnd(FilterNot(FilterOr()), FilterMatch("has_urgent", true)), true},
	}
	for i, c := range cases {
		got, err := c.f.Eval(facts, testMatcher)
		if err != nil {
			t.Fatalf("case %d (%s): %v", i, c.f, err)
		}
		if got != c.want {
			t.Errorf("case %d (%s) = %v, want %v", i, c.f, got, c.want)
		}
	}
}

// TestFilterEvalShortCircuits: And stops at the first false and Or at the
// first true, so a later erroring node is never reached.
func TestFilterEvalShortCircuits(t *testing.T) {
	// `true`/`false` are PATHS in the expr language, not literals, so drive
	// the branches off real facts.
	facts := map[string]any{"yes": true, "no": false}
	boom := FilterMatch("boom", nil)
	if ok, err := FilterAnd(FilterExpr("no"), boom).Eval(facts, testMatcher); err != nil || ok {
		t.Errorf("And should short-circuit before boom; got %v, %v", ok, err)
	}
	if ok, err := FilterOr(FilterExpr("yes"), boom).Eval(facts, testMatcher); err != nil || !ok {
		t.Errorf("Or should short-circuit before boom; got %v, %v", ok, err)
	}
	// Reached, the error propagates rather than silently reading false.
	if _, err := FilterAnd(FilterExpr("yes"), boom).Eval(facts, testMatcher); err == nil {
		t.Error("a reached match error should propagate")
	}
}

func TestFilterEvalNoMatcher(t *testing.T) {
	if _, err := FilterMatch("labels_any", []string{"x"}).Eval(nil, nil); err == nil {
		t.Fatal("a Match with no matcher should error, not evaluate false")
	}
}

// TestFilterYAMLRoundTrip: the connectors lowering marshals a config through
// YAML and reparses it (connector.buildIntegration), so a user-authored filter
// must survive the trip with its structure intact.
func TestFilterYAMLRoundTrip(t *testing.T) {
	docs := []string{
		`filter: "!is_draft && !contains(title, 'Release')"`,
		`filter: {not_draft: true, authors: [dependabot]}`,
		"filter:\n  - {authors: [dependabot], not_draft: true}\n  - {labels_any: [urgent, security]}\n  - \"!is_draft\"",
		`filter: {not_draft: true, expr: "!contains(title, 'Release')"}`,
	}
	for _, doc := range docs {
		orig := mustDecodeFilter(t, doc)
		out, err := yaml.Marshal(map[string]any{"filter": orig})
		if err != nil {
			t.Fatalf("marshal %q: %v", doc, err)
		}
		again := mustDecodeFilter(t, string(out))
		if got, want := again.String(), orig.String(); got != want {
			t.Errorf("round trip of %q:\n got %s\nwant %s", doc, got, want)
		}
	}
}

// TestFilterMarshalProgrammaticNode: the legacy lowering's Not/And trees have
// no surface syntax. Marshalling one is a bug, and must say so loudly rather
// than emit null and silently drop the filter.
func TestFilterMarshalProgrammaticNode(t *testing.T) {
	built := FilterNot(FilterOr(FilterMatch("branches", []string{"release/*"})))
	if _, err := yaml.Marshal(map[string]any{"filter": built}); err == nil {
		t.Fatal("marshalling a programmatically built filter should error")
	}
}

func TestFilterMatchKeysAndFactRefs(t *testing.T) {
	f := mustDecodeFilter(t, "filter:\n  - {labels_any: [urgent], expr: \"!is_draft\"}\n  - {authors: [bot], expr: \"head_branch == 'main'\"}")
	gotKeys := strings.Join(f.MatchKeys(), ",")
	if want := "authors,labels_any"; gotKeys != want {
		t.Errorf("MatchKeys = %q, want %q", gotKeys, want)
	}
	gotRefs := strings.Join(f.FactRefs(), ",")
	if want := "head_branch,is_draft"; gotRefs != want {
		t.Errorf("FactRefs = %q, want %q", gotRefs, want)
	}
}
