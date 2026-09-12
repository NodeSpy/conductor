package config

import (
	"sort"
	"strings"
)

// PluginRef is one external plugin the daemon must run out-of-process. It is
// DERIVED, never authored: there is no `plugins:` block. Every entry here comes
// from a `use:` reference on a connectors:/runtimes: entry that did not resolve
// to a builtin (see PluginRefs).
//
// The kind is the block the reference appeared in — a connector can never be
// wired as a runtime — and is re-checked against the plugin's own `describe` at
// install/start time.
type PluginRef struct {
	// Name is the implementation name: the connector type it registers, or the
	// runtime name agents select with `runtime:`. It is the `use:` reference's
	// resolved leaf, not the config map key.
	Name string
	// Instance is the connectors:/runtimes: map key that referenced it. Several
	// instances may share one plugin; the first in sorted order names it here.
	Instance string
	// Use is the parsed reference — where the implementation comes from.
	Use Use
	// Isolation is OPTIONAL hardening carried from the referencing entry. nil
	// is the normal case: the default model is the permission manifest, not OS
	// confinement.
	Isolation *IsolationConfig
	// Network is the referencing connector's declared egress. Empty for a
	// runtime (a runtime executes your agents; its egress is not narrowed).
	Network []string
	// AllowSecrets optionally tightens which secret refs may cross to the
	// plugin (exact names, no globs).
	AllowSecrets []string
}

// PluginKind values, retained as the wire/CLI spelling of UseKind.
const (
	PluginKindConnector = string(UseKindConnector)
	PluginKindRuntime   = string(UseKindRuntime)
)

// Kind is what this plugin provides, derived from the block it was referenced
// from.
func (p PluginRef) Kind() string { return string(p.Use.Kind) }

// Key is the plugin's stable identity in install state: "<kind-dir>/<name>".
func (p PluginRef) Key() string { return p.Use.InstallKey() }

// IsRemote reports whether the plugin must be fetched from a forge (as opposed
// to a local development binary).
func (p PluginRef) IsRemote() bool { return p.Use.IsRemote() }

// Source is the canonical source string for the trust allowlist — "" for a
// local binary, which is not fetched.
func (p PluginRef) Source() string { return p.Use.Source() }

// Version is the `@…` constraint from the reference, if any.
func (p PluginRef) Version() string { return p.Use.Version }

// Ref is the `name@version` attribution carried on audit records and shown by
// `plugin list`.
func (p PluginRef) Ref() string {
	if p.Use.Version == "" {
		return p.Name
	}
	return p.Name + "@" + p.Use.Version
}

// PluginRefs derives the set of external plugins this config needs: every
// connectors:/runtimes: entry whose `use:` did not resolve to a builtin, keyed
// by "<kind-dir>/<name>" so a connector and a runtime of the same name never
// collide. Entries whose `use:` does not parse are skipped — validateConnectors
// reports those as config errors.
//
// Two instances may legitimately share one plugin (two Jira connectors, one
// jira plugin). They are folded into a single entry; a second instance naming
// the SAME name from a DIFFERENT source is reported by validatePluginRefs.
func (c *Config) PluginRefs() map[string]PluginRef {
	out := map[string]PluginRef{}

	names := make([]string, 0, len(c.ConnectorsMap))
	for n := range c.ConnectorsMap {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		ref := c.ConnectorsMap[name]
		u, err := ref.Resolved()
		if err != nil || u.IsBuiltin() {
			continue
		}
		p, seen := out[u.InstallKey()]
		if !seen {
			p = PluginRef{Name: u.Name, Instance: name, Use: u}
		}
		// Hardening and declared egress union across instances: the plugin runs
		// once, so it must be permitted whatever any of its instances declares.
		if ref.Isolation != nil && p.Isolation == nil {
			p.Isolation = ref.Isolation
		}
		p.Network = appendUnique(p.Network, ref.Network...)
		p.AllowSecrets = appendUnique(p.AllowSecrets, ref.AllowSecrets...)
		out[u.InstallKey()] = p
	}

	rnames := make([]string, 0, len(c.Runtimes))
	for n := range c.Runtimes {
		rnames = append(rnames, n)
	}
	sort.Strings(rnames)
	for _, name := range rnames {
		rt := c.Runtimes[name]
		u, err := rt.Resolved()
		if err != nil || u.IsBuiltin() {
			continue
		}
		// A runtime's map key IS the runtime name agents select, so it wins over
		// the reference leaf (`runtimes: { modal: { use: acme/p/conductor-modal } }`
		// is selected as `runtime: modal`).
		u.Name = name
		p := PluginRef{Name: name, Instance: name, Use: u, Isolation: rt.Isolation}
		out[u.InstallKey()] = p
	}
	return out
}

// validatePluginRefs checks the derived plugin set for the conflicts the
// per-entry validation cannot see: two connectors claiming the same
// implementation name from different sources would silently route one
// instance's credentials to the other's binary.
func (c *Config) validatePluginRefs() error {
	bySource := map[string]string{} // "<kind>/<name>" -> source, first wins
	byInstance := map[string]string{}

	check := func(kind UseKind, instance, ref string) error {
		u, err := ParseUse(kind, ref)
		if err != nil || u.IsBuiltin() {
			return nil
		}
		key := u.InstallKey()
		src := u.Source()
		if src == "" {
			src = "local:" + u.Path
		}
		if prev, ok := bySource[key]; ok && prev != src {
			return errConflict(kind, u.Name, byInstance[key], prev, instance, src)
		}
		bySource[key], byInstance[key] = src, instance
		return nil
	}

	names := make([]string, 0, len(c.ConnectorsMap))
	for n := range c.ConnectorsMap {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := check(UseKindConnector, n, c.ConnectorsMap[n].Use); err != nil {
			return err
		}
	}
	rnames := make([]string, 0, len(c.Runtimes))
	for n := range c.Runtimes {
		rnames = append(rnames, n)
	}
	sort.Strings(rnames)
	for _, n := range rnames {
		if err := check(UseKindRuntime, n, c.Runtimes[n].Use); err != nil {
			return err
		}
	}
	return nil
}

func errConflict(kind UseKind, name, aInst, aSrc, bInst, bSrc string) error {
	return &conflictError{kind: kind, name: name, aInst: aInst, aSrc: aSrc, bInst: bInst, bSrc: bSrc}
}

type conflictError struct {
	kind                     UseKind
	name                     string
	aInst, aSrc, bInst, bSrc string
	_                        struct{}
}

func (e *conflictError) Error() string {
	return "config: " + e.kind.Block() + ": " + e.aInst + " and " + e.bInst +
		" both resolve to the " + string(e.kind) + " " + e.name +
		", but from different sources (" + e.aSrc + " vs " + e.bSrc +
		") — two implementations cannot share a name; rename one, or point both at the same source"
}

func appendUnique(dst []string, add ...string) []string {
	for _, a := range add {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		dup := false
		for _, d := range dst {
			if d == a {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, a)
		}
	}
	return dst
}

// isHexSHA256 reports whether s is exactly 64 lowercase/uppercase hex digits.
func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
