package config

import (
	"fmt"
	"path/filepath"
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
	case "none":
		// The explicit opt-out of the agent sandbox for one runtime/step:
		// the launch runs unwrapped. Nothing else in the block applies.
		if iso.User != "" || iso.Container != nil || iso.Limits != nil || iso.Network != nil || len(iso.FS) > 0 || iso.Privileged || iso.AllowRoot {
			return fmt.Errorf("config: %s: isolation mode none runs the launch unwrapped — it takes no other isolation settings", where)
		}
		return nil
	case "user":
		if iso.User == "" {
			return fmt.Errorf("config: %s: isolation mode user needs `user:` (the low-privilege account)", where)
		}
	case "namespace":
		// `namespace` is the OS-native jail: Linux user namespaces, or macOS
		// Seatbelt (sandbox-exec) — one config surface, backend by OS. Other
		// platforms have neither.
		if !remote && isolationGOOS != "linux" && isolationGOOS != "darwin" {
			return fmt.Errorf("config: %s: isolation mode namespace needs Linux user namespaces or macOS Seatbelt (this box is %s) — use mode user or container here", where, isolationGOOS)
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
		return fmt.Errorf("config: %s: isolation needs `mode: user|namespace|container|none`", where)
	default:
		return fmt.Errorf("config: %s: unknown isolation mode %q (want user|namespace|container|none)", where, iso.Mode)
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
		// Enforced egress (deny + allowlist) under namespace needs the in-sandbox
		// network-namespace forwarder, which is Linux-only. macOS Seatbelt can
		// cut the network wholesale but not run the forwarder, so the allowlist
		// can't be structurally enforced there.
		if n.Deny && len(n.Egress) > 0 && iso.Mode == "namespace" && !remote && isolationGOOS == "darwin" {
			return fmt.Errorf("config: %s: an enforced egress allowlist (network `deny: true` + `egress:`) under mode namespace is Linux-only on this box (macOS Seatbelt has no in-sandbox forwarder) — use mode container for an enforced allowlist on macOS, or plain `deny: true` (full network cut) / an advisory `egress:` without deny", where)
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
	// fs: is a real filesystem allow-list only where there is a filesystem
	// boundary to add to — the namespace pivot_root jail, or a container's
	// bind mounts. Under mode user the sandbox shares the daemon's full view,
	// so an fs: list would be a silent no-op (a footgun — reject it).
	if len(iso.FS) > 0 && iso.Mode == "user" {
		return fmt.Errorf("config: %s: isolation `fs:` (a filesystem allow-list) needs mode namespace or container — mode user shares the daemon's filesystem view, so it would be a silent no-op", where)
	}
	for _, p := range iso.FS {
		if p == "" {
			return fmt.Errorf("config: %s: isolation fs: empty path", where)
		}
		if !filepath.IsAbs(p) {
			return fmt.Errorf("config: %s: isolation fs: %q must be an absolute path", where, p)
		}
	}
	return nil
}

// validateStepIsolation checks a step's isolation against what actually runs
// it. A CODE step (`use:`/`run:` — internal/code) is not dispatched through a
// runtime/controller at all, so it takes a path of its own; everything below
// that is the runtime-resolution check for an AGENT step.
//
// A paseo runtime's agents are children of the paseo daemon — conductor
// never holds that process, so an isolation: there would be a silent no-op;
// it is rejected instead.
func (c *Config) validateStepIsolation(where string, p Step) error {
	if p.Isolation == nil {
		return nil
	}
	if _, class := p.StepEngine(); class != EngineNone {
		return validateCodeStepIsolation(where, p, class)
	}
	rn := p.Runtime
	if rn == "" {
		rn = c.DefaultRuntimeName()
	}
	if rn == "" {
		// The built-in paseo runtime.
		return validatePaseoIsolation(where+" (the built-in paseo runtime)", p.Isolation)
	}
	cc, ok := c.MergedControllers()[rn]
	if !ok {
		return nil // the unknown-runtime error is reported by the step's own validation
	}
	if cc.Type == "paseo" {
		return validatePaseoIsolation(fmt.Sprintf("%s (paseo runtime %q)", where, rn), p.Isolation)
	}
	remote := p.Host != "" || cc.Host != ""
	if err := validateIsolation(where, p.Isolation, remote); err != nil {
		return err
	}
	return validateIsolationControlChannel(where, p.Isolation, cc)
}

// validateCodeStepIsolation checks an `isolation:` block on a CODE step
// (`use:`/`run:`, StepEngine() != EngineNone). Unlike an agent step, a code
// step is executed directly by internal/code — never handed to a
// runtime/controller — so it needs none of the runtime-resolution machinery
// above; it needs to know only whether internal/code can actually enforce
// the block it wrote.
//
//   - EngineCLI / EngineHost (a local `use: cli`/`command:` or `run:
//     <interpreter>` step) is the case this wraps: internal/code's
//     execCLILocal/execHostLocal route spec.Isolation through
//     sandbox.WrapLocalCommand, the same wrap controller.prepareLaunch
//     applies to an agent runtime's own local launch.
//   - A REMOTE code step (`host:`/inline `ssh:`) is refused outright: its
//     execCLIRemote/execRemote paths are a generated ssh script with no
//     sandbox wrap at all, so accepting isolation: there would be exactly
//     the silent no-op this function exists to prevent. Configure the
//     remote box's own conductor instead.
//   - An ENGINE PLUGIN step (`use: js`, …) is refused too: it is a
//     subprocess of the plugin manager, sandboxed (if at all) by its own
//     `engines: <name>.isolation` block — a step-level isolation: there has
//     nothing to attach to.
func validateCodeStepIsolation(where string, p Step, class EngineClass) error {
	if class == EnginePlugin {
		return fmt.Errorf("config: %s: isolation: cannot wrap engine %q — it runs as a plugin subprocess sandboxed by its own `engines: %s: { isolation: … }` block, not a step's; set it there instead", where, p.EngineSelector(), p.EngineSelector())
	}
	if p.Host != "" || p.SSH != nil {
		return fmt.Errorf("config: %s: isolation: is not enforced for a remote code step (host:/ssh:) — conductor only wraps LOCAL `use: cli`/`command:`/`run: <interpreter>` code steps today; drop isolation: here, or run this step locally", where)
	}
	return validateIsolation(where, p.Isolation, false)
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

// OptsOut reports the explicit opt-out of the agent sandbox: `mode: none`.
func (iso *IsolationConfig) OptsOut() bool { return iso != nil && iso.Mode == "none" }

// validatePaseoIsolation checks isolation: on a paseo step or runtime. A
// paseo agent is the paseo daemon's child: conductor does not wrap it, it
// names the provider entry paseo launches it with. The only isolation that
// says something there is `{mode: none}` — the plain provider, not the
// conductor-owned sandboxed one; anything else would be a silent no-op.
func validatePaseoIsolation(where string, iso *IsolationConfig) error {
	if iso.OptsOut() {
		return validateIsolation(where, iso, false)
	}
	return fmt.Errorf("config: %s: on a paseo runtime only `isolation: {mode: none}` applies (it launches the agent through paseo's plain provider instead of conductor's sandboxed entry) — conductor does not wrap a paseo agent itself; for another sandbox use an acp/cli/opencode/agent-deck runtime", where)
}

// validateScopeIsolation checks a trigger's or workflow's isolation: — the
// default for its agent steps — as if written on each of its own agent
// steps that carries none: it must make sense for the runtime that runs
// them.
func (c *Config) validateScopeIsolation(where string, iso *IsolationConfig, steps []Step) error {
	if iso == nil {
		return nil
	}
	if err := validateIsolation(where, iso, false); err != nil {
		return err
	}
	for i, s := range steps {
		if s.Isolation != nil || !isAgentStep(s) {
			continue
		}
		s.Isolation = iso
		if err := c.validateStepIsolation(fmt.Sprintf("%s (its isolation: on step %s)", where, s.slotLabel(i)), s); err != nil {
			return err
		}
	}
	return nil
}

// isAgentStep reports an agent step: what a scope's default isolation:
// applies to.
func isAgentStep(s Step) bool { return s.Form() == "agent" }

// checkPackSandboxLoosening reports every isolation: a pack ships that would
// loosen the agent sandbox — a pack can narrow, never widen. `mode: none`
// (anywhere: a step, a trigger, a workflow) opts its own agents or code out
// of their sandbox; on an agent step (or the trigger/workflow default for
// them) another wrapper (user, container, a privileged namespace) replaces
// the agent sandbox, and egress / fs: paths add to what it shows. The
// operator decides that, from the packs: block. Checked on the PRISTINE
// manifest (before the consumer's overlay), like the runtime/host ban.
func checkPackSandboxLoosening(man *PackManifest) []string {
	var problems []string
	ns := man.Pack.Name
	check := func(where string, iso *IsolationConfig, agent bool, override string) {
		why := isolationLoosens(iso, agent)
		if why == "" {
			return
		}
		problems = append(problems, fmt.Sprintf("pack %q: %s sets isolation: %s — a pack can narrow its sandbox, never loosen it. That is the operator's decision: set it from your config's packs: block, %s", ns, where, why, override))
	}
	man.WalkPackSteps(func(where string, s *Step) {
		check("step "+where, s.Isolation, isAgentStep(*s),
			fmt.Sprintf("e.g. `packs: { <instance>: { steps: { %s: { isolation: { mode: none } } } } }`", where))
	})
	for _, name := range sortedNames(man.Workflows) {
		wf := man.Workflows[name]
		check("workflow "+name, wf.Isolation, true, "on the trigger that runs it (`packs: { <instance>: { triggers: { <trigger>: { isolation: { mode: none } } } } }`) or per step (`steps: { <step>: { isolation: … } }`)")
	}
	for i, tr := range man.Triggers {
		label := tr.Name
		if label == "" {
			label = fmt.Sprintf("triggers[%d]", i)
		}
		check("trigger "+label, tr.Isolation, true, fmt.Sprintf("e.g. `packs: { <instance>: { triggers: { %s: { isolation: { mode: none } } } } }`", label))
	}
	return problems
}

// isolationLoosens says how iso loosens the sandbox ("" = it does not).
// agent: it applies to agent launches (the agent sandbox), not only code.
func isolationLoosens(iso *IsolationConfig, agent bool) string {
	switch {
	case iso == nil:
		return ""
	case iso.Mode == "none":
		return "{mode: none} (no sandbox at all)"
	case !agent:
		return "" // a code step's own wrapper choice: its synthesized jail is the default, not a floor
	case iso.Mode == "user" || iso.Mode == "container":
		return fmt.Sprintf("{mode: %s} (another wrapper in place of the agent sandbox)", iso.Mode)
	case iso.Privileged:
		return "{privileged: true} (the daemon's own filesystem view)"
	case iso.Network != nil && len(iso.Network.Egress) > 0:
		return fmt.Sprintf("network.egress %v (hosts beyond the model endpoints)", iso.Network.Egress)
	case len(iso.FS) > 0:
		return fmt.Sprintf("fs: %v (host paths shown read-write)", iso.FS)
	}
	return ""
}
