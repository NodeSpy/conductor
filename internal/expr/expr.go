// Package expr evaluates the small boolean expressions used in workflow step
// `if:` conditions — enough to branch on step outputs without a full language.
//
// Supported:
//   - dotted paths resolved against the data map: steps.evaluate.outputs.has_context
//   - equality/inequality against literals:       x == true, x != "question"
//   - numeric ordering against literals:          score > 7, score <= 3.5
//   - truthiness of a bare path (and negation):   x   /   !x
//   - boolean combinators (no parentheses):       a && b || c
//
// Precedence: ! / comparison > && > ||.
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
	cond = strings.TrimSpace(stripTemplateTokens(cond))
	if cond == "" {
		return true, nil
	}
	for _, or := range splitTop(cond, "||") { // OR: any true
		all := true
		for _, and := range splitTop(or, "&&") { // AND: all true
			ok, err := atom(strings.TrimSpace(and), data)
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

func atom(a string, data map[string]any) (bool, error) {
	if a == "" {
		return false, fmt.Errorf("empty condition term")
	}
	if strings.HasPrefix(a, "!") {
		ok, err := atom(strings.TrimSpace(a[1:]), data)
		return !ok, err
	}
	if ok, handled, err := function(a, data); handled {
		return ok, err
	}
	for _, op := range comparators {
		if i := strings.Index(a, op); i >= 0 {
			l := strings.TrimSpace(a[:i])
			r := strings.TrimSpace(a[i+len(op):])
			return compare(resolve(l, data), literal(r), op), nil
		}
	}
	return truthy(resolve(a, data)), nil
}

// function evaluates the pinned function set: contains(x, y) — substring or
// list membership — and exists(path) — the path resolves to a non-nil value.
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
	}
	return false, false, nil
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

// splitTop splits on sep at the top level (no nesting/quotes in conditions).
func splitTop(s, sep string) []string {
	parts := strings.Split(s, sep)
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
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
