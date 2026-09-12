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
	if iso.Privileged && iso.Mode != "namespace" {
		return fmt.Errorf("config: %s: isolation `privileged: true` only applies to mode namespace (it opts out of the default filesystem masking there) — on %s it would be a silent no-op", where, iso.Mode)
	}
	if iso.AllowRoot && iso.Mode != "namespace" {
		return fmt.Errorf("config: %s: isolation `allow_root: true` only applies to mode namespace (it opts into running the user-namespace sandbox as root) — on %s it would be a silent no-op", where, iso.Mode)
	}
	if n := iso.Network; n != nil {
		// `deny: true` + `egress:` together is the ENFORCED allowlist (#36
		// iso-review C1): the namespace/container drops the network and the
		// in-sandbox forwarder into conductor's proxy is the only path out.
		// mode: user has no network namespace to enforce with, so deny —
		// alone or with an allowlist — needs a structural mode there.
		if n.Deny && iso.Mode == "user" {
			return fmt.Errorf("config: %s: isolation network `deny: true` needs a structural mode (namespace or container) — mode user can only run the ADVISORY egress proxy", where)
		}
		if len(n.Egress) > 0 && remote {
			return fmt.Errorf("config: %s: an isolation egress allowlist needs a local launch (conductor's egress proxy lives on this box) — use plain `deny: true` with mode namespace for a remote box", where)
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

// validateStepIsolation checks a step's isolation against the runtime it
// resolves to: only runtimes conductor launches itself can be wrapped. A
// paseo runtime's agents are children of the paseo daemon — conductor never
// holds that process, so an isolation: there would be a silent no-op; it is
// rejected instead.
func (c *Config) validateStepIsolation(where string, p Step) error {
	if p.Isolation == nil {
		return nil
	}
	rn := p.Runtime
	if rn == "" {
		rn = c.DefaultRuntimeName()
	}
	if rn == "" {
		return fmt.Errorf("config: %s: isolation requires a runtime conductor launches itself (acp/cli/opencode/agent-deck) — the built-in paseo runtime's agents are the paseo daemon's children and cannot be wrapped", where)
	}
	cc, ok := c.MergedControllers()[rn]
	if !ok {
		return nil // the unknown-runtime error is reported by the step's own validation
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

// validateStepSkillIsolation refuses skill: on a step whose EFFECTIVE
// isolation (its own, else its runtime's) is mode: user (#36 iso-review C3).
// The skill's one-shot claim code rides the tool subprocess's environment;
// under mode: user every dispatch of the scope shares one EUID, so a sibling
// agent reads /proc/<pid>/environ and races ClaimSession for the claim —
// re-opening exactly the broker-identity hijack the claim flow closed. The
// broker's peer-binding protects the session AFTER a claim, not the claim
// itself. Safe default with no footgun: there is no override — use
// namespace/container isolation (structurally separate /proc views) or drop
// the isolation, both of which keep the claim private.
func (c *Config) validateStepSkillIsolation(where string, p Step) error {
	if p.Skill == nil {
		return nil
	}
	iso := p.Isolation
	if iso == nil {
		rn := p.Runtime
		if rn == "" {
			rn = c.DefaultRuntimeName()
		}
		if cc, ok := c.MergedControllers()[rn]; ok {
			iso = cc.Isolation
		}
	}
	if iso != nil && iso.Mode == "user" {
		return fmt.Errorf("config: %s: skill: cannot be combined with isolation mode user — the one-shot skill claim rides the tool server's environment, and every dispatch under the shared account %q has the same EUID (a sibling reads /proc/<pid>/environ and steals the claim). Use mode namespace or container, or drop skill: on this step", where, iso.User)
	}
	return nil
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
