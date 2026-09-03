// Package controller abstracts "launch and manage an agent" behind a pluggable
// contract, so conductor's runtime isn't hardwired to paseo. The interface is
// shaped to ACP (the Agent Client Protocol): capability negotiation, session
// lifecycle, prompt turns that stream updates, and a permission/input-request
// callback — one contract that paseo (the built-in default), opencode, and ACP
// agents can all satisfy.
//
// Scope in this milestone (M1): define the contract, ship the built-in `paseo`
// controller (session_model: native) that delegates to the existing
// dispatch.Dispatcher, and add the registry + resolution order that selects a
// controller per agent. The session broker, ACP client, and other controllers
// land in later milestones; until then a controller conductor can't drive yet is
// registered but reports ErrNotRunnable when selected. Crucially, with no
// `controllers:` block configured, resolution always yields paseo — today's
// behavior, unchanged.
package controller

import (
	"context"
	"errors"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
)

// SessionModel describes how a controller keeps an agent alive across turns.
type SessionModel string

const (
	// ModelNative — the controller owns the whole session lifecycle (paseo).
	ModelNative SessionModel = "native"
	// ModelResumable — a session survives by id and is resumed on demand
	// (survives a conductor restart; preferred over held processes).
	ModelResumable SessionModel = "resumable"
	// ModelOneshot — each turn is a fresh process; no persistent session.
	ModelOneshot SessionModel = "oneshot"
)

// Valid reports whether m is a known session model.
func (m SessionModel) Valid() bool {
	switch m {
	case ModelNative, ModelResumable, ModelOneshot:
		return true
	}
	return false
}

// Transport is how conductor talks to a controller's runtime.
type Transport string

const (
	TransportACP    Transport = "acp"    // JSON-RPC 2.0 over stdio (ACP)
	TransportNative Transport = "native" // the runtime's own CLI/API (paseo, opencode serve)
	TransportCLI    Transport = "cli"    // a bare per-tool command recipe
)

// Valid reports whether t is a known transport.
func (t Transport) Valid() bool {
	switch t {
	case TransportACP, TransportNative, TransportCLI:
		return true
	}
	return false
}

// ErrNotRunnable is returned by a controller whose transport conductor cannot
// drive in this build yet (everything except paseo, until the ACP/opencode/
// agent-deck/cli milestones land). Selection surfaces it as an escalation rather
// than silently falling back to a different runtime.
var ErrNotRunnable = errors.New("controller transport not supported in this build")

// ErrNoFollowup is returned when a follow-up turn is sent to a controller that
// can't accept one on a live session.
var ErrNoFollowup = errors.New("controller does not support session follow-up")

// Capabilities is the negotiated feature set of a controller/agent (ACP
// `initialize`). Conductor supplies the user-facing capabilities a runtime lacks
// natively (notifications, interactive hand-off); these flags describe what the
// runtime itself provides.
type Capabilities struct {
	SessionModel       SessionModel
	Transport          Transport
	CheckoutPR         bool // accepts a conductor-provisioned PR/branch worktree as cwd
	InteractiveHandoff bool // can hand a live session to a human to drive
	SendFollowup       bool // accepts a follow-up prompt on a live session
	Remote             bool // runs the agent off-box
}

// Message is one prompt turn sent to a session (ACP session/prompt).
type Message struct {
	Text string
}

// UpdateKind classifies a streamed session update (ACP session/update).
type UpdateKind string

const (
	UpdateStarted UpdateKind = "started" // the turn/agent launched
	UpdateOutput  UpdateKind = "output"  // a streamed output chunk
	UpdateDone    UpdateKind = "done"    // the turn finished
)

// Update is one streamed event from a session. A native controller (paseo) does
// not stream token-by-token; it emits a single terminal Update carrying the
// launched agent id and any captured output.
type Update struct {
	Kind    UpdateKind
	AgentID string
	Output  string
	Err     error
}

// PermissionRequest is an agent's request to take a gated action (ACP
// session/request_permission): run a tool, write a file, execute a command.
type PermissionRequest struct {
	SessionID string
	Tool      string
	Detail    string
	Options   []string // allowed responses, e.g. allow-once / allow-always / reject
}

// PermissionOutcome is the client's decision on a PermissionRequest.
type PermissionOutcome struct {
	Selected string // the chosen option
	Approved bool
}

// InputRequest is an agent's request for free-form human input (a question).
type InputRequest struct {
	SessionID string
	Prompt    string
}

// InputOutcome carries the human's answer to an InputRequest.
type InputOutcome struct {
	Text      string
	Cancelled bool
}

// Handler receives the agent-initiated requests that need a client-side
// decision — permission grants and input questions (ACP client handlers). The
// session broker + HandoffChannel (a later milestone) provide the real
// implementation; a native controller may never call it.
type Handler interface {
	RequestPermission(ctx context.Context, req PermissionRequest) (PermissionOutcome, error)
	RequestInput(ctx context.Context, req InputRequest) (InputOutcome, error)
}

// Spec is a fully-resolved request to open a session. For M1 it wraps conductor's
// dispatch.Request; the paseo controller passes it straight to the CLI
// dispatcher, so there is exactly one argv-building path and no drift. Future
// (neutral) controllers will read the portable fields off the request.
type Spec struct {
	Request dispatch.Request
}

// Session is a live agent conversation (ACP session): native = a launched paseo
// agent, resumable = a session resumed by id, oneshot = a single turn.
type Session interface {
	// ID is the controller's identifier for the session (the paseo agent id).
	ID() string
	// Prompt sends a turn and returns a stream of updates (ACP session/prompt +
	// session/update). Native controllers emit a single terminal Update.
	Prompt(ctx context.Context, msg Message) (<-chan Update, error)
	// Cancel interrupts the in-flight turn (ACP session/cancel).
	Cancel(ctx context.Context) error
	// Close releases the session's resources.
	Close(ctx context.Context) error
}

// Controller launches and manages agent sessions for one configured runtime.
type Controller interface {
	// Name is the configured controller name (or "paseo" for the built-in).
	Name() string
	// Model is the controller's session model.
	Model() SessionModel
	// Transport is how conductor drives the runtime.
	Transport() Transport
	// Initialize negotiates capabilities (ACP `initialize`).
	Initialize(ctx context.Context) (Capabilities, error)
	// NewSession opens a session and runs its first turn (ACP session/new +
	// session/prompt). h receives any permission/input requests.
	NewSession(ctx context.Context, spec Spec, h Handler) (Session, error)
	// ResumeSession re-attaches to an existing session by id (ACP session/load);
	// only meaningful for resumable/native controllers.
	ResumeSession(ctx context.Context, id string, h Handler) (Session, error)
	// Runner returns the concrete dispatch surface the engine drives in M1. paseo
	// returns the CLI dispatcher unchanged; a controller whose transport isn't
	// supported in this build returns ErrNotRunnable.
	Runner() (Runner, error)
}

// Runner is the dispatch surface the engine drives today — identical to the
// operations the engine already performed against paseo. A controller exposes
// one via Runner(); the built-in paseo controller returns the CLI dispatcher
// unchanged, so controller selection is behavior-preserving. *dispatch.Dispatcher
// satisfies this.
type Runner interface {
	Dispatch(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error)
	WaitForAgent(ctx context.Context, id string, timeout time.Duration)
	HasLiveAgent(ctx context.Context, prKey, kind string) bool
	Archive(ctx context.Context, agentID string) error
}

// Sender is an optional session-follow-up surface (ACP prompt on a live session);
// paseo's is `paseo send`. Controllers that can't accept a follow-up omit it.
// *dispatch.Dispatcher satisfies this.
type Sender interface {
	Send(ctx context.Context, id, prompt string) error
}

// The built-in paseo controller runs through the CLI dispatcher unchanged; assert
// it satisfies both surfaces so a signature drift fails the build, not a dispatch.
var (
	_ Runner = (*dispatch.Dispatcher)(nil)
	_ Sender = (*dispatch.Dispatcher)(nil)
)
