package plugin

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/acp"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// DefaultCallTimeout bounds a single plugin RPC. A hung plugin must not hang the
// daemon (§8.5); the per-call ctx deadline guarantees the call returns.
const DefaultCallTimeout = 30 * time.Second

// restart backoff: after restartBurst failed (re)starts inside restartWindow,
// the plugin is parked "down" for the rest of the window instead of being
// respawned. On top of that trailing-window burst limit, restartLifetimeCap
// bounds TOTAL (re)starts over the client's whole life — otherwise a plugin
// crashing at a steady rate just under the burst threshold (e.g. every ~11s)
// would respawn forever. Once the lifetime cap is hit the plugin is parked
// permanently (until config reload), so a crashing plugin can never crash-loop
// the daemon (§8.5).
const (
	restartBurst       = 3
	restartWindow      = 30 * time.Second
	restartLifetimeCap = 32
)

// transport is the subset of *acp.Conn the client drives; the seam lets tests
// substitute an in-process fake with no subprocess.
type transport interface {
	Call(ctx context.Context, method string, params, result any) error
	Close() error
	Done() <-chan struct{}
}

// AuthProvider is the host side of host.auth (plugin-contract.md §1): it
// returns the current managed-OAuth2 access token for one connector
// instance — minting or refreshing it exactly as a verb invoke's own
// retry-on-401 would (internal/connector's buildManagedAuth) — and errors
// when that instance has no managed auth configured.
//
// An INTERFACE, not a concrete type (unlike StateStore): the authenticator
// behind it lives in internal/connector, which imports this package to drive
// *Client, so a concrete dependency would cycle. internal/connector
// implements this and cmd/conductor wires it into every Client's Deps, the
// same way it wires State.
type AuthProvider interface {
	// AccessToken returns instance's current access token, refreshing it
	// first when refresh is true (the caller's own upstream call just came
	// back 401 with the cached one). It errors when instance has no managed
	// (oauth2) auth.
	AccessToken(ctx context.Context, instance string, refresh bool) (string, error)
}

// Deps carries what a Client needs from the daemon.
type Deps struct {
	Log     func(string, ...any)
	Redact  func(string) string // secrets.Resolver.Redact — scrubs the plugin's stderr
	Audit   func(map[string]any)
	Sandbox SandboxDeps

	MaxMessageBytes int
	CallTimeout     time.Duration

	// State backs host.state (instance-scoped durable state). nil answers
	// every host.state call with an error.
	State *StateStore
	// Auth backs host.auth (a connector instance's current managed-OAuth2
	// access token). nil answers every host.auth call with an error, same as
	// a nil State does for host.state.
	Auth AuthProvider
	// HostVersion is the daemon version sent on describe.
	HostVersion string

	// dial is the transport opener; nil uses the real subprocess dialer.
	// Tests inject a fake.
	dial func(ctx context.Context, s Spec, d Deps) (t transport, kill func(), err error)

	// onNotify routes a plugin→daemon notification (e.g. a source plugin's
	// streamed events) to the owning Client. Set by NewClient.
	onNotify func(method string, params json.RawMessage)

	// onRequest answers a plugin→daemon REQUEST (the host.* data-plane
	// callbacks an engine issues during a run). Set by NewClient. nil would
	// mean "the daemon exposes no callbacks", which is what every caller got
	// before engines existed.
	onRequest func(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError)
}

// Client is one external plugin: a verified, sandboxed subprocess reached over
// JSON-RPC. It is safe for concurrent use and supervises its own subprocess.
type Client struct {
	spec Spec
	deps Deps

	logLim hostLogLimiter

	mu          sync.Mutex
	conn        transport
	kill        func()
	digest      string // verified sha, set on first successful start
	starts      []time.Time
	totalStart  int // cumulative (re)starts over the client's lifetime
	downForGood bool
	downUntil   time.Time
	closed      bool
	onEvent     func(json.RawMessage) // current source-event sink (set by StartSource)
	// sinks are the per-instance source-event sinks: an event that names its
	// instance (plugin.SourceEvent.Instance) goes to that instance's sink, so
	// one plugin process can serve several instances. An event naming none
	// goes to onEvent — the last instance started — as it always did.
	sinks map[string]func(json.RawMessage)

	// reloading is set for the brief window of a Reload (drain → teardown →
	// swap spec). New bounded calls park on reloadCond until it clears, then
	// re-dial the NEW binary; inflight counts the bounded calls Reload waits to
	// drain before it tears the old process down. StartSource is deliberately
	// NOT counted (it only returns on teardown; draining it would deadlock) —
	// a client that ever sourced refuses in-place reload (onEvent != nil).
	reloading  bool
	reloadCond *sync.Cond
	inflight   sync.WaitGroup
	// closing is set by Close() BEFORE it does anything else, and broadcasts
	// reloadCond — strictly to release a bounded call parked waiting for a
	// Reload that is stuck draining (finding 4): Close must not wait up to
	// reloadDrainTimeout just because Reload happens to be mid-drain when
	// it's invoked. Distinct from `closed` (set only once Close actually
	// tears the connection down, at the very end): a call that was never
	// parked behind a reload is unaffected by `closing` and still gets its
	// best-effort plugin.stop out over the live connection exactly as
	// before stopServed runs.
	closing bool
	// runs are the step-engine runs currently in flight on this plugin, keyed
	// by the capability token minted for each. It is the daemon half of the
	// run_id model: a host.* callback is answered only while the run it names
	// is in this map, which is exactly the span of the plugin.run call that
	// authorized it. Empty for every connector and runtime plugin, and empty
	// again the instant a run returns — so a plugin that keeps a token, or
	// guesses one, has nothing to present it to.
	runs map[string]RunHost
	// served are every instance this plugin has EVER been called for,
	// including a boot-time plugin.validate pass that runs for every
	// configured instance of this plugin's type whether or not this specific
	// client process ever starts a source or is invoked for it (serve() is
	// called from Validate too). It is deliberately broad: it only backs
	// stopServed's best-effort plugin.stop courtesy, where touching an
	// instance this process never really served is harmless.
	served map[string]bool
	// active are the instances this plugin has been handed REAL traffic for:
	// a started source, a poll, a translate, or an invoke — never a mere
	// plugin.validate (finding 5). host.state and host.auth answer only for
	// these: a plugin process serving several configured instances of the
	// same type (one spawned process, several `connectors:` entries) must
	// not be able to read or mint another, merely-validated sibling
	// instance's state or managed-auth token just because it shares the
	// process. Validate alone must never be enough to unlock either.
	active map[string]bool
}

// RunHost authorizes and executes ONE data-plane op on behalf of a run. It is
// the seam between this package (which owns the transport and the token) and
// internal/code (which owns the policy): the daemon passes
// code.CtxHandler.InvokeHost, so a plugin engine's ctx.store call lands in the
// same kvInvoke/sqlInvoke/memInvoke behind the same Spec.DataGuard a `use: js`
// step goes through. Nothing in this package decides what a step may touch.
//
// An ALIAS rather than a defined type, deliberately: internal/code declares
// the same seam in its own words (code.PluginEngine) and cannot import this
// package, so the two have to be the identical type for *Client to satisfy
// that interface — a defined type here would be a near-miss the compiler
// reports as "wrong type for method Run".
type RunHost = func(HostRequest) HostResult

// NewClient builds a Client for spec. It does not start the subprocess; call
// Start (or the first Describe/Invoke) to launch it.
func NewClient(spec Spec, deps Deps) *Client {
	if deps.Log == nil {
		deps.Log = func(string, ...any) {}
	}
	if deps.Redact == nil {
		deps.Redact = func(s string) string { return s }
	}
	if deps.CallTimeout <= 0 {
		deps.CallTimeout = DefaultCallTimeout
	}
	if deps.dial == nil {
		deps.dial = realDial
	}
	c := &Client{spec: spec, runs: map[string]RunHost{}, served: map[string]bool{}, active: map[string]bool{}}
	c.reloadCond = sync.NewCond(&c.mu)
	deps.onNotify = c.handleNotify
	deps.onRequest = c.handleRequest
	c.deps = deps
	return c
}

// handleNotify routes a plugin→daemon notification. Only source-event
// notifications are meaningful today; anything else is ignored.
func (c *Client) handleNotify(method string, params json.RawMessage) {
	if method != MethodEvent {
		return
	}
	var hdr struct {
		Instance string `json:"instance"`
	}
	_ = json.Unmarshal(params, &hdr)
	c.mu.Lock()
	emit := c.onEvent
	if s, ok := c.sinks[hdr.Instance]; ok && hdr.Instance != "" {
		emit = s
	}
	c.mu.Unlock()
	if emit != nil {
		emit(params)
	}
}

// StartSource asks a source plugin to begin streaming events for one instance.
// emit is called for each event (the raw serialized trigger) until ctx is
// cancelled or the plugin exits — cancelling ctx tears down the subprocess,
// which ends the stream. It returns once streaming is acknowledged.
func (c *Client) StartSource(ctx context.Context, req StartSourceRequest, emit func(json.RawMessage)) error {
	_, err := c.startSource(ctx, req, emit)
	return err
}

// startSource is StartSource returning the transport the stream rides, whose
// Done channel closes when that plugin process goes away.
func (c *Client) startSource(ctx context.Context, req StartSourceRequest, emit func(json.RawMessage)) (transport, error) {
	c.serve(req.Instance)
	c.markActive(req.Instance)
	c.mu.Lock()
	c.onEvent = emit
	if c.sinks == nil {
		c.sinks = map[string]func(json.RawMessage){}
	}
	c.sinks[req.Instance] = emit
	c.mu.Unlock()
	// No per-call timeout: start_source is long-lived; the plugin acks quickly but
	// the stream lives for the daemon's lifetime, bounded by ctx.
	c.mu.Lock()
	if err := c.ensureLocked(ctx); err != nil {
		c.mu.Unlock()
		return nil, err
	}
	conn := c.conn
	c.mu.Unlock()
	return conn, conn.Call(ctx, MethodStartSource, req, &struct{}{})
}

// streamRestartDelay is how long StreamSource waits before re-opening a
// stream whose plugin process went away. The client's own crash-loop guard
// (restartBurst / restartWindow) bounds how often that can succeed. A var so
// tests can shrink it.
var streamRestartDelay = 2 * time.Second

// StreamSource is StartSource SUPERVISED: it opens the stream and, whenever
// the plugin process behind it goes away — a crash, a hung call torn down, an
// in-place restart — opens it again on the restarted process, until ctx ends.
// A source plugin's events must not stop for the rest of the daemon's life
// because one verb call timed out. Blocks until ctx is cancelled.
func (c *Client) StreamSource(ctx context.Context, req StartSourceRequest, emit func(json.RawMessage)) error {
	for {
		conn, err := c.startSource(ctx, req, emit)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var answered *acp.RPCError
		if errors.As(err, &answered) {
			// The plugin refused the stream (not a source, bad config): it
			// will refuse it again. That is not a restart to supervise.
			return err
		}
		if err != nil {
			c.deps.Log("plugin %s: source %s: %v — retrying in %s", c.spec.Name, req.Instance, err, streamRestartDelay)
		} else {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-conn.Done():
				c.deps.Log("plugin %s: source %s: the plugin process went away — re-opening the stream", c.spec.Name, req.Instance)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(streamRestartDelay):
		}
	}
}

// ErrNotSupported is what a source-extension call returns when the plugin
// answered method-not-found: it does not implement that part of the ABI.
var ErrNotSupported = errors.New("plugin: not supported")

func notSupported(err error) error {
	var re *acp.RPCError
	if errors.As(err, &re) && re.Code == acp.CodeMethodNotFound {
		return ErrNotSupported
	}
	return err
}

// UpstreamUnauthorized reports whether err is a plugin-returned
// sdk.CodeUpstream error (plugin-contract.md §1.11) whose data names HTTP 401
// — the upstream rejected the credential the daemon handed it. It lets a
// connector with a managed OAuth2 credential (buildManagedAuth) retry once
// with a freshly-minted token, GENERICALLY: the plugin (spawned or an
// in-process contract builtin such as rest/graphql) never sees or manages the
// token's lifecycle itself, so it reports the upstream status and the host
// decides whether that is worth a retry.
func UpstreamUnauthorized(err error) bool {
	var re *acp.RPCError
	if !errors.As(err, &re) || re.Code != sdk.CodeUpstream || len(re.Data) == 0 {
		return false
	}
	var data struct {
		Status int `json:"status"`
	}
	if json.Unmarshal(re.Data, &data) != nil {
		return false
	}
	return data.Status == 401
}

// serve records that this plugin has been handed instance.
func (c *Client) serve(instance string) {
	if instance == "" {
		return
	}
	c.mu.Lock()
	c.served[instance] = true
	c.mu.Unlock()
}

func (c *Client) serves(instance string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.served[instance]
}

// markActive records that this plugin was handed REAL traffic for instance —
// a started source, a poll, a translate, or an invoke (finding 5) — never
// from Validate, which runs for every configured instance regardless of
// whether this client is ever really used for it.
func (c *Client) markActive(instance string) {
	if instance == "" {
		return
	}
	c.mu.Lock()
	c.active[instance] = true
	c.mu.Unlock()
}

// isActive reports whether instance has been handed real traffic (see
// markActive) — the guard host.state and host.auth use, tighter than serves.
func (c *Client) isActive(instance string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active[instance]
}

// Poll asks a source to poll: now (a catch-up pass), for one target (the
// returned events are forced), or as a dry run. ErrNotSupported when the
// plugin does not poll.
func (c *Client) Poll(ctx context.Context, req sdk.PollRequest) ([]sdk.SourceEvent, error) {
	c.serve(req.Instance)
	c.markActive(req.Instance)
	var res sdk.PollResult
	if err := c.call(ctx, sdk.MethodPoll, req, &res); err != nil {
		return nil, notSupported(err)
	}
	return res.Events, nil
}

// Translate decodes one raw delivery into events (replay, once).
func (c *Client) Translate(ctx context.Context, req sdk.TranslateRequest) ([]sdk.SourceEvent, error) {
	c.serve(req.Instance)
	c.markActive(req.Instance)
	var res sdk.TranslateResult
	if err := c.call(ctx, sdk.MethodTranslate, req, &res); err != nil {
		return nil, notSupported(err)
	}
	return res.Events, nil
}

// Validate runs the plugin's own config and trigger checks. Deliberately
// does NOT markActive: Validate runs at boot (and on every reload/validate
// pass) for every configured instance of this plugin's type, whether or not
// this process ever really serves it — marking active here would let a
// plugin process serving several instances answer host.state/host.auth for
// a sibling instance it was merely validated against (finding 5).
func (c *Client) Validate(ctx context.Context, req sdk.ValidateRequest) ([]sdk.Problem, error) {
	c.serve(req.Instance)
	var res sdk.ValidateResult
	if err := c.call(ctx, sdk.MethodValidate, req, &res); err != nil {
		return nil, notSupported(err)
	}
	return res.Problems, nil
}

// Stop tells the plugin one instance is going away. A plugin with nothing to
// stop answers ErrNotSupported, which callers treat as done.
func (c *Client) Stop(ctx context.Context, instance string) error {
	if err := c.call(ctx, sdk.MethodStop, sdk.StopRequest{Instance: instance}, &struct{}{}); err != nil {
		return notSupported(err)
	}
	return nil
}

// AppToken asks a source for a fresh App installation token.
func (c *Client) AppToken(ctx context.Context, instance string, installationID int64) (string, error) {
	var res sdk.AppTokenResult
	req := sdk.AppTokenRequest{Instance: instance, InstallationID: installationID}
	if err := c.call(ctx, sdk.MethodAppToken, req, &res); err != nil {
		return "", notSupported(err)
	}
	return res.Token, nil
}

// runIDBytes is the run token's entropy, matching the ctx socket's token
// (internal/code/ctxsock.go): 32 bytes is far past guessing, and the token
// only ever travels between conductor and a plugin it spawned.
const runIDBytes = 32

// Run executes ONE code step on a step-engine plugin and returns its outputs.
//
// It is the plugin wire's answer to what startCtxServer does for the `cli`
// engine, and the security model is deliberately the same shape:
//
//   - A per-run capability token (req.RunID) is minted here, handed to the
//     plugin as part of THIS call, and registered for exactly the duration of
//     the call. Run B's token presented during run A is simply a wrong token;
//     there is no daemon-wide credential and no way for one step's capability
//     to name another step's data plane.
//   - host authorizes every callback. This package checks the token and
//     nothing else — authentication belongs to the transport, authorization
//     to the handler — and hands the request straight to host, which is
//     internal/code's CtxHandler carrying this step's DataGuard.
//   - The registration is torn down on EVERY exit path (defer), including a
//     timeout, a cancellation, and a transport failure. A plugin that keeps
//     the token and calls back later finds a run nobody is holding.
//
// host may be nil for a step granted no data plane (the engine then gets an
// empty run_id and its host.* calls are refused, exactly as a remote `use:
// cli` step finds no socket).
//
// Unlike a verb call, a run carries NO per-call timeout: a code step is the
// operator's own work and may legitimately take minutes, so its bound is the
// caller's ctx (the step's timeout, the run's cancellation, daemon shutdown)
// rather than DefaultCallTimeout — the same choice StartSource makes for the
// same reason. A transport failure still tears the subprocess down.
func (c *Client) Run(ctx context.Context, req RunRequest, host RunHost) (map[string]any, error) {
	if host != nil {
		tok := make([]byte, runIDBytes)
		if _, err := rand.Read(tok); err != nil {
			return nil, fmt.Errorf("plugin %s: run token: %w", c.spec.Name, err)
		}
		runID := hex.EncodeToString(tok)
		req.RunID = runID
		c.mu.Lock()
		c.runs[runID] = host
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			delete(c.runs, runID)
			c.mu.Unlock()
		}()
	} else {
		req.RunID = ""
	}
	var res RunResult
	if err := c.callFor(ctx, 0, MethodRun, req, &res); err != nil {
		return nil, err
	}
	return res.Outputs, nil
}

// handleRequest answers a plugin→daemon request. The ONLY callable surface is
// the ctx data plane, and only from inside a run that is holding its token.
//
// Authentication happens FIRST and is the whole of this function's security
// job: an unauthenticated caller has no policy to evaluate it against, so the
// guard is never consulted and no store is ever touched. The comparison is
// constant-time over the registered runs, mirroring ctxsock.go's answer().
//
// A connector or runtime plugin reaching this method has no run registered —
// it never received a token, because it is never given a plugin.run — so
// every call it could make is refused here.
func (c *Client) handleRequest(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	if method == sdk.MethodHostState {
		var req sdk.HostStateRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, acp.NewRPCError(acp.CodeInvalidParams, err.Error())
		}
		// Instance-scoped, and only for instances this plugin was handed REAL
		// traffic for (isActive) — a boot-time plugin.validate pass alone
		// (serves, but never isActive) is not enough (finding 5): a plugin
		// process serving several configured instances of the same type must
		// not read or write a merely-validated sibling's state.
		if !c.isActive(req.Instance) {
			return sdk.HostStateResult{Error: fmt.Sprintf("instance %q is not one this plugin serves", req.Instance)}, nil
		}
		return c.deps.State.Do(c.spec.Key(), req), nil
	}
	if method == sdk.MethodHostAuth {
		var req sdk.HostAuthRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, acp.NewRPCError(acp.CodeInvalidParams, err.Error())
		}
		// Instance-scoped exactly like host.state: only for instances this
		// plugin was handed REAL traffic for — never another plugin's, and
		// never a sibling instance this one merely happens to share a
		// process with but was only ever validated for, not invoked/started
		// (finding 5: serves() alone was not enough of a guard, because
		// Validate marks it too).
		if !c.isActive(req.Instance) {
			return sdk.HostAuthResult{Error: fmt.Sprintf("instance %q is not one this plugin serves", req.Instance)}, nil
		}
		if c.deps.Auth == nil {
			return sdk.HostAuthResult{Error: "this daemon has no managed-auth provider"}, nil
		}
		tok, err := c.deps.Auth.AccessToken(ctx, req.Instance, req.Refresh)
		if err != nil {
			// Never the token itself — only the (redacted-by-construction)
			// reason it could not be produced, e.g. "no managed auth".
			return sdk.HostAuthResult{Error: err.Error()}, nil
		}
		return sdk.HostAuthResult{OK: true, Token: tok}, nil
	}
	if method == sdk.MethodHostLog {
		var req sdk.HostLogRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, acp.NewRPCError(acp.CodeInvalidParams, err.Error())
		}
		// Scoped exactly like host.state/host.auth — a plugin may only log
		// as an instance it was handed real traffic for, so a log line can
		// never misattribute itself to a sibling instance it merely shares a
		// process with.
		if !c.isActive(req.Instance) {
			return sdk.HostLogResult{Error: fmt.Sprintf("instance %q is not one this plugin serves", req.Instance)}, nil
		}
		ok, dropped := c.logLim.admit(req.Instance, time.Now())
		if dropped > 0 {
			c.deps.Log("plugin %s instance %s: (%d log line(s) dropped over the host.log rate limit)", c.spec.Name, req.Instance, dropped)
		}
		if !ok {
			return sdk.HostLogResult{Error: "host.log rate limit exceeded; line dropped"}, nil
		}
		c.deps.Log("plugin %s instance %s: %s", c.spec.Name, req.Instance, sanitizeHostLog(req.Message))
		return sdk.HostLogResult{OK: true}, nil
	}
	kind := sdk.HostKindFor(method)
	if kind == "" {
		return nil, acp.NewRPCError(acp.CodeMethodNotFound,
			"daemon exposes no plugin callbacks other than "+sdk.MethodHostState+"/"+sdk.MethodHostAuth+"/"+MethodHostKV+"/"+MethodHostSQL+"/"+MethodHostMemory)
	}
	var req HostRequest
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, acp.NewRPCError(acp.CodeInvalidParams, err.Error())
		}
	}
	// The METHOD is the kind. A body naming a different one is refused rather
	// than reconciled: `host.kv` carrying a sql op would make the method name
	// a lie to every reader of a log or an audit row.
	if req.Kind != "" && req.Kind != kind {
		return nil, acp.NewRPCError(acp.CodeInvalidParams,
			fmt.Sprintf("%s carries kind %q — the method is the kind", method, req.Kind))
	}
	req.Kind = kind

	host := c.runFor(req.RunID)
	if host == nil {
		c.deps.Log("plugin %s: %s refused — no run holds that run_id", c.spec.Name, method)
		return HostResult{OK: false, Refused: true, Error: "host: bad or missing run_id"}, nil
	}
	return host(req), nil
}

// runFor returns the handler for a presented token, or nil. The scan is
// constant-time per candidate (subtle.ConstantTimeCompare) so a token cannot
// be recovered a byte at a time from response latency; an empty token matches
// nothing, since a registered run always has 32 random bytes behind it.
func (c *Client) runFor(token string) RunHost {
	if token == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var match RunHost
	for id, h := range c.runs {
		if subtle.ConstantTimeCompare([]byte(id), []byte(token)) == 1 {
			match = h
		}
	}
	return match
}

// Digest is the verified SHA-256 of the running binary (empty until started).
func (c *Client) Digest() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.digest
}

// EffectiveManifest is this client's own confinement — its Spec's
// EffectiveManifest (manifest.go), i.e. what THIS process (one configured
// instance's own, by default — see Manager.InstanceClient) is actually
// confined to. Read-only/observational, for a caller that wants to show or
// assert a specific client's own narrowed egress (e.g. a per-instance
// isolation proof) without reaching into the unexported Spec field.
func (c *Client) EffectiveManifest() Manifest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.spec.EffectiveManifest()
}

// PID is the OS process id of this client's live subprocess, or 0 when it has
// none — not started yet, an in-process builtin (no subprocess at all), or a
// test fake with no pid to report. Purely observational: nothing in this
// package authorizes anything off of it. It is what lets a consumer (the
// `plugin.go`-instance start log line, `conductor connectors ls`, the e2e
// suite) show that two connector instances of one plugin are two distinct
// processes, which is the point of multi-instance isolation.
func (c *Client) PID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return 0
	}
	if pt, ok := c.conn.(interface{ Pid() int }); ok {
		return pt.Pid()
	}
	return 0
}

// Start verifies the binary (verify-before-execute) and launches the
// subprocess. Safe to call repeatedly; a no-op when already up.
func (c *Client) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureLocked(ctx)
}

// ensureLocked (re)starts the subprocess if it is not currently up, applying
// the restart-burst backoff. Caller holds c.mu.
func (c *Client) ensureLocked(ctx context.Context) error {
	if c.closed {
		return fmt.Errorf("plugin %s: closed", c.spec.Name)
	}
	if c.conn != nil {
		select {
		case <-c.conn.Done():
			// died since last use — fall through to restart
			c.teardownLocked()
		default:
			return nil // up
		}
	}
	if c.downForGood {
		return fmt.Errorf("plugin %s is down for good (exceeded %d lifetime restarts) — reload config to retry", c.spec.Name, restartLifetimeCap)
	}
	now := time.Now()
	if now.Before(c.downUntil) {
		return fmt.Errorf("plugin %s is down (restart backoff until %s)", c.spec.Name, c.downUntil.Format(time.RFC3339))
	}
	if c.totalStart >= restartLifetimeCap {
		c.downForGood = true
		c.deps.Log("plugin %s: exceeded %d lifetime restarts — parking down for good", c.spec.Name, restartLifetimeCap)
		return fmt.Errorf("plugin %s is down for good (crash-loop lifetime cap)", c.spec.Name)
	}
	// prune starts outside the window, then enforce the burst cap
	kept := c.starts[:0]
	for _, t := range c.starts {
		if now.Sub(t) < restartWindow {
			kept = append(kept, t)
		}
	}
	c.starts = kept
	if len(c.starts) >= restartBurst {
		c.downUntil = now.Add(restartWindow)
		c.deps.Log("plugin %s: too many restarts (%d in %s) — parking down until %s", c.spec.Name, len(c.starts), restartWindow, c.downUntil.Format(time.RFC3339))
		return fmt.Errorf("plugin %s is down (crash-loop guard)", c.spec.Name)
	}

	if c.spec.InProcess != nil {
		// A builtin speaking the contract in-process: no binary to verify,
		// the same transport and protocol as every spawned plugin.
		c.starts = append(c.starts, now)
		c.totalStart++
		c.conn, c.kill = inProcessDial(c.spec.InProcess, c.deps)
		c.digest = "in-process"
		return nil
	}
	if c.spec.SnapshotErr != nil {
		// A LOCAL reference that could not be snapshotted (local-build TOCTOU
		// fix, SpecFromRef): refuse rather than fall back to verifying/exec'ing
		// the raw, mutable source path unpinned.
		return fmt.Errorf("plugin %s: local build: %w", c.spec.Name, c.spec.SnapshotErr)
	}
	if !c.spec.Installed() {
		return c.spec.NotInstalledError()
	}
	// verify-before-execute, every (re)start
	digest, err := verify(c.spec)
	if err != nil {
		return err
	}
	if c.spec.Local {
		// A development binary the operator pointed at directly: SpecFromRef
		// already snapshotted it to a content-addressed, immutable copy (the
		// local-build TOCTOU fix) and recorded that snapshot's own sha, which
		// verify() just checked like any other pin — so say THAT, not "no sha
		// to verify against", which stopped being true the moment the snapshot
		// existed.
		c.deps.Log("plugin %s: local build snapshotted at %s (sha %s) — rebuild and reload/restart to pick up a new build", c.spec.Name, c.spec.BinPath, digest)
		// GC coordination (local_snapshot.go's GCLocalSnapshotsOld): every
		// SPAWN refreshes the snapshot dir's mtime, same as every resolve
		// does, so a GC running concurrently (this process's own boot GC, or
		// a sibling daemon's) never removes a snapshot a respawn is about to
		// exec from.
		touchSnapshotUsed(filepath.Dir(c.spec.BinPath))
	}
	c.starts = append(c.starts, now)
	c.totalStart++
	// The process lives as long as the client, not as long as the call that
	// happened to start it: a describe made under a boot timeout must not
	// take the plugin (and every source stream it serves) down when that
	// timeout's context is cancelled. Close and teardown end it.
	conn, kill, err := c.deps.dial(context.WithoutCancel(ctx), c.spec, c.deps)
	if err != nil {
		return fmt.Errorf("plugin %s: launch: %w", c.spec.Name, err)
	}
	c.conn, c.kill, c.digest = conn, kill, digest
	if pt, ok := conn.(interface{ Pid() int }); ok {
		// One subprocess per configured connector instance is the point of
		// multi-instance isolation — log the pid so an operator (or the e2e
		// suite) can see two instances of one plugin are two processes, not
		// one serving both.
		c.deps.Log("plugin %s: subprocess started (pid %d)", c.spec.Identity(), pt.Pid())
	}
	return nil
}

func (c *Client) teardownLocked() {
	if c.kill != nil {
		c.kill()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.conn, c.kill = nil, nil
}

// Describe fetches and validates the plugin's self-description.
func (c *Client) Describe(ctx context.Context) (*Decl, error) {
	var raw json.RawMessage
	req := sdk.DescribeRequest{Host: &sdk.HostInfo{Version: c.deps.HostVersion, Semantics: sdk.KnownSemantics()}}
	if err := c.call(ctx, MethodDescribe, req, &raw); err != nil {
		return nil, err
	}
	var decl Decl
	if err := json.Unmarshal(raw, &decl); err != nil {
		return nil, fmt.Errorf("plugin %s: describe: %w", c.spec.Name, err)
	}
	if decl.ProtocolVersion != ProtocolVersion {
		return nil, fmt.Errorf("plugin %s: unsupported protocol version %d (daemon speaks %d)", c.spec.Name, decl.ProtocolVersion, ProtocolVersion)
	}
	if decl.Type == "" {
		return nil, fmt.Errorf("plugin %s: describe returned empty type", c.spec.Name)
	}
	// Identity anti-forgery (§8.2): the type a connector or engine plugin
	// serves is the operator's configured name, not whatever the plugin
	// claims. (A runtime is exempt: its name is the runtimes: map key, which
	// the operator chose independently of the reference leaf.)
	if (c.spec.Kind == KindConnector || c.spec.Kind == KindStep) && decl.Type != c.spec.Provides {
		return nil, fmt.Errorf("plugin %s: describe claims type %q but is configured to provide %q — refusing (identity forgery)", c.spec.Name, decl.Type, c.spec.Provides)
	}
	// Declarations are must-understand and must hang together: a semantic
	// this daemon does not implement, or one naming a verb that is not
	// there, refuses the plugin rather than half-applying it.
	// (Decl.ABI is not read: there are no tiers.)
	if p := append(sdk.CheckSemantics(raw), sdk.ValidateSemantics(decl)...); len(p) > 0 {
		return nil, fmt.Errorf("plugin %s: declarations refused:\n  %s", c.spec.Name, strings.Join(p, "\n  "))
	}
	return &decl, nil
}

// DescribeInstance asks the plugin for ONE configured instance's declaration
// (Q6, plugin-contract.md §1.4, §3.9 G13): rest/graphql materialize their
// user-declared verbs and events this way, instance by instance, replacing
// the InstanceDecler Go-side door. supported=false (with a nil error) means
// the plugin does not implement InstanceDescriber (CodeMethodNotFound) — the
// type-level Describe() applies to this instance too.
//
// The returned Decl passes the same checks the type-level one does:
// must-understand semantics (CheckSemantics) and internal consistency
// (ValidateSemantics), so a per-instance declaration can never act on a
// semantic the engine does not implement or that does not hang together.
func (c *Client) DescribeInstance(ctx context.Context, instance string, config map[string]any) (decl *Decl, supported bool, err error) {
	var raw json.RawMessage
	req := sdk.DescribeRequest{
		Host:     &sdk.HostInfo{Version: c.deps.HostVersion, Semantics: sdk.KnownSemantics()},
		Instance: instance,
		Config:   config,
	}
	if err := c.call(ctx, MethodDescribe, req, &raw); err != nil {
		var re *acp.RPCError
		if errors.As(err, &re) && re.Code == acp.CodeMethodNotFound {
			return nil, false, nil
		}
		return nil, false, err
	}
	var d Decl
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, false, fmt.Errorf("plugin %s: describe instance %s: %w", c.spec.Name, instance, err)
	}
	if d.ProtocolVersion != ProtocolVersion {
		return nil, false, fmt.Errorf("plugin %s: instance %s: unsupported protocol version %d (daemon speaks %d)", c.spec.Name, instance, d.ProtocolVersion, ProtocolVersion)
	}
	if d.Type == "" {
		return nil, false, fmt.Errorf("plugin %s: instance %s: describe returned empty type", c.spec.Name, instance)
	}
	if (c.spec.Kind == KindConnector || c.spec.Kind == KindStep) && d.Type != c.spec.Provides {
		return nil, false, fmt.Errorf("plugin %s: instance %s: describe claims type %q but is configured to provide %q — refusing (identity forgery)", c.spec.Name, instance, d.Type, c.spec.Provides)
	}
	if p := append(sdk.CheckSemantics(raw), sdk.ValidateSemantics(d)...); len(p) > 0 {
		return nil, false, fmt.Errorf("plugin %s: instance %s: declarations refused:\n  %s", c.spec.Name, instance, strings.Join(p, "\n  "))
	}
	return &d, true, nil
}

// Invoke runs a verb. The connection map carries only the calling instance's
// resolved credentials (least privilege). A transport error tears the
// subprocess down so the next call restarts it (subject to backoff).
func (c *Client) Invoke(ctx context.Context, req InvokeRequest) (map[string]any, error) {
	c.serve(req.Instance)
	c.markActive(req.Instance)
	var res InvokeResult
	if err := c.call(ctx, MethodInvoke, req, &res); err != nil {
		return nil, err
	}
	return res.Outputs, nil
}

// call ensures the subprocess is up, applies the per-call timeout, and on a
// transport failure tears down so a later call can restart.
func (c *Client) call(ctx context.Context, method string, params, result any) error {
	return c.callFor(ctx, c.deps.CallTimeout, method, params, result)
}

// callFor is call with an explicit bound: timeout <= 0 means the call is
// bounded by ctx alone (plugin.run — see Run).
func (c *Client) callFor(ctx context.Context, timeout time.Duration, method string, params, result any) error {
	c.mu.Lock()
	// Park while a Reload is swapping the process, then re-dial the new
	// binary. A plain `for c.reloading { c.reloadCond.Wait() }` (as this once
	// was) ignores ctx entirely and ignores Close(): sync.Cond has no
	// ctx-aware wait, so a call bounded by a short ctx (stopServed's
	// stopGrace) or arriving during Close() would still sleep until Reload's
	// own reloadDrainTimeout elapses and broadcasts (finding 4) — up to 30s
	// of a Close() that was supposed to return in ~stopGrace. A watcher
	// goroutine broadcasts reloadCond when ctx ends, so a parked call wakes
	// and re-checks instead of waiting on Reload alone; `closing` (set by
	// Close before it does anything else) gives the same early wakeup to a
	// call with no deadline at all.
	if c.reloading {
		stopWatch := make(chan struct{})
		if done := ctx.Done(); done != nil {
			go func() {
				select {
				case <-done:
					c.mu.Lock()
					c.reloadCond.Broadcast()
					c.mu.Unlock()
				case <-stopWatch:
				}
			}()
		}
		for c.reloading && !c.closing && ctx.Err() == nil {
			c.reloadCond.Wait()
		}
		close(stopWatch)
		if c.reloading {
			// Bailed without the reload actually finishing: Close is tearing
			// this client down, or ctx ended first. Either way, waiting any
			// longer serves nothing — report whichever applies.
			c.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("plugin %s: closing", c.spec.Name)
		}
	}
	if err := c.ensureLocked(ctx); err != nil {
		c.mu.Unlock()
		return err
	}
	conn := c.conn
	// Count this bounded call so Reload can drain it before teardown. Add under
	// mu, before unlock, so a Reload that observes reloading=false is guaranteed
	// to see this in inflight (and vice-versa) — no lost call across the swap.
	c.inflight.Add(1)
	defer c.inflight.Done()
	c.mu.Unlock()

	cctx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	err := conn.Call(cctx, method, params, result)
	var answered *acp.RPCError
	if err != nil && !errors.As(err, &answered) {
		// transport/timeout failure: drop the connection so the plugin is
		// restarted on the next call (never takes the daemon down). A
		// JSON-RPC ERROR RESPONSE is not one: the plugin answered, the
		// transport is healthy, and tearing the process down for it would
		// also end every source stream it serves — a verb that failed on the
		// platform's side (a 422, a missing repo) would silence the source.
		c.mu.Lock()
		if c.conn == conn {
			c.teardownLocked()
		}
		c.mu.Unlock()
	}
	return err
}

// Close stops the subprocess for good.
func (c *Client) Close() error {
	// Mark closing and wake anything parked behind a stuck Reload FIRST
	// (finding 4) — before stopServed's best-effort plugin.stop calls, which
	// would otherwise themselves park on reloadCond and make Close wait out
	// the full reloadDrainTimeout just because a reload happened to be
	// mid-drain. A call that isn't parked on a reload is unaffected: it never
	// consults `closing` at all (callFor only checks it inside the
	// `c.reloading` branch), so stopServed's calls still go out over the live
	// connection exactly as before in the common (no concurrent reload) case.
	c.mu.Lock()
	c.closing = true
	c.reloadCond.Broadcast()
	c.mu.Unlock()
	c.stopServed()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.teardownLocked()
	return nil
}

// stopGrace bounds the plugin.stop courtesy at close. A var so tests can
// shrink it.
var stopGrace = 2 * time.Second

// stopServed sends plugin.stop for every instance this plugin served, when
// its process is up, so it can release what it holds (a tunnel, a relay
// subscription) before the process goes. Best effort and bounded: a plugin
// with nothing to stop answers method-not-found, and a hung one is killed
// anyway.
func (c *Client) stopServed() {
	c.mu.Lock()
	up := c.conn != nil && !c.closed
	insts := make([]string, 0, len(c.served))
	for i := range c.served {
		insts = append(insts, i)
	}
	c.mu.Unlock()
	if !up {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	for _, i := range insts {
		if err := c.Stop(ctx, i); err != nil && err != ErrNotSupported && ctx.Err() == nil {
			c.deps.Log("plugin %s: stop %s: %v", c.spec.Name, i, err)
		}
	}
}

// reloadDrainTimeout bounds how long Reload waits for in-flight bounded calls to
// finish before giving up (and letting the caller restart instead of force-
// killing a live call). A var so tests can shrink it.
var reloadDrainTimeout = 30 * time.Second

// ErrReloadUnsupported means this client can't be swapped in place — it holds a
// live source stream (StartSource), which only ends on teardown, so there is
// nothing to drain toward. The caller falls back to a full restart.
var ErrReloadUnsupported = errors.New("plugin: in-place reload unsupported (live source)")

// ErrReloadBusy means in-flight calls did not drain within reloadDrainTimeout
// (or a reload is already running). The caller falls back to a full restart
// rather than tearing a process down under a live call.
var ErrReloadBusy = errors.New("plugin: in-place reload busy (calls did not drain)")

// Reload swaps this client's subprocess to newSpec's binary IN PLACE — same
// *Client pointer, so every consumer (engine lookup, connector impl, rpcBackend)
// transparently drives the new build with no re-registration. It drains bounded
// calls first, then tears the old process down and points spec at the new one;
// the next call lazily re-dials (verify-before-execute runs against newSpec).
//
// The caller must have already confirmed the new build's Decl surface is
// compatible (SameReloadSurface) — Reload does not re-validate registrations.
func (c *Client) Reload(newSpec Spec) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("plugin %s: closed", c.spec.Name)
	}
	if c.onEvent != nil {
		c.mu.Unlock()
		return ErrReloadUnsupported
	}
	if c.reloading {
		c.mu.Unlock()
		return ErrReloadBusy
	}
	c.reloading = true
	c.mu.Unlock()

	// Drain bounded in-flight calls (new ones now park on reloadCond, so the
	// count only falls). Bounded so a stuck/long call can't wedge the reload —
	// on timeout we abort and the caller restarts instead.
	done := make(chan struct{})
	go func() { c.inflight.Wait(); close(done) }()
	drained := false
	select {
	case <-done:
		drained = true
	case <-time.After(reloadDrainTimeout):
	}

	c.mu.Lock()
	defer func() {
		c.reloading = false
		c.reloadCond.Broadcast()
		c.mu.Unlock()
	}()
	if c.closed {
		// Close ran concurrently while this reload was draining (finding 4:
		// Close no longer waits for us). The client is torn down; swapping
		// its spec now would just be racing teardownLocked for nothing.
		return fmt.Errorf("plugin %s: closed", c.spec.Name)
	}
	if !drained {
		return ErrReloadBusy
	}
	// Swap. teardown the old process; point at the new binary; reset the
	// crash-loop bookkeeping — a deliberate reload is not a crash and must not
	// count against the burst/lifetime caps. Lazy re-dial on the next call.
	//
	// Only the binary-identity fields are mutated (all read exclusively under
	// mu, in ensureLocked/verify/dial). The invariant fields — Name, Kind,
	// Provides, Local, Manifest — are UNCHANGED (a compatible reload is the same
	// plugin+permissions, guaranteed by the caller's SameReloadSurface check) and
	// are the only ones read lock-free (handleRequest/Run/Describe), so leaving
	// them untouched keeps those reads race-free without a whole-struct write.
	c.teardownLocked()
	c.spec.BinPath = newSpec.BinPath
	c.spec.Sha256 = newSpec.Sha256
	c.spec.Resolved = newSpec.Resolved
	c.digest = ""
	c.starts = nil
	c.totalStart = 0
	c.downForGood = false
	c.downUntil = time.Time{}
	return nil
}

// pidConn is *acp.Conn (satisfying transport by promotion) plus the pid of
// the subprocess it talks to, so Client.PID can report it without the
// transport interface itself needing to grow a method every fake in the test
// suite would then have to implement. A test fake simply doesn't implement
// Pid() int, and Client.PID() reports 0 for it — the same as "not started".
type pidConn struct {
	*acp.Conn
	pid int
}

func (p pidConn) Pid() int { return p.pid }

// realDial spawns the verified, sandbox-wrapped subprocess and wires the
// JSON-RPC transport over its stdio, with stderr pumped through the redactor.
func realDial(ctx context.Context, s Spec, d Deps) (transport, func(), error) {
	cmd, cleanup, sandboxed, err := buildCommand(s, d.Sandbox)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case sandboxed:
		// A confirmed OS jail — say so positively (this is the good path for an
		// untrusted-by-default code engine).
		d.Log("plugin %s: OS-sandboxed", s.Name)
	case s.TrustFull:
		// The operator opted this engine out of sandboxing deliberately — quiet.
	case s.IsolationDefaulted:
		// A synthesized default that degraded: buildCommand already logged the
		// actionable warning at the point it knew why. Nothing to add.
	default:
		// The app-extension default for a connector/runtime with no isolation:.
		d.Log("plugin %s: running WITHOUT OS sandbox (no isolation: block) — add one for filesystem/process confinement", s.Name)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		cleanup()
		return nil, nil, err
	}
	// Redacted stderr pump (§8.1): the plugin's own logs never leak a
	// credential value into the daemon log/audit.
	go pumpStderr(stderr, s.Ref(), d)

	bounded := newBoundedReader(stdout, d.MaxMessageBytes)
	conn := pidConn{Conn: acp.NewConn(bounded, stdin, pluginHandler{onNotify: d.onNotify, onRequest: d.onRequest}), pid: cmd.Process.Pid}

	var once sync.Once
	kill := func() {
		once.Do(func() {
			_ = conn.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = cmd.Wait()
			cleanup()
		})
	}
	// Bind the process to ctx (the client's lifetime — ensureLocked strips a
	// call's cancellation) and to the connection: on either path we call
	// kill(): it is once-guarded and reaps the child via
	// cmd.Wait() (avoiding a zombie when the plugin exits on its own) and
	// releases the egress credential.
	go func() {
		select {
		case <-ctx.Done():
		case <-conn.Done():
		}
		kill()
	}()
	return conn, kill, nil
}

func pumpStderr(r io.Reader, ref string, d Deps) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		d.Log("plugin[%s]: %s", ref, d.Redact(sc.Text()))
	}
}

// pluginHandler handles peer-initiated traffic from a plugin: a SOURCE
// plugin's streamed events (one-way notifications, routed via onNotify) and a
// STEP ENGINE's host.* data-plane callbacks (requests, routed via onRequest —
// the one direction the daemon answers).
//
// Both are nil-safe, and nil is the pre-engines behavior exactly: no
// callbacks, every request answered with method-not-found.
type pluginHandler struct {
	onNotify  func(method string, params json.RawMessage)
	onRequest func(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError)
}

func (h pluginHandler) HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	if h.onRequest == nil {
		return nil, acp.NewRPCError(acp.CodeMethodNotFound, "daemon exposes no plugin callbacks")
	}
	return h.onRequest(ctx, method, params)
}
func (h pluginHandler) HandleNotification(_ context.Context, method string, params json.RawMessage) {
	if h.onNotify != nil {
		h.onNotify(method, params)
	}
}

// inProcessDial serves h over an in-memory pipe and returns the daemon's end
// of it: the same acp.Conn, the same handler routing (notifications, host
// callbacks) as a subprocess.
func inProcessDial(h sdk.Handler, d Deps) (transport, func()) {
	toPlugin, fromDaemon := io.Pipe()
	toDaemon, fromPlugin := io.Pipe()
	go func() {
		_ = sdk.ServeConn(toPlugin, fromPlugin, h)
		_ = fromPlugin.Close()
	}()
	conn := acp.NewConn(toDaemon, fromDaemon, pluginHandler{onNotify: d.onNotify, onRequest: d.onRequest})
	kill := func() {
		_ = fromDaemon.Close()
		_ = toDaemon.Close()
	}
	return conn, kill
}

// StagingDir is instance's staging directory (plugin-contract.md Q7), made
// on first use: <StagingRoot>/<plugin>/<instance>, private to the daemon's
// user. "" when the host gives none.
func (c *Client) StagingDir(instance string) (string, error) {
	root := c.deps.Sandbox.StagingRoot
	if root == "" || instance == "" || strings.ContainsAny(instance, `/\`) || strings.HasPrefix(instance, ".") {
		return "", nil
	}
	dir := filepath.Join(root, c.spec.Name, instance)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}
