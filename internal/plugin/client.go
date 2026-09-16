package plugin

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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

// Deps carries what a Client needs from the daemon.
type Deps struct {
	Log     func(string, ...any)
	Redact  func(string) string // secrets.Resolver.Redact — scrubs the plugin's stderr
	Audit   func(map[string]any)
	Sandbox SandboxDeps

	MaxMessageBytes int
	CallTimeout     time.Duration

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
	// runs are the step-engine runs currently in flight on this plugin, keyed
	// by the capability token minted for each. It is the daemon half of the
	// run_id model: a host.* callback is answered only while the run it names
	// is in this map, which is exactly the span of the plugin.run call that
	// authorized it. Empty for every connector and runtime plugin, and empty
	// again the instant a run returns — so a plugin that keeps a token, or
	// guesses one, has nothing to present it to.
	runs map[string]RunHost
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
	c := &Client{spec: spec, runs: map[string]RunHost{}}
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
	c.mu.Lock()
	emit := c.onEvent
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
	c.mu.Lock()
	c.onEvent = emit
	c.mu.Unlock()
	// No per-call timeout: start_source is long-lived; the plugin acks quickly but
	// the stream lives for the daemon's lifetime, bounded by ctx.
	c.mu.Lock()
	if err := c.ensureLocked(ctx); err != nil {
		c.mu.Unlock()
		return err
	}
	conn := c.conn
	c.mu.Unlock()
	return conn.Call(ctx, MethodStartSource, req, &struct{}{})
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
func (c *Client) handleRequest(_ context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	kind := sdk.HostKindFor(method)
	if kind == "" {
		return nil, acp.NewRPCError(acp.CodeMethodNotFound,
			"daemon exposes no plugin callbacks other than "+MethodHostKV+"/"+MethodHostSQL+"/"+MethodHostMemory)
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

	if !c.spec.Installed() {
		return c.spec.NotInstalledError()
	}
	// verify-before-execute, every (re)start
	digest, err := verify(c.spec)
	if err != nil {
		return err
	}
	if c.spec.Local {
		// A development binary the operator pointed at directly: there is no
		// release sha to check it against (it changes on every build), so the
		// guarantee is the safe-permissions check verify() just made. Say so,
		// with the digest, so it is at least attributable in the log.
		c.deps.Log("plugin %s: local build at %s (digest %s) — no release sha to verify against", c.spec.Name, c.spec.BinPath, digest)
	}
	c.starts = append(c.starts, now)
	c.totalStart++
	conn, kill, err := c.deps.dial(ctx, c.spec, c.deps)
	if err != nil {
		return fmt.Errorf("plugin %s: launch: %w", c.spec.Name, err)
	}
	c.conn, c.kill, c.digest = conn, kill, digest
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
	var decl Decl
	if err := c.call(ctx, MethodDescribe, struct{}{}, &decl); err != nil {
		return nil, err
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
	// ABI is read HERE and only here, and only for a step engine. A connector
	// or runtime never reaches this branch, which is why adding the field
	// cannot change what any existing plugin means: its ABI is zero and
	// nobody asks.
	if decl.Kind == KindStep && decl.ABI != EngineABI {
		return nil, fmt.Errorf("plugin %s: step-engine ABI %d, daemon speaks %d — rebuild the engine against a matching SDK (the wire protocol itself is unchanged at version %d)", c.spec.Name, decl.ABI, EngineABI, ProtocolVersion)
	}
	return &decl, nil
}

// Invoke runs a verb. The connection map carries only the calling instance's
// resolved credentials (least privilege). A transport error tears the
// subprocess down so the next call restarts it (subject to backoff).
func (c *Client) Invoke(ctx context.Context, req InvokeRequest) (map[string]any, error) {
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
	if err := c.ensureLocked(ctx); err != nil {
		c.mu.Unlock()
		return err
	}
	conn := c.conn
	c.mu.Unlock()

	cctx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		cctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	err := conn.Call(cctx, method, params, result)
	if err != nil {
		// transport/timeout failure: drop the connection so the plugin is
		// restarted on the next call (never takes the daemon down).
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
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.teardownLocked()
	return nil
}

// realDial spawns the verified, sandbox-wrapped subprocess and wires the
// JSON-RPC transport over its stdio, with stderr pumped through the redactor.
func realDial(ctx context.Context, s Spec, d Deps) (transport, func(), error) {
	cmd, cleanup, sandboxed, err := buildCommand(s, d.Sandbox)
	if err != nil {
		return nil, nil, err
	}
	if !sandboxed {
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
	conn := acp.NewConn(bounded, stdin, pluginHandler{onNotify: d.onNotify, onRequest: d.onRequest})

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
	// Bind the process to ctx: daemon shutdown (ctx cancel) kills the plugin.
	// On either path we call kill(): it is once-guarded and reaps the child via
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
