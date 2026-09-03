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
// runs through; paseoSender is its optional follow-up surface (may be nil).
// Config is assumed already validated (see config.Config.Validate).
func NewRegistry(cfgs map[string]config.ControllerConfig, defaultName string, paseoRunner Runner, paseoSender Sender) *Registry {
	r := &Registry{
		controllers: make(map[string]Controller, len(cfgs)),
		defaultName: defaultName,
		builtin:     newPaseoController(BuiltinPaseo, paseoRunner, paseoSender),
	}
	for name, cc := range cfgs {
		r.controllers[name] = buildController(name, cc, paseoRunner, paseoSender)
	}
	return r
}

// buildController constructs one controller from its config. A `type: paseo`
// entry yields a runnable paseo controller (delegating to the same dispatcher as
// the built-in); every other type/agent is registered as a stub that reports
// ErrNotRunnable until its transport lands in a later milestone — so config is
// forward-compatible today without changing behavior.
func buildController(name string, cc config.ControllerConfig, paseoRunner Runner, paseoSender Sender) Controller {
	if cc.Type == BuiltinPaseo {
		return newPaseoController(name, paseoRunner, paseoSender)
	}
	// Reserved for later milestones. Carry the negotiated session_model/transport
	// so resolution and future wiring see the right shape, but refuse to run.
	model := SessionModel(cc.SessionModel)
	if !model.Valid() {
		model = ModelResumable // agent runtimes are resumable by default
	}
	return &stubController{
		name:      name,
		model:     model,
		transport: Transport(cc.EffectiveTransport()),
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
