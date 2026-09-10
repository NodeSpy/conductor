package config

import (
	"fmt"
	"sort"
	"strings"
)

// Connector-owned scope and source dormancy
// (docs/design/runtimes-models-packs.md §5.2).
//
// A pack bundles triggers from possibly several sources — github, gitlab,
// pagerduty. `repos:` is meaningless to a pagerduty trigger, so scope CANNOT
// be a pack-level field: each trigger binds to the connector OF ITS OWN
// SOURCE TYPE, and the scope (repos, a pd service) lives on that connector,
// configured once by the consumer.
//
// The consequence that matters operationally: a consumer who has no
// connector of some type the pack uses should still get the rest of the
// pack. Those triggers go DORMANT and are SURFACED as a load-time notice
// rather than failing the boot — degrade-safe, like everything else on this
// path. A pack author who knows their pack is meaningless without a source
// marks it `required:` and gets a hard error instead.

// bindPackSources resolves each shipped trigger's connector by source type,
// disabling and reporting the ones the consumer cannot serve.
//
// Resolution per trigger, from its `on: <source>.<event>`:
//
//  1. an explicit `packs.<name>.connectors: { <type>: <instance> }` binding
//     (the disambiguation the consumer writes when they have MORE THAN ONE
//     connector of a type);
//  2. a connector whose own `use:` implementation IS that type, when exactly
//     one exists — the zero-config case;
//  3. nothing → the trigger goes dormant (or errors, if the source is
//     declared required).
func (st *packInstantiation) bindPackSources(ns string, man *PackManifest, inst PackInstance, trs []TriggerSpec) error {
	if len(trs) == 0 {
		return nil
	}
	byType := st.cfg.connectorsByType()
	required := man.Pack.Requires.Sources

	// Report each unservable source once, not once per trigger.
	reported := map[string]bool{}
	for i := range trs {
		src := trs[i].Connector()
		if src == "" || src == ManualSource || src == "conductor" {
			continue
		}
		// The pack writes its own source name (`github.pull_request`); the
		// consumer's connector may be called anything (`gh`). Rebinding
		// already mapped a DECLARED requires.connectors name; what is left
		// here is a source type the pack uses but did not declare.
		if _, isInstance := st.cfg.ConnectorsMap[src]; isInstance {
			continue // already names one of the consumer's connectors
		}
		bound, err := st.resolveSourceConnector(ns, src, inst, byType)
		if err != nil {
			return err
		}
		if bound != "" {
			trs[i].On = bound + "." + trs[i].Event()
			continue
		}
		// Dormant: disable it and say so. A required source is a hard error.
		if req, ok := required[src]; ok && req.Required {
			return fmt.Errorf("pack %q: trigger %q needs a %s connector, which this config has none of — the pack declares that source REQUIRED (requires.sources.%s.required). Add a connectors: entry with `use: %s`",
				ns, triggerRef(trs[i], i), src, src, src)
		}
		off := false
		trs[i].Enabled = &off
		if !reported[src] {
			reported[src] = true
			st.warnf("pack %q: no %s connector is configured — its %s trigger(s) are DORMANT. Add a connectors: entry with `use: %s` to arm them; the rest of the pack runs.",
				ns, src, src, src)
		}
	}
	return nil
}

// resolveSourceConnector picks the consumer connector serving one source
// type. An ambiguous type (more than one instance, no explicit binding) is a
// config error naming the fix — guessing would silently point a pack's
// triggers at the wrong account.
func (st *packInstantiation) resolveSourceConnector(ns, srcType string, inst PackInstance, byType map[string][]string) (string, error) {
	if pick, ok := inst.Connectors[srcType]; ok && pick != "" {
		if _, exists := st.cfg.ConnectorsMap[pick]; !exists {
			return "", fmt.Errorf("pack %q: connectors: { %s: %s } names no connector in this config (defined: %s)",
				ns, srcType, pick, st.cfg.connectorNames())
		}
		return pick, nil
	}
	switch names := byType[srcType]; len(names) {
	case 0:
		return "", nil
	case 1:
		return names[0], nil
	default:
		return "", fmt.Errorf("pack %q: this config has %d %s connectors (%s), so the pack's %s triggers are ambiguous — disambiguate with packs.%s.connectors: { %s: <one of them> }",
			ns, len(names), srcType, strings.Join(names, ", "), srcType, lastPathSegment(ns), srcType)
	}
}

// connectorsByType groups the consumer's connector instances by the
// implementation their `use:` resolves to, so a pack's `github.…` trigger
// can find the instance named `gh`.
func (c *Config) connectorsByType() map[string][]string {
	out := map[string][]string{}
	for name, ref := range c.ConnectorsMap {
		if !ref.IsEnabled() {
			continue
		}
		if typ := ref.TypeName(); typ != "" {
			out[typ] = append(out[typ], name)
		}
	}
	for _, names := range out {
		sort.Strings(names)
	}
	return out
}

// lastPathSegment is a nested instance's own alias ("kit/base" -> "base"),
// which is what the consumer writes in the block.
func lastPathSegment(ns string) string {
	if i := strings.LastIndex(ns, "/"); i >= 0 {
		return ns[i+1:]
	}
	return ns
}

// validateSourceDeclarations rejects a `connectors:` disambiguation that
// names a source type the pack never uses — almost always a typo that would
// otherwise sit inert.
func (st *packInstantiation) validateSourceDeclarations(ns string, man *PackManifest, inst PackInstance, trs []TriggerSpec) error {
	if len(inst.Connectors) == 0 {
		return nil
	}
	used := map[string]bool{}
	for _, t := range trs {
		if src := t.Connector(); src != "" {
			used[src] = true
		}
	}
	for _, n := range man.Pack.Requires.ConnectorNames() {
		used[n] = true
	}
	for typ := range man.Pack.Requires.Sources {
		used[typ] = true
	}
	for typ := range inst.Connectors {
		if !used[typ] {
			return fmt.Errorf("pack %q: connectors: { %s: … } names a source this pack does not use (it uses: %s)",
				ns, typ, sortedJoin(mapKeys(used)))
		}
	}
	return nil
}
