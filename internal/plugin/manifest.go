package plugin

import (
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// The permission manifest: what a plugin declared (Capabilities), recorded
// at install, shown to the user wherever the plugin appears (`plugin add`,
// `plugin show`, `plugin list --caps`, the boot log), and enforced as its
// jail (jail.go). A config may narrow it (network:, allow_env), never widen
// it; an update that widens it waits for the user's approval (approve.go).

// EffectiveManifest is the permission set this plugin actually runs with: what
// it declared, narrowed by whatever the referencing connector declared under
// `network:`. Unset `network:` means "no narrowing" — the plugin's own
// declaration stands; a set but empty one narrows it to no network.
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
	if s.Network == nil {
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
// Host globs (`*.atlassian.net`) match. Both sides are read as the jail
// grants them (normHostPort): a bare host is host:443, on either side, so a
// declared "api.example.com" covers api.example.com:443 and no other port.
// A config: entry covers nothing here: what it names is only known once it
// is resolved against an instance.
func egressCovered(declared []string, target string) bool {
	th, tp, ok := egressHostPort(target)
	if !ok {
		return false
	}
	for _, d := range declared {
		if strings.HasPrefix(d, "config:") {
			continue
		}
		dh, dp, ok := egressHostPort(d)
		if ok && dp == tp && hostGlobMatch(dh, th) {
			return true
		}
	}
	return false
}

func egressHostPort(s string) (host, port string, ok bool) {
	hp, err := normHostPort(s)
	if err != nil {
		return "", "", false
	}
	h, p, err := net.SplitHostPort(hp)
	if err != nil {
		return "", "", false
	}
	return strings.ToLower(h), p, true
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

// validateCaps is c.Validate plus what core checks before any entry is
// shown or granted: an entry is printed at consent (Manifest.Lines,
// Widens, the HELD log line), so one holding a control character, a
// terminal escape or a space other than ' ' could rewrite what the user
// reads there; and a static path is granted as written, so it names no
// ".." component (a command: or env: reference may, relative to what it
// resolves to).
func validateCaps(c sdk.Capabilities, conn sdk.Schema) error {
	if err := c.Validate(conn); err != nil {
		return err
	}
	for where, es := range map[string][]string{
		"egress": c.Egress, "listen": c.Listen, "read": c.Read, "write": c.Write, "fs": c.FS,
		"exec": c.Exec, "commands": c.Commands, "env": c.Env,
	} {
		for _, e := range es {
			if !plainText(e) {
				return fmt.Errorf("capabilities.%s: %s holds a control or non-printing character", where, strconv.Quote(e))
			}
			switch where {
			case "read", "write", "fs", "exec":
				p := strings.TrimPrefix(e, sdk.OptionalPrefix)
				if p == "~" || strings.HasPrefix(p, "~/") || filepath.IsAbs(p) {
					if slices.Contains(strings.Split(p, "/"), "..") {
						return fmt.Errorf("capabilities.%s: %q has a .. component — name the path itself", where, e)
					}
				}
			}
		}
	}
	return nil
}

// plainText: s is valid UTF-8 of printable runes, its only space ' '.
func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsGraphic(r) || (unicode.IsSpace(r) && r != ' ') {
			return false
		}
	}
	return true
}

// shown is s as consent prints it: quoted when it is not plain text (a
// record an older conductor wrote before validateCaps checked it).
func shown(s string) string {
	if plainText(s) {
		return s
	}
	return strconv.Quote(s)
}
