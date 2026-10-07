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

// ConnectorReq is one declared connector: a version constraint, and
// whether the pack can run without it.
//
// Required defaults to TRUE, deliberately. A pack declares a connector
// because it uses it, so an unbound one is a config mistake and saying so
// loudly beats a pack that installs clean and then does nothing when the
// event arrives. `required: false` is the author's explicit statement that
// the pack degrades — its triggers for that source go dormant and the rest
// of it runs (the same treatment requires.sources gives).
type ConnectorReq struct {
	Version  string
	Required bool
}

// ConnectorReqs is the `requires.connectors` value: connector name → its
// requirement. It decodes from a MAP (name → constraint, or name → block)
// or, as sugar, a LIST of bare names meaning "any version, required".
type ConnectorReqs map[string]ConnectorReq

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
		*c = ConnectorReqs{one: {Version: AnyVersion, Required: true}}
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
			out[name] = ConnectorReq{Version: AnyVersion, Required: true}
		}
		*c = out
		return nil
	case yaml.MappingNode:
		// Each value is either a bare version constraint (the common case)
		// or a block that can also say `required: false`.
		out := make(ConnectorReqs, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			name := strings.TrimSpace(n.Content[i].Value)
			if name == "" {
				return fmt.Errorf("requires.connectors: empty connector name")
			}
			v := n.Content[i+1]
			req := ConnectorReq{Version: AnyVersion, Required: true}
			switch v.Kind {
			case yaml.ScalarNode:
				var constraint string
				if err := v.Decode(&constraint); err != nil {
					return fmt.Errorf("requires.connectors.%s: want a version constraint or a { version, required } block: %w", name, err)
				}
				if strings.TrimSpace(constraint) != "" {
					req.Version = constraint
				}
			case yaml.MappingNode:
				var blk struct {
					Version  string `yaml:"version,omitempty"`
					Required *bool  `yaml:"required,omitempty"`
				}
				if err := strictNodeDecode(v, &blk); err != nil {
					return fmt.Errorf("requires.connectors.%s: %w", name, err)
				}
				if strings.TrimSpace(blk.Version) != "" {
					req.Version = blk.Version
				}
				if blk.Required != nil {
					req.Required = *blk.Required
				}
			default:
				return fmt.Errorf("requires.connectors.%s: want a version constraint or a { version, required } block", name)
			}
			out[name] = req
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
		if c[n].Version != AnyVersion || !c[n].Required {
			plain = false
			break
		}
	}
	if plain {
		return names, nil
	}
	out := make(map[string]any, len(c))
	for n, r := range c {
		if r.Required {
			out[n] = r.Version
			continue
		}
		out[n] = map[string]any{"version": r.Version, "required": false}
	}
	return out, nil
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
	connVerMu        sync.RWMutex
	connectorVerTags = map[string][]string{}
	connVersionsSet  bool
)

// SetConnectorVersions records, per installed PLUGIN KEY ("connectors/jira"
// — Use.InstallKey()), every resolved release tag CURRENTLY installed for
// it — one entry for the common case, several side by side when two
// connector instances pin different versions of the same plugin
// (docs/wiki/Plugins.md "Side-by-side versions"). Called once at boot from
// the plugin install state, which has no configured instance names to key
// by at that point (publishConnectorVersions runs before any config is
// loaded) — keying by plugin identity instead and leaving the per-INSTANCE
// match to resolvedConnectorVersion (below) is what lets each instance gate
// on its own resolved version rather than on whichever version install
// state happened to consider "representative".
//
// A connector absent from the map is treated as builtin (daemon-versioned)
// or not yet installed.
func SetConnectorVersions(v map[string][]string) {
	connVerMu.Lock()
	defer connVerMu.Unlock()
	connectorVerTags = make(map[string][]string, len(v))
	for k, tags := range v {
		connectorVerTags[k] = append([]string(nil), tags...)
	}
	connVersionsSet = true
}

// resolvedConnectorVersion is the version to gate a constraint against: the
// installed plugin release THIS INSTANCE'S OWN `use:` constraint resolves
// to, else the daemon version for a builtin. The second result is false
// when the version is genuinely unknown, in which case the gate is skipped
// rather than guessed at.
//
// Finding 3: this used to be looked up by a flat instance/type -> version
// map that SetConnectorVersions' caller populated by plugin TYPE name
// (Installed.Name), while this function read it by connector INSTANCE name
// — the two agree only when an instance happens to be named after its
// type, so `requires.connectors` version gating silently no-op'd otherwise.
// With side-by-side versions, two instances of the SAME type can be on
// DIFFERENT resolved versions, so there is no single "the" version for a
// type to begin with: each instance resolves its OWN constraint (u.Version)
// against the full set of tags published for its plugin KEY, exactly the
// way internal/plugin.InstallState.GetForConstraint picks a specific
// installed version for a specific configured instance.
func resolvedConnectorVersion(ref ConnectorRef) (string, bool) {
	u, err := ref.Resolved()
	if err != nil {
		return "", false
	}
	if u.IsBuiltin() {
		// A builtin ships in the daemon, so its version is the daemon's.
		return runtimeVersion, true
	}
	connVerMu.RLock()
	tags := connectorVerTags[u.InstallKey()]
	connVerMu.RUnlock()
	if len(tags) == 0 {
		// A plugin whose install state we have not been given (a dev build,
		// an as-yet-uninstalled plugin, a test): unknown, so do not gate.
		return "", false
	}
	// Mirrors internal/plugin.InstallState.GetForConstraint: a genuinely
	// UNCONSTRAINED instance (no @version at all) with only one candidate
	// published under this key IS that instance's build, whether or not it
	// happens to parse as semver (a non-semver snapshot tag, a monorepo
	// prefix bestMatch can't place) — only a REAL constraint (a pin or a
	// range) needs to pick among several via semver comparison, and doing
	// that unconditionally would turn an unparseable tag into a silent
	// "unknown, don't gate" instead of the ungatable-version WARNING
	// checkConductorConstraint already reports for exactly this case.
	if u.Version == "" && len(tags) == 1 {
		return tags[0], true
	}
	tag, ok := bestMatch(tags, u.TagPrefix(), u.Version)
	if !ok {
		return "", false
	}
	return tag, true
}

// checkConnectorVersions gates every declared constraint against the
// consumer's resolved connector. It never fetches — connectors are
// bind-only, so this only ever reads what the consumer already resolved.
func (st *packInstantiation) checkConnectorVersions(ns string, reqs ConnectorReqs, env envBindings) error {
	for _, name := range reqs.Names() {
		constraint := strings.TrimSpace(reqs[name].Version)
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
		have, known := resolvedConnectorVersion(ref)
		if !known {
			// Nothing resolved to gate against (a local dev plugin, an
			// unversioned build). Say so rather than failing a box that may
			// be perfectly fine — the same posture as an unversioned daemon.
			st.warnf("pack %q: requires connector %q %s but the resolved version of %q is unknown — not gated", ns, name, constraint, bound)
			continue
		}
		if err := checkConductorConstraint(constraint, have); err != nil {
			if Ungatable(err) {
				// Same posture as an unknown version: surface it, do not
				// crash-loop a box over a version string we cannot read.
				st.warnf("pack %q: requires connector %q %s, but %v — %q is not gated", ns, name, constraint, err, bound)
				continue
			}
			return fmt.Errorf("pack %q: requires connector %q %s, but %q is at %s — upgrade it, or use a pack release compatible with what you have",
				ns, name, constraint, bound, have)
		}
	}
	return nil
}

// AUTO-BIND THE SOLE CONNECTOR OF A REQUIRED TYPE.
//
// A pack's `requires.connectors: {github: "*"}` names a connector TYPE. When
// the consumer has exactly one connector of that type, asking them to write
// `connectors: {github: gh}` is ceremony that carries no decision: there is
// nothing else it could mean. Two or more, and the choice is real, so it stays
// theirs.
//
//	0 candidates  → unchanged (required → error, optional → dormant)
//	1 candidate   → bound automatically, and it must satisfy the version
//	                constraint — an incompatible sole candidate is an error,
//	                never a silent bind
//	2+ candidates → the explicit binding is required, and the error names them
//
// An explicit binding always wins; this only ever fills a gap. Matching is by
// the instance's `use:` type, not its name, so a connector called `gh` is
// found for a pack that requires `github`.
//
// Candidates are counted by resolved TYPE, never by the raw `use:` string —
// see connectorType. Comparing raw text made a pinned connector invisible, and
// an invisible candidate is an ambiguity that never gets raised.
//
// This is connector PLUMBING, not consent. Trigger arming (`repos:`) stays
// explicit — a pack that can now reach your github connector still fires on no
// repo until you say which.
// connectorType resolves a connector instance's TYPE through the canonical
// `use:` parser — the same one the connector registry uses — rather than
// reading the raw string.
//
// The raw string is not the type. `use: sentry@^2.0`, `use: sentry`, and
// `use: github.com/acme/conductor-plugins//sentry` are three spellings of one
// type, and a version pin or an explicit path must not hide a same-type
// connector from the ambiguity check:
//
//	connectors: {sentry1: {use: "sentry@^2.0"}, sentry2: {use: sentry}}
//
// counted ONE candidate under string comparison, so a pack requiring `sentry`
// was silently auto-bound to sentry2 instead of being told the choice was
// ambiguous — conductor picking, for the operator, between two connectors
// that may hold different credentials and point at different projects.
//
// A `use:` that will not parse resolves to no type and matches nothing; the
// load fails on it in connector validation, with a better message than this
// function could give.
func connectorType(ref ConnectorRef) string {
	u, err := ref.Resolved()
	if err != nil {
		return ""
	}
	return u.Name
}

func (st *packInstantiation) autoBindConnectors(ns string, reqs ConnectorReqs, env envBindings) error {
	for _, name := range reqs.Names() {
		if _, already := env.conn[name]; already {
			continue // explicit wins
		}
		if IsPackSelfBindingConnector(name) {
			continue // binds to itself; there is nothing to choose
		}
		var candidates []string
		for instName, ref := range st.cfg.ConnectorsMap {
			if connectorType(ref) == name {
				candidates = append(candidates, instName)
			}
		}
		sort.Strings(candidates)
		switch len(candidates) {
		case 0:
			// Leave it: validateRequires reports the required-vs-optional
			// case, with the message that already explains binding.
		case 1:
			sole := candidates[0]
			// Never silently bind something the pack said it cannot use.
			if constraint := strings.TrimSpace(reqs[name].Version); constraint != "" && constraint != AnyVersion {
				if have, known := resolvedConnectorVersion(st.cfg.ConnectorsMap[sole]); known {
					if err := checkConductorConstraint(constraint, have); err != nil && !Ungatable(err) {
						return fmt.Errorf("pack %q: requires connector %q %s and your only %s connector (%q) is at %s — upgrade it, or bind a compatible one explicitly: connectors: { %s: <your-connector> }",
							ns, name, constraint, name, sole, have, name)
					}
				}
			}
			env.conn[name] = sole
		default:
			return fmt.Errorf("pack %q: connector %q is ambiguous — your config has %d of type %s (%s), so which one this pack should use is a real choice and yours to make: connectors: { %s: <your-connector> }",
				ns, name, len(candidates), name, strings.Join(candidates, ", "), name)
		}
	}
	return nil
}
