package config

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// Config reuse via YAML anchors (docs/design/anchors-reuse.md).
//
// Reuse of step BEHAVIOR is plain YAML — `&anchor`, `*alias`, and the `<<:`
// merge key — exactly what docker-compose does, in the main config and in a
// pack manifest alike:
//
//	x-templates:
//	  reviewer: &reviewer
//	    model: heavy
//	    workspace: worktree
//
//	workflows:
//	  review:
//	    steps:
//	      - <<: *reviewer          # merge the base…
//	        id: review             # …and add this step's own fields
//
// Two things make that work under a STRICT decoder, and both live here.
//
// 1. The `x-` holder. Anchors must be defined somewhere, and a top-level
//    section conductor does not know is exactly what strict decode exists to
//    reject. So top-level keys beginning with `x-` are IGNORED rather than
//    rejected — docker-compose's extension-field convention. The prefix is
//    the whole rule: `x-anything` passes, `stpes:` is still a named error.
//
// 2. Alias resolution before the strict pass. `strictNodeDecode` re-encodes
//    a node before decoding it (KnownFields does not reach into a custom
//    UnmarshalYAML otherwise), and an alias whose anchor definition was
//    dropped cannot be re-encoded — the emitter writes `*reviewer` with
//    nothing to point at, and the decode fails with "unknown anchor". So
//    aliases are resolved INTO their content first; the `<<:` merge itself
//    is then handled natively by yaml.v3, which supports it under
//    KnownFields (verified against the pinned v3.0.1).
//
// Anchors are a FILE-LOCAL mechanism: YAML resolves them per document, so
// they do not cross `imports:`. Cross-file reuse is `extends:` on a map
// section (see extends.go). This is a property of YAML, not a conductor
// limitation, and the docs say so.

// extensionPrefix marks a top-level key strict decode ignores.
const extensionPrefix = "x-"

// prepareStrict rewrites a parsed document so a strict decode of it both
// tolerates `x-` holders and still resolves every anchor: aliases are
// expanded into their content, then top-level `x-` entries are dropped.
//
// Returns nil when there is nothing to decode (an empty document).
func prepareStrict(doc *yaml.Node) *yaml.Node {
	if doc == nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil
	}
	out := resolveAliases(doc)
	dropExtensionKeys(out.Content[0])
	return out
}

// resolveAliases returns a copy of the tree with every alias replaced by its
// target's content and every anchor label cleared, so the result re-encodes
// to a self-contained document.
//
// The copy is deep because an anchor's value can be referenced many times;
// sharing the node would let a later mutation (the pack ref rewriter, the
// migration) leak across every use of the anchor.
func resolveAliases(n *yaml.Node) *yaml.Node {
	return resolveAliasesDepth(n, 0)
}

// maxAliasDepth bounds alias expansion. A self-referential anchor is not
// expressible in well-formed YAML (an anchor cannot alias itself before it
// is defined), but a hand-crafted or generated document could nest deeply
// enough to matter, and a loader must not be a stack-overflow surface.
const maxAliasDepth = 100

func resolveAliasesDepth(n *yaml.Node, depth int) *yaml.Node {
	if n == nil || depth > maxAliasDepth {
		return n
	}
	if n.Kind == yaml.AliasNode {
		if n.Alias == nil {
			return n // unresolvable; the decoder reports it
		}
		return resolveAliasesDepth(n.Alias, depth+1)
	}
	c := *n
	c.Anchor = ""
	c.Content = nil
	if len(n.Content) > 0 {
		c.Content = make([]*yaml.Node, 0, len(n.Content))
		for _, child := range n.Content {
			c.Content = append(c.Content, resolveAliasesDepth(child, depth+1))
		}
	}
	return &c
}

// dropExtensionKeys removes the `x-`-prefixed entries of a mapping. Only the
// TOP level of a document is filtered: an `x-` key nested inside a section
// is that section's business, and strict decode judges it normally.
func dropExtensionKeys(root *yaml.Node) {
	if root == nil || root.Kind != yaml.MappingNode {
		return
	}
	out := make([]*yaml.Node, 0, len(root.Content))
	for i := 0; i+1 < len(root.Content); i += 2 {
		if IsExtensionKey(root.Content[i].Value) {
			continue
		}
		out = append(out, root.Content[i], root.Content[i+1])
	}
	root.Content = out
}

// IsExtensionKey reports whether a top-level key is an `x-` extension field —
// a holder for anchors, ignored by the loader. Exported so the migration
// (which runs its own strict decoders over raw YAML) applies the same rule.
func IsExtensionKey(key string) bool {
	return strings.HasPrefix(key, extensionPrefix)
}

// StripExtensionKeys removes top-level `x-` entries from an already-decoded
// generic map — the imports path merges files as `map[string]any`, where the
// aliases have already been resolved by the decoder and only the holder
// needs dropping.
func StripExtensionKeys(m map[string]any) {
	for k := range m {
		if IsExtensionKey(k) {
			delete(m, k)
		}
	}
}
