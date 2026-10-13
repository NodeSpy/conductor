package expr

import (
	"sort"
	"strings"
)

// Refs returns the data paths cond READS, sorted and deduplicated. It is the
// static counterpart of Eval: a caller validating a condition against a
// declared universe of facts (see internal/config.Filter) uses it to reject
// "!is_drafft" at load time instead of silently evaluating it as nil.
//
// It deliberately under-reports rather than over-reports, because a false
// positive would reject a valid config. Collected:
//
//   - the LEFT side of a comparison            (head_branch == 'staging')
//   - a bare truthiness term                   (is_draft, !is_draft)
//   - exists()'s argument
//   - contains()/startswith()/endswith()'s FIRST argument (the haystack)
//
// Not collected: a comparison's right side, and any other function argument.
// A bare word in those positions is a string literal — `head_branch ==
// staging` compares against the text "staging" — so treating it as a fact
// reference would reject working conditions.
func Refs(cond string) []string {
	seen := map[string]bool{}
	collect(stripTemplateTokens(cond), seen, 0)
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// collect walks cond's boolean structure, mirroring eval's descent.
func collect(cond string, seen map[string]bool, depth int) {
	if depth > maxGroupDepth {
		return
	}
	for _, or := range splitTop(strings.TrimSpace(cond), "||") {
		for _, and := range splitTop(or, "&&") {
			collectTerm(strings.TrimSpace(and), seen, depth)
		}
	}
}

// collectTerm walks one term: negations, a parenthesised group, a function
// call, a comparison, or a bare path.
func collectTerm(a string, seen map[string]bool, depth int) {
	for strings.HasPrefix(a, "!") {
		a = strings.TrimSpace(a[1:])
	}
	if a == "" {
		return
	}
	if inner, ok := group(a); ok {
		collect(inner, seen, depth+1)
		return
	}
	if collectFunc(a, seen) {
		return
	}
	for _, op := range comparators {
		if i := indexTop(a, op); i >= 0 {
			l := strings.TrimSpace(a[:i])
			if !collectFunc(l, seen) {
				addRef(l, seen)
			}
			return // the right side is a literal, never a fact
		}
	}
	addRef(a, seen)
}

// collectFunc records the path arguments of a known function call and reports
// whether a was one. default()/coalesce() arguments are skipped: a bare word
// there is an explicit literal fallback, not a fact.
func collectFunc(a string, seen map[string]bool) bool {
	name, rest, found := strings.Cut(a, "(")
	if !found || !strings.HasSuffix(rest, ")") || strings.ContainsAny(name, " \t") {
		return false
	}
	args := splitArgs(strings.TrimSuffix(rest, ")"))
	switch name {
	case "exists":
		if len(args) > 0 {
			addRef(args[0], seen)
		}
	case "contains", "startswith", "endswith":
		if len(args) > 0 {
			addRef(args[0], seen) // the haystack; the needle may be a literal
		}
	case "default", "coalesce":
		// arguments are literal-or-path by design — not reference sites
	default:
		return false // not a known function: the whole term is a path
	}
	return true
}

// addRef records s as a data path unless it is a literal (quoted, boolean, or
// numeric) or empty.
func addRef(s string, seen map[string]bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "true" || s == "false" {
		return
	}
	if s[0] == '\'' || s[0] == '"' {
		return
	}
	if l := literal(s); l.isNum {
		return
	}
	seen[s] = true
}
