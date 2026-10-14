package config

import (
	"fmt"
	"time"
)

// PaseoProviderConfig configures paseo's `conductor` provider. The paseo
// plugin conductor ships (internal/paseoplugin) registers a provider whose
// ACP stream is conductor's socket: for every paseo session on it, conductor
// launches the agent itself — on Runtime, an acp runtime on this box — in
// the workspace jail, and relays ACP both ways. It speaks only the ACP spec:
// a session is keyed by the session id conductor mints, and its identity
// is the cwd paseo opens it in. paseo never holds the agent process; conductor owns the jail's
// lifetime, its host commands, grants and environment.
type PaseoProviderConfig struct {
	// Runtime names the acp runtime (`use: acp`, local, jail-eligible) each
	// session's agent is launched on. Required.
	Runtime string `yaml:"runtime"`
	// Socket is the unix socket the plugin dials. Default
	// <state>/paseo-provider.sock.
	Socket string `yaml:"socket,omitempty"`
	// MCPServers are the MCP servers paseo passes in a session (ACP's
	// session/new|load mcpServers) the agent keeps, by name. Default none:
	// every one is dropped. paseo adds its own server to every session — a
	// paseo bearer token, and tools that create and drive agents on any
	// provider, unjailed ones included — and conductor cannot tell it from
	// the operator's servers without reading paseo's internals, so the rule
	// is deny-by-default. ["*"] keeps them all: a FOOTGUN, it hands the
	// jailed agent paseo's credential and a way out of the jail.
	MCPServers []string `yaml:"mcp_servers,omitempty"`
	// DetachedTTL is how long a session's jailed agent stays alive with no
	// paseo stream attached — paseo restarted, unloaded the agent, or (by
	// hand) archived it; over ACP all three are the same detach — waiting
	// for session/load to re-attach it. Past it the agent and its jail are
	// ended; a later session/load relaunches the agent and has it load its
	// own session, so the TTL bounds resources, never loses a session.
	// conductor's own archive (ArchivePaseoCwd) ends a jail at once.
	// Default 1h.
	DetachedTTL Duration `yaml:"detached_ttl,omitempty"`
}

// DefaultPaseoDetachedTTL is PaseoProviderConfig.DetachedTTL's default.
const DefaultPaseoDetachedTTL = time.Hour

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
