package migrate

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// The `use:` migration pass (docs/design/use-unification.md §F).
//
// It folds the old four-field shape into one:
//
//	plugins:   { jira: { source: github.com/a/b//jira, kind: connector, … } }
//	connectors:{ tickets: { type: jira, … } }
//	runtimes:  { rt: { type: paseo } }
//	           { g:  { agent: gemini } }
//
// becomes
//
//	connectors:{ tickets: { use: a/b/jira, … } }
//	runtimes:  { rt: { use: paseo } }
//	           { g:  { use: acp, agent: gemini } }
//
// It is a RAW-NODE pass, like applyNotifyPass and applyVaultsPass: it never
// decodes into config.Config, so it keeps working after `type:`/`source:`/
// `kind:` left the schema — which is exactly when it is needed.
//
// The lenient-migration posture holds throughout: a field the new shape has no
// home for is dropped WITH A NOTE, never a hard refusal. A deployed box
// auto-updates into this binary, and a migration that refuses is a crash-loop.

// applyUsePass rewrites connectors:/runtimes:/plugins: onto `use:`.
func applyUsePass(masked []byte, notes *[]string) (out []byte, changed bool, err error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(masked, &doc); err != nil {
		return nil, false, err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, false, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, false, nil
	}

	plugins := collectPluginDefs(mapValue(root, "plugins"), notes)
	touched := false

	if conns := mapValue(root, "connectors"); conns != nil && conns.Kind == yaml.MappingNode {
		if rewriteInstances(conns, plugins, "connector", notes) {
			touched = true
		}
	}
	rts := mapValue(root, "runtimes")
	if rts != nil && rts.Kind == yaml.MappingNode {
		if rewriteInstances(rts, plugins, "runtime", notes) {
			touched = true
		}
	}

	// A runtime-kind plugin with no runtimes: entry pointing at it would be
	// lost when plugins: goes away. Materialize one so nothing disappears
	// silently.
	if n, added := materializeRuntimePlugins(root, rts, plugins, notes); added {
		touched, rts = true, n
	}

	// Finally drop the plugins: block itself — every entry has now either been
	// folded into a reference or reported.
	if removeMapKey(root, "plugins") {
		touched = true
	}
	if !touched {
		return nil, false, nil
	}
	b, err := marshalDoc(&doc)
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// pluginDef is one old `plugins:` entry, reduced to what the new shape keeps.
type pluginDef struct {
	name      string // the plugins: map key
	provides  string // the connector type / runtime name it registered
	kind      string // connector | runtime
	use       string // the `use:` reference its source: became
	isolation *yaml.Node
	used      bool // a connectors:/runtimes: entry referenced it
}

// collectPluginDefs reads the old plugins: block into definitions keyed by what
// each entry PROVIDES — which is what a connector's `type:` named.
func collectPluginDefs(block *yaml.Node, notes *[]string) map[string]*pluginDef {
	defs := map[string]*pluginDef{}
	if block == nil || block.Kind != yaml.MappingNode {
		return defs
	}
	for i := 0; i+1 < len(block.Content); i += 2 {
		name, body := block.Content[i].Value, block.Content[i+1]
		if body.Kind != yaml.MappingNode {
			continue
		}
		d := &pluginDef{
			name:      name,
			provides:  scalarAt(body, "provides"),
			kind:      scalarAt(body, "kind"),
			isolation: mapValue(body, "isolation"),
		}
		if d.provides == "" {
			d.provides = name
		}
		if d.kind == "" {
			d.kind = "connector"
		}
		d.use = sourceToUse(scalarAt(body, "source"), scalarAt(body, "version"))
		if d.use == "" {
			*notes = append(*notes, fmt.Sprintf(
				"plugins.%s: no source: — cannot build a use: reference for it; declare it by hand under %ss:", name, d.kind))
			continue
		}
		// Fields the new surface has no home for, reported rather than dropped
		// in silence.
		for _, k := range []string{"sha256", "allow_unverified", "allow_unsandboxed", "hold", "args"} {
			if nodeAt(body, k) == nil {
				continue
			}
			*notes = append(*notes, dropNote(name, k))
		}
		defs[d.provides] = d
	}
	return defs
}

// dropNote explains why one retired plugins: field has no equivalent, so an
// operator reading the summary knows whether they need to do anything.
func dropNote(name, key string) string {
	switch key {
	case "sha256":
		return fmt.Sprintf("plugins.%s.sha256 dropped — the verified sha now lives in LOCAL install state, recorded when `conductor init` fetches the binary (nothing to pin by hand)", name)
	case "allow_unverified":
		return fmt.Sprintf("plugins.%s.allow_unverified dropped — a local `use: ./path` binary is verified on safe permissions rather than a pin; a fetched one always carries its release sha", name)
	case "allow_unsandboxed":
		return fmt.Sprintf("plugins.%s.allow_unsandboxed dropped — running without OS isolation is now the DEFAULT (the permission manifest is the confinement); add isolation: to opt back into hardening", name)
	case "hold":
		return fmt.Sprintf("plugins.%s.hold dropped — freeze it by pinning an exact version instead (use: <ref>@v1.2.3)", name)
	case "args":
		return fmt.Sprintf("plugins.%s.args dropped — a plugin is configured over the RPC transport per instance, not by shared process arguments", name)
	}
	return fmt.Sprintf("plugins.%s.%s dropped (no equivalent in the use: surface)", name, key)
}

// sourceToUse converts an old `source:` (plus `version:`) into a `use:`
// reference: github.com/acme/repo//jira @ "~> 1.0" -> acme/repo/jira@~> 1.0.
// A local path passes through unchanged — `use:` still takes one.
func sourceToUse(source, version string) string {
	s := strings.TrimSpace(source)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "/") || strings.HasPrefix(s, "~/") {
		return s // a local binary has no version to carry
	}
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "github.com/")
	s = strings.ReplaceAll(s, "//", "/")
	s = strings.Trim(s, "/")
	if v := strings.TrimSpace(version); v != "" {
		s += "@" + v
	}
	return s
}

// rewriteInstances turns each entry's `type:` (or, for a runtime, `agent:`)
// into `use:`, resolving a plugin-provided type back to the plugin's reference.
func rewriteInstances(block *yaml.Node, plugins map[string]*pluginDef, kind string, notes *[]string) bool {
	changed := false
	for i := 0; i+1 < len(block.Content); i += 2 {
		name, body := block.Content[i].Value, block.Content[i+1]
		if body.Kind != yaml.MappingNode {
			continue
		}
		if scalarAt(body, "use") != "" {
			continue // already migrated
		}
		typ := scalarAt(body, "type")
		agent := scalarAt(body, "agent")

		var use string
		switch {
		case typ != "":
			use = typ
			if d, ok := plugins[typ]; ok {
				use, d.used = d.use, true
				*notes = append(*notes, fmt.Sprintf(
					"%ss.%s: type: %s + plugins.%s -> use: %s", kind, name, typ, d.name, use))
				if d.isolation != nil && nodeAt(body, "isolation") == nil {
					setMapKey(body, "isolation", d.isolation)
				}
			} else {
				*notes = append(*notes, fmt.Sprintf("%ss.%s: type: %s -> use: %s", kind, name, typ, use))
			}
		case kind == "runtime" && agent != "":
			// An `agent:` runtime was always the ACP transport; that is now
			// spelt `use: acp` with the agent alongside it.
			use = "acp"
			*notes = append(*notes, fmt.Sprintf("runtimes.%s: agent: %s -> use: acp (agent: kept)", name, agent))
		default:
			continue
		}

		removeMapKey(body, "type")
		setMapKeyFirst(body, "use", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: use})
		changed = true
	}
	return changed
}

// materializeRuntimePlugins gives every runtime-kind plugin that nothing
// referenced its own runtimes: entry, so removing plugins: loses nothing.
func materializeRuntimePlugins(root, rts *yaml.Node, plugins map[string]*pluginDef, notes *[]string) (*yaml.Node, bool) {
	var orphans []*pluginDef
	for _, d := range plugins {
		if d.kind == "runtime" && !d.used {
			orphans = append(orphans, d)
		}
	}
	if len(orphans) == 0 {
		return rts, false
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].provides < orphans[j].provides })

	if rts == nil || rts.Kind != yaml.MappingNode {
		rts = &yaml.Node{Kind: yaml.MappingNode}
		setMapKey(root, "runtimes", rts)
	}
	for _, d := range orphans {
		if nodeAt(rts, d.provides) != nil {
			continue
		}
		body := &yaml.Node{Kind: yaml.MappingNode}
		setMapKey(body, "use", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: d.use})
		if d.isolation != nil {
			setMapKey(body, "isolation", d.isolation)
		}
		setMapKey(rts, d.provides, body)
		*notes = append(*notes, fmt.Sprintf(
			"plugins.%s (kind: runtime) -> runtimes.%s: { use: %s }", d.name, d.provides, d.use))
	}
	return rts, true
}

// --- small yaml.Node helpers -------------------------------------------------

// nodeAt returns the value node under key, of ANY kind. (mapValue, shared with
// the vaults pass, deliberately returns mappings only — a `type:` scalar needs
// this one.)
func nodeAt(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// scalarAt returns the scalar value at key, or "".
func scalarAt(m *yaml.Node, key string) string {
	v := nodeAt(m, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return ""
	}
	return v.Value
}

// removeMapKey deletes a key/value pair, reporting whether it was there.
func removeMapKey(m *yaml.Node, key string) bool {
	if m == nil || m.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return true
		}
	}
	return false
}

// setMapKey sets (or replaces) a key, appending at the end.
func setMapKey(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}

// setMapKeyFirst sets a key at the FRONT of the mapping, so `use:` reads as the
// entry's header the way `type:` did.
func setMapKeyFirst(m *yaml.Node, key string, val *yaml.Node) {
	removeMapKey(m, key)
	m.Content = append([]*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val,
	}, m.Content...)
}
