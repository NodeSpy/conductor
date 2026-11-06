package plugin

import (
	"fmt"
	"strings"
)

// The permission manifest: what a plugin declared (Capabilities), recorded
// at install, shown to the user wherever the plugin appears (`plugin add`,
// `plugin show`, `plugin list --caps`, the boot log), and enforced as its
// jail (jail.go). A config may narrow it (network:, allow_env), never widen
// it; an update that widens it waits for the user's approval (approve.go).

// EffectiveManifest is the permission set this plugin actually runs with: what
// it declared, narrowed by whatever the referencing connector declared under
// `network:`. Empty `network:` means "no narrowing" — the plugin's own
// declaration stands.
//
// A Probe Spec (the type-level `plugin.describe` probe, finding 2) gets NO
// egress, env, or managed auth at all, regardless of what the plugin
// declares — docs/wiki/Plugins.md says a pure self-description needs
// "neither network, secrets, nor env". Without this, an empty Spec.Network
// was read as "unconfigured", which for the throwaway type-level probe
// (never a real instance, so there is nothing to narrow FOR) silently fell
// back to the plugin's WHOLE declared egress.
//
// What the probe KEEPS: Commands, Spawns, and FS. None of those are egress,
// secrets, or env — they are the plugin's declared command/filesystem
// footprint, which commandPathDir (below) needs to build the probe's own
// confined PATH. Dropping them too would strip a declared command off PATH
// during the probe, breaking a type-level describe that shells out to a tool
// the plugin itself declares.
func (s Spec) EffectiveManifest() Manifest {
	if s.Probe {
		return Manifest{
			Commands: s.Manifest.Commands,
			Spawns:   s.Manifest.Spawns,
			FS:       s.Manifest.FS,
		}
	}
	m := s.Manifest
	if len(s.Network) == 0 {
		return m
	}
	m.Egress = append([]string(nil), s.Network...)
	return m
}

// CheckNetworkWithinManifest enforces can't-exceed-declaration: every host the
// config declares under `network:` must be covered by the plugin's own declared
// egress. A config that asks for more than the plugin said it needs is a config
// error, not a silent widening.
//
// A plugin that declares NO egress at all is treated as "unspecified" rather
// than "denies everything": refusing every `network:` entry against an older or
// terse plugin would break working setups to enforce a declaration the plugin
// never made. The manifest still shows exactly what the config granted.
func CheckNetworkWithinManifest(name string, declared []string, network []string) error {
	if len(network) == 0 || len(declared) == 0 {
		return nil
	}
	for _, want := range network {
		if !egressCovered(declared, want) {
			return fmt.Errorf("connector %q: network: %q exceeds what the %s plugin declares it needs (%s) — a config can narrow a plugin's declared egress, never widen it",
				name, want, name, strings.Join(declared, ", "))
		}
	}
	return nil
}

// egressCovered reports whether target matches at least one declared pattern.
// Host globs (`*.atlassian.net`) match; a declared entry with no port covers
// every port on that host, since the plugin declared the host itself.
func egressCovered(declared []string, target string) bool {
	th, tp := splitHostPort(target)
	for _, d := range declared {
		dh, dp := splitHostPort(strings.TrimSpace(d))
		if !hostGlobMatch(dh, th) {
			continue
		}
		if dp == "" || dp == tp {
			return true
		}
	}
	return false
}

func splitHostPort(s string) (host, port string) {
	if h, p, ok := strings.Cut(s, ":"); ok {
		return h, p
	}
	return s, ""
}

// hostGlobMatch matches a declared host pattern against a concrete host. `*`
// matches any run of characters, so `*.atlassian.net` covers
// `your-org.atlassian.net`.
func hostGlobMatch(pattern, host string) bool {
	if pattern == "*" || pattern == host {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(host, parts[0]) {
		return false
	}
	host = host[len(parts[0]):]
	for _, seg := range parts[1 : len(parts)-1] {
		i := strings.Index(host, seg)
		if i < 0 {
			return false
		}
		host = host[i+len(seg):]
	}
	return strings.HasSuffix(host, parts[len(parts)-1])
}
