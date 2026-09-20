// Package expr evaluates the small boolean expressions used in workflow step
// `if:` conditions — enough to branch on step outputs without a full language.
//
// Supported:
//   - dotted paths resolved against the data map: steps.evaluate.outputs.has_context
//   - equality/inequality against literals:       x == true, x != "question"
//   - equality/ordering against another path:     pr.head_sha != handoff.pr.head_sha
//   - numeric ordering against literals:          score > 7, score <= 3.5
//   - truthiness of a bare path (and negation):   x   /   !x
//   - boolean combinators:                        a && b || c
//   - parenthesised grouping (and its negation):  !(a && (b || c))
//   - functions: contains(x, y), startswith(x, y), endswith(x, y),
//     exists(path), and the value-producing default(x, fallback) /
//     coalesce(a, b, …) — usable bare (truthiness) or as a comparison's left
//     side: default(sev, "low") == "high"
//
// Precedence: ! / comparison > && > ||; parentheses override it.
package expr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Eval reports whether cond is true given data (nested map[string]any).
// An empty condition is true.
//
// Conditions may write paths bare (steps.x.outputs.y) or in template form
// ({{.x.y}} — the connectors-model spelling); template tokens are rewritten
// to bare paths before evaluation, so a missing value stays nil/falsy instead
// of rendering to text first.
func Eval(cond string, data map[string]any) (bool, error) {
	return eval(stripTemplateTokens(cond), data, 0)
}

// maxGroupDepth bounds parenthesis nesting. Real conditions nest two or three
// deep; a deeper run is pathological input, and refusing it keeps a term like
// "((((…))))" from recursing far enough to overflow the stack.
const maxGroupDepth = 32

// eval is Eval's recursive core; depth counts the parenthesis groups entered.
func eval(cond string, data map[string]any, depth int) (bool, error) {
	if depth > maxGroupDepth {
		return false, fmt.Errorf("condition nests parentheses more than %d deep", maxGroupDepth)
	}
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return true, nil
	}
	for _, or := range splitTop(cond, "||") { // OR: any true
		all := true
		for _, and := range splitTop(or, "&&") { // AND: all true
			ok, err := atom(strings.TrimSpace(and), data, depth)
			if err != nil {
				return false, err
			}
			if !ok {
				all = false
				break
			}
		}
		if all {
			return true, nil
		}
	}
	return false, nil
}

// comparison operators, longest first so ">=" wins over ">".
var comparators = []string{"==", "!=", ">=", "<=", ">", "<"}

// maxNegations bounds leading `!` operators on a single term. Real conditions
// use zero or one; a large run is pathological input, and stripping them
// iteratively under this cap keeps a term like "!!!!…!x" from recursing deep
// enough to overflow the stack.
const maxNegations = 64

func atom(a string, data map[string]any, depth int) (bool, error) {
	if a == "" {
		return false, fmt.Errorf("empty condition term")
	}
	// Fold leading `!` negations iteratively (not by recursion), tracking parity.
	neg := false
	for n := 0; strings.HasPrefix(a, "!"); n++ {
		if n >= maxNegations {
			return false, fmt.Errorf("too many '!' negations in condition term (max %d)", maxNegations)
		}
		neg = !neg
		a = strings.TrimSpace(a[1:])
		if a == "" {
			return false, fmt.Errorf("empty condition term")
		}
	}

	// A parenthesised group is a full sub-condition: `!(a && b)` negates the
	// whole thing rather than just `a`.
	if inner, ok := group(a); ok {
		res, err := eval(inner, data, depth+1)
		if err != nil {
			return false, err
		}
		return res != neg, nil
	}

	res, err := evalTerm(a, data)
	if err != nil {
		return false, err
	}
	return res != neg, nil
}

// group returns the contents of a term wrapped in ONE pair of parentheses —
// "(a && b)" → "a && b". A function call (`contains(x, y)`) is not a group:
// text precedes the paren. Neither is "(a) && (b)": the opening paren closes
// before the end, so it is left to splitTop.
func group(a string) (string, bool) {
	if len(a) < 2 || a[0] != '(' || a[len(a)-1] != ')' {
		return "", false
	}
	depth := 0
	var quote byte
	for i := 0; i < len(a); i++ {
		switch c := a[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 && i != len(a)-1 {
				return "", false // closed early: "(a) && (b)"
			}
		}
	}
	if depth != 0 {
		return "", false // unbalanced — let the term fail on its own terms
	}
	return strings.TrimSpace(a[1 : len(a)-1]), true
}

// evalTerm evaluates a single term with any leading negations already stripped:
// a function call, a comparison, or the truthiness of a bare path.
func evalTerm(a string, data map[string]any) (bool, error) {
	if ok, handled, err := function(a, data); handled {
		return ok, err
	}
	for _, op := range comparators {
		if i := indexTop(a, op); i >= 0 {
			l := strings.TrimSpace(a[:i])
			r := strings.TrimSpace(a[i+len(op):])
			lv, err := sideValue(l, data)
			if err != nil {
				return false, err
			}
			// A right side that is itself a dotted data path (unquoted,
			// non-numeric, resolving to a real value) is compared value-to-value
			// — this is what lets a condition compare two facts, e.g.
			// `pr.head_sha != handoff.pr.head_sha`. Anything else (a quoted
			// string, bool, number, or a bare word that resolves to nothing)
			// stays a literal, so existing conditions are unchanged.
			if rv, ok := rhsPathValue(r, data); ok {
				return compareValues(lv, rv, op), nil
			}
			return compare(lv, literal(r), op), nil
		}
	}
	return truthy(resolve(a, data)), nil
}

// sideValue resolves a comparison's left side: a default()/coalesce() call
// or a data path.
func sideValue(s string, data map[string]any) (any, error) {
	if v, handled, err := valueFunction(s, data); handled {
		return v, err
	}
	return resolve(s, data), nil
}

// function evaluates the pinned function set: contains(x, y) — substring or
// list membership — exists(path) — the path resolves to a non-nil value —
// and the truthiness of a bare default()/coalesce() call.
// handled=false means the term isn't a function call.
func function(a string, data map[string]any) (ok, handled bool, err error) {
	name, rest, found := strings.Cut(a, "(")
	if !found || !strings.HasSuffix(rest, ")") || strings.ContainsAny(name, " \t") {
		return false, false, nil
	}
	argstr := strings.TrimSuffix(rest, ")")
	switch name {
	case "exists":
		return resolve(strings.TrimSpace(argstr), data) != nil, true, nil
	case "contains":
		args := splitArgs(argstr)
		if len(args) != 2 {
			return false, true, fmt.Errorf("contains() takes two arguments, got %d", len(args))
		}
		hay := resolveTerm(args[0], data)
		needle := resolveTerm(args[1], data)
		return containsValue(hay, needle), true, nil
	case "startswith", "endswith":
		args := splitArgs(argstr)
		if len(args) != 2 {
			return false, true, fmt.Errorf("%s() takes two arguments, got %d", name, len(args))
		}
		s := asString(resolveTerm(args[0], data))
		affix := asString(resolveTerm(args[1], data))
		if name == "startswith" {
			return strings.HasPrefix(s, affix), true, nil
		}
		return strings.HasSuffix(s, affix), true, nil
	case "default", "coalesce":
		v, _, err := valueFunction(a, data)
		return truthy(v), true, err
	}
	return false, false, nil
}

// valueFunction evaluates the value-producing functions — default(x,
// fallback) and coalesce(a, b, …): the first argument that is present and
// non-empty (nil and "" count as empty; 0 and false are real values). Each
// argument is a quoted/bool/number literal or a data path (a missing path is
// empty). handled=false means the term isn't a value function call.
func valueFunction(a string, data map[string]any) (v any, handled bool, err error) {
	name, rest, found := strings.Cut(a, "(")
	if !found || !strings.HasSuffix(rest, ")") || strings.ContainsAny(name, " \t") {
		return nil, false, nil
	}
	args := splitArgs(strings.TrimSuffix(rest, ")"))
	switch name {
	case "default":
		if len(args) != 2 {
			return nil, true, fmt.Errorf("default() takes two arguments, got %d", len(args))
		}
	case "coalesce":
		if len(args) == 0 {
			return nil, true, fmt.Errorf("coalesce() takes at least one argument")
		}
	default:
		return nil, false, nil
	}
	for _, arg := range args {
		if v := argValue(arg, data); !emptyValue(v) {
			return v, true, nil
		}
	}
	return nil, true, nil
}

// argValue resolves a default/coalesce argument: a quoted/bool/number
// literal stays a literal; anything else is a data path (nil when missing) —
// unlike resolveTerm, a bare word with no data match is NOT promoted to a
// string, so fallbacks only come from explicit literals.
func argValue(s string, data map[string]any) any {
	l := literal(s)
	if l.isB {
		return l.b
	}
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') {
		return l.s
	}
	if l.isNum {
		return l.f
	}
	return resolve(s, data)
}

// emptyValue reports the default/coalesce notion of "absent": nil or an
// empty string. Zero numbers and false are real values.
func emptyValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && s == ""
}

// splitArgs splits a function argument list on commas outside quotes.
func splitArgs(s string) []string {
	var out []string
	var b strings.Builder
	var inS, inD bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inS:
			if c == '\'' {
				inS = false
			}
			b.WriteByte(c)
		case inD:
			if c == '"' {
				inD = false
			}
			b.WriteByte(c)
		case c == '\'':
			inS = true
			b.WriteByte(c)
		case c == '"':
			inD = true
			b.WriteByte(c)
		case c == ',':
			out = append(out, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if t := strings.TrimSpace(b.String()); t != "" || len(out) > 0 {
		out = append(out, t)
	}
	return out
}

// resolveTerm resolves a function argument: a quoted/bool/number literal, or
// a data path.
func resolveTerm(s string, data map[string]any) any {
	l := literal(s)
	if l.isB {
		return l.b
	}
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') {
		return l.s
	}
	if l.isNum {
		return l.f
	}
	if v := resolve(s, data); v != nil {
		return v
	}
	return s // bare word with no data match — treat as a literal string
}

// containsValue reports substring match for strings and membership for lists.
func containsValue(hay, needle any) bool {
	switch h := hay.(type) {
	case string:
		return strings.Contains(h, asString(needle))
	case []string:
		for _, e := range h {
			if e == asString(needle) {
				return true
			}
		}
	case []any:
		for _, e := range h {
			if asString(e) == asString(needle) {
				return true
			}
		}
	}
	return false
}

// tmplTokenRe matches {{.a.b}} template tokens (no pipelines/functions).
var tmplTokenRe = regexp.MustCompile(`\{\{\s*\.([A-Za-z0-9_.]+)\s*\}\}`)

// stripTemplateTokens rewrites {{.a.b}} tokens to bare paths so both
// condition spellings evaluate identically.
func stripTemplateTokens(s string) string {
	return tmplTokenRe.ReplaceAllString(s, "$1")
}

// compare applies an operator between a resolved value and a literal. Ordering
// operators (>, <, >=, <=) require both sides to be numeric, else false.
// rhsPathValue treats the right side of a comparison as a data path and returns
// its resolved value — but only when it is unmistakably a path: unquoted, dot-
// separated, made of path characters, not a number, and resolving to a non-nil
// value. Everything else returns ok=false so the caller falls back to literal
// parsing (a quoted string, bool, number, or a bare word that names nothing).
func rhsPathValue(r string, data map[string]any) (any, bool) {
	if len(r) >= 2 && (r[0] == '"' || r[0] == '\'') {
		return nil, false // a quoted string is a literal
	}
	if !strings.Contains(r, ".") {
		return nil, false // a bare word / bool / int is a literal
	}
	if _, err := strconv.ParseFloat(r, 64); err == nil {
		return nil, false // a decimal number (e.g. 3.5) is a literal
	}
	for _, c := range r {
		if c != '.' && c != '_' && c != '-' &&
			!(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
			return nil, false // not a plain path token
		}
	}
	v := resolve(r, data)
	if v == nil {
		return nil, false
	}
	return v, true
}

// compareValues compares two resolved values (both sides are data). Equality
// coerces numerically when both are numeric, else by string — mirroring how a
// literal comparison treats numbers and strings; ordering needs both numeric.
func compareValues(a, b any, op string) bool {
	switch op {
	case "==":
		return equalValues(a, b)
	case "!=":
		return !equalValues(a, b)
	}
	fa, oka := asFloat(a)
	fb, okb := asFloat(b)
	if !oka || !okb {
		return false
	}
	switch op {
	case ">":
		return fa > fb
	case "<":
		return fa < fb
	case ">=":
		return fa >= fb
	case "<=":
		return fa <= fb
	}
	return false
}

func equalValues(a, b any) bool {
	if fa, ok := asFloat(a); ok {
		if fb, ok := asFloat(b); ok {
			return fa == fb
		}
	}
	return asString(a) == asString(b)
}

func compare(v any, l lit, op string) bool {
	switch op {
	case "==":
		return equal(v, l)
	case "!=":
		return !equal(v, l)
	}
	fv, ok := asFloat(v)
	if !ok || !l.isNum {
		return false
	}
	switch op {
	case ">":
		return fv > l.f
	case "<":
		return fv < l.f
	case ">=":
		return fv >= l.f
	case "<=":
		return fv <= l.f
	}
	return false
}

// splitTop splits on sep at the top level: outside quotes and outside
// parentheses, so a grouped sub-condition and a function call's argument list
// survive intact.
func splitTop(s, sep string) []string {
	var parts []string
	last := 0
	scanTop(s, func(i int) bool {
		if !strings.HasPrefix(s[i:], sep) {
			return false
		}
		parts = append(parts, strings.TrimSpace(s[last:i]))
		last = i + len(sep)
		return true // skip past sep
	}, len(sep))
	return append(parts, strings.TrimSpace(s[last:]))
}

// indexTop returns the index of the first occurrence of op outside quotes and
// parentheses, or -1.
func indexTop(s, op string) int {
	found := -1
	scanTop(s, func(i int) bool {
		if found >= 0 || !strings.HasPrefix(s[i:], op) {
			return false
		}
		found = i
		return true
	}, len(op))
	return found
}

// scanTop walks s, calling hit at every byte offset that sits outside quotes
// and at parenthesis depth zero. hit returns true when it consumed a token of
// skip bytes starting there, which the scan then steps over. An unmatched ')'
// is ignored rather than driving the depth negative, so a stray one cannot
// hide the rest of the string.
func scanTop(s string, hit func(i int) bool, skip int) {
	depth := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case depth == 0 && hit(i):
			i += skip - 1
		}
	}
}

// resolve walks a dotted path through nested maps.
func resolve(path string, data map[string]any) any {
	cur := any(data)
	for _, key := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

type lit struct {
	b     bool
	isB   bool
	f     float64
	isNum bool
	s     string
}

func literal(s string) lit {
	switch s {
	case "true":
		return lit{b: true, isB: true}
	case "false":
		return lit{b: false, isB: true}
	}
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return lit{s: s[1 : len(s)-1]}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return lit{f: f, isNum: true, s: s}
	}
	return lit{s: s}
}

func equal(v any, l lit) bool {
	switch {
	case l.isB:
		return truthy(v) == l.b
	case l.isNum:
		f, ok := asFloat(v)
		return ok && f == l.f
	default:
		return asString(v) == l.s
	}
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != "" && x != "false" && x != "0"
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	default:
		return true
	}
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

func asString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}
