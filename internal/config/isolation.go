package config

import (
	"fmt"
	"runtime"
)

// isolationGOOS is runtime.GOOS, a var so tests can validate the Linux-only
// rules from any platform.
var isolationGOOS = runtime.GOOS

// validateIsolation checks one isolation: block's shape. remote reports the
// wrapper will execute on another box over ssh (a `hosts:` entry, or a
// runtime/profile pinned to one): the local-platform check is skipped there
// and the container/proxy modes — which need conductor's own filesystem or
// loopback — are rejected.
func validateIsolation(where string, iso *IsolationConfig, remote bool) error {
	if iso == nil {
		return nil
	}
	switch iso.Mode {
	case "user":
		if iso.User == "" {
			return fmt.Errorf("config: %s: isolation mode user needs `user:` (the low-privilege account)", where)
		}
	case "namespace":
		if !remote && isolationGOOS != "linux" {
			return fmt.Errorf("config: %s: isolation mode namespace is Linux-only (this box is %s) — use mode user or container here", where, isolationGOOS)
		}
	case "container":
		if remote {
			return fmt.Errorf("config: %s: isolation mode container is not supported with a remote host — configure it on that box's own conductor, or use mode user/namespace", where)
		}
		if iso.Container == nil || iso.Container.Image == "" {
			return fmt.Errorf("config: %s: isolation mode container needs `container.image:`", where)
		}
		switch iso.Container.Engine {
		case "", "docker", "podman":
		default:
			return fmt.Errorf("config: %s: isolation container.engine must be docker|podman, got %q", where, iso.Container.Engine)
		}
	case "":
		return fmt.Errorf("config: %s: isolation needs `mode: user|namespace|container`", where)
	default:
		return fmt.Errorf("config: %s: unknown isolation mode %q (want user|namespace|container)", where, iso.Mode)
	}
	if n := iso.Network; n != nil {
		if n.Deny && len(n.Egress) > 0 {
			return fmt.Errorf("config: %s: isolation network `deny: true` and an `egress:` allowlist are mutually exclusive", where)
		}
		if n.Deny && iso.Mode == "user" {
			return fmt.Errorf("config: %s: isolation network `deny: true` needs a structural mode (namespace or container) — mode user can only enforce the egress proxy", where)
		}
		if !n.Deny && remote {
			return fmt.Errorf("config: %s: an isolation egress allowlist needs a local launch (conductor's egress proxy is loopback-only) — use `deny: true` with mode namespace for a remote box", where)
		}
		for _, pat := range n.Egress {
			if pat == "" {
				return fmt.Errorf("config: %s: isolation network.egress: empty pattern", where)
			}
		}
	}
	if l := iso.Limits; l != nil && l.Pids < 0 {
		return fmt.Errorf("config: %s: isolation limits.pids must be >= 0", where)
	}
	return nil
}

// validateProfileIsolation checks an agent profile's isolation against the
// runtime it resolves to: only runtimes conductor launches itself can be
// wrapped. A paseo runtime's agents are children of the paseo daemon —
// conductor never holds that process, so an isolation: there would be a
// silent no-op; it is rejected instead.
func (c *Config) validateProfileIsolation(name string, p AgentProfile) error {
	if p.Isolation == nil {
		return nil
	}
	where := "agent " + name
	rn := p.RuntimeName()
	if rn == "" {
		rn = c.DefaultRuntimeName()
	}
	if rn == "" {
		return fmt.Errorf("config: %s: isolation requires a runtime conductor launches itself (acp/cli/opencode/agent-deck) — the built-in paseo runtime's agents are the paseo daemon's children and cannot be wrapped", where)
	}
	cc, ok := c.MergedControllers()[rn]
	if !ok {
		return nil // the unknown-runtime error is reported by the profile's own validation
	}
	if cc.Type == "paseo" {
		return fmt.Errorf("config: %s: isolation cannot apply to paseo runtime %q (its agents are the paseo daemon's children) — use an acp/cli/opencode/agent-deck runtime, or paseo's own sandboxing", where, rn)
	}
	remote := p.Host != "" || cc.Host != ""
	if err := validateIsolation(where, p.Isolation, remote); err != nil {
		return err
	}
	return validateIsolationControlChannel(where, p.Isolation, cc)
}

// validateIsolationControlChannel rejects the network configurations that
// would sever conductor's own control channel to the runtime it launched: an
// opencode server is reached over local HTTP, so a structural network cutoff
// (namespace --net / --network=none) would leave the server running but
// unreachable. The egress-proxy path (allowlist / present-but-empty network)
// stays fine — loopback is NO_PROXY'd.
func validateIsolationControlChannel(where string, iso *IsolationConfig, cc ControllerConfig) error {
	if iso == nil || iso.Network == nil || !iso.Network.Deny {
		return nil
	}
	opencode := cc.Type == "opencode" || (cc.Agent == "opencode" && cc.EffectiveTransport() == "native")
	if opencode {
		return fmt.Errorf("config: %s: isolation network `deny: true` would sever conductor's HTTP control channel to the opencode server — use an `egress: []` proxy deny instead", where)
	}
	return nil
}
