package plugin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/acp"
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
}

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
	return &Client{spec: spec, deps: deps}
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

	// verify-before-execute, every (re)start
	digest, err := verify(c.spec)
	if err != nil {
		return err
	}
	if c.spec.Sha256 == "" && c.spec.AllowUnverified {
		c.deps.Log("plugin %s: WARNING running UNVERIFIED (no sha256 pin); on-disk digest %s", c.spec.Name, digest)
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
	// Identity anti-forgery (§8.2): the type a connector plugin serves is the
	// operator's configured name, not whatever the plugin claims.
	if c.spec.Kind == KindConnector && decl.Type != c.spec.Provides {
		return nil, fmt.Errorf("plugin %s: describe claims type %q but is configured to provide %q — refusing (identity forgery)", c.spec.Name, decl.Type, c.spec.Provides)
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
	c.mu.Lock()
	if err := c.ensureLocked(ctx); err != nil {
		c.mu.Unlock()
		return err
	}
	conn := c.conn
	c.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, c.deps.CallTimeout)
	defer cancel()
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
	conn := acp.NewConn(bounded, stdin, noopHandler{})

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

// noopHandler ignores peer-initiated traffic — a connector plugin never calls
// back into the daemon (runtime plugins use the ACP controller's own delegate).
type noopHandler struct{}

func (noopHandler) HandleRequest(context.Context, string, json.RawMessage) (any, *acp.RPCError) {
	return nil, acp.NewRPCError(acp.CodeMethodNotFound, "daemon exposes no plugin callbacks")
}
func (noopHandler) HandleNotification(context.Context, string, json.RawMessage) {}
