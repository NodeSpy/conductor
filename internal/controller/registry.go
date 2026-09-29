package controller

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// BuiltinPaseo is the reserved name of the built-in default controller.
const BuiltinPaseo = "paseo"

// Registry holds the configured controllers plus the built-in paseo default, and
// resolves which one runs a given agent.
type Registry struct {
	controllers map[string]Controller // user-configured, by name
	defaultName string                // the controller flagged default:true, or ""
	builtin     Controller            // the built-in paseo controller (final fallback)
}

// NewRegistry builds the controller set from config. paseoRunner is the concrete
// dispatch surface the built-in paseo controller (and any `type: paseo` entry)
// runs through; paseoSender is its optional follow-up surface (may be nil). When
// paseoRunner also satisfies Provisioner (the real *dispatch.Dispatcher does), the
// non-paseo controllers reuse it to check out the conductor-supplied worktree.
// Config is assumed already validated (see config.Config.Validate).
func NewRegistry(cfgs map[string]config.ControllerConfig, defaultName string, paseoRunner Runner, paseoSender Sender, opts ...RegistryOption) *Registry {
	prov, _ := paseoRunner.(Provisioner)
	var o registryOpts
	for _, opt := range opts {
		opt(&o)
	}
	r := &Registry{
		controllers: make(map[string]Controller, len(cfgs)),
		defaultName: defaultName,
		builtin:     newPaseoController(BuiltinPaseo, paseoRunner, paseoSender),
	}
	for name, cc := range cfgs {
		r.controllers[name] = buildController(name, cc, paseoRunner, paseoSender, prov, o.cliProv)
	}
	return r
}

// RegistryOption tunes registry construction without churning every call site.
type RegistryOption func(*registryOpts)

type registryOpts struct{ cliProv Provisioner }

// WithCLIProvisioner injects the checkout provisioner the CLI controllers use
// for LOCAL launches — the git-native one (internal/gitwt), which creates a
// plain worktree under conductor's state dir and removes it when the session
// closes. Unset, the cli path keeps using the paseo-backed provisioner. Only
// the cli controllers take it: acp/opencode/agent-deck stay on paseo (phase 1,
// docs/design/cli-git-worktrees.md).
func WithCLIProvisioner(p Provisioner) RegistryOption {
	return func(o *registryOpts) { o.cliProv = p }
}

// OverridePaseo rebinds a paseo-type entry to its OWN dispatch surface — a
// per-runtime dispatcher (its own bin:, or one whose paseo CLI runs over SSH
// for a runtime with host:). Called by main wiring after construction, once
// per paseo runtime that differs from the shared/primary dispatcher.
func (r *Registry) OverridePaseo(name string, runner Runner, sender Sender) {
	r.controllers[name] = newPaseoController(name, runner, sender)
}

// buildController constructs one controller from its config, dispatching on
// type/transport exactly once:
//
//   - type: paseo                          → the built-in paseo runner
//   - type: opencode / agent:opencode+native → opencode native HTTP controller
//   - type: agent-deck                     → agent-deck CLI controller
//   - type: cli (use: cli)                 → bare-cli controller
//   - transport: acp (the default for an agent runtime) → ACP controller
//   - transport: cli                       → bare-cli fallback controller
//   - anything else                        → a stub reporting ErrNotRunnable
//
// prov is the worktree provisioner the non-paseo controllers use (nil-tolerant);
// cliProv is the git-native provisioner the CLI controllers prefer for a local
// launch (nil → they use prov too). An unrecognized type/transport stays a stub
// so a future runtime is forward-compatible in config without changing behavior
// today.
func buildController(name string, cc config.ControllerConfig, paseoRunner Runner, paseoSender Sender, prov, cliProv Provisioner) Controller {
	if cc.Type == BuiltinPaseo {
		return newPaseoController(name, paseoRunner, paseoSender)
	}
	transport := Transport(cc.EffectiveTransport())
	switch {
	case cc.Type == "opencode":
		return newOpencodeController(name, cc, prov)
	case cc.Type == "agent-deck":
		return newAgentDeckController(name, cc, prov)
	case cc.Type == "cli":
		// `use: cli` (the connectors-model runtime) carries Type "cli" but no
		// transport, so EffectiveTransport() reports "native" and the
		// transport-keyed case below never fires. Dispatch on the type as
		// opencode/agent-deck already do — else a `use: cli` runtime silently
		// degrades to a stub (ErrNotRunnable) and every step pinned to it
		// escalates. The legacy `transport: cli` spelling still resolves via
		// the transport case.
		return newCLIController(name, cc, cliProvisioner(cc, prov, cliProv))
	case cc.Agent == "opencode" && transport == TransportNative:
		return newOpencodeController(name, cc, prov)
	case transport == TransportACP:
		return newACPController(name, cc, prov)
	case transport == TransportCLI:
		return newCLIController(name, cc, cliProvisioner(cc, prov, cliProv))
	}
	// Unknown type/transport: keep it registered as a stub (negotiates the intended
	// shape, refuses to run) until a later milestone teaches conductor to drive it.
	model := SessionModel(cc.SessionModel)
	if !model.Valid() {
		model = ModelResumable // agent runtimes are resumable by default
	}
	return &stubController{
		name:      name,
		model:     model,
		transport: transport,
	}
}

// cliProvisioner is the checkout provisioner one cli controller gets: the
// git-native one for local launches with the paseo one held in reserve for a
// `host:`-pinned dispatch, or — when no git provisioner is wired (tests, and
// any build that doesn't opt in) — plain paseo, exactly as before.
func cliProvisioner(cc config.ControllerConfig, paseoProv, gitProv Provisioner) Provisioner {
	if gitProv == nil {
		return paseoProv
	}
	return newRoutedProvisioner(cc.Host, gitProv, paseoProv)
}

// Resolve returns the controller that should run an agent, applying the
// resolution order: an explicit per-agent controller name → the controller
// flagged default:true → the built-in paseo default.
func (r *Registry) Resolve(perAgent string) (Controller, error) {
	if perAgent != "" {
		c, ok := r.controllers[perAgent]
		if !ok {
			return nil, fmt.Errorf("unknown controller %q", perAgent)
		}
		return c, nil
	}
	if r.defaultName != "" {
		c, ok := r.controllers[r.defaultName]
		if !ok {
			return nil, fmt.Errorf("default controller %q not found", r.defaultName)
		}
		return c, nil
	}
	return r.builtin, nil
}

// ByName returns the controller with the given Controller.Name() — a configured
// entry, or the built-in paseo default for "paseo"/"". Used by the broker to
// re-attach to a persisted session by the name of the controller that owns it
// (which is not necessarily an agent's configured `controller:` selector).
func (r *Registry) ByName(name string) (Controller, error) {
	if c, ok := r.controllers[name]; ok {
		return c, nil
	}
	if name == "" || name == BuiltinPaseo {
		return r.builtin, nil
	}
	return nil, fmt.Errorf("unknown controller %q", name)
}

// targetCanceller is the optional Runner capability controllerRunner provides:
// interrupt every live session for one target across every kind it dispatched.
type targetCanceller interface {
	CancelTarget(ctx context.Context, prKey, reason string) []string
}

// paseoCanceller is the shape the paseo dispatcher's Runner satisfies instead
// (it has no controller-side liveness table to walk — its sessions are the
// paseo daemon's own agents, found and archived by label). Registry.CancelTarget
// falls back to this when a Runner isn't a targetCanceller.
type paseoCanceller interface {
	ListAgents(ctx context.Context, labels map[string]string) ([]dispatch.AgentInfo, error)
	Archive(ctx context.Context, agentID string) error
}

// CancelTarget interrupts every live agent dispatched for prKey, walking EVERY
// registered controller (including the built-in paseo default) — called when
// the engine observes prKey's target (PR/issue) merged or closed. A
// controllerRunner-backed controller is reached via CancelTarget directly; the
// paseo dispatcher (which tracks no controller-side liveness table) is reached
// by listing its agents labeled for this target and archiving each. Best-effort
// per controller — one controller's failure does not stop the walk. Returns
// every id it cancelled/archived.
func (r *Registry) CancelTarget(ctx context.Context, prKey, reason string) []string {
	all := make([]Controller, 0, len(r.controllers)+1)
	for _, c := range r.controllers {
		all = append(all, c)
	}
	all = append(all, r.builtin)
	seen := map[Runner]bool{}
	var ids []string
	for _, c := range all {
		run, err := c.Runner()
		if err != nil || run == nil || seen[run] {
			continue
		}
		seen[run] = true
		if tc, ok := run.(targetCanceller); ok {
			ids = append(ids, tc.CancelTarget(ctx, prKey, reason)...)
			continue
		}
		pc, ok := run.(paseoCanceller)
		if !ok {
			continue
		}
		agents, err := pc.ListAgents(ctx, map[string]string{"conductor": "1", "pr": prKey})
		if err != nil {
			continue
		}
		for _, a := range agents {
			if a.ID == "" {
				continue
			}
			if aerr := pc.Archive(ctx, a.ID); aerr == nil {
				ids = append(ids, a.ID)
			}
		}
	}
	return ids
}

// RunnerFor resolves the controller for an agent and returns its dispatch runner,
// or an error if the controller is unknown or its transport isn't runnable in
// this build.
func (r *Registry) RunnerFor(perAgent string) (Runner, error) {
	c, err := r.Resolve(perAgent)
	if err != nil {
		return nil, err
	}
	return c.Runner()
}

// stubController is a placeholder for a configured controller whose transport
// conductor can't drive yet. It negotiates capabilities (so tooling can inspect
// the intended shape) but refuses to open sessions or hand back a runner.
type stubController struct {
	name      string
	model     SessionModel
	transport Transport
}

func (c *stubController) Name() string         { return c.name }
func (c *stubController) Model() SessionModel  { return c.model }
func (c *stubController) Transport() Transport { return c.transport }

func (c *stubController) Initialize(context.Context) (Capabilities, error) {
	return Capabilities{SessionModel: c.model, Transport: c.transport}, nil
}

func (c *stubController) NewSession(context.Context, Spec, Handler) (Session, error) {
	return nil, ErrNotRunnable
}

func (c *stubController) ResumeSession(context.Context, string, bool, Handler) (Session, error) {
	return nil, ErrNotRunnable
}

func (c *stubController) Runner() (Runner, error) { return nil, ErrNotRunnable }
