package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
)

// cliController is the bare-runner fallback for tools with no ACP or HTTP server: it
// runs a per-tool command recipe (claude-code `-p`, codex `exec`, …) as a direct
// subprocess in the conductor-provisioned worktree, with the acts-as-user identity
// env applied. A launched process is tracked in a state table so the engine can
// wait on and reap it (liveness for a runtime that has no session server).
//
// session_model follows the recipe: a tool that can continue a prior run (claude-
// code `--resume`) is resumable; otherwise each turn is a fresh process (oneshot).
type cliController struct {
	name   string
	recipe cliRecipe
	prov   Provisioner
	launch cliLauncher // injectable; nil → real subprocess

	seq  atomic.Int64
	mu   sync.Mutex
	live map[string]*cliSession // agent id → running session (process/state table)
}

// cliProc is a launched process the controller waits on and can kill.
type cliProc interface {
	// Wait blocks until the process exits, returning its captured output.
	Wait() (output string, err error)
	// Kill terminates the process.
	Kill() error
}

// cliLauncher starts argv in dir with env applied and returns a handle to the
// running process. Injected in tests to fake the tool.
type cliLauncher func(ctx context.Context, dir string, env []string, argv []string) (cliProc, error)

// newCLIController builds a cli fallback controller from its config. The recipe is
// chosen from the tool/agent name (claude-code, codex), or a generic recipe over an
// explicit `command:`.
func newCLIController(name string, cc config.ControllerConfig, prov Provisioner) *cliController {
	return &cliController{
		name:   name,
		recipe: cliRecipeFor(cc),
		prov:   prov,
		live:   map[string]*cliSession{},
	}
}

func (c *cliController) Name() string         { return c.name }
func (c *cliController) Model() SessionModel  { return c.recipe.model }
func (c *cliController) Transport() Transport { return TransportCLI }

// Initialize reports the recipe's capabilities: it accepts the conductor worktree,
// its session model is the recipe's, and it takes a follow-up only when the recipe
// can resume.
func (c *cliController) Initialize(context.Context) (Capabilities, error) {
	return Capabilities{
		SessionModel: c.recipe.model,
		Transport:    TransportCLI,
		CheckoutPR:   true,
		SendFollowup: c.recipe.resume != nil,
	}, nil
}

func (c *cliController) Runner() (Runner, error) {
	return newControllerRunner(c, c.prov, nil), nil
}

// NewSession launches the tool for its first (and, for a oneshot recipe, only) turn
// in the worktree, registers the process in the state table, and returns a handle
// bound to a conductor-assigned id.
func (c *cliController) NewSession(ctx context.Context, spec Spec, _ Handler) (Session, error) {
	env, err := dispatch.AgentEnv(spec.Request)
	if err != nil {
		return nil, fmt.Errorf("cli: build env: %w", err)
	}
	prompt, err := dispatch.RenderPrompt(spec.Request)
	if err != nil {
		return nil, fmt.Errorf("cli: render prompt: %w", err)
	}

	id := c.recipe.tool + "-" + strconv.FormatInt(c.seq.Add(1), 10)
	sctx, scancel := context.WithCancel(context.Background())
	proc, err := c.start(sctx, spec.Cwd, env, c.recipe.launch(prompt))
	if err != nil {
		scancel()
		return nil, fmt.Errorf("cli: launch %s: %w", c.recipe.tool, err)
	}

	s := &cliSession{
		id:     id,
		c:      c,
		cwd:    spec.Cwd,
		env:    env,
		cancel: scancel,
		ctx:    sctx,
	}
	c.mu.Lock()
	c.live[id] = s
	c.mu.Unlock()

	s.run(proc)
	return s, nil
}

// ResumeSession re-binds a session id for a resumable recipe. The bound handle
// continues the tool session (--resume) on the next Prompt; a oneshot recipe cannot
// resume.
func (c *cliController) ResumeSession(_ context.Context, id string, _ Handler) (Session, error) {
	if c.recipe.resume == nil {
		return nil, ErrNoFollowup
	}
	sctx, scancel := context.WithCancel(context.Background())
	return &cliSession{id: id, c: c, toolID: id, cancel: scancel, ctx: sctx}, nil
}

func (c *cliController) start(ctx context.Context, dir string, env, argv []string) (cliProc, error) {
	if c.launch != nil {
		return c.launch(ctx, dir, env, argv)
	}
	return startCLIProc(ctx, dir, env, argv)
}

func (c *cliController) forget(id string) {
	c.mu.Lock()
	delete(c.live, id)
	c.mu.Unlock()
}

// startCLIProc starts argv as a subprocess capturing its combined output. The
// process is session-scoped (ctx cancels it), matching the other controllers'
// background-agent lifetime.
func startCLIProc(ctx context.Context, dir string, env, argv []string) (cliProc, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), env...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProc{cmd: cmd, buf: &buf}, nil
}

type execProc struct {
	cmd *exec.Cmd
	buf *bytes.Buffer
}

func (p *execProc) Wait() (string, error) {
	err := p.cmd.Wait()
	return p.buf.String(), err
}

func (p *execProc) Kill() error {
	if p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

// ---- recipes ------------------------------------------------------------------

// cliRecipe is a per-tool command recipe: how to launch a fresh turn, how to resume
// a prior one (nil → oneshot), how to parse the tool's own session id from its
// output (for resume), and the session model to report.
type cliRecipe struct {
	tool    string
	launch  func(prompt string) []string
	resume  func(toolSessionID, prompt string) []string
	parseID func(output string) string
	model   SessionModel
}

// cliRecipeFor selects a recipe from the config. An explicit `command:` yields a
// generic oneshot recipe (the prompt is appended as the final argument); otherwise
// the tool/agent name selects a built-in recipe, defaulting to a bare oneshot run
// of the named binary.
func cliRecipeFor(cc config.ControllerConfig) cliRecipe {
	tool := cc.Tool
	if tool == "" {
		tool = cc.Agent
	}
	if len(cc.Command) > 0 {
		base := append([]string(nil), cc.Command...)
		return cliRecipe{
			tool:   firstNonEmpty(tool, base[0]),
			launch: func(prompt string) []string { return append(append([]string(nil), base...), prompt) },
			model:  ModelOneshot,
		}
	}
	switch tool {
	case "claude-code", "claude":
		return cliRecipe{
			tool: "claude-code",
			launch: func(prompt string) []string {
				return []string{"claude", "-p", prompt, "--output-format", "json"}
			},
			resume: func(id, prompt string) []string {
				return []string{"claude", "-p", prompt, "--resume", id, "--output-format", "json"}
			},
			parseID: parseClaudeSessionID,
			model:   ModelResumable,
		}
	case "codex":
		return cliRecipe{
			tool:   "codex",
			launch: func(prompt string) []string { return []string{"codex", "exec", prompt} },
			model:  ModelOneshot,
		}
	default:
		bin := tool
		if bin == "" {
			bin = "true" // nothing configured to run
		}
		return cliRecipe{
			tool:   firstNonEmpty(tool, bin),
			launch: func(prompt string) []string { return []string{bin, prompt} },
			model:  ModelOneshot,
		}
	}
}

// parseClaudeSessionID pulls the session id out of `claude -p --output-format json`
// output, so a follow-up can `--resume` it.
func parseClaudeSessionID(output string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &obj) != nil {
		return ""
	}
	for _, k := range []string{"session_id", "sessionId", "sessionID"} {
		if v, ok := obj[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- session ------------------------------------------------------------------

// cliSession is a handle to a launched tool run. The first turn runs in the
// background (so the engine can wait on it); a resumable recipe accepts follow-up
// turns that continue the captured tool session id.
type cliSession struct {
	id     string
	c      *cliController
	cwd    string
	env    []string
	cancel context.CancelFunc
	ctx    context.Context

	mu     sync.Mutex
	done   chan struct{}
	toolID string // the tool's own session id, captured for --resume
	out    string
}

func (s *cliSession) ID() string { return s.id }

// run drives a launched process to completion in the background, capturing its
// output and the tool's session id (for resume), then dropping it from the state
// table.
func (s *cliSession) run(proc cliProc) {
	done := make(chan struct{})
	s.mu.Lock()
	s.done = done
	s.mu.Unlock()
	go func() {
		defer close(done)
		out, _ := proc.Wait()
		s.mu.Lock()
		s.out = out
		if s.c.recipe.parseID != nil {
			if tid := s.c.recipe.parseID(out); tid != "" {
				s.toolID = tid
			}
		}
		s.mu.Unlock()
		s.c.forget(s.id)
	}()
}

// Prompt continues the session with another turn. Only a resumable recipe supports
// it; a oneshot recipe reports ErrNoFollowup.
func (s *cliSession) Prompt(_ context.Context, msg Message) (<-chan Update, error) {
	if s.c.recipe.resume == nil {
		ch := make(chan Update, 1)
		close(ch)
		return ch, ErrNoFollowup
	}
	s.mu.Lock()
	tid := s.toolID
	s.mu.Unlock()
	if tid == "" {
		tid = s.id // fall back to the conductor id if the tool's wasn't captured
	}

	ch := make(chan Update, 4)
	proc, err := s.c.start(s.ctx, s.cwd, s.env, s.c.recipe.resume(tid, msg.Text))
	if err != nil {
		ch <- Update{Kind: UpdateDone, AgentID: s.id, Err: err}
		close(ch)
		return ch, err
	}
	go func() {
		defer close(ch)
		sendUpdate(ch, Update{Kind: UpdateStarted, AgentID: s.id})
		out, werr := proc.Wait()
		sendUpdate(ch, Update{Kind: UpdateDone, AgentID: s.id, Output: out, Err: werr})
	}()
	return ch, nil
}

// Wait blocks until the launched turn exits (or ctx/timeout fires).
func (s *cliSession) Wait(ctx context.Context, timeout time.Duration) {
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

// Cancel kills the in-flight process.
func (s *cliSession) Cancel(context.Context) error {
	s.cancel()
	return nil
}

// Close cancels the process and drops the session from the state table.
func (s *cliSession) Close(context.Context) error {
	s.cancel()
	s.c.forget(s.id)
	return nil
}
