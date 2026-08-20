// Package expr evaluates the small boolean expressions used in workflow step
// `if:` conditions — enough to branch on step outputs without a full language.
//
// Supported:
//   - dotted paths resolved against the data map: steps.evaluate.outputs.has_context
//   - equality/inequality against literals:       x == true, x != "question"
//   - truthiness of a bare path (and negation):   x   /   !x
//   - boolean combinators (no parentheses):       a && b || c
//
// Precedence: ! > == / != > && > ||.
package expr

import (
	"fmt"
	"strconv"
	"strings"
)

// Eval reports whether cond is true given data (nested map[string]any).
// An empty condition is true.
func Eval(cond string, data map[string]any) (bool, error) {
	cond = strings.TrimSpace(cond)
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

func atom(a string, data map[string]any) (bool, error) {
	if a == "" {
		return false, fmt.Errorf("empty condition term")
	}
	switch {
	case strings.Contains(a, "=="):
		l, r, _ := cut(a, "==")
		return equal(resolve(l, data), literal(r)), nil
	case strings.Contains(a, "!="):
		l, r, _ := cut(a, "!=")
		return !equal(resolve(l, data), literal(r)), nil
	}
	if strings.HasPrefix(a, "!") {
		return !truthy(resolve(strings.TrimSpace(a[1:]), data)), nil
	}
	return truthy(resolve(a, data)), nil
}

// splitTop splits on sep at the top level (no nesting/quotes in conditions).
func splitTop(s, sep string) []string {
	parts := strings.Split(s, sep)
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func cut(s, sep string) (string, string, bool) {
	i := strings.Index(s, sep)
	if i < 0 {
		return s, "", false
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(sep):]), true
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
