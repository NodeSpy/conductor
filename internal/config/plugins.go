package config

import (
	"fmt"
	"strings"
)

// PluginRef is one entry in the `plugins:` map — an EXTERNAL plugin: arbitrary
// code the daemon fetches (a follow-up; today a local path) and runs
// out-of-process, speaking conductor's plugin protocol over stdio. A plugin
// acquires a connector `type:` or a `runtime:` name; it configures nothing on
// its own (instances live in connectors:/runtimes:, with their own creds).
//
// Bundled connectors/runtimes (github, slack, paseo, acp, …) are NOT declared
// here — they ship in the binary and are always present. `plugins:` only
// APPENDS external ones; `conductor plugin list` shows both.
//
// Because an external plugin is code the daemon executes (and, for connectors,
// code that RECEIVES the instance's credential), every entry is gated:
// verify-before-execute (Sha256), a reviewed sandbox (Isolation, deny-by-default
// egress), and per-@version audit attribution. See internal/plugin and
// docs/wiki/Plugins.md.
type PluginRef struct {
	// Source is the plugin executable. In this release it is a LOCAL path
	// (absolute, or relative to the config file's directory). Remote sources
	// (github.com/acme/conductor-jira@1.4.0) with fetch + lockfile + signing
	// are a documented follow-up — see docs/wiki/Plugins.md.
	Source string `yaml:"source"`
	// Kind is what the plugin provides: connector | runtime.
	Kind string `yaml:"kind"`
	// Provides names the connector type or runtime this plugin registers.
	// Defaults to the plugins: map key when empty.
	Provides string `yaml:"provides,omitempty"`
	// Version pins the plugin version — advisory, and the attribution carried
	// on every audit record (`plugin@version`).
	Version string `yaml:"version,omitempty"`
	// Sha256 pins the binary's hex-encoded SHA-256, verified BEFORE the binary
	// is ever executed (§8.4). REQUIRED unless AllowUnverified is set: with no
	// pin and no opt-in, the plugin loads but is REFUSED execution.
	Sha256 string `yaml:"sha256,omitempty"`
	// AllowUnverified is the deliberate, insecure opt-in to run a plugin with
	// no Sha256 pin (local development). Never use in production — it disables
	// verify-before-execute. Default false = a plugin without a pin is inert.
	AllowUnverified bool `yaml:"allow_unverified,omitempty"`
	// Args are extra arguments appended to the plugin binary's argv at spawn.
	Args []string `yaml:"args,omitempty"`
	// Isolation is the sandbox policy for this plugin's subprocess (#36 §15,
	// the OPERATOR's grant of the plugin's declared capabilities). Grant egress
	// by listing hosts under isolation.network.egress. With NO isolation block
	// the plugin would run same-uid with a full filesystem view (able to read
	// ~/.config/conductor, App keys, other on-disk secrets), so an external
	// plugin without an isolation block is REFUSED unless AllowUnsandboxed is
	// set (deny-by-default, §8.3).
	Isolation *IsolationConfig `yaml:"isolation,omitempty"`
	// AllowUnsandboxed is the deliberate, insecure opt-in to run an EXTERNAL
	// plugin with no isolation block (no OS confinement — same uid, full
	// filesystem read). Never use for third-party plugins. Default false = a
	// plugin without isolation refuses to launch.
	AllowUnsandboxed bool `yaml:"allow_unsandboxed,omitempty"`
	// AllowSecrets optionally tightens which secret refs the plugin's instances
	// may hand across the process boundary — an EXACT-match allowlist (no
	// globs), mirroring the skill broker. Empty = no extra restriction beyond
	// the structural guarantee that a plugin only ever receives creds for
	// instances of its own type.
	AllowSecrets []string `yaml:"allow_secrets,omitempty"`
}

// PluginKind values.
const (
	PluginKindConnector = "connector"
	PluginKindRuntime   = "runtime"
)

// ProvidesName is the connector type / runtime name this plugin registers,
// defaulting to the map key.
func (p PluginRef) ProvidesName(key string) string {
	if p.Provides != "" {
		return p.Provides
	}
	return key
}

// bundledConnectorTypes and bundledRuntimeTypes name the always-present
// in-binary plugins a plugins: entry must NOT collide with (external-overrides-
// bundled is deliberately disallowed in this release — see §7 open Q4).
var bundledRuntimeTypes = map[string]bool{
	"paseo": true, "opencode": true, "agent-deck": true, "cli": true,
}

// validatePlugins checks the `plugins:` block: names, kinds, sources, and the
// sandbox grant. It does NOT touch the binary (no I/O, no exec) — that is the
// plugin manager's job at build time (verify-before-execute).
func (c *Config) validatePlugins() error {
	for name, p := range c.Plugins {
		where := "plugin " + name
		if name == "" {
			return fmt.Errorf("config: plugins: empty plugin name")
		}
		switch p.Kind {
		case PluginKindConnector, PluginKindRuntime:
		case "":
			return fmt.Errorf("config: %s: missing kind (connector | runtime)", where)
		default:
			return fmt.Errorf("config: %s: unknown kind %q (connector | runtime)", where, p.Kind)
		}
		if strings.TrimSpace(p.Source) == "" {
			return fmt.Errorf("config: %s: missing source (a local executable path)", where)
		}
		if p.Sha256 == "" && !p.AllowUnverified {
			return fmt.Errorf("config: %s: missing sha256 pin — set sha256: to the binary's SHA-256 (verify-before-execute), or allow_unverified: true to run it unpinned (insecure, dev only)", where)
		}
		if p.Sha256 != "" && !isHexSHA256(p.Sha256) {
			return fmt.Errorf("config: %s: sha256 must be 64 hex characters", where)
		}
		provides := p.ProvidesName(name)
		if provides == "" {
			return fmt.Errorf("config: %s: empty provides", where)
		}
		// External-overrides-bundled is disallowed: a plugin may not claim a
		// name a built-in already owns (safe default; opt-in override is a
		// documented follow-up).
		if p.Kind == PluginKindRuntime && bundledRuntimeTypes[provides] {
			return fmt.Errorf("config: %s: runtime %q is a bundled runtime and cannot be replaced by a plugin", where, provides)
		}
		if p.Isolation != nil {
			// A plugin subprocess is always conductor-launched, so the same
			// isolation rules as a cli/acp runtime apply (never remote-only
			// here; host: plugins are a follow-up).
			if err := validateIsolation(where, p.Isolation, false); err != nil {
				return err
			}
		}
		for _, s := range p.AllowSecrets {
			if strings.ContainsAny(s, "*?") {
				return fmt.Errorf("config: %s: allow_secrets entries are exact names, no globs (%q)", where, s)
			}
		}
	}
	// Reject a plugin providing a connector type that also collides with a
	// bundled connector type — checked at build time against the live registry
	// (validatePlugins has no registry handle); connector-name reservation for
	// instances is enforced separately in validateConnectors.
	return nil
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
