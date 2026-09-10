package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Permission-manifest confinement (docs/design/use-unification.md §D).
//
// The default security model for a plugin is NOT an OS jail. Conductor is a
// privileged app the operator chose to run, and a plugin they added is one too.
// What conductor owes them instead is:
//
//   - VISIBILITY — the plugin declares the commands it spawns and the hosts it
//     calls, conductor records that at install and surfaces it wherever the
//     plugin appears (`plugin add`, `plugin list --caps`, `plugin show`,
//     `connectors ls`), so they can see what they are accepting.
//   - CAN'T-EXCEED-DECLARATION — a connector instance's `network:` may NARROW
//     the plugin's declared egress but never widen it, and the launch is
//     confined to what remains.
//
// What that does and does not enforce, stated plainly:
//
//   - Egress confinement is REAL: it runs through the same egress proxy the
//     isolation: path uses, and a host outside the effective set is refused.
//   - Command confinement is DEFAULT-PATH confinement, not a jail: the child's
//     PATH is a directory holding links to exactly the declared commands, so a
//     plugin that reaches for an undeclared tool by name fails. A plugin that
//     invokes an absolute path bypasses it. That is a manifest with teeth
//     against accident and drift, not against a determined adversary — the
//     opt-in isolation: block is what actually jails.

// EffectiveManifest is the permission set this plugin actually runs with: what
// it declared, narrowed by whatever the referencing connector declared under
// `network:`. Empty `network:` means "no narrowing" — the plugin's own
// declaration stands.
func (s Spec) EffectiveManifest() Manifest {
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

// commandPathDir materializes the PATH a confined plugin runs with: a directory
// under the plugin's own install dir holding a symlink per DECLARED command.
// The child's PATH is set to exactly this directory, so an undeclared command
// is not on the path.
//
// Returns "" when the plugin declared commands but conductor is not in a
// position to confine them (no install dir), and an error only when the
// directory cannot be built — a plugin that declares NO commands gets an empty
// directory, which is the strictest outcome, not a bypass.
func commandPathDir(s Spec) (string, error) {
	if s.BinPath == "" {
		return "", nil
	}
	base := filepath.Dir(s.BinPath)
	if base == "" || base == "." {
		return "", nil
	}
	m := s.EffectiveManifest()
	// "Spawns children I am not naming" is a declaration conductor cannot
	// confine to a list. Honor it by leaving PATH alone rather than pretending
	// to a confinement that is not happening — `plugin list --caps` shows it as
	// "commands (unnamed)" so it is visible.
	if m.Spawns && len(m.Commands) == 0 {
		return "", nil
	}
	dir := filepath.Join(base, ".path")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	cmds := append([]string(nil), m.Commands...)
	sort.Strings(cmds)
	for _, c := range cmds {
		c = strings.TrimSpace(c)
		if c == "" || strings.ContainsAny(c, "/\\") {
			continue // a declared command is a NAME, not a path
		}
		real, err := exec.LookPath(c)
		if err != nil {
			continue // declared but absent on this box: the plugin finds out at use
		}
		_ = os.Symlink(real, filepath.Join(dir, c))
	}
	return dir, nil
}
