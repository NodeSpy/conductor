package controller

import "context"

// paseoController is the built-in default controller. It runs agents via the
// paseo CLI (session_model: native, transport: native) by delegating to the
// existing dispatch.Dispatcher — so selecting it is behavior-identical to
// conductor's original hardwired paseo path. The checkout-provisioning and
// acts-as-the-user identity helpers stay in the dispatch package, reusable by
// future controllers.
type paseoController struct {
	name   string
	runner Runner
	sender Sender // optional (paseo send); nil disables session follow-up
}

// newPaseoController builds a paseo controller with the given name (usually
// "paseo") over a dispatch runner and optional follow-up sender.
func newPaseoController(name string, r Runner, s Sender) *paseoController {
	if name == "" {
		name = BuiltinPaseo
	}
	return &paseoController{name: name, runner: r, sender: s}
}

func (c *paseoController) Name() string         { return c.name }
func (c *paseoController) Model() SessionModel  { return ModelNative }
func (c *paseoController) Transport() Transport { return TransportNative }

// Initialize reports paseo's native capabilities. paseo owns the whole session
// lifecycle, accepts a conductor-provisioned worktree as the agent cwd, and can
// hand a live agent off to a human to drive.
func (c *paseoController) Initialize(context.Context) (Capabilities, error) {
	return Capabilities{
		SessionModel:       ModelNative,
		Transport:          TransportNative,
		CheckoutPR:         true,
		InteractiveHandoff: true,
		SendFollowup:       c.sender != nil,
		Remote:             false,
	}, nil
}

// Runner returns the CLI dispatcher (the M1 engine dispatch path).
func (c *paseoController) Runner() (Runner, error) {
	if c.runner == nil {
		return nil, ErrNotRunnable
	}
	return c.runner, nil
}

// NewSession launches a paseo agent for the request (open + first turn) and
// returns a session bound to its agent id. The handler is unused: a native paseo
// agent surfaces permission/input requests through paseo's own UI, not through a
// conductor-side callback.
func (c *paseoController) NewSession(ctx context.Context, spec Spec, _ Handler) (Session, error) {
	if c.runner == nil {
		return nil, ErrNotRunnable
	}
	ref, err := c.runner.Dispatch(ctx, spec.Request)
	if err != nil {
		return nil, err
	}
	return &paseoSession{id: ref.AgentID, sender: c.sender}, nil
}

// ResumeSession re-attaches to a paseo agent by id. There's no re-attach step —
// a follow-up is just `paseo send` — so this simply binds the id.
func (c *paseoController) ResumeSession(_ context.Context, id string, _ Handler) (Session, error) {
	return &paseoSession{id: id, sender: c.sender}, nil
}

// paseoSession is a live paseo agent. Follow-up turns are delivered via
// `paseo send`; a native agent does not stream token-by-token, so a turn yields
// a single terminal Update.
type paseoSession struct {
	id     string
	sender Sender
}

func (s *paseoSession) ID() string { return s.id }

// Prompt delivers a follow-up turn to the live agent (`paseo send`) and returns a
// single-element stream with the terminal Update.
func (s *paseoSession) Prompt(ctx context.Context, msg Message) (<-chan Update, error) {
	ch := make(chan Update, 1)
	if s.sender == nil {
		close(ch)
		return ch, ErrNoFollowup
	}
	err := s.sender.Send(ctx, s.id, msg.Text)
	ch <- Update{Kind: UpdateDone, AgentID: s.id, Err: err}
	close(ch)
	return ch, err
}

// Cancel is a no-op for paseo: an in-flight turn is managed by paseo itself.
func (s *paseoSession) Cancel(context.Context) error { return nil }

// Close is a no-op: the agent's lifecycle (archive/reap) is owned by paseo and
// the reaper, not by the session handle.
func (s *paseoSession) Close(context.Context) error { return nil }
