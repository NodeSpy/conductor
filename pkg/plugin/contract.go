package plugin

import (
	"context"
	"io"
)

// The rest of the one contract (docs/design/plugin-contract.md §1): methods
// every plugin MAY implement, the same for every plugin. An unimplemented
// one answers CodeMethodNotFound, which the host reads as "unsupported" —
// the only negotiation there is.
const (
	// MethodPoll (daemon→plugin): run a catch-up pass now, return one
	// target's events (forced), or preview a pass (dry run).
	MethodPoll = "plugin.poll"
	// MethodTranslate (daemon→plugin): decode one raw upstream delivery into
	// events, without a listener (replay, once).
	MethodTranslate = "plugin.translate"
	// MethodValidate (daemon→plugin): the plugin's own config and trigger
	// checks, run at load and by `conductor validate`.
	MethodValidate = "plugin.validate"
	// MethodStop (daemon→plugin): one instance is going away (removed or
	// reloaded). Stop its source and release what it holds.
	MethodStop = "plugin.stop"
	// MethodHostState (plugin→daemon): durable key/value state scoped to one
	// instance, surviving plugin restarts.
	MethodHostState = "host.state"
	// MethodHostAuth (plugin→daemon): the CURRENT managed OAuth2 access
	// token for the calling instance's own connector, when it has managed
	// auth. A connector verb invoke gets a fresh token injected by the host
	// on every call (plugin-contract.md §1.11, internal/connector's
	// buildManagedAuth) — a SOURCE has no such call to hang that on, so it
	// asks explicitly, as often as it needs to (every poll, not just the
	// first), and again with refresh: true after its own upstream call comes
	// back 401. Scoped exactly like host.state: refused for an instance this
	// plugin was never handed, and refused when that instance has no managed
	// auth at all.
	MethodHostAuth = "host.auth"
	// MethodHostLog (plugin→daemon): best-effort logging through the
	// daemon's own logger, for a plugin with no stderr of its own (an
	// in-process builtin) — a background poller (rest/graphql's events:)
	// otherwise has nowhere to report a failure the operator can see.
	// Scoped exactly like host.state/host.auth: refused for an instance this
	// plugin was never handed. Never fatal to the caller — HostConn.Log
	// ignores any error this returns.
	MethodHostLog = "host.log"
)

// Poll modes.
const (
	PollNow    = "now"     // run the catch-up pass now
	PollTarget = "target"  // return the events for one target; the host marks them forced
	PollDryRun = "dry_run" // return what a pass would emit, emitting nothing
)

// DescribeRequest is plugin.describe's params. Host is absent from an older
// daemon.
//
// Instance and Config are Q6 (plugin-contract.md §1.4, §3.9 G13): when
// Instance is set, the host is asking for ONE CONFIGURED INSTANCE's
// declaration rather than the plugin's type-level one — rest/graphql's
// user-declared verbs and events depend on the instance's own config, so the
// type-level Decl alone cannot describe them. The host calls this once per
// configured instance, in ADDITION to the once-per-process {host} call.
// CodeMethodNotFound (the plugin does not implement InstanceDescriber) means
// "no per-instance declaration" — the type-level Describe() is this
// instance's declaration too.
type DescribeRequest struct {
	Host     *HostInfo      `json:"host,omitempty"`
	Instance string         `json:"instance,omitempty"`
	Config   map[string]any `json:"config,omitempty"`
}

// HostInfo is what the daemon tells a plugin about itself on describe.
type HostInfo struct {
	Version string `json:"version,omitempty"`
	// Semantics are the semantics the host implements ("event.cursor", …).
	// Serve drops every OPTIONAL semantic the host lacks from the Decl
	// before replying (StripUnknownOptional).
	Semantics []string `json:"semantics,omitempty"`
}

// PollRequest is plugin.poll's params. Target (mode target) is an opaque
// target reference: a target key, or what the operator typed.
type PollRequest struct {
	Instance string `json:"instance"`
	Mode     string `json:"mode"`
	Target   string `json:"target,omitempty"`
	// Event, with mode target, limits the poll to events of this name.
	Event string `json:"event,omitempty"`
}

// PollResult carries the events the pass produced (a plugin may also stream
// them; for mode target only the RETURNED events are forced).
type PollResult struct {
	Events []SourceEvent `json:"events,omitempty"`
}

// TranslateRequest is plugin.translate's params: one raw delivery, and the
// instance's config and triggers, so a plugin translates without a running
// source (replay and once run in a CLI process that starts none). Event is
// the delivery's event name when the caller knows it (a replay fixture, a CI
// runner's environment) rather than reading it from a header.
type TranslateRequest struct {
	Instance string            `json:"instance"`
	Config   map[string]any    `json:"config,omitempty"`
	Triggers []SourceTrigger   `json:"triggers,omitempty"`
	Event    string            `json:"event,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     string            `json:"body"`
}

// TranslateResult carries the events the delivery decodes to.
type TranslateResult struct {
	Events []SourceEvent `json:"events,omitempty"`
}

// ValidateRequest is plugin.validate's params.
type ValidateRequest struct {
	Instance string          `json:"instance"`
	Config   map[string]any  `json:"config,omitempty"`
	Triggers []SourceTrigger `json:"triggers,omitempty"`
}

// ValidateResult lists every problem; empty means valid.
type ValidateResult struct {
	Problems []Problem `json:"problems,omitempty"`
}

// Problem is one validation failure. Path is dot-separated into the config
// ("webhook.secret") or names a trigger ("triggers[2].options.form").
type Problem struct {
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// StopRequest is plugin.stop's params.
type StopRequest struct {
	Instance string `json:"instance"`
}

// HostStateRequest is one host.state call.
type HostStateRequest struct {
	Instance string `json:"instance"`
	Op       string `json:"op"` // get | put | delete | list
	Key      string `json:"key,omitempty"`
	Value    any    `json:"value,omitempty"`
	TTL      string `json:"ttl,omitempty"`
}

// HostStateResult answers a host.state call. For list, Value is the keys.
type HostStateResult struct {
	OK    bool   `json:"ok"`
	Value any    `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// HostAuthRequest is one host.auth call.
type HostAuthRequest struct {
	Instance string `json:"instance"`
	// Refresh asks the host to mint a fresh token rather than hand back a
	// cached one — the upstream just answered 401 with the last one.
	Refresh bool `json:"refresh,omitempty"`
}

// HostAuthResult answers a host.auth call. Token is never logged by the
// host; a plugin that logs it breaks the one rule this method has.
type HostAuthResult struct {
	OK    bool   `json:"ok"`
	Token string `json:"token,omitempty"`
	Error string `json:"error,omitempty"`
}

// HostLogRequest is one host.log call.
type HostLogRequest struct {
	Instance string `json:"instance"`
	Message  string `json:"message"`
}

// HostLogResult answers a host.log call. Logging is best-effort: HostConn.Log
// ignores both this and any transport error entirely, so a daemon that is
// down, or an older one that doesn't implement host.log at all, never costs
// the caller anything beyond the one swallowed call.
type HostLogResult struct {
	OK    bool   `json:"ok,omitempty"`
	Error string `json:"error,omitempty"`
}

// PollHandler, TranslateHandler, ValidateHandler and StopHandler are the
// optional method handlers; a Handler that implements one serves it.
type PollHandler interface {
	Poll(ctx context.Context, req PollRequest) (PollResult, error)
}

// TranslateHandler decodes a raw delivery.
type TranslateHandler interface {
	Translate(ctx context.Context, req TranslateRequest) (TranslateResult, error)
}

// ValidateHandler runs the plugin's own checks.
type ValidateHandler interface {
	Validate(ctx context.Context, req ValidateRequest) (ValidateResult, error)
}

// StopHandler releases one instance.
type StopHandler interface {
	Stop(ctx context.Context, req StopRequest) error
}

// InstanceDescriber is implemented by a plugin whose declaration depends on a
// configured INSTANCE's config (Q6, plugin-contract.md §3.9 G13): rest and
// graphql materialize their user-declared verbs and events this way instead
// of through a Go-side door (the retired InstanceDecler). The host calls
// plugin.describe{instance, config} once per configured instance; a plugin
// that does not implement this interface answers CodeMethodNotFound for that
// call, and the host falls back to treating Describe() (the type-level
// declaration) as this instance's declaration too.
//
// The returned Decl is checked exactly like the type-level one — must-
// understand semantics (CheckSemantics) and internal consistency
// (ValidateSemantics) — so an instance cannot declare something the engine
// does not implement or that does not hang together.
type InstanceDescriber interface {
	DescribeInstance(ctx context.Context, instance string, config map[string]any) (Decl, error)
}

// HostAware is implemented by a Handler that wants the host channel for
// instance-scoped calls (host.state, host.auth). Serve calls SetHost once,
// before it serves anything.
type HostAware interface {
	SetHost(*HostConn)
}

// HostConn is the plugin's channel back to the daemon outside any one run.
type HostConn struct{ calls *callTable }

// State is instance's durable key/value store.
func (h *HostConn) State(instance string) *State { return &State{h: h, instance: instance} }

// Auth is instance's managed-OAuth2 access token face (host.auth). A source
// that polls on its own schedule (rest's events:, and anything like it) has
// no per-call verb invoke to get a fresh token injected into — this is how
// it asks for one directly, as often as it needs (every poll is fine; the
// host caches), and again with refresh after its own 401.
func (h *HostConn) Auth(instance string) *Auth { return &Auth{h: h, instance: instance} }

// Log sends instance's msg to the daemon's own logger (host.log) — best
// effort, and deliberately silent about the outcome: a plugin with no stderr
// of its own (an in-process builtin's background poller, rest/graphql's
// events:) has nowhere else to report a failure, but logging must never be
// something that itself needs error handling at every call site. Safe to
// call as often as needed; a caller wanting to avoid flooding the daemon's
// log with a repeating failure should rate-limit before calling, not after.
func (h *HostConn) Log(ctx context.Context, instance, msg string) {
	var res HostLogResult
	_ = h.calls.call(ctx, MethodHostLog, HostLogRequest{Instance: instance, Message: msg}, &res)
}

// Auth is one instance's host.auth face.
type Auth struct {
	h        *HostConn
	instance string
}

// Token returns the instance's current managed access token, or a freshly
// minted one when refresh is true. It errors when this instance has no
// managed (oauth2) auth, or when the plugin was never handed this instance —
// the same two refusals host.state gives.
func (a *Auth) Token(ctx context.Context, refresh bool) (string, error) {
	var res HostAuthResult
	req := HostAuthRequest{Instance: a.instance, Refresh: refresh}
	if err := a.h.calls.call(ctx, MethodHostAuth, req, &res); err != nil {
		return "", err
	}
	if !res.OK {
		return "", &Error{Code: CodeInternalError, Message: "host.auth: " + res.Error}
	}
	return res.Token, nil
}

// State is one instance's host.state.
type State struct {
	h        *HostConn
	instance string
}

func (s *State) call(ctx context.Context, req HostStateRequest) (any, error) {
	req.Instance = s.instance
	var res HostStateResult
	if err := s.h.calls.call(ctx, MethodHostState, req, &res); err != nil {
		return nil, err
	}
	if !res.OK {
		return nil, &Error{Code: CodeInternalError, Message: "host.state: " + res.Error}
	}
	return res.Value, nil
}

// Get returns key's value, or nil when absent.
func (s *State) Get(ctx context.Context, key string) (any, error) {
	return s.call(ctx, HostStateRequest{Op: "get", Key: key})
}

// Put stores value under key; ttl ("" for none) is a Go duration.
func (s *State) Put(ctx context.Context, key string, value any, ttl string) error {
	_, err := s.call(ctx, HostStateRequest{Op: "put", Key: key, Value: value, TTL: ttl})
	return err
}

// Delete removes key.
func (s *State) Delete(ctx context.Context, key string) error {
	_, err := s.call(ctx, HostStateRequest{Op: "delete", Key: key})
	return err
}

// List returns the keys with prefix.
func (s *State) List(ctx context.Context, prefix string) ([]string, error) {
	v, err := s.call(ctx, HostStateRequest{Op: "list", Key: prefix})
	if err != nil {
		return nil, err
	}
	l, _ := v.([]any)
	out := make([]string, 0, len(l))
	for _, k := range l {
		if s, ok := k.(string); ok {
			out = append(out, s)
		}
	}
	return out, nil
}

// Contract error codes (§1.11), in the JSON-RPC server-error range. A
// handler returns one through Fail; the host maps each to engine behavior
// (internal/connector.ContractError and internal/connector.RetryContract
// carry a decoded answer from there into the engine).
const (
	// CodeUpstream: the upstream answered with an error. Data: {status,
	// retryable}. The engine retries it only when BOTH retryable is true AND
	// the caller's own retry: policy allows it (e.g. a flow step's retry:
	// block) — a false or absent retryable fails closed, never retried, no
	// matter how generous the caller's own retry: is.
	CodeUpstream = -32010
	// CodeTargetGone: the target closed or disappeared under the call. Data
	// MUST carry {target: "<key>"} — the key of the target the call
	// ADDRESSED, the same string spelling as the events' target.key (e.g.
	// "acme/repo#7"). The host honors it as a stop (stop hooks, not a
	// failure — the engine's dispatch.ErrTargetClosed, the same sentinel a
	// dispatch-detected closure already produces) ONLY when data.target
	// equals the run's own trigger target key; otherwise it is a loud,
	// non-retryable upstream failure instead, never a silent stop. This
	// matters because a call under a run can address a target OTHER than
	// the run's own — a notify verb posting to a chat channel, say — and
	// that channel being gone must never be read as "this run's own target
	// (the PR this workflow is about) is gone" merely because the CODE
	// matches. Omitting data.target (an older or careless plugin) is the
	// same as a mismatch: never honored as a stop.
	CodeTargetGone = -32011
	// CodeInvalid: the request can never succeed. Never retried, regardless
	// of any retry: the caller configured.
	CodeInvalid = -32012
	// CodeRateLimited: Data: {retry_after} (a Go duration). The engine
	// retries after it (capped, e.g. 15m) independently of the caller's own
	// retry: policy — a step with no retry: configured at all still gets
	// this retry.
	CodeRateLimited = -32013
	// CodeNotReady: state not computed yet. The engine retries it with a
	// short, bounded backoff (a fixed small number of attempts), also
	// independent of the caller's own retry: policy.
	CodeNotReady = -32014
)

// Fail builds a contract error with optional structured data.
func Fail(code int, msg string, data map[string]any) *Error {
	return &Error{Code: code, Message: msg, Data: data}
}

// ServeConn runs the protocol over any reader/writer pair until r reaches
// EOF. Serve is ServeConn over stdin/stdout; the daemon serves its in-process
// builtins through it over a pipe, so they take the same path as a spawned
// plugin.
func ServeConn(r io.Reader, w io.Writer, h Handler) error { return serve(r, w, h) }
