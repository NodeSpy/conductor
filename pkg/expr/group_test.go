package expr

import (
	"reflect"
	"strings"
	"testing"
)

// TestEvalParens covers the grouping the unified `filter:` grammar leans on:
// without it, `!( a && b )` splits naively on && and the negation binds to the
// first term only — which is how a "skip only release PRs on release branches"
// condition would silently become "skip every PR".
func TestEvalParens(t *testing.T) {
	data := map[string]any{
		"is_draft":    false,
		"head_branch": "codex-changelog",
		"title":       "changelog: publish each release entry",
		"labels":      []string{"chore"},
		"score":       7,
	}
	cases := []struct {
		cond string
		want bool
	}{
		// The negation covers the WHOLE group, not just its first term.
		{"!(is_draft && head_branch == 'codex-changelog')", true},
		{"!(head_branch == 'codex-changelog' && !is_draft)", false},
		// Grouping overrides the &&-binds-tighter default.
		{"(is_draft || head_branch == 'codex-changelog') && !is_draft", true},
		{"is_draft || head_branch == 'codex-changelog' && is_draft", false},
		// Nested groups.
		{"!is_draft && !((head_branch == 'staging' || head_branch == 'prod') && contains(title, 'release'))", true},
		{"!is_draft && !((head_branch == 'codex-changelog' || head_branch == 'prod') && contains(title, 'release'))", false},
		// A group as a whole term next to ungrouped ones.
		{"(is_draft) || (!is_draft)", true},
		{"(is_draft) && (!is_draft)", false},
		// Function calls are NOT groups — text precedes the paren.
		{"contains(title, 'changelog')", true},
		{"!contains(title, 'changelog')", false},
		{"contains(labels, 'chore')", true},
		// Comparison inside a group.
		{"(score > 5) && (score < 10)", true},
		{"(score > 5) && (score < 6)", false},
		// Separators inside quotes are not separators.
		{"title == 'changelog: publish each release entry'", true},
		{"contains(title, 'publish each')", true},
	}
	for _, c := range cases {
		got, err := Eval(c.cond, data)
		if err != nil {
			t.Errorf("Eval(%q): unexpected error: %v", c.cond, err)
			continue
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %v, want %v", c.cond, got, c.want)
		}
	}
}

// TestEvalParenDepthGuard rejects pathological nesting instead of recursing
// into a stack overflow.
func TestEvalParenDepthGuard(t *testing.T) {
	deep := strings.Repeat("(", 200) + "x" + strings.Repeat(")", 200)
	if _, err := Eval(deep, map[string]any{"x": true}); err == nil {
		t.Fatal("want an error for 200-deep parenthesis nesting, got nil")
	}
	// A realistic depth still evaluates.
	ok, err := Eval("(((x)))", map[string]any{"x": true})
	if err != nil || !ok {
		t.Fatalf("Eval(\"(((x)))\") = %v, %v; want true, nil", ok, err)
	}
}

// TestEvalUnbalancedParens must not panic or hang; an unbalanced term just
// fails to resolve (falsy), the same as any other unknown path.
func TestEvalUnbalancedParens(t *testing.T) {
	for _, cond := range []string{"(x", "x)", "((x)", "(x))"} {
		if _, err := Eval(cond, map[string]any{"x": true}); err != nil {
			t.Errorf("Eval(%q): unexpected error: %v", cond, err)
		}
	}
}

func TestStartsWithEndsWith(t *testing.T) {
	data := map[string]any{"head_branch": "release/2.4.0", "title": "Release 2.4.0"}
	cases := []struct {
		cond string
		want bool
	}{
		{"startswith(head_branch, 'release/')", true},
		{"startswith(head_branch, 'Release/')", false}, // case-sensitive, unlike legacy exclude.title
		{"!startswith(head_branch, 'feature/')", true},
		{"endswith(head_branch, '2.4.0')", true},
		{"endswith(head_branch, '.tmp')", false},
		{"startswith(title, 'Release')", true},
		// Reads a fact on both sides.
		{"startswith(title, 'Release') && startswith(head_branch, 'release')", true},
	}
	for _, c := range cases {
		got, err := Eval(c.cond, data)
		if err != nil {
			t.Errorf("Eval(%q): unexpected error: %v", c.cond, err)
			continue
		}
		if got != c.want {
			t.Errorf("Eval(%q) = %v, want %v", c.cond, got, c.want)
		}
	}
	if _, err := Eval("startswith(title)", data); err == nil {
		t.Error("startswith() with one argument should error")
	}
	if _, err := Eval("endswith(title, 'a', 'b')", data); err == nil {
		t.Error("endswith() with three arguments should error")
	}
}

func TestRefs(t *testing.T) {
	cases := []struct {
		cond string
		want []string
	}{
		{"", nil},
		{"is_draft", []string{"is_draft"}},
		{"!is_draft", []string{"is_draft"}},
		{"!!is_draft", []string{"is_draft"}},
		// A comparison's RIGHT side is a literal, never a fact: an unquoted
		// bare word there must not be reported (it would reject valid configs).
		{"head_branch == staging", []string{"head_branch"}},
		{"head_branch == 'staging'", []string{"head_branch"}},
		{"score >= 7", []string{"score"}},
		// contains()/startswith() report the haystack only.
		{"contains(title, 'Release')", []string{"title"}},
		{"contains(labels, urgent)", []string{"labels"}},
		{"startswith(head_branch, 'release/')", []string{"head_branch"}},
		{"endswith(head_branch, '.tmp')", []string{"head_branch"}},
		{"exists(review_decision)", []string{"review_decision"}},
		// default()/coalesce() arguments are literal-or-path — not reported.
		{"default(merge_state, 'CLEAN') == 'CLEAN'", nil},
		// Grouping and nesting.
		{"!is_draft && !((head_branch == 'staging' || head_branch == 'prod') && contains(title, 'Release'))",
			[]string{"head_branch", "is_draft", "title"}},
		// A typo'd fact and a typo'd function both surface as references, so
		// validation can reject them.
		{"is_drafft", []string{"is_drafft"}},
		{"nosuchfn(title)", []string{"nosuchfn(title)"}},
		// Template spelling resolves to the same bare path.
		{"{{.is_draft}}", []string{"is_draft"}},
		// Dedup across both sides of a boolean.
		{"is_draft || is_draft", []string{"is_draft"}},
	}
	for _, c := range cases {
		got := Refs(c.cond)
		if len(got) == 0 && len(c.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Refs(%q) = %v, want %v", c.cond, got, c.want)
		}
	}
}
