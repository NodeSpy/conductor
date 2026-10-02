package sourcekit

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/pkg/expr"
)

// Filter is the unified trigger `filter:` as a source evaluates it — the IR an
// operator's `filter:` decodes into, and the shape a source's INTRINSIC
// DEFAULT keep-condition lowers into, so both run through one evaluator
// (docs/design/unified-filter.md).
//
// It is the daemon's own IR, not a copy of it: conductor's config package
// decodes the YAML grammar (`filter: "!is_draft"`, `{not_draft: true}`,
// `[a, b]`) into its own node type for error reporting, and converts to this
// one for everything else — evaluation and the structural queries below — so a
// source plugin evaluating a trigger's filter and the daemon evaluating it
// agree by construction.
//
// It crosses the plugin wire in its STRUCTURAL form (see MarshalJSON): a
// source plugin receives each trigger's filter already parsed, and never has to
// implement the surface grammar.
//
// Only Match knows what a key means. The boolean structure, negation, and expr
// conditions (pkg/expr) are connector-agnostic; a source supplies a Matcher for
// its own keys (`label_any`, `branch`, …) and the facts map an event publishes.
type Filter struct {
	Op FilterOp
	// Kids are the operands of And/Or (any number) and Not (exactly one).
	Kids []*Filter
	// Expr is the condition source of an Expr node (pkg/expr syntax).
	Expr string
	// Key/Val are one structured match key and its configured value.
	Key string
	Val any
}

// FilterOp names a Filter node's kind.
type FilterOp string

// The node kinds.
const (
	FilterOpAnd   FilterOp = "and"
	FilterOpOr    FilterOp = "or"
	FilterOpNot   FilterOp = "not"
	FilterOpExpr  FilterOp = "expr"
	FilterOpMatch FilterOp = "match"
)

// MaxFilterDepth bounds how deeply a filter may nest. Real filters are two or
// three levels; a deeper one is pathological input, and refusing it keeps
// evaluation from recursing far enough to overflow the stack.
const MaxFilterDepth = 32

// FilterAnd builds an And node, dropping nil operands so a lowering can emit
// conditionally without branching at every call site. And of nothing is true.
func FilterAnd(kids ...*Filter) *Filter { return combine(FilterOpAnd, kids) }

// FilterOr builds an Or node, dropping nil operands. Or of nothing is false.
func FilterOr(kids ...*Filter) *Filter { return combine(FilterOpOr, kids) }

func combine(op FilterOp, kids []*Filter) *Filter {
	live := make([]*Filter, 0, len(kids))
	for _, k := range kids {
		if k != nil {
			live = append(live, k)
		}
	}
	return &Filter{Op: op, Kids: live}
}

// FilterNot negates a filter. Not(nil) is false (nil evaluates true).
func FilterNot(k *Filter) *Filter { return &Filter{Op: FilterOpNot, Kids: []*Filter{k}} }

// FilterExpr builds an Expr node over the source's facts.
func FilterExpr(cond string) *Filter { return &Filter{Op: FilterOpExpr, Expr: cond} }

// FilterMatch builds one structured match key node.
func FilterMatch(key string, val any) *Filter {
	return &Filter{Op: FilterOpMatch, Key: key, Val: val}
}

// Matcher evaluates one source match key against the event's facts. It is the
// only source-aware part of evaluation.
type Matcher func(key string, val any, facts map[string]any) (bool, error)

// Eval reports whether the filter holds for facts. An absent (nil) filter is
// true — a trigger with no filter fires.
func (f *Filter) Eval(facts map[string]any, match Matcher) (bool, error) {
	return f.eval(facts, match, 0)
}

func (f *Filter) eval(facts map[string]any, match Matcher, depth int) (bool, error) {
	if f == nil {
		return true, nil
	}
	if depth > MaxFilterDepth {
		return false, fmt.Errorf("filter nests more than %d deep", MaxFilterDepth)
	}
	switch f.Op {
	case FilterOpAnd:
		for _, k := range f.Kids {
			ok, err := k.eval(facts, match, depth+1)
			if err != nil || !ok {
				return false, err
			}
		}
		return true, nil
	case FilterOpOr:
		for _, k := range f.Kids {
			ok, err := k.eval(facts, match, depth+1)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case FilterOpNot:
		if len(f.Kids) != 1 {
			return false, fmt.Errorf("filter: not takes exactly one operand, got %d", len(f.Kids))
		}
		ok, err := f.Kids[0].eval(facts, match, depth+1)
		return !ok, err
	case FilterOpExpr:
		return expr.Eval(f.Expr, facts)
	case FilterOpMatch:
		if match == nil {
			return false, fmt.Errorf("filter: no matcher for match key %q", f.Key)
		}
		return match(f.Key, f.Val, facts)
	}
	return false, fmt.Errorf("filter: unknown operator %q", f.Op)
}

// Walk calls fn on this node and every descendant, parents first. A nil filter
// walks nothing.
func (f *Filter) Walk(fn func(*Filter)) {
	if f == nil {
		return
	}
	fn(f)
	for _, k := range f.Kids {
		k.Walk(fn)
	}
}

// MatchUnion returns every value the given match key is compared against
// anywhere in the filter — through Ands, Ors and any nesting — in first-seen
// order, deduplicated.
//
// Negated branches are SKIPPED: the union answers "what could this filter ever
// fire for", so a `not_repo:` exclusion must not be read as a scope. The result
// is therefore a SUPERSET of what the filter actually admits (an OR arm's
// `repo` counts even though its siblings may never hold), which is the safe
// direction for the two things it feeds — a routing pre-gate and the sweep's
// repo scope — because the full filter is still evaluated precisely afterwards.
func (f *Filter) MatchUnion(key string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(n *Filter)
	walk = func(n *Filter) {
		if n == nil || n.Op == FilterOpNot {
			return
		}
		if n.Op == FilterOpMatch {
			if n.Key == key {
				for _, s := range FilterValueStrings(n.Val) {
					if !seen[s] {
						seen[s] = true
						out = append(out, s)
					}
				}
			}
			return
		}
		for _, k := range n.Kids {
			walk(k)
		}
	}
	walk(f)
	return out
}

// TopLevelNegated returns the values of every top-level `not_<key>` — a
// Not(Match(key,…)) that is a direct conjunct of the filter's root.
//
// Only the top level, deliberately: a conjunct of the root holds for every
// event the filter admits, so hoisting it into a structural exclusion is
// sound. The same key under an Or arm is conditional, and hoisting it would
// suppress events the filter says should fire — so it stays inside the filter
// and is evaluated there.
func (f *Filter) TopLevelNegated(key string) []string {
	if f == nil {
		return nil
	}
	kids := f.Kids
	if f.Op != FilterOpAnd {
		kids = []*Filter{f}
	}
	var out []string
	seen := map[string]bool{}
	for _, k := range kids {
		if k == nil || k.Op != FilterOpNot || len(k.Kids) != 1 {
			continue
		}
		if m := k.Kids[0]; m != nil && m.Op == FilterOpMatch && m.Key == key {
			for _, s := range FilterValueStrings(m.Val) {
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// OnlyKeys reports whether the filter constrains nothing beyond the named
// match keys: no expr condition anywhere, and every Match key among them.
func (f *Filter) OnlyKeys(keys ...string) bool {
	allow := make(map[string]bool, len(keys))
	for _, k := range keys {
		allow[k] = true
	}
	only := true
	f.Walk(func(n *Filter) {
		switch n.Op {
		case FilterOpExpr:
			only = false
		case FilterOpMatch:
			if !allow[n.Key] {
				only = false
			}
		}
	})
	return only
}

// FlatConjunctionOf reports whether the filter is nothing but a top-level AND
// of the named match keys and their `not_` twins — no Or, no nesting, no expr.
//
// This is what separates pure ROUTING from a PREDICATE. A filter of this shape
// says only WHERE the trigger applies, and says it in a form a structural
// pre-gate can carry in full; the event's intrinsic default keep-condition
// therefore still stands. Anything else — an Or over repo sets, a negation
// under an Or, a predicate key, an expr — is a claim the pre-gate can only
// approximate, so the filter stays and is evaluated precisely instead.
func (f *Filter) FlatConjunctionOf(keys ...string) bool {
	if f == nil {
		return true
	}
	allow := make(map[string]bool, len(keys))
	for _, k := range keys {
		allow[k] = true
	}
	conjunct := func(n *Filter) bool {
		if n == nil {
			return false
		}
		if n.Op == FilterOpNot {
			if len(n.Kids) != 1 {
				return false
			}
			n = n.Kids[0]
		}
		return n != nil && n.Op == FilterOpMatch && allow[n.Key]
	}
	if f.Op != FilterOpAnd {
		return conjunct(f)
	}
	for _, k := range f.Kids {
		if !conjunct(k) {
			return false
		}
	}
	return true
}

// FilterValueStrings coerces a match key's configured value to a string list,
// accepting the single-scalar shorthand (`repo: owner/name`) YAML authors
// expect. A value that is neither yields nothing rather than an error: the
// callers are structural queries over an already-validated filter, and the
// source's own matcher is where a bad value is reported.
func FilterValueStrings(val any) []string {
	switch x := val.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// String renders the filter in a compact prefix form, for error messages and
// test failures: and(not(or(match(branch,…))),not(match(draft,true))).
func (f *Filter) String() string {
	if f == nil {
		return "<nil>"
	}
	switch f.Op {
	case FilterOpExpr:
		return fmt.Sprintf("expr(%q)", f.Expr)
	case FilterOpMatch:
		return fmt.Sprintf("match(%s,%v)", f.Key, f.Val)
	}
	parts := make([]string, 0, len(f.Kids))
	for _, k := range f.Kids {
		parts = append(parts, k.String())
	}
	return fmt.Sprintf("%s(%s)", f.Op, strings.Join(parts, ","))
}

// filterWire is the structural JSON form: exactly one of kids/expr/key+val is
// meaningful, selected by op. It is what a filter looks like on the plugin
// wire — never what an operator writes.
type filterWire struct {
	Op   FilterOp  `json:"op"`
	Kids []*Filter `json:"kids,omitempty"`
	Expr string    `json:"expr,omitempty"`
	Key  string    `json:"key,omitempty"`
	Val  any       `json:"val,omitempty"`
}

// MarshalJSON emits the structural form: {"op":"and","kids":[…]},
// {"op":"match","key":"label_any","val":["x"]}, {"op":"expr","expr":"…"}.
func (f *Filter) MarshalJSON() ([]byte, error) {
	if f == nil {
		return []byte("null"), nil
	}
	switch f.Op {
	case FilterOpAnd, FilterOpOr, FilterOpNot, FilterOpExpr, FilterOpMatch:
	default:
		return nil, fmt.Errorf("filter: unknown operator %q", f.Op)
	}
	return json.Marshal(filterWire{Op: f.Op, Kids: f.Kids, Expr: f.Expr, Key: f.Key, Val: f.Val})
}

// UnmarshalJSON reads the structural form back, refusing an unknown operator
// or a tree deeper than MaxFilterDepth.
func (f *Filter) UnmarshalJSON(b []byte) error {
	var w filterWire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*f = Filter{Op: w.Op, Kids: w.Kids, Expr: w.Expr, Key: w.Key, Val: w.Val}
	return f.validate(0)
}

func (f *Filter) validate(depth int) error {
	if depth > MaxFilterDepth {
		return fmt.Errorf("filter nests more than %d deep", MaxFilterDepth)
	}
	switch f.Op {
	case FilterOpAnd, FilterOpOr, FilterOpExpr, FilterOpMatch:
	case FilterOpNot:
		if len(f.Kids) != 1 {
			return fmt.Errorf("filter: not takes exactly one operand, got %d", len(f.Kids))
		}
	default:
		return fmt.Errorf("filter: unknown operator %q", f.Op)
	}
	for _, k := range f.Kids {
		if k == nil {
			return fmt.Errorf("filter: %s has a null operand", f.Op)
		}
		if err := k.validate(depth + 1); err != nil {
			return err
		}
	}
	return nil
}
