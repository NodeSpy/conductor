package config

import (
	"fmt"
	"time"
)

// PaseoProviderConfig configures paseo's `conductor` provider. The paseo
// plugin conductor ships (internal/paseoplugin) registers a provider whose
// ACP stream is conductor's socket: for every paseo session on it, conductor
// launches the agent itself — on Runtime, an acp runtime on this box — in
// the workspace jail, keyed by the paseo agent's id, and relays ACP both
// ways. paseo never holds the agent process; conductor owns the jail's
// lifetime, its host commands, grants and environment.
type PaseoProviderConfig struct {
	// Runtime names the acp runtime (`use: acp`, local, jail-eligible) each
	// session's agent is launched on. Required.
	Runtime string `yaml:"runtime"`
	// Socket is the unix socket the plugin dials. Default
	// <state>/paseo-provider.sock.
	Socket string `yaml:"socket,omitempty"`
	// PaseoTools keeps paseo's own MCP server (the `paseo` http server paseo
	// adds to every session) in the agent's session. FOOTGUN: that server
	// carries a paseo bearer token and its tools create and drive agents —
	// on any provider, including ones conductor does not jail — so an agent
	// holding it can start an unconfined agent and hand it work. Off by
	// default: conductor strips the server from session/new and
	// session/load.
	PaseoTools bool `yaml:"paseo_tools,omitempty"`
	// DetachedTTL is how long a session's jailed agent stays alive with no
	// paseo stream attached (a paseo restart, an unloaded agent) waiting for
	// session/load to re-attach it. Archiving the paseo agent tears it down
	// at once. Default 24h.
	DetachedTTL Duration `yaml:"detached_ttl,omitempty"`
}

// DefaultPaseoDetachedTTL is PaseoProviderConfig.DetachedTTL's default.
const DefaultPaseoDetachedTTL = 24 * time.Hour

// Detached is the effective DetachedTTL.
func (p *PaseoProviderConfig) Detached() time.Duration {
	if p == nil || p.DetachedTTL.D() <= 0 {
		return DefaultPaseoDetachedTTL
	}
	return p.DetachedTTL.D()
}

// validatePaseoProvider checks paseo_provider names an acp runtime conductor
// launches itself on this box — the only kind it can jail and relay.
func (c *Config) validatePaseoProvider() error {
	p := c.PaseoProvider
	if p == nil {
		return nil
	}
	if p.Runtime == "" {
		return fmt.Errorf("config: paseo_provider.runtime is required (an acp runtime, e.g. `use: acp` with `agent: claude-code`)")
	}
	cc, ok := c.MergedControllers()[p.Runtime]
	if !ok {
		return fmt.Errorf("config: paseo_provider.runtime %q is not a configured runtime", p.Runtime)
	}
	rt := c.Runtimes[p.Runtime]
	if u, err := rt.Resolved(); err != nil || !u.IsBuiltin() || u.Name != "acp" {
		return fmt.Errorf("config: paseo_provider.runtime %q must be an acp runtime (use: acp) — conductor relays paseo's ACP stream to the agent it launches", p.Runtime)
	}
	if cc.Host != "" {
		return fmt.Errorf("config: paseo_provider.runtime %q runs on host %q — the provider launches agents on this box, in the workspace jail", p.Runtime, cc.Host)
	}
	if !AgentJailEligible(cc) {
		return fmt.Errorf("config: paseo_provider.runtime %q is not jail-eligible — paseo provider sessions always run in the workspace jail", p.Runtime)
	}
	if iso := cc.Isolation; iso != nil && (iso.Mode == "none" || iso.Privileged) {
		return fmt.Errorf("config: paseo_provider.runtime %q opts out of the workspace jail (isolation mode none / privileged) — paseo provider sessions always run jailed", p.Runtime)
	}
	return nil
}
