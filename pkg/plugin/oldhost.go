package plugin

import "strings"

// SemanticReferences is the semantic a host advertises (HostInfo.Semantics)
// when it resolves the config: and env: entries of a declaration
// (Capabilities.Resolve). Every host built with this SDK advertises it.
const SemanticReferences = "capabilities.references"

// ResolvesReferences reports whether the host asking for a declaration
// resolves its config: and env: entries.
//
// Only the advertised semantic counts. A conductor without it reads a
// "config:<field>" egress entry as a literal host pattern that matches
// nothing, so a plugin declaring only such entries is denied every host.
// Conductors up to v0.87 are like that; v0.88.x and v0.89.0 resolve entries
// but do not advertise it. None of them can be told apart by version: each
// sends "dev" as HostInfo.Version, or, before v0.61, no HostInfo at all. A host
// that does not advertise is therefore treated as one that does not resolve.
// Withholding the entries from it never grants more than the full
// declaration: a conductor that cannot resolve them runs the plugin as it
// did before plugins declared them, and v0.88.x and v0.89.0 jail it without
// them until they update.
func (h *HostInfo) ResolvesReferences() bool {
	if h == nil {
		return false
	}
	for _, s := range h.Semantics {
		if s == SemanticReferences {
			return true
		}
	}
	return false
}

// WithoutReferences is the declaration for a host that does not resolve
// references (HostInfo.ResolvesReferences): every config: and env: entry is
// dropped, optional ("?") ones included, and a list left with nothing is
// returned unspecified (nil), which such a host does not confine.
func (c Capabilities) WithoutReferences() Capabilities {
	strip := func(in []string) []string {
		var out []string
		for _, e := range in {
			v := strings.TrimPrefix(e, OptionalPrefix)
			if strings.HasPrefix(v, refConfig) || strings.HasPrefix(v, refEnv) {
				continue
			}
			out = append(out, e)
		}
		return out
	}
	c.Egress = strip(c.Egress)
	c.Listen = strip(c.Listen)
	c.Read = strip(c.Read)
	c.Write = strip(c.Write)
	c.FS = strip(c.FS)
	c.Exec = strip(c.Exec)
	return c
}
