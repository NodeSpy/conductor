package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/pkg/expr"
	"github.com/NodeSpy/conductor/pkg/sourcekit"
)

// Filter is one composable trigger filter — the IR the polymorphic `filter:`
// key decodes into, and the shape each event's INTRINSIC DEFAULT keep-condition
// lowers into so both run through one evaluator
// (docs/design/unified-filter.md, docs/design/unified-filter-phase2.md).
//
// The YAML SHAPE is the boolean structure:
//
//	filter: "!is_draft && !contains(title, 'Release')"   # string → Expr
//	filter: { not_draft: true, author: [dependabot] }    # object → And of Match
//	filter: [ {label_any: [urgent]}, "!is_draft" ]       # array  → Or
//
// Nest freely. An object may carry the reserved key `expr:` (a condition
// string, AND-ed with its siblings), and ANY key may be written with the
// `not_` prefix to negate it — `not_expr:` negates a condition, `not_<key>:`
// negates that match key. Negation is therefore universal and lives in the
// grammar, not in each connector's key set: a connector declares `draft` and
// gets `not_draft` for free.
//
// The IR is connector-agnostic: only Match and the facts map it is evaluated
// against know what a `label_any` or a `head_branch` is.
type Filter struct {
	Op FilterOp
	// Kids are the operands of And/Or (any number) and Not (exactly one).
	Kids []*Filter
	// Expr is the condition source of an Expr node (pkg/expr syntax).
	Expr string
	// Key/Val are one structured match key and its configured value.
	Key string
	Val any

	// raw is the YAML value this node decoded from, kept so a user-authored
	// filter survives the marshal/reparse round trip the connectors lowering
	// does (internal/connector.buildIntegration) byte-for-byte. A node built
	// programmatically has none and marshals through the structural form
	// below instead.
	raw any
}

// FilterOp names a Filter node's kind. It IS sourcekit's: the node kinds are
// the wire vocabulary a source plugin evaluates, so there is one list of them.
type FilterOp = sourcekit.FilterOp

const (
	FilterOpAnd   = sourcekit.FilterOpAnd
	FilterOpOr    = sourcekit.FilterOpOr
	FilterOpNot   = sourcekit.FilterOpNot
	FilterOpExpr  = sourcekit.FilterOpExpr
	FilterOpMatch = sourcekit.FilterOpMatch
)

// FilterExprKey is the one reserved object key: an `expr` string AND-ed with
// its sibling match keys. Every other key is a connector match key.
const FilterExprKey = "expr"

// FilterNotPrefix negates any object key. `not_expr:` negates a condition
// string; `not_<matchkey>:` negates that match key. The negation is a Not node
// wrapping exactly what the un-prefixed key would have produced, so a
// connector's matcher only ever sees BASE keys — there is no `not_` case to
// write, and no key can gain a baked-in polarity that contradicts the prefix.
//
// A key and its `not_` twin are DISTINCT keys: writing both in one object is
// legal and ANDs (`{draft: false, not_repo: [x/y]}`).
const FilterNotPrefix = "not_"

// The structural keys a programmatically built node marshals through. A filter
// composed in code — pack arming ANDs an arm's repo scope with the shipped
// trigger's own `filter:` — has no surface spelling, because an object ANDs
// KEYS and a list ORs, so "AND of two arbitrary filters" cannot be written in
// the user grammar. These keys give it a lossless YAML form for the internal
// round trips (cloneTriggerSpec, connector.buildIntegration). They are
// x_-prefixed like Action's other lowering-only fields, appear in no
// user-facing config, and are documented nowhere an author reads.
const (
	filterOpKey   = "x_filter_op"
	filterKidsKey = "x_filter_kids"
	filterKeyKey  = "x_filter_key"
	filterValKey  = "x_filter_val"
	filterExprKey = "x_filter_expr"
)

// maxFilterDepth bounds how deeply a `filter:` may nest. Real filters are two
// or three levels; a deeper one is pathological input, and refusing it at
// decode keeps evaluation from recursing far enough to overflow the stack.
const maxFilterDepth = sourcekit.MaxFilterDepth

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
type FilterMatcher = sourcekit.Matcher

// Eval reports whether the filter holds for facts. An absent (nil) filter is
// true — a trigger with no filter fires.
//
// Evaluation is sourcekit's: this node type is the YAML face (it keeps the
// source value and the decode errors), and Kit is the IR every evaluator runs
// — the daemon's flow runner, the bundled github source, and a source plugin
// handed the same filter over the wire.
func (f *Filter) Eval(facts map[string]any, match FilterMatcher) (bool, error) {
	return f.Kit().Eval(facts, match)
}

// Kit converts the filter to the public IR (pkg/sourcekit), structurally:
// node for node, with the configured values shared rather than copied. nil
// stays nil, so "no filter" means the same thing on both sides.
func (f *Filter) Kit() *sourcekit.Filter {
	if f == nil {
		return nil
	}
	out := &sourcekit.Filter{Op: f.Op, Expr: f.Expr, Key: f.Key, Val: f.Val}
	if len(f.Kids) > 0 {
		out.Kids = make([]*sourcekit.Filter, len(f.Kids))
		for i, k := range f.Kids {
			out.Kids[i] = k.Kit()
		}
	}
	return out
}

// FilterFromKit is Kit's inverse, for a filter that arrives as IR (a lowering
// built in a kit). The result has no surface spelling and marshals through the
// structural x_filter_* form, like any node composed in code.
func FilterFromKit(k *sourcekit.Filter) *Filter {
	if k == nil {
		return nil
	}
	out := &Filter{Op: k.Op, Expr: k.Expr, Key: k.Key, Val: k.Val}
	if len(k.Kids) > 0 {
		out.Kids = make([]*Filter, len(k.Kids))
		for i, c := range k.Kids {
			out.Kids[i] = FilterFromKit(c)
		}
	}
	return out
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
// its keys (the reserved `expr:` key contributing an Expr, and any `not_`
// prefix wrapping its key's node in a Not). Keys are visited in sorted order —
// AND is commutative, so ordering only has to be STABLE, and a stable order
// keeps error messages and any re-serialisation deterministic.
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
		if seen[filterOpKey] {
			return f.decodeStructural(n, depth)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })

		kids := make([]*Filter, 0, len(entries))
		for _, e := range entries {
			// Universal negation: `not_<X>` is exactly what `<X>` would
			// produce, wrapped in a Not. A bare "not_" is not a prefix — it
			// stays a match key (and fails validation by name).
			key, negate := e.key, false
			if base, ok := strings.CutPrefix(e.key, FilterNotPrefix); ok && base != "" {
				key, negate = base, true
			}
			var kid *Filter
			if key == FilterExprKey {
				var cond string
				if err := e.node.Decode(&cond); err != nil {
					return fmt.Errorf("filter: %s: must be a condition string: %w", e.key, err)
				}
				kid = FilterExpr(cond)
			} else {
				var val any
				if err := e.node.Decode(&val); err != nil {
					return fmt.Errorf("filter: %s: %w", e.key, err)
				}
				kid = FilterMatch(key, val)
			}
			if negate {
				kid = FilterNot(kid)
			}
			kids = append(kids, kid)
		}
		*f = Filter{Op: FilterOpAnd, Kids: kids, raw: raw}
		return nil
	}
	return fmt.Errorf("filter: must be a condition string, a list (OR), or a map of match keys (AND)")
}

// decodeStructural reads the x_filter_* internal form MarshalYAML emits for a
// node with no surface spelling (see filterOpKey). Only conductor's own round
// trips produce it; it carries no raw, so re-marshalling re-synthesises it.
func (f *Filter) decodeStructural(n *yaml.Node, depth int) error {
	var body struct {
		Op   string      `yaml:"x_filter_op"`
		Kids []yaml.Node `yaml:"x_filter_kids"`
		Key  string      `yaml:"x_filter_key"`
		Val  any         `yaml:"x_filter_val"`
		Expr string      `yaml:"x_filter_expr"`
	}
	if err := n.Decode(&body); err != nil {
		return fmt.Errorf("filter: internal form: %w", err)
	}
	switch op := FilterOp(body.Op); op {
	case FilterOpExpr:
		*f = Filter{Op: op, Expr: body.Expr}
	case FilterOpMatch:
		*f = Filter{Op: op, Key: body.Key, Val: body.Val}
	case FilterOpAnd, FilterOpOr, FilterOpNot:
		kids := make([]*Filter, 0, len(body.Kids))
		for i := range body.Kids {
			var kid Filter
			if err := kid.decode(&body.Kids[i], depth+1); err != nil {
				return fmt.Errorf("filter: internal form: kid %d: %w", i, unwrapFilterPrefix(err))
			}
			kids = append(kids, &kid)
		}
		*f = Filter{Op: op, Kids: kids}
	default:
		return fmt.Errorf("filter: internal form: unknown operator %q", body.Op)
	}
	return nil
}

// unwrapFilterPrefix strips the inner "filter: " prefix so nested errors read
// `filter[0]: label_any: …` rather than `filter[0]: filter: label_any: …`.
func unwrapFilterPrefix(err error) error {
	if s, ok := strings.CutPrefix(err.Error(), "filter: "); ok {
		return fmt.Errorf("%s", s)
	}
	return err
}

// MarshalYAML re-emits the filter as the user wrote it. Several internal round
// trips re-serialise a config (cloneTriggerSpec, and the connectors lowering in
// internal/connector.buildIntegration), and an operator's `filter:` must come
// back out of them unchanged — hence the kept raw.
//
// A node composed in code has no raw and falls back to the x_filter_* internal
// form, which decodeStructural reads back losslessly. That is the pack-arming
// case: an arm's repo scope AND-ed with the shipped trigger's own filter is a
// shape the user grammar cannot spell.
func (f *Filter) MarshalYAML() (any, error) {
	if f == nil {
		return nil, nil
	}
	if f.raw != nil {
		return f.raw, nil
	}
	body := map[string]any{filterOpKey: string(f.Op)}
	switch f.Op {
	case FilterOpExpr:
		body[filterExprKey] = f.Expr
	case FilterOpMatch:
		body[filterKeyKey], body[filterValKey] = f.Key, f.Val
	case FilterOpAnd, FilterOpOr, FilterOpNot:
		kids := make([]any, 0, len(f.Kids))
		for _, k := range f.Kids {
			v, err := k.MarshalYAML()
			if err != nil {
				return nil, err
			}
			kids = append(kids, v)
		}
		body[filterKidsKey] = kids
	default:
		return nil, fmt.Errorf("filter: unknown operator %q", f.Op)
	}
	return body, nil
}

// FilterFromValue builds a Filter from an in-memory value in the same shapes
// the YAML accepts (a condition string, a list to OR, a map of keys to AND) —
// for the producers that SYNTHESISE a filter rather than parse one, notably
// `conductor config migrate` rewriting a legacy `filters:` block. Going through
// the decoder rather than assembling nodes keeps one grammar (and one set of
// error messages), and leaves the result marshallable as what was passed in.
func FilterFromValue(v any) (*Filter, error) {
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("filter: %w", err)
	}
	if len(doc.Content) != 1 {
		return nil, fmt.Errorf("filter: cannot build a filter from %T", v)
	}
	var f Filter
	if err := f.decode(doc.Content[0], 0); err != nil {
		return nil, err
	}
	return &f, nil
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
func (f *Filter) MatchUnion(key string) []string { return f.Kit().MatchUnion(key) }

// TopLevelNegated returns the values of every top-level `not_<key>` — a
// Not(Match(key,…)) that is a direct conjunct of the filter's root.
//
// Only the top level, deliberately: a conjunct of the root holds for every
// event the filter admits, so hoisting it into a structural exclusion is
// sound. The same key under an Or arm is conditional, and hoisting it would
// suppress events the filter says should fire — so it stays inside the filter
// and is evaluated there.
func (f *Filter) TopLevelNegated(key string) []string { return f.Kit().TopLevelNegated(key) }

// OnlyKeys reports whether the filter constrains nothing beyond the named
// match keys: no expr condition anywhere, and every Match key among them.
func (f *Filter) OnlyKeys(keys ...string) bool { return f.Kit().OnlyKeys(keys...) }

// FlatConjunctionOf reports whether the filter is nothing but a top-level AND
// of the named match keys and their `not_` twins — no Or, no nesting, no expr.
//
// This is what separates pure ROUTING from a PREDICATE. A filter of this shape
// says only WHERE the trigger applies, and says it in a form a structural
// pre-gate can carry in full; the event's intrinsic default keep-condition
// therefore still stands, exactly as it did under the routing keys this
// replaced. Anything else — an Or over repo sets, a negation under an Or, a
// predicate key, an expr — is a claim the pre-gate can only approximate, so
// the filter stays and is evaluated precisely instead.
func (f *Filter) FlatConjunctionOf(keys ...string) bool { return f.Kit().FlatConjunctionOf(keys...) }

// FilterValueStrings coerces a match key's configured value to a string list,
// accepting the single-scalar shorthand (`repo: owner/name`) YAML authors
// expect. A value that is neither yields nothing rather than an error: the
// callers are structural queries over an already-validated filter, and the
// connector's own matcher is where a bad value is reported.
func FilterValueStrings(val any) []string { return sourcekit.FilterValueStrings(val) }

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
