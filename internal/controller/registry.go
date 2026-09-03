package controller

import (
	"context"
	"fmt"

	"github.com/NodeSpy/paseo-conductor/internal/config"
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
func NewRegistry(cfgs map[string]config.ControllerConfig, defaultName string, paseoRunner Runner, paseoSender Sender) *Registry {
	prov, _ := paseoRunner.(Provisioner)
	r := &Registry{
		controllers: make(map[string]Controller, len(cfgs)),
		defaultName: defaultName,
		builtin:     newPaseoController(BuiltinPaseo, paseoRunner, paseoSender),
	}
	for name, cc := range cfgs {
		r.controllers[name] = buildController(name, cc, paseoRunner, paseoSender, prov)
	}
	return r
}

// buildController constructs one controller from its config, dispatching on
// type/transport exactly once:
//
//   - type: paseo                          → the built-in paseo runner
//   - type: opencode / agent:opencode+native → opencode native HTTP controller
//   - type: agent-deck                     → agent-deck CLI controller
//   - transport: acp (the default for an agent runtime) → ACP controller
//   - transport: cli                       → bare-cli fallback controller
//   - anything else                        → a stub reporting ErrNotRunnable
//
// prov is the worktree provisioner the non-paseo controllers use (nil-tolerant);
// an unrecognized type/transport stays a stub so a future runtime is
// forward-compatible in config without changing behavior today.
func buildController(name string, cc config.ControllerConfig, paseoRunner Runner, paseoSender Sender, prov Provisioner) Controller {
	if cc.Type == BuiltinPaseo {
		return newPaseoController(name, paseoRunner, paseoSender)
	}
	transport := Transport(cc.EffectiveTransport())
	switch {
	case cc.Type == "opencode":
		return newOpencodeController(name, cc, prov)
	case cc.Type == "agent-deck":
		return newAgentDeckController(name, cc, prov)
	case cc.Agent == "opencode" && transport == TransportNative:
		return newOpencodeController(name, cc, prov)
	case transport == TransportACP:
		return newACPController(name, cc, prov)
	case transport == TransportCLI:
		return newCLIController(name, cc, prov)
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

func (c *stubController) ResumeSession(context.Context, string, Handler) (Session, error) {
	return nil, ErrNotRunnable
}

func (c *stubController) Runner() (Runner, error) { return nil, ErrNotRunnable }
