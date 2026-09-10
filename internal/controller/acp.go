package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/sandbox"
)

// acpController drives an ACP agent (gemini, codex-via-adapter, opencode-over-acp,
// …) over JSON-RPC 2.0 on stdio, using internal/acp as the transport. It maps the
// ACP lifecycle onto the Controller/Session contract: initialize negotiates
// capabilities, session/new opens a session rooted at the conductor-provisioned
// worktree, and session/prompt runs a turn whose streamed session/update
// notifications become controller Updates. An agent's session/request_permission
// callbacks are routed through the permission policy (auto-approve until M2 wires a
// HandoffChannel).
//
// session_model is negotiated: an agent that advertises loadSession is resumable
// (a session survives by id), otherwise native. Transport is always acp.
type acpController struct {
	runner   runnerMemo
	name     string
	command  []string // launch argv for the agent subprocess (best-effort default; overridable via `command:`)
	prov     Provisioner
	dial     acpDialer // injectable connection factory; nil → spawn the subprocess
	host     string    // configured `host:`; "" = local (see resolveHost/prepareLaunch)
	iso      *config.IsolationConfig
	scrubEnv bool // inherit only a minimal env allowlist (external runtime plugins, #54)

	mu    sync.Mutex
	model SessionModel // cached negotiated model (native until an Initialize proves loadSession)
	// cwds remembers each session's worktree, so session/load can re-root
	// the agent where the session was working. ResumeSession is handed
	// only an id — the Controller interface carries no Spec there.
	cwds map[string]string
}

func (c *acpController) rememberCwd(id, cwd string) {
	if id == "" || cwd == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cwds == nil {
		c.cwds = map[string]string{}
	}
	c.cwds[id] = cwd
}

func (c *acpController) cwdFor(id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cwds[id]
}

// acpDialer opens a connection to an ACP agent and returns a Client wired to the
// given delegate plus a cleanup that tears the connection (and any subprocess)
// down. Production spawns the agent subprocess; tests wire the Client to an
// in-process fake agent over pipes.
type acpDialer func(ctx context.Context, cwd string, env []string, delegate acp.ClientDelegate) (*acp.Client, func() error, error)

// newACPController builds an ACP controller for a config entry. The launch command
// is the explicit `command:` if set, else a best-effort default derived from the
// agent/tool name (see acpCommand). session_model starts native and is upgraded to
// resumable if the agent negotiates loadSession.
func newACPController(name string, cc config.ControllerConfig, prov Provisioner) *acpController {
	model := SessionModel(cc.SessionModel)
	if !model.Valid() {
		model = ModelNative
	}
	return &acpController{
		name:     name,
		command:  acpCommand(cc),
		prov:     prov,
		host:     cc.Host,
		iso:      cc.Isolation,
		scrubEnv: cc.ScrubEnv,
		model:    model,
	}
}

func (c *acpController) Name() string           { return c.name }
func (c *acpController) ConfiguredHost() string { return c.host }
func (c *acpController) Transport() Transport   { return TransportACP }

// SessionPersistent: an ACP session is addressable by id (session/load for
// loadSession-capable agents) — the session-affinity gate. An agent that
// can't actually resume surfaces as a follow-up failure, which affinity
// degrades to a fresh spawn.
func (c *acpController) SessionPersistent() bool { return true }

func (c *acpController) Model() SessionModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model
}

// Initialize opens a throwaway connection, performs the ACP initialize handshake,
// and reports the negotiated capabilities. loadSession → a resumable session model
// and CheckoutPR is always true (conductor supplies the worktree as the session
// cwd). The connection is closed before returning; NewSession opens its own.
func (c *acpController) Initialize(ctx context.Context) (Capabilities, error) {
	client, cleanup, err := c.connect(ctx, "", nil, acp.DelegateFuncs{}, "", resumeOpts(c.iso, false))
	if err != nil {
		return Capabilities{SessionModel: c.Model(), Transport: TransportACP, CheckoutPR: true}, err
	}
	defer cleanup()

	res, err := client.Initialize(ctx, acp.DefaultInitializeParams(acp.Implementation{
		Name: "conductor", Title: "Conductor",
	}))
	if err != nil {
		return Capabilities{SessionModel: c.Model(), Transport: TransportACP, CheckoutPR: true}, err
	}

	model := ModelNative
	if res.AgentCapabilities.LoadSession {
		model = ModelResumable
	}
	c.mu.Lock()
	c.model = model
	c.mu.Unlock()

	return Capabilities{
		SessionModel:       model,
		Transport:          TransportACP,
		CheckoutPR:         true,
		InteractiveHandoff: true, // permission/input requests can reach a human via the Handler (M2)
		SendFollowup:       true, // a live session accepts another session/prompt turn
		Remote:             false,
	}, nil
}

// Runner returns the engine-facing dispatch surface for this controller.
func (c *acpController) Runner() (Runner, error) {
	// One runner per controller: its live-agent tracking is the state
	// the engine's duplicate-dispatch gate reads (see runnerMemo).
	return c.runner.get(func() Runner { return newControllerRunner(c, c.prov, nil) }), nil
}

// NewSession launches an ACP agent in the conductor-provisioned worktree, opens a
// session, and starts its first prompt turn (streamed asynchronously so the engine
// can wait on completion). h receives the agent's permission requests; nil applies
// the auto-approve policy.
func (c *acpController) NewSession(ctx context.Context, spec Spec, h Handler) (Session, error) {
	env, err := dispatch.AgentEnv(spec.Request)
	if err != nil {
		return nil, fmt.Errorf("acp: build env: %w", err)
	}
	prompt, err := dispatch.RenderPrompt(spec.Request)
	if err != nil {
		return nil, fmt.Errorf("acp: render prompt: %w", err)
	}

	// The session outlives the dispatch call (a background agent), so it runs under
	// its own context, cancelled only by Close — not by the request ctx returning.
	sctx, scancel := context.WithCancel(context.Background())
	del := &acpDelegate{handler: h}
	client, cleanup, err := c.connect(sctx, spec.Cwd, env, del, spec.Request.Step.Host, launchOptsFor(c.iso, spec.Request))
	if err != nil {
		scancel()
		return nil, err
	}

	if _, err := client.Initialize(sctx, acp.DefaultInitializeParams(acp.Implementation{
		Name: "conductor", Title: "Conductor",
	})); err != nil {
		cleanup()
		scancel()
		return nil, fmt.Errorf("acp: initialize: %w", err)
	}
	res, err := client.NewSession(sctx, acp.NewSessionParams{
		Cwd:        spec.Cwd,
		McpServers: c.memoryServers(spec),
		// The RESOLVED model. Empty is a bare launch — the field is
		// omitted and the agent picks its own. Dropping it here meant an
		// exact `model:` pin silently ran whatever the tool defaulted to.
		Model: spec.Request.Model,
	})
	if err != nil {
		cleanup()
		scancel()
		return nil, fmt.Errorf("acp: session/new: %w", err)
	}

	// Remember where this session lives, so session/load can re-root the
	// agent in the same worktree on resume.
	c.rememberCwd(res.SessionID, spec.Cwd)
	s := &acpSession{
		id:      res.SessionID,
		client:  client,
		del:     del,
		cleanup: cleanup,
		cancel:  scancel,
		ctx:     sctx,
	}
	s.startTurn(prompt)
	return s, nil
}

// memoryServers builds the MCP server list for a new ACP session: the shared
// conductor tool server (memory + run_step + the skill's verb tools and
// broker; see skillwire.go), with this dispatch's provenance baked into its
// flags and the one-shot skill claim in its env. Local sessions only: the
// daemon's socket doesn't exist on a remote `host:` box, so remote sessions
// fall back to the output contract like any runtime without live tools.
func (c *acpController) memoryServers(spec Spec) []acp.McpServer {
	ts := dispatch.BuildToolServer(spec.Request, c.host)
	if ts == nil {
		return nil
	}
	var env []acp.EnvVariable
	for k, v := range ts.Env {
		env = append(env, acp.EnvVariable{Name: k, Value: v})
	}
	return []acp.McpServer{{Name: "conductor-memory", Command: ts.Command, Args: ts.Args, Env: env}}
}

// ResumeSession re-attaches to a prior session by id over a fresh connection. Only
// meaningful for a loadSession-capable (resumable) agent; the bound session accepts
// follow-up prompt turns.
func (c *acpController) ResumeSession(ctx context.Context, id string, agentAuthored bool, h Handler) (Session, error) {
	sctx, scancel := context.WithCancel(context.Background())
	del := &acpDelegate{handler: h}
	client, cleanup, err := c.connect(sctx, "", nil, del, "", resumeOpts(c.iso, agentAuthored))
	if err != nil {
		scancel()
		return nil, err
	}
	init, err := client.Initialize(sctx, acp.DefaultInitializeParams(acp.Implementation{
		Name: "conductor",
	}))
	if err != nil {
		cleanup()
		scancel()
		return nil, fmt.Errorf("acp: initialize: %w", err)
	}
	// THE resume. Without it this function spawned a fresh agent, ran
	// initialize, and handed back a session id the agent had never been
	// told about — so every "resumed" turn started from an empty context
	// while conductor reported the session as continuous. An agent that
	// does not advertise loadSession cannot be resumed at all, and saying
	// so beats returning a session that silently forgets.
	if !init.AgentCapabilities.LoadSession {
		cleanup()
		scancel()
		return nil, fmt.Errorf("acp: %s does not support session/load, so a prior session cannot be resumed: %w", c.name, ErrNoFollowup)
	}
	if err := client.LoadSession(sctx, acp.LoadSessionParams{
		SessionID:  id,
		Cwd:        c.cwdFor(id),
		McpServers: []acp.McpServer{},
	}); err != nil {
		cleanup()
		scancel()
		return nil, fmt.Errorf("acp: session/load %s: %w", id, err)
	}
	return &acpSession{id: id, client: client, del: del, cleanup: cleanup, cancel: scancel, ctx: sctx}, nil
}

// connect opens a connection via the injected dialer, or spawns the subprocess when
// none is set. profileHost is the dispatched profile's host override (wins over
// the controller's own configured host — see resolveHost); "" when no profile is
// in reach (Initialize/ResumeSession).
func (c *acpController) connect(ctx context.Context, cwd string, env []string, del acp.ClientDelegate, profileHost string, opt launchOpts) (*acp.Client, func() error, error) {
	if c.dial != nil {
		return c.dial(ctx, cwd, env, del)
	}
	return spawnACP(ctx, c.command, cwd, env, del, resolveHost(c.host, profileHost), opt, c.scrubEnv)
}

// spawnACP starts the agent subprocess wired for ACP over its stdio, with the
// conductor worktree as cwd and the acts-as-user env applied. The process lifetime
// is owned by the returned cleanup (session-scoped), not by ctx — a background
// agent must survive the dispatch call returning. host != "" wraps the launch for
// remote execution via prepareLaunch (see its doc for what changes locally in
// that case: no cwd, no local env — both travel inside the wrapped command).
func spawnACP(_ context.Context, command []string, cwd string, env []string, del acp.ClientDelegate, host string, opt launchOpts, scrubEnv bool) (*acp.Client, func() error, error) {
	if len(command) == 0 {
		return nil, nil, errors.New("acp: no launch command configured")
	}
	opt, revoke := withEgressRevoke(opt)
	argv, dir, localEnv, _, err := prepareLaunch(host, cwd, env, command, opt)
	if err != nil {
		revoke()
		return nil, nil, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	// scrubEnv (external runtime plugins, #54 §8.1): inherit only a minimal env
	// allowlist so the daemon's credential-bearing environment is not handed to
	// untrusted third-party code. Bundled ACP runtimes keep full os.Environ().
	base := os.Environ()
	if scrubEnv {
		base = sandbox.MinimalEnv()
	}
	cmd.Env = append(base, localEnv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		revoke()
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		revoke()
		return nil, nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		revoke()
		return nil, nil, fmt.Errorf("acp: start %s: %w", argv[0], err)
	}
	client := acp.NewClient(stdout, stdin, del)
	// This ACP agent is a background session owned by cleanup, not ctx (see the
	// doc above) — so the egress credential is retired in cleanup, at session
	// end, rather than when the dispatch call returns (#36 iso-review round 2,
	// item 4).
	cleanup := func() error {
		client.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		revoke()
		return nil
	}
	return client, cleanup, nil
}

// acpCommand resolves the launch argv for an ACP agent: an explicit `command:`
// wins; otherwise a best-effort default per known agent (overridable — these are
// the common invocations, not a guarantee), falling back to the bare tool/agent
// name.
func acpCommand(cc config.ControllerConfig) []string {
	if len(cc.Command) > 0 {
		return cc.Command
	}
	name := cc.Agent
	if name == "" {
		name = cc.Tool
	}
	if cmd, ok := knownACPCommands[name]; ok {
		return append([]string(nil), cmd...)
	}
	if name == "" {
		return nil
	}
	return []string{name}
}

// knownACPCommands maps a registry agent name to its best-effort ACP launch argv.
// These are the documented stdio-ACP invocations at time of writing; a deployment
// pins the exact command via `command:` when an agent's flag differs.
var knownACPCommands = map[string][]string{
	"gemini":      {"gemini", "--experimental-acp"},
	"qwen":        {"qwen", "--experimental-acp"},
	"claude-code": {"claude-code-acp"},
	"codex":       {"codex-acp"},
	"goose":       {"goose", "acp"},
	"opencode":    {"opencode", "acp"},
}

// ---- session ------------------------------------------------------------------

// acpSession is one live ACP session. Turns run under the session's own context so
// the agent survives the dispatch call; the latest turn's completion is exposed via
// done for the engine's wait, and its streamed output is accumulated by the
// delegate.
type acpSession struct {
	id      string
	client  *acp.Client
	del     *acpDelegate
	cleanup func() error
	cancel  context.CancelFunc
	ctx     context.Context

	mu   sync.Mutex
	done chan struct{} // closed when the latest turn ends
}

func (s *acpSession) ID() string { return s.id }

// Prompt runs a follow-up turn and returns its update stream.
func (s *acpSession) Prompt(_ context.Context, msg Message) (<-chan Update, error) {
	return s.startTurn(msg.Text), nil
}

// startTurn issues a session/prompt turn on the session context, streaming updates
// on the returned channel and closing it (and the turn's done channel) when the
// agent ends the turn.
func (s *acpSession) startTurn(text string) <-chan Update {
	ch := make(chan Update, 64)
	done := make(chan struct{})
	s.mu.Lock()
	s.done = done
	s.mu.Unlock()
	s.del.setStream(ch)

	go func() {
		defer close(done)
		defer close(ch)
		defer s.del.setStream(nil)

		sendUpdate(ch, Update{Kind: UpdateStarted, AgentID: s.id})
		res, err := s.client.Prompt(s.ctx, acp.PromptParams{
			SessionID: s.id,
			Prompt:    []acp.ContentBlock{acp.TextBlock(text)},
		})
		u := Update{Kind: UpdateDone, AgentID: s.id, Output: s.del.output(), Err: err}
		if err == nil && res != nil && res.StopReason == acp.StopReasonRefusal {
			u.Err = errors.New("acp: agent refused the turn")
		}
		sendUpdate(ch, u)
	}()
	return ch
}

// Wait blocks until the latest turn ends (or ctx/timeout fires).
func (s *acpSession) Wait(ctx context.Context, timeout time.Duration) {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done == nil {
		return
	}
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		select {
		case <-done:
		case <-ctx.Done():
		case <-t.C:
		}
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Cancel interrupts the in-flight turn (ACP session/cancel).
func (s *acpSession) Cancel(ctx context.Context) error {
	return s.client.Cancel(ctx, s.id)
}

// Close tears the session's connection (and subprocess) down.
func (s *acpSession) Close(context.Context) error {
	s.cancel()
	if s.cleanup != nil {
		return s.cleanup()
	}
	return nil
}

// ---- delegate -----------------------------------------------------------------

// acpDelegate bridges ACP agent-initiated traffic onto the controller: streamed
// session/update chunks become controller Updates and accumulate as the turn's
// output, and session/request_permission callbacks route through the permission
// policy. It is shared across a session's turns; setStream swaps the active turn's
// channel.
type acpDelegate struct {
	handler Handler

	mu     sync.Mutex
	stream chan<- Update
	buf    strings.Builder
}

func (d *acpDelegate) setStream(ch chan<- Update) {
	d.mu.Lock()
	d.stream = ch
	if ch != nil {
		d.buf.Reset() // a fresh turn starts a fresh output buffer
	}
	d.mu.Unlock()
}

func (d *acpDelegate) output() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.String()
}

// SessionUpdate maps an ACP update to a controller Update. Agent message chunks
// accumulate as the turn's text output; every update is forwarded (best-effort) to
// the active turn stream. It must not block the ACP read loop.
func (d *acpDelegate) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	var text string
	if mc := n.Update.MessageChunk; mc != nil && n.Update.Kind == acp.UpdateAgentMessageChunk {
		text = mc.Content.Text
	}
	d.mu.Lock()
	if text != "" {
		d.buf.WriteString(text)
	}
	ch := d.stream
	d.mu.Unlock()
	if ch != nil && text != "" {
		sendUpdate(ch, Update{Kind: UpdateOutput, Output: text})
	}
	return nil
}

// RequestPermission routes an agent's permission request through conductor's policy
// (the Handler when wired, else auto-approve) and maps the decision back to an ACP
// outcome.
func (d *acpDelegate) RequestPermission(ctx context.Context, p acp.RequestPermissionParams) (acp.RequestPermissionOutcome, error) {
	// Present the human-readable option names to the policy, then map the chosen
	// name back to its ACP option id.
	names := make([]string, len(p.Options))
	byName := make(map[string]string, len(p.Options))
	for i, o := range p.Options {
		names[i] = o.Name
		byName[o.Name] = o.OptionID
	}
	outcome, err := resolvePermission(ctx, d.handler, PermissionRequest{
		SessionID: p.SessionID,
		Tool:      p.ToolCall.Title,
		Detail:    p.ToolCall.ToolCallID,
		Options:   names,
	})
	if err != nil {
		return acp.RequestPermissionOutcome{}, err
	}
	if !outcome.Approved {
		return acp.CancelledOutcome(), nil
	}
	if id, ok := byName[outcome.Selected]; ok {
		return acp.SelectedOutcome(id), nil
	}
	// Approved but no explicit selection matched: prefer an allow_* option id.
	for _, o := range p.Options {
		if strings.HasPrefix(o.Kind, "allow") {
			return acp.SelectedOutcome(o.OptionID), nil
		}
	}
	if len(p.Options) > 0 {
		return acp.SelectedOutcome(p.Options[0].OptionID), nil
	}
	return acp.CancelledOutcome(), nil
}

// sendUpdate delivers u to ch without blocking; a full buffer drops the update
// rather than stalling the ACP read loop (output is still preserved in the
// delegate's buffer, which the terminal Update carries).
func sendUpdate(ch chan<- Update, u Update) {
	select {
	case ch <- u:
	default:
	}
}

// CwdOf and RememberCwd expose the session→worktree map to the broker, which
// persists it at bind time and hands it back before a post-restart resume —
// the in-process map is empty after a restart, and resuming with no cwd would
// root the agent at the daemon's own directory instead of the worktree.
func (c *acpController) CwdOf(sessionID string) string { return c.cwdFor(sessionID) }

func (c *acpController) RememberCwd(sessionID, cwd string) { c.rememberCwd(sessionID, cwd) }
