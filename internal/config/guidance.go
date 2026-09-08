package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// GuidanceSpec is an agent profile's `guidance:` — house tone/format text layered
// onto a dispatched agent's prompt. It is *additive*: the effective guidance is a
// stack (top-level `agent_guidance` as layer 0, then each `extends:` ancestor's
// parts root→leaf, then this profile's own parts), joined as separate blocks. This
// replaces the old replace-only semantics, so a profile can extend the base tone
// instead of overwriting it.
//
// YAML forms:
//
//	guidance: "one line"           # a single part
//	guidance: [ "a", "b" ]         # several parts, in order
//	guidance: { replace: "only" }  # reset: drop layer 0 and every inherited part,
//	guidance: { replace: [x, y] }  #   use only these (and `replace: ""` disables
//	                               #   guidance entirely).
//
// Parts are concatenated with the ancestors' parts during `extends:` resolution
// (see resolveExtends); Replace short-circuits that inheritance. The layer-0
// `agent_guidance` prepend and the built-in concise default live in the engine
// (see (*Engine).agentGuidance) — the one reader of this type.
type GuidanceSpec struct {
	// Parts are this level's guidance blocks, in order. Empty strings are
	// dropped at render time (so `guidance: ""` contributes nothing but does
	// not, by itself, suppress ancestors — use `replace: ""` for that).
	Parts []string
	// Replace resets the stack: when true, the effective guidance is exactly
	// Parts — layer-0 `agent_guidance` and every inherited part are dropped.
	Replace bool
}

// UnmarshalYAML accepts a scalar, a sequence, or a single-key `{ replace: … }`
// mapping (whose value is itself a scalar or sequence).
func (g *GuidanceSpec) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		g.Parts = []string{s}
		return nil
	case yaml.SequenceNode:
		return n.Decode(&g.Parts)
	case yaml.MappingNode:
		if len(n.Content) != 2 || n.Content[0].Value != "replace" {
			return fmt.Errorf("guidance: a mapping form must be exactly `{ replace: <string|list> }`")
		}
		g.Replace = true
		return decodeStringOrList(n.Content[1], &g.Parts)
	default:
		return fmt.Errorf("guidance: expected a string, a list of strings, or `{ replace: … }`")
	}
}

// decodeStringOrList fills dst from a scalar (one element) or a sequence node.
func decodeStringOrList(n *yaml.Node, dst *[]string) error {
	if n.Kind == yaml.ScalarNode {
		var s string
		if err := n.Decode(&s); err != nil {
			return err
		}
		*dst = []string{s}
		return nil
	}
	return n.Decode(dst)
}

// prepend returns a copy of g with parent's parts placed before g's own. It is
// the guidance merge rule for `extends:`: a child that does not reset inherits
// the parent's guidance underneath its own. A child with Replace short-circuits
// (keeps only its own parts); a nil child inherits the parent wholesale — both
// handled by the caller (resolveExtends), not here.
func (g GuidanceSpec) prepend(parent []string) GuidanceSpec {
	merged := make([]string, 0, len(parent)+len(g.Parts))
	merged = append(merged, parent...)
	merged = append(merged, g.Parts...)
	return GuidanceSpec{Parts: merged, Replace: g.Replace}
}
