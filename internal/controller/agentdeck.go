package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
)

// agentDeckController drives agent-deck through its CLI (launch / list --json /
// session send / session show --json / remove). agent-deck is itself an
// orchestrator that owns the agent's lifecycle, so session_model is native — the
// controller launches the agent in the conductor-provisioned worktree, tags it with
// the PR identity (title + group), and polls agent-deck for liveness. Identity env
// is applied to the exec'd process.
type agentDeckController struct {
	name string
	bin  string   // agent-deck binary (default "agent-deck")
	args []string // extra launch args from `command:` (after the bin)
	prov Provisioner
	run  deckRunner // injectable exec; nil → real subprocess
	host string     // configured `host:`; "" = local (see resolveHost/prepareLaunch)

	pollInterval time.Duration
}

// deckRunner runs an agent-deck subcommand in dir with env applied and returns its
// combined output. Injected in tests to stub the CLI.
type deckRunner func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error)

// newAgentDeckController builds an agent-deck controller. The binary is the first
// element of an explicit `command:` (with the rest passed through as extra launch
// args), else `tool:`, else "agent-deck" — except an explicit `bin:` always wins
// over all three, since it's the dedicated field for pinning the runtime binary.
func newAgentDeckController(name string, cc config.ControllerConfig, prov Provisioner) *agentDeckController {
	bin := "agent-deck"
	var extra []string
	switch {
	case len(cc.Command) > 0:
		bin = cc.Command[0]
		extra = cc.Command[1:]
	case cc.Tool != "":
		bin = cc.Tool
	}
	if cc.Bin != "" {
		bin = cc.Bin
	}
	return &agentDeckController{
		name:         name,
		bin:          bin,
		args:         extra,
		prov:         prov,
		host:         cc.Host,
		pollInterval: 2 * time.Second,
	}
}

func (c *agentDeckController) Name() string         { return c.name }
func (c *agentDeckController) Model() SessionModel  { return ModelNative }
func (c *agentDeckController) Transport() Transport { return TransportNative }

// Initialize reports agent-deck's capabilities: it owns the session (native),
// accepts the conductor worktree, and takes follow-up sends. Permission requests
// surface in agent-deck's own UI, not through a conductor callback.
func (c *agentDeckController) Initialize(context.Context) (Capabilities, error) {
	return Capabilities{
		SessionModel:       ModelNative,
		Transport:          TransportNative,
		CheckoutPR:         true,
		InteractiveHandoff: true, // agent-deck has its own interactive surface
		SendFollowup:       true,
		Remote:             false,
	}, nil
}

func (c *agentDeckController) Runner() (Runner, error) {
	return newControllerRunner(c, c.prov, nil), nil
}

// NewSession launches an agent-deck session in the worktree, tagged with the PR
// identity, and returns a handle bound to its session id.
func (c *agentDeckController) NewSession(ctx context.Context, spec Spec, _ Handler) (Session, error) {
	env, err := dispatch.AgentEnv(spec.Request)
	if err != nil {
		return nil, fmt.Errorf("agent-deck: build env: %w", err)
	}
	prompt, err := dispatch.RenderPrompt(spec.Request)
	if err != nil {
		return nil, fmt.Errorf("agent-deck: render prompt: %w", err)
	}
	title := deckTitle(spec.Request)
	group := deckGroup(spec.Request)
	host := resolveHost(c.host, spec.Request.Profile.Host)

	args := append([]string{"launch"}, c.args...)
	args = append(args, "--title", title, "--group", group, "--prompt", prompt)
	if p := spec.Request.Profile; p.Provider != "" {
		args = append(args, "--provider", p.Provider)
	}
	if p := spec.Request.Profile; p.Model != "" {
		args = append(args, "--model", p.Model)
	}

	out, err := c.exec(ctx, host, spec.Cwd, env, args...)
	if err != nil {
		return nil, fmt.Errorf("agent-deck: launch: %w", err)
	}
	id := parseDeckID(out)
	if id == "" {
		// Fall back to resolving the just-launched session by its unique title.
		id = c.findByTitle(ctx, host, env, title)
	}
	if id == "" {
		return nil, fmt.Errorf("agent-deck: launch returned no session id (%s)", strings.TrimSpace(string(out)))
	}
	return &agentDeckSession{id: id, c: c, env: env, host: host}, nil
}

// ResumeSession binds an existing agent-deck session by id (native lifecycle).
func (c *agentDeckController) ResumeSession(_ context.Context, id string, _ Handler) (Session, error) {
	return &agentDeckSession{id: id, c: c, host: c.host}, nil
}

// exec runs one agent-deck subcommand (list/launch/session .../remove). host ==
// "" runs it locally (byte-for-byte the pre-remote-support behavior: dir/env
// applied to the local process). host != "" wraps the bin+args argv via
// prepareLaunch and runs the resulting ssh command instead — see its doc for
// what changes locally in that case (no dir, no env). deckRunner's signature
// (name string, args ...string) is unchanged either way: wrapped[0] is passed
// as name and wrapped[1:] as args, so a host=="" call is indistinguishable
// from before this feature existed.
func (c *agentDeckController) exec(ctx context.Context, host, dir string, env []string, args ...string) ([]byte, error) {
	wrapped, localDir, remote, err := prepareLaunch(host, dir, env, append([]string{c.bin}, args...))
	if err != nil {
		return nil, err
	}
	localEnv := env
	if remote {
		localEnv = nil
	}
	if c.run != nil {
		return c.run(ctx, localDir, localEnv, wrapped[0], wrapped[1:]...)
	}
	cmd := exec.CommandContext(ctx, wrapped[0], wrapped[1:]...)
	if localDir != "" {
		cmd.Dir = localDir
	}
	cmd.Env = append(os.Environ(), localEnv...)
	return cmd.CombinedOutput()
}

// findByTitle returns the id of the session whose title matches (via list --json),
// or "".
func (c *agentDeckController) findByTitle(ctx context.Context, host string, env []string, title string) string {
	out, err := c.exec(ctx, host, "", env, "list", "--json")
	if err != nil {
		return ""
	}
	var sessions []deckSession
	if json.Unmarshal(out, &sessions) != nil {
		return ""
	}
	for _, s := range sessions {
		if s.Title == title && s.id() != "" {
			return s.id()
		}
	}
	return ""
}

// deckSession is the subset of agent-deck's `list`/`show --json` we read.
type deckSession struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Title     string `json:"title"`
	Group     string `json:"group"`
	Status    string `json:"status"`
}

func (s deckSession) id() string {
	if s.ID != "" {
		return s.ID
	}
	return s.SessionID
}

// parseDeckID best-effort extracts a session id from `agent-deck launch` output
// (JSON object with an id field, else the first token of a plain-text line).
func parseDeckID(out []byte) string {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal([]byte(trimmed), &obj) == nil {
		for _, k := range []string{"id", "sessionId", "session_id", "sessionID"} {
			if v, ok := obj[k].(string); ok && v != "" {
				return v
			}
		}
	}
	return ""
}

// deckTitle / deckGroup encode the PR identity so a session is findable and grouped
// per PR in agent-deck's board.
func deckTitle(req dispatch.Request) string {
	return fmt.Sprintf("conductor: %s %s #%s", req.Trigger.Target.Repo, req.Trigger.Kind, req.Trigger.Key())
}

func deckGroup(req dispatch.Request) string {
	if req.Trigger.Target.Repo != "" {
		return req.Trigger.Target.Repo
	}
	return "conductor"
}

// ---- session ------------------------------------------------------------------

// agentDeckSession is a handle to an agent-deck session; agent-deck owns its
// lifecycle, so the handle sends follow-ups, polls status for liveness, and removes
// the session on close.
type agentDeckSession struct {
	id   string
	c    *agentDeckController
	env  []string
	host string // resolved at creation (controller/profile host, or ""; see resolveHost)

	mu   sync.Mutex
	done chan struct{}
}

func (s *agentDeckSession) ID() string { return s.id }

// Prompt delivers a follow-up turn (`session send`) and returns a terminal update.
func (s *agentDeckSession) Prompt(ctx context.Context, msg Message) (<-chan Update, error) {
	ch := make(chan Update, 1)
	_, err := s.c.exec(ctx, s.host, "", s.env, "session", "send", s.id, msg.Text)
	ch <- Update{Kind: UpdateDone, AgentID: s.id, Err: err}
	close(ch)
	return ch, err
}

// Wait polls agent-deck until the session goes idle/done (or ctx/timeout fires) —
// the liveness signal for a native session whose work runs inside agent-deck.
func (s *agentDeckSession) Wait(ctx context.Context, timeout time.Duration) {
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	interval := s.c.pollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		if s.idle(ctx) {
			return
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

// idle reports whether the session has reached a terminal/idle status per
// `session show --json`. An unreadable status is treated as idle so a broken poll
// can't wedge the wait forever.
func (s *agentDeckSession) idle(ctx context.Context) bool {
	out, err := s.c.exec(ctx, s.host, "", s.env, "session", "show", s.id, "--json")
	if err != nil {
		return true
	}
	var ds deckSession
	if json.Unmarshal(out, &ds) != nil {
		return true
	}
	switch strings.ToLower(ds.Status) {
	case "", "idle", "done", "completed", "finished", "ready", "stopped", "exited":
		return true
	}
	return false
}

// Cancel is best-effort: agent-deck owns interruption via its own UI.
func (s *agentDeckSession) Cancel(context.Context) error { return nil }

// Close removes the session from agent-deck (`remove`).
func (s *agentDeckSession) Close(ctx context.Context) error {
	_, err := s.c.exec(ctx, s.host, "", s.env, "remove", s.id)
	return err
}
