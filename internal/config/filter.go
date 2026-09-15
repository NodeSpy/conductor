package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/expr"
)

// Filter is one composable trigger filter — the IR the polymorphic `filter:`
// key decodes into, and the shape the legacy `filters: {exclude, gates, …}`
// block lowers into so both run through one evaluator
// (docs/design/unified-filter.md).
//
// The YAML SHAPE is the boolean structure:
//
//	filter: "!is_draft && !contains(title, 'Release')"   # string → Expr
//	filter: { not_draft: true, authors: [dependabot] }   # object → And of Match
//	filter: [ {labels_any: [urgent]}, "!is_draft" ]      # array  → Or
//
// Nest freely. An object may carry the one reserved key `expr:` (a string,
// AND-ed with its siblings) so a negation needs no separate `exclude` concept.
//
// The IR is connector-agnostic: only Match and the facts map it is evaluated
// against know what a `labels_any` or a `head_branch` is.
type Filter struct {
	Op FilterOp
	// Kids are the operands of And/Or (any number) and Not (exactly one).
	Kids []*Filter
	// Expr is the condition source of an Expr node (internal/expr syntax).
	Expr string
	// Key/Val are one structured match key and its configured value.
	Key string
	Val any

	// raw is the YAML value this node decoded from, kept so a user-authored
	// filter survives the marshal/reparse round trip the connectors lowering
	// does (internal/connector.buildIntegration). Nodes built programmatically
	// by the legacy lowering have none and are never marshalled.
	raw any
}

// FilterOp names a Filter node's kind.
type FilterOp string

const (
	FilterOpAnd   FilterOp = "and"
	FilterOpOr    FilterOp = "or"
	FilterOpNot   FilterOp = "not"
	FilterOpExpr  FilterOp = "expr"
	FilterOpMatch FilterOp = "match"
)

// FilterExprKey is the one reserved object key: an `expr` string AND-ed with
// its sibling match keys. Every other key is a connector match key.
const FilterExprKey = "expr"

// maxFilterDepth bounds how deeply a `filter:` may nest. Real filters are two
// or three levels; a deeper one is pathological input, and refusing it at
// decode keeps evaluation from recursing far enough to overflow the stack.
const maxFilterDepth = 32

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

// FilterExpr builds an Expr node over the connector's facts.
func FilterExpr(cond string) *Filter { return &Filter{Op: FilterOpExpr, Expr: cond} }

// FilterMatch builds one structured match key node.
func FilterMatch(key string, val any) *Filter {
	return &Filter{Op: FilterOpMatch, Key: key, Val: val}
}

// FilterMatcher evaluates one connector match key against the event's facts.
// It is the only connector-aware part of evaluation.
type FilterMatcher func(key string, val any, facts map[string]any) (bool, error)

// Eval reports whether the filter holds for facts. An absent (nil) filter is
// true — a trigger with no filter fires.
func (f *Filter) Eval(facts map[string]any, match FilterMatcher) (bool, error) {
	return f.eval(facts, match, 0)
}

func (f *Filter) eval(facts map[string]any, match FilterMatcher, depth int) (bool, error) {
	if f == nil {
		return true, nil
	}
	if depth > maxFilterDepth {
		return false, fmt.Errorf("filter nests more than %d deep", maxFilterDepth)
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

// MatchKeys returns every structured match key the filter uses, sorted and
// deduplicated — the set load-time validation checks against the connector's
// declared keys.
func (f *Filter) MatchKeys() []string {
	seen := map[string]bool{}
	f.Walk(func(n *Filter) {
		if n.Op == FilterOpMatch {
			seen[n.Key] = true
		}
	})
	return sortedSet(seen)
}

// FactRefs returns every fact path the filter's expr strings read, sorted and
// deduplicated (see expr.Refs for what counts as a read).
func (f *Filter) FactRefs() []string {
	seen := map[string]bool{}
	f.Walk(func(n *Filter) {
		if n.Op != FilterOpExpr {
			return
		}
		for _, r := range expr.Refs(n.Expr) {
			seen[r] = true
		}
	})
	return sortedSet(seen)
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// UnmarshalYAML decodes the polymorphic `filter:` shape into the IR: a string
// is an Expr, a sequence is an Or of its entries, and a mapping is an And of
// its keys (the reserved `expr:` key contributing an Expr). Keys are visited
// in sorted order — AND is commutative, so ordering only has to be STABLE, and
// a stable order keeps error messages and any re-serialisation deterministic.
func (f *Filter) UnmarshalYAML(n *yaml.Node) error {
	return f.decode(n, 0)
}

func (f *Filter) decode(n *yaml.Node, depth int) error {
	if depth > maxFilterDepth {
		return fmt.Errorf("filter: nests more than %d deep", maxFilterDepth)
	}
	// Keep the source value so MarshalYAML can re-emit exactly what was
	// written, whatever shape it was.
	var raw any
	if err := n.Decode(&raw); err != nil {
		return fmt.Errorf("filter: %w", err)
	}

	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			return fmt.Errorf("filter: is empty — give it a condition string, a list, or a map of match keys")
		}
		s, ok := raw.(string)
		if !ok {
			return fmt.Errorf("filter: a scalar filter is an expr condition string, got %T", raw)
		}
		*f = Filter{Op: FilterOpExpr, Expr: s, raw: raw}
		return nil

	case yaml.SequenceNode:
		if len(n.Content) == 0 {
			return fmt.Errorf("filter: an empty list matches nothing — remove it, or list the alternatives to OR")
		}
		kids := make([]*Filter, 0, len(n.Content))
		for i, item := range n.Content {
			var kid Filter
			if err := kid.decode(item, depth+1); err != nil {
				return fmt.Errorf("filter[%d]: %w", i, unwrapFilterPrefix(err))
			}
			kids = append(kids, &kid)
		}
		*f = Filter{Op: FilterOpOr, Kids: kids, raw: raw}
		return nil

	case yaml.MappingNode:
		if len(n.Content) == 0 {
			return fmt.Errorf("filter: an empty map constrains nothing — remove it")
		}
		// Collect key → value node, then visit in sorted key order.
		type entry struct {
			key  string
			node *yaml.Node
		}
		entries := make([]entry, 0, len(n.Content)/2)
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			var key string
			if err := n.Content[i].Decode(&key); err != nil {
				return fmt.Errorf("filter: map key: %w", err)
			}
			if seen[key] {
				return fmt.Errorf("filter: duplicate key %q", key)
			}
			seen[key] = true
			entries = append(entries, entry{key, n.Content[i+1]})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })

		kids := make([]*Filter, 0, len(entries))
		for _, e := range entries {
			if e.key == FilterExprKey {
				var cond string
				if err := e.node.Decode(&cond); err != nil {
					return fmt.Errorf("filter: %s: must be a condition string: %w", FilterExprKey, err)
				}
				kids = append(kids, FilterExpr(cond))
				continue
			}
			var val any
			if err := e.node.Decode(&val); err != nil {
				return fmt.Errorf("filter: %s: %w", e.key, err)
			}
			kids = append(kids, FilterMatch(e.key, val))
		}
		*f = Filter{Op: FilterOpAnd, Kids: kids, raw: raw}
		return nil
	}
	return fmt.Errorf("filter: must be a condition string, a list (OR), or a map of match keys (AND)")
}

// unwrapFilterPrefix strips the inner "filter: " prefix so nested errors read
// `filter[0]: labels_any: …` rather than `filter[0]: filter: labels_any: …`.
func unwrapFilterPrefix(err error) error {
	if s, ok := strings.CutPrefix(err.Error(), "filter: "); ok {
		return fmt.Errorf("%s", s)
	}
	return err
}

// MarshalYAML re-emits the filter as the user wrote it. Only user-authored
// filters are ever marshalled — the connectors lowering round-trips a config
// through YAML (internal/connector.buildIntegration) — so a programmatically
// built node (the legacy lowering's Not/And trees, which have no surface
// syntax) is a bug worth surfacing rather than silently dropping.
func (f *Filter) MarshalYAML() (any, error) {
	if f == nil {
		return nil, nil
	}
	if f.raw == nil {
		return nil, fmt.Errorf("filter: a programmatically built %s node has no YAML form", f.Op)
	}
	return f.raw, nil
}

// String renders the filter in a compact prefix form, for error messages and
// test failures: and(not(or(match(branches,…))), match(not_draft,true)).
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
