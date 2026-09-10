package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Two ways to reuse a chunk of step config, side by side
// (docs/design/merge-and-extends.md).
//
//	<<: *base    plain YAML merge. Dumb: every key the child sets wins
//	             outright, scalars and LISTS alike. Resolved by the parser;
//	             conductor only ever sees the result.
//
//	extends: *base   conductor's own merge. It receives both layers as data,
//	                 so it can be field-aware: scalars override, lists
//	                 APPEND, maps deep-merge, and `guidance:` always stacks.
//
// The difference is not a style choice — it follows from WHEN each one
// happens. `<<:` is finished before conductor sees the document, so there
// is no "base" left to append to; that is why append and the escape-hatch
// tags exist only under `extends:`.
//
// Both take the same thing on the right: a YAML alias to an `x-*` anchor,
// or an inline map. There is no registry of named steps to point at (see
// stepref.go) and `extends:` here is NOT the sibling-key inheritance that
// runtimes/workflows/handoffs and triggers use (extends.go). Those merge
// DECODED Go values by reflection, which cannot see a node tag; this one
// has to run on the node tree, because the tags are the point.
//
// Escape hatches, read straight off the child's node tag:
//
//	guidance: !override "Only this."   replace instead of appending
//	network:  !reset                   drop what was inherited
//
// A tag that inherited nothing is stripped rather than failing: writing
// `!reset` on a field no base supplied means the same thing either way,
// and a load error there would be pedantry.

// maxExtendsDepth bounds an `extends:` chain. An ALIAS cannot be
// self-referential in YAML (the anchor must be defined before it is
// referenced), but a hand-written chain of inline maps can nest without
// limit, and a loader must not be a stack-overflow surface.
const maxExtendsDepth = 32

const (
	tagOverride = "!override"
	tagReset    = "!reset"
)

// UnmarshalYAML resolves a step's reuse before the field decode: `<<:`
// first (it is YAML-level and would already be finished if a custom
// unmarshaler were not intercepting the node), then `extends:` field-aware
// on top. A step using both is unusual, but the order is defined.
func (s *Step) UnmarshalYAML(n *yaml.Node) error {
	merged, err := resolveStepReuse(n, 0)
	if err != nil {
		return err
	}
	// A plain alias to a whole step (`- *base`) arrives as its target's
	// content; anything that is not a mapping is left to the field decode
	// to reject with its own message.
	type plain Step
	var p plain
	if err := strictNodeDecode(merged, &p); err != nil {
		return err
	}
	*s = Step(p)
	return nil
}

// resolveStepReuse returns a copy of a step node with `<<:` applied,
// `extends:` merged in, and every escape-hatch tag consumed.
func resolveStepReuse(n *yaml.Node, depth int) (*yaml.Node, error) {
	if n == nil || n.Kind != yaml.MappingNode {
		return n, nil
	}
	if depth > maxExtendsDepth {
		return nil, fmt.Errorf("extends: chain is more than %d deep — an inline `extends:` that eventually points back at itself never terminates; break the cycle", maxExtendsDepth)
	}
	child := applyMergeKeys(n)

	base, rest := splitExtends(child)
	if base == nil {
		stripReuseTags(rest)
		return rest, nil
	}
	if base.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("extends: takes an anchor alias or an inline map (e.g. `extends: *reviewer`), got a %s", nodeKindName(base))
	}
	// Base first, so a chain stacks in definition order: the outermost
	// child's guidance ends up last, under nothing.
	resolvedBase, err := resolveStepReuse(base, depth+1)
	if err != nil {
		return nil, err
	}
	out := fieldAwareMerge(resolvedBase, rest)
	stripReuseTags(out)
	return out, nil
}

// applyMergeKeys folds `<<:` into a plain mapping with DUMB override
// semantics: an explicit key always wins over a merged one, whole.
//
// yaml.v3 does this itself when it decodes a mapping, but a custom
// UnmarshalYAML is handed the node before that happens — so a step would
// silently lose its `<<:` if this did not exist.
func applyMergeKeys(n *yaml.Node) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return n
	}
	var merged []*yaml.Node // key/value pairs contributed by `<<:`
	own := make([]*yaml.Node, 0, len(n.Content))
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Value != "<<" {
			own = append(own, k, v)
			continue
		}
		// `<<: *one` or `<<: [*one, *two]`. In a sequence the EARLIER
		// entry wins, per the YAML merge-key spec.
		for _, m := range mergeSources(v) {
			if m.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(m.Content); j += 2 {
				if !hasKey(merged, m.Content[j].Value) {
					merged = append(merged, m.Content[j], m.Content[j+1])
				}
			}
		}
	}
	if len(merged) == 0 {
		return n
	}
	out := *n
	out.Content = own
	for i := 0; i+1 < len(merged); i += 2 {
		if !hasKey(own, merged[i].Value) {
			out.Content = append(out.Content, merged[i], merged[i+1])
		}
	}
	return &out
}

// mergeSources lists the mappings one `<<:` value contributes.
func mergeSources(v *yaml.Node) []*yaml.Node {
	if v == nil {
		return nil
	}
	if v.Kind == yaml.SequenceNode {
		return v.Content
	}
	return []*yaml.Node{v}
}

func hasKey(pairs []*yaml.Node, key string) bool {
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i].Value == key {
			return true
		}
	}
	return false
}

// splitExtends removes the `extends:` entry, returning its value and the
// rest of the mapping. The value is already a concrete map: an alias was
// expanded into its target's content before the strict pass (anchors.go).
func splitExtends(n *yaml.Node) (base, rest *yaml.Node) {
	out := *n
	out.Content = make([]*yaml.Node, 0, len(n.Content))
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "extends" {
			base = n.Content[i+1]
			continue
		}
		out.Content = append(out.Content, n.Content[i], n.Content[i+1])
	}
	return base, &out
}

// fieldAwareMerge layers a child mapping over a base one.
//
//	!reset on the child    the key is dropped — neither layer's value survives
//	!override on the child the child's value replaces, whatever the kind
//	guidance               ALWAYS appends: it is a stack of tone, and a base
//	                       that set the house voice should still be heard
//	                       under a step that adds to it. No opt-in.
//	sequences              append, base items first
//	mappings               deep-merge, recursively
//	anything else          the child wins
func fieldAwareMerge(base, child *yaml.Node) *yaml.Node {
	out := *child
	out.Content = nil

	seen := map[string]bool{}
	for i := 0; i+1 < len(child.Content); i += 2 {
		k, cv := child.Content[i], child.Content[i+1]
		seen[k.Value] = true
		if cv.Tag == tagReset {
			continue // both layers dropped
		}
		bv := valueAt(base, k.Value)
		if bv == nil || cv.Tag == tagOverride || isGuidanceReplace(k.Value, cv) {
			out.Content = append(out.Content, k, untag(cv))
			continue
		}
		out.Content = append(out.Content, k, mergeValues(k.Value, bv, cv))
	}
	// Whatever the child said nothing about comes through from the base.
	for i := 0; i+1 < len(base.Content); i += 2 {
		if !seen[base.Content[i].Value] {
			out.Content = append(out.Content, base.Content[i], base.Content[i+1])
		}
	}
	return &out
}

// mergeValues combines one field's two layers by kind.
func mergeValues(key string, base, child *yaml.Node) *yaml.Node {
	if key == "guidance" {
		return appendSequences(guidanceParts(base), guidanceParts(child))
	}
	switch {
	case isArgvField(key):
		// An argv is an ORDERED INVOCATION, not a set of permissions.
		// Appending `["make","test"]` to `["go","build"]` produces
		// `go build make test`, which is not a command anyone wrote. The
		// sibling section extends: replaces the same field, so this also
		// keeps the two spellings of the word agreeing.
		return child
	case base.Kind == yaml.SequenceNode && child.Kind == yaml.SequenceNode:
		return appendSequences(base, child)
	case base.Kind == yaml.MappingNode && child.Kind == yaml.MappingNode:
		return fieldAwareMerge(base, child)
	default:
		return child
	}
}

// isArgvField reports a list field whose items are a single ordered
// invocation rather than an accumulating allow-list. Appending is right for
// `network:` and `skill.verbs:` — more entries mean more reach, and a base
// that granted something should keep granting it. It is wrong for argv,
// where more entries mean a different command.
func isArgvField(key string) bool {
	return key == "command" || key == "args"
}

// appendSequences concatenates two sequences, base items first.
func appendSequences(base, child *yaml.Node) *yaml.Node {
	out := *child
	out.Kind, out.Tag, out.Style = yaml.SequenceNode, "!!seq", 0
	out.Content = make([]*yaml.Node, 0, len(base.Content)+len(child.Content))
	out.Content = append(out.Content, base.Content...)
	out.Content = append(out.Content, child.Content...)
	return &out
}

// guidanceParts normalises one layer's guidance to a sequence of parts, so
// the scalar, list, and `{ replace: … }` forms all stack the same way.
func guidanceParts(n *yaml.Node) *yaml.Node {
	switch {
	case n == nil:
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	case n.Kind == yaml.SequenceNode:
		return n
	case n.Kind == yaml.MappingNode:
		// `{ replace: … }` — the value is this layer's parts. Reaching
		// here means it is the BASE (a child that replaces short-circuits
		// in fieldAwareMerge), and a base that reset the stack below it
		// still contributes its own parts.
		if v := valueAt(n, "replace"); v != nil {
			return guidanceParts(v)
		}
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	default:
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{n}}
	}
}

// isGuidanceReplace reports the legacy `guidance: { replace: … }` form
// (#149), which predates the tags and means exactly `!override`. It keeps
// working; the tag is the spelling to document.
func isGuidanceReplace(key string, n *yaml.Node) bool {
	return key == "guidance" && n.Kind == yaml.MappingNode && valueAt(n, "replace") != nil
}

// untag returns a copy with an escape-hatch tag removed, so the value that
// reaches the typed decode looks like anything else. The tag is a merge
// instruction, not a type.
func untag(n *yaml.Node) *yaml.Node {
	if n == nil || (n.Tag != tagOverride && n.Tag != tagReset) {
		return n
	}
	c := *n
	c.Tag = ""
	if c.Kind == yaml.ScalarNode && c.Value == "" {
		c.Tag = "!!null"
	}
	return &c
}

// stripReuseTags clears the escape-hatch tags left on a node that inherited
// nothing — no `extends:`, or a key the base never set. `!reset` on such a
// key drops it (same outcome, said out loud); `!override` is a no-op.
func stripReuseTags(n *yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode {
		out := make([]*yaml.Node, 0, len(n.Content))
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i+1].Tag == tagReset {
				continue
			}
			out = append(out, n.Content[i], untag(n.Content[i+1]))
		}
		n.Content = out
	}
	for _, c := range n.Content {
		stripReuseTags(c)
	}
}

// valueAt returns a mapping's value for one key, or nil.
func valueAt(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func nodeKindName(n *yaml.Node) string {
	switch n.Kind {
	case yaml.SequenceNode:
		return "list"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "unresolved alias"
	default:
		return "value"
	}
}
