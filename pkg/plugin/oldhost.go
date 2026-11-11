package plugin

import (
	"slices"
	"strings"
)

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
// returned unspecified (nil), which such a host does not confine. So is a
// keyring item whose attributes hold a reference.
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
	var items []KeyringItem
	for _, it := range c.Keyring {
		ref := false
		for _, v := range it.Attributes {
			if _, ok, _ := ParseKeyringValue(v); ok {
				ref = true
			}
		}
		if !ref {
			items = append(items, it)
		}
	}
	c.Keyring = items
	return c
}

// SemanticExposeLocal is the semantic a host advertises (HostInfo.Semantics)
// when it grants Capabilities.ExposeLocal: an exposure reaching, from its
// jail, the local listeners the engine wires to it. Every host built with
// this SDK advertises it.
const SemanticExposeLocal = "capabilities.expose_local"

// GrantsExposeLocal reports whether the host asking for a declaration grants
// Capabilities.ExposeLocal. Only the advertised semantic counts, as for
// ResolvesReferences: no conductor before it can be told apart by version.
// Those conductors do not know the field and ignore it; Serve withholds it
// from them anyway, so what such a host is sent is exactly what it can act
// on, and what it records is what it was shown.
func (h *HostInfo) GrantsExposeLocal() bool {
	if h == nil {
		return false
	}
	for _, s := range h.Semantics {
		if s == SemanticExposeLocal {
			return true
		}
	}
	return false
}

// SemanticKeyring is the semantic a host advertises (HostInfo.Semantics)
// when it grants Capabilities.Keyring: on Linux it serves the jail the
// declared keyring items, on macOS it grants the Keychain for them. Every
// host built with this SDK advertises it.
const SemanticKeyring = "capabilities.keyring"

// GrantsKeyring reports whether the host asking for a declaration grants
// Capabilities.Keyring. Only the advertised semantic counts, as for
// GrantsExposeLocal. A conductor before it does not know the field and
// ignores it; Serve withholds it anyway, so what such a host records is
// what it was shown, and the conductor that grants it holds it for the
// user's approval as a new permission.
func (h *HostInfo) GrantsKeyring() bool { return h.advertises(SemanticKeyring) }

// SemanticListenerDefaultPath is the semantic a host advertises
// (HostInfo.Semantics) when it reads Listener.DefaultPath. Every host built
// with this SDK advertises it.
const SemanticListenerDefaultPath = "listeners.default_path"

// ReadsListenerDefaultPath reports whether the host asking for a declaration
// reads Listener.DefaultPath. A host before it decodes a declaration's
// listeners strictly and would refuse the plugin over the unknown field, so
// Serve withholds it (WithoutListenerDefaultPath).
func (h *HostInfo) ReadsListenerDefaultPath() bool { return h.advertises(SemanticListenerDefaultPath) }

// advertises reports whether the host advertises semantic s.
func (h *HostInfo) advertises(s string) bool {
	if h == nil {
		return false
	}
	return slices.Contains(h.Semantics, s)
}

// WithoutListenerDefaultPath clears every listener's DefaultPath, for a
// host that does not read it. The Listeners slice is copied, never edited
// in place.
func WithoutListenerDefaultPath(d *Decl) {
	if d.Semantics == nil || len(d.Semantics.Listeners) == 0 {
		return
	}
	sem := *d.Semantics
	sem.Listeners = append([]Listener(nil), sem.Listeners...)
	for i := range sem.Listeners {
		sem.Listeners[i].DefaultPath = ""
	}
	d.Semantics = &sem
}
