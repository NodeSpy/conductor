package config

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Version-aware `requires.connectors`
// (docs/design/skill-capability-and-pack-interface.md §D).
//
// `requires.conductor` and `requires.packs` already carry version
// constraints; connectors were a bare name list, so a pack could declare it
// needs jira but not that it needs jira ≥2.0 — and a consumer on 1.x would
// find out at the first live trigger instead of at load.
//
//	requires:
//	  connectors:
//	    github: "*"        # any version (identical to the bare-list form)
//	    jira:   ">=2.0"    # a plugin connector at a compatible release
//	  # connectors: [github]   # still valid — sugar for { github: "*" }
//
// It GATES, it does not fetch: connectors are bind-only, so conductor checks
// the version the consumer already resolved and reports a clear load error
// naming pack + connector + required-vs-actual — the same failure mode as a
// bad `requires.conductor`, and degrade-safe for the same reason.

// ConnectorReqs is the `requires.connectors` value: connector name → version
// constraint. It decodes from a MAP (name → constraint) or, as sugar, a LIST
// of bare names meaning "any version".
type ConnectorReqs map[string]string

// AnyVersion is the constraint that gates nothing.
const AnyVersion = "*"

// UnmarshalYAML accepts the list and map forms.
func (c *ConnectorReqs) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!null" {
			*c = nil
			return nil
		}
		var one string
		if err := n.Decode(&one); err != nil {
			return fmt.Errorf("requires.connectors: want a list of names or a map of name -> version constraint: %w", err)
		}
		*c = ConnectorReqs{one: AnyVersion}
		return nil
	case yaml.SequenceNode:
		var names []string
		if err := n.Decode(&names); err != nil {
			return fmt.Errorf("requires.connectors: list form takes connector names: %w", err)
		}
		out := make(ConnectorReqs, len(names))
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				return fmt.Errorf("requires.connectors: empty connector name")
			}
			out[name] = AnyVersion
		}
		*c = out
		return nil
	case yaml.MappingNode:
		var m map[string]string
		if err := n.Decode(&m); err != nil {
			return fmt.Errorf("requires.connectors: map form takes name -> version constraint (e.g. { jira: \">=2.0\" }): %w", err)
		}
		out := make(ConnectorReqs, len(m))
		for name, constraint := range m {
			name = strings.TrimSpace(name)
			if name == "" {
				return fmt.Errorf("requires.connectors: empty connector name")
			}
			if strings.TrimSpace(constraint) == "" {
				constraint = AnyVersion
			}
			out[name] = constraint
		}
		*c = out
		return nil
	}
	return fmt.Errorf("requires.connectors: want a list of names or a map of name -> version constraint")
}

// MarshalYAML renders the list form when every constraint is "any" (so a
// round-trip of the sugar stays sugar), else the map.
func (c ConnectorReqs) MarshalYAML() (any, error) {
	if len(c) == 0 {
		return nil, nil
	}
	names := c.Names()
	plain := true
	for _, n := range names {
		if c[n] != AnyVersion {
			plain = false
			break
		}
	}
	if plain {
		return names, nil
	}
	return map[string]string(c), nil
}

// Names lists the declared connectors, sorted.
func (c ConnectorReqs) Names() []string {
	out := make([]string, 0, len(c))
	for n := range c {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Resolved connector versions
// ---------------------------------------------------------------------------

// A PLUGIN connector's version is the release its binary came from, which
// lives in local install state — owned by internal/plugin, which imports
// this package. So the daemon injects the resolved map at boot rather than
// this package reaching upward. A BUILTIN connector ships in the binary, so
// its version IS the daemon version.

var (
	connVerMu       sync.RWMutex
	connectorVers   = map[string]string{}
	connVersionsSet bool
)

// SetConnectorVersions records the resolved release version of each
// PLUGIN-backed connector instance, keyed by the connectors: map key. Called
// once at boot from the plugin install state. A connector absent from the
// map is treated as builtin (daemon-versioned).
func SetConnectorVersions(v map[string]string) {
	connVerMu.Lock()
	defer connVerMu.Unlock()
	connectorVers = make(map[string]string, len(v))
	for k, ver := range v {
		connectorVers[k] = ver
	}
	connVersionsSet = true
}

// resolvedConnectorVersion is the version to gate a constraint against: the
// installed plugin release for a plugin connector, else the daemon version.
// The second result is false when the version is genuinely unknown, in which
// case the gate is skipped rather than guessed at.
func resolvedConnectorVersion(instance string, ref ConnectorRef) (string, bool) {
	u, err := ref.Resolved()
	if err == nil && u.IsBuiltin() {
		// A builtin ships in the daemon, so its version is the daemon's.
		return runtimeVersion, true
	}
	connVerMu.RLock()
	v, ok := connectorVers[instance]
	connVerMu.RUnlock()
	if ok && strings.TrimSpace(v) != "" {
		return v, true
	}
	// A plugin whose install state we have not been given (a dev build, an
	// as-yet-uninstalled plugin, a test): unknown, so do not gate.
	return "", false
}

// checkConnectorVersions gates every declared constraint against the
// consumer's resolved connector. It never fetches — connectors are
// bind-only, so this only ever reads what the consumer already resolved.
func (st *packInstantiation) checkConnectorVersions(ns string, reqs ConnectorReqs, env envBindings) error {
	for _, name := range reqs.Names() {
		constraint := strings.TrimSpace(reqs[name])
		if constraint == "" || constraint == AnyVersion {
			continue // declared, but any version will do
		}
		bound, ok := env.conn[name]
		if !ok {
			continue // the missing-binding error is reported by validateRequires
		}
		ref, ok := st.cfg.ConnectorsMap[bound]
		if !ok {
			continue // likewise
		}
		have, known := resolvedConnectorVersion(bound, ref)
		if !known {
			// Nothing resolved to gate against (a local dev plugin, an
			// unversioned build). Say so rather than failing a box that may
			// be perfectly fine — the same posture as an unversioned daemon.
			st.warnf("pack %q: requires connector %q %s but the resolved version of %q is unknown — not gated", ns, name, constraint, bound)
			continue
		}
		if err := checkConductorConstraint(constraint, have); err != nil {
			return fmt.Errorf("pack %q: requires connector %q %s, but %q is at %s — upgrade it, or use a pack release compatible with what you have",
				ns, name, constraint, bound, have)
		}
	}
	return nil
}
