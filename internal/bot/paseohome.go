package bot

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/inbound"
)

// PaseoDispatcher is the slice of *dispatch.Dispatcher the home launcher
// uses — the existing paseo launch path (labels, ownership ledger, skill
// session), not a second one.
type PaseoDispatcher interface {
	Dispatch(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error)
	CreateLocalWorkspace(ctx context.Context, path, title string) (string, error)
	AgentStatus(ctx context.Context, id string) (string, error)
	Send(ctx context.Context, id, prompt string) error
	SendImages(ctx context.Context, id, prompt string, images []string) error
	Archive(ctx context.Context, agentID string) error
	ReleaseWorkspace(ctx context.Context, id string) error
}

var _ PaseoDispatcher = (*dispatch.Dispatcher)(nil)

// PaseoHome launches home sessions on paseo.
//
// Without conductor's agent sandbox a home session is an ordinary paseo agent
// running as the operator. With it (Sandbox active), the launch names the
// sandboxed paseo provider entry for its base provider (`conductor paseo
// setup`: claude → claude-sandboxed), and the session's cwd — unique per
// session — is what the sandbox resolves to the bot's identity
// (Service.SessionForCwd).
type PaseoHome struct {
	Cfg func() *config.Config
	// For resolves the paseo dispatcher of a runtime name ("" = default).
	For func(runtime string) (PaseoDispatcher, error)
	// Hold / Unhold keep a home agent out of the reaper's reach while live.
	Hold, Unhold func(agentID string)
	// Revoke kills a retired session's skill token.
	Revoke func(dispatchID string)
	// Credentials resolves the acts-as-you env for the session (optional).
	Credentials func(ctx context.Context, t core.Trigger) (dispatch.Credentials, error)
	// Resolve picks the home step's model and provider through the engine's
	// fleet ladder (Engine.ResolveModel). nil: an exact pin passes through,
	// anything else bare-launches.
	Resolve func(ctx context.Context, step config.Step) (model, runtime, provider string)
	// BeforeLaunch, when set, prepares a session's cwd before its agent
	// starts (e.g. writing a .claude/settings.json that confines it). An
	// error aborts the launch before anything is created.
	BeforeLaunch func(ctx context.Context, spec HomeSpec) error
	// Sandbox, while active, launches the home session on the sandboxed
	// paseo provider entry (`conductor paseo setup`) for its base provider.
	Sandbox Sandbox

	mu       sync.Mutex
	provider map[string]string // bot → the provider its live home launched on
}

// ImageProvider reports whether a paseo provider takes images with a
// message: Claude's do (claude, and its sandboxed entry).
func ImageProvider(provider string) bool {
	return provider == "claude" || strings.HasPrefix(provider, "claude-")
}

// AcceptsImages implements HomeLauncher: whether the bot's home session
// runs on a provider that takes images.
func (h *PaseoHome) AcceptsImages(ctx context.Context, bot string) bool {
	h.mu.Lock()
	p, ok := h.provider[bot]
	h.mu.Unlock()
	if !ok && h.Resolve != nil {
		// Not launched by this process (a daemon restart): resolve it the
		// way the launch did.
		_, _, p = h.Resolve(ctx, h.homeStep(h.Cfg(), bot))
	}
	return ImageProvider(p)
}

// homeStep is the step a bot's home session launches as.
func (h *PaseoHome) homeStep(cfg *config.Config, bot string) config.Step {
	m := cfg.Bots[bot]
	var sk *config.SkillPolicy
	if m.Skill != nil {
		cp := *m.Skill
		sk = &cp
	}
	return config.Step{Type: "agent", Runtime: cfg.BotHomeRuntime(bot), Model: m.Home.Model, Mode: m.Home.Mode, Skill: sk, Checkout: "none",
		BoundBot: bot, BoundBotRole: config.BotRoleHome}
}

var _ HomeLauncher = (*PaseoHome)(nil)

// disp is the paseo dispatcher of the bot's home runtime.
func (h *PaseoHome) disp(bot string) (PaseoDispatcher, error) {
	return h.For(h.Cfg().BotHomeRuntime(bot))
}

// Launch implements HomeLauncher.
func (h *PaseoHome) Launch(ctx context.Context, spec HomeSpec) (string, error) {
	cfg := h.Cfg()
	d, err := h.disp(spec.Bot)
	if err != nil {
		return "", err
	}
	if h.BeforeLaunch != nil {
		if err := h.BeforeLaunch(ctx, spec); err != nil {
			return "", fmt.Errorf("prepare session %s: %w", spec.Session, err)
		}
	}
	wsID, err := d.CreateLocalWorkspace(ctx, spec.Cwd, "bot "+spec.Bot+" ("+spec.Session+")")
	if err != nil {
		return "", fmt.Errorf("home workspace: %w", err)
	}
	act := config.Action{Type: "agent", Agent: "bot:" + spec.Bot, Prompt: spec.Prompt, Checkout: "none", Name: "bot:" + spec.Bot}
	t := core.Trigger{
		Source: "bot", Instance: "bot", Kind: "bot",
		Target: inbound.SyntheticTarget("bot:"+spec.Bot, spec.Session),
		Title:  "bot " + spec.Bot,
		Action: act,
	}
	step := h.homeStep(cfg, spec.Bot)
	step.Labels = map[string]string{"bot": spec.Bot, "bot_role": config.BotRoleHome, "bot_session": spec.Session}
	model, provider := "", ""
	if h.Resolve != nil {
		model, _, provider = h.Resolve(ctx, step)
	}
	req := dispatch.Request{
		Trigger:       t,
		Action:        act,
		Step:          step,
		Identity:      "bot:" + spec.Bot,
		Provider:      provider,
		Model:         model,
		Workspace:     wsID,
		DispatchID:    spec.Dispatch,
		LiteralPrompt: true,
	}
	if h.Sandbox != nil && h.Sandbox.Active() {
		// The sandboxed provider entry extends the base one (claude →
		// claude-sandboxed) with conductor's wrapper as its command; the
		// model pin rides along unchanged. Its identity and per-launch token
		// come from the daemon's cwd lookup (Service.SessionForCwd).
		if sb, ok := h.Sandbox.PaseoProvider(req.Provider); ok {
			req.Provider = sb
		}
	}
	if h.Credentials != nil {
		if c, err := h.Credentials(ctx, t); err == nil {
			req.Credentials = c
		}
	}
	ref, err := d.Dispatch(ctx, req)
	if err == nil && ref.AgentID == "" {
		err = fmt.Errorf("paseo returned no agent id")
	}
	if err != nil {
		// The workspace was created for this launch alone: release it rather
		// than leave an agent-less workspace behind for every failed attempt.
		if rerr := d.ReleaseWorkspace(context.WithoutCancel(ctx), wsID); rerr != nil {
			err = fmt.Errorf("%w (and releasing its workspace %s: %v)", err, wsID, rerr)
		}
		return "", err
	}
	if h.Hold != nil {
		h.Hold(ref.AgentID)
	}
	h.mu.Lock()
	if h.provider == nil {
		h.provider = map[string]string{}
	}
	h.provider[spec.Bot] = req.Provider
	h.mu.Unlock()
	return ref.AgentID, nil
}

// Send implements HomeLauncher.
func (h *PaseoHome) Send(ctx context.Context, bot, agentID, text string, images []string) error {
	d, err := h.disp(bot)
	if err != nil {
		return err
	}
	if len(images) > 0 {
		return d.SendImages(ctx, agentID, text, images)
	}
	return d.Send(ctx, agentID, text)
}

// State implements HomeLauncher. A "closed" agent (its process ended, e.g.
// across a paseo daemon restart) counts as gone: a send would resume it
// without the launch env, so without its conductor endpoint and token, and
// the bot rotates to a fresh session instead.
func (h *PaseoHome) State(ctx context.Context, bot, agentID string) (string, error) {
	d, err := h.disp(bot)
	if err != nil {
		return "", err
	}
	st, err := d.AgentStatus(ctx, agentID)
	if st == "closed" {
		st = "gone"
	}
	return st, err
}

// Retire implements HomeLauncher.
func (h *PaseoHome) Retire(ctx context.Context, bot, agentID, dispatchID, cwd string) error {
	if h.Unhold != nil {
		h.Unhold(agentID)
	}
	if h.Revoke != nil && dispatchID != "" {
		h.Revoke(dispatchID)
	}
	d, err := h.disp(bot)
	if err != nil {
		return err
	}
	return d.Archive(ctx, agentID)
}
