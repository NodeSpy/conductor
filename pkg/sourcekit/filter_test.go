package sourcekit

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func equalsMatcher(key string, val any, facts map[string]any) (bool, error) {
	for _, want := range FilterValueStrings(val) {
		if s, _ := facts[key].(string); s == want {
			return true, nil
		}
	}
	if b, ok := val.(bool); ok {
		got, _ := facts[key].(bool)
		return got == b, nil
	}
	return false, nil
}

func TestFilterEvalStructure(t *testing.T) {
	facts := map[string]any{"repo": "a/b", "draft": false, "n": 3}
	cases := []struct {
		name string
		f    *Filter
		want bool
	}{
		{"nil is true", nil, true},
		{"empty and is true", FilterAnd(), true},
		{"empty or is false", FilterOr(), false},
		{"match", FilterMatch("repo", []any{"x/y", "a/b"}), true},
		{"not match", FilterNot(FilterMatch("repo", "a/b")), false},
		{"and short-circuits false", FilterAnd(FilterMatch("repo", "a/b"), FilterMatch("draft", true)), false},
		{"or finds one", FilterOr(FilterMatch("repo", "z/z"), FilterMatch("draft", false)), true},
		{"expr", FilterExpr("n > 2 && !draft"), true},
		{"and drops nil kids", FilterAnd(nil, FilterMatch("repo", "a/b"), nil), true},
	}
	for _, c := range cases {
		got, err := c.f.Eval(facts, equalsMatcher)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: %s = %v, want %v", c.name, c.f, got, c.want)
		}
	}
}

func TestFilterEvalErrorsFailClosed(t *testing.T) {
	if ok, err := FilterMatch("k", 1).Eval(nil, nil); ok || err == nil {
		t.Fatalf("a match with no matcher must error, got %v %v", ok, err)
	}
	bad := &Filter{Op: FilterOpNot}
	if ok, err := bad.Eval(nil, equalsMatcher); ok || err == nil {
		t.Fatalf("a malformed not must error, got %v %v", ok, err)
	}
	deep := FilterMatch("repo", "a/b")
	for i := 0; i <= MaxFilterDepth+1; i++ {
		deep = FilterAnd(deep)
	}
	if _, err := deep.Eval(map[string]any{"repo": "a/b"}, equalsMatcher); err == nil {
		t.Fatal("a filter deeper than MaxFilterDepth must be refused")
	}
}

// The wire form is what a source plugin is handed: it must round-trip every
// node kind losslessly and evaluate identically on the far side.
func TestFilterJSONRoundTrip(t *testing.T) {
	f := FilterAnd(
		FilterNot(FilterOr(FilterMatch("branch", []any{"wip/*"}), FilterMatch("label_any", []any{"skip"}))),
		FilterMatch("draft", false),
		FilterExpr("contains(title, 'x')"),
	)
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"op":"and"`) || !strings.Contains(string(b), `"op":"expr"`) {
		t.Fatalf("not the structural form: %s", b)
	}
	var back Filter
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.String() != f.String() {
		t.Fatalf("round trip changed the filter:\n got %s\nwant %s", back.String(), f.String())
	}
	again, _ := json.Marshal(&back)
	if string(again) != string(b) {
		t.Fatalf("re-marshal differs:\n got %s\nwant %s", again, b)
	}
	facts := map[string]any{"branch": "main", "label_any": "", "draft": false, "title": "x"}
	want, _ := f.Eval(facts, equalsMatcher)
	got, _ := back.Eval(facts, equalsMatcher)
	if got != want || !got {
		t.Fatalf("evaluates differently after the round trip: %v vs %v", got, want)
	}
}

func TestFilterJSONRefusesMalformed(t *testing.T) {
	for _, raw := range []string{
		`{"op":"xor"}`,
		`{"op":"not","kids":[]}`,
		`{"op":"and","kids":[null]}`,
	} {
		var f Filter
		if err := json.Unmarshal([]byte(raw), &f); err == nil {
			t.Errorf("%s: decoded without error", raw)
		}
	}
}

func TestFilterStructuralQueries(t *testing.T) {
	f := FilterAnd(
		FilterMatch("repo", []any{"a/*", "b/c"}),
		FilterNot(FilterMatch("repo", "a/x")),
		FilterOr(FilterMatch("repo", "d/e"), FilterMatch("label_any", "x")),
	)
	if got := f.MatchUnion("repo"); !reflect.DeepEqual(got, []string{"a/*", "b/c", "d/e"}) {
		t.Errorf("MatchUnion = %v", got)
	}
	if got := f.TopLevelNegated("repo"); !reflect.DeepEqual(got, []string{"a/x"}) {
		t.Errorf("TopLevelNegated = %v", got)
	}
	if f.FlatConjunctionOf("repo") {
		t.Error("an Or arm is not a flat conjunction")
	}
	if !FilterAnd(FilterMatch("repo", "a/b"), FilterNot(FilterMatch("repo", "c/d"))).FlatConjunctionOf("repo") {
		t.Error("repo AND not_repo is a flat conjunction")
	}
	if f.OnlyKeys("repo") {
		t.Error("label_any is not repo")
	}
}
