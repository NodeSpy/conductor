package member

import (
	"context"
	"fmt"

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
	Archive(ctx context.Context, agentID string) error
	ReleaseWorkspace(ctx context.Context, id string) error
}

var _ PaseoDispatcher = (*dispatch.Dispatcher)(nil)

// PaseoHome launches home sessions on paseo.
//
// Before #154 lands, a home session is NOT jailed: it is an ordinary paseo
// agent running as the operator. When the jail's `conductor` paseo provider
// and its cwd→identity lookup land, this is the one place that changes — the
// launch passes `--provider conductor` and the session's cwd (already unique
// per session) is what the jail keys it by.
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
}

var _ HomeLauncher = (*PaseoHome)(nil)

// disp is the paseo dispatcher of the member's home runtime.
func (h *PaseoHome) disp(member string) (PaseoDispatcher, error) {
	return h.For(h.Cfg().MemberHomeRuntime(member))
}

// Launch implements HomeLauncher.
func (h *PaseoHome) Launch(ctx context.Context, spec HomeSpec) (string, error) {
	cfg := h.Cfg()
	m := cfg.Members[spec.Member]
	d, err := h.disp(spec.Member)
	if err != nil {
		return "", err
	}
	if h.BeforeLaunch != nil {
		if err := h.BeforeLaunch(ctx, spec); err != nil {
			return "", fmt.Errorf("prepare session %s: %w", spec.Session, err)
		}
	}
	wsID, err := d.CreateLocalWorkspace(ctx, spec.Cwd, "member "+spec.Member+" ("+spec.Session+")")
	if err != nil {
		return "", fmt.Errorf("home workspace: %w", err)
	}
	var sk *config.SkillPolicy
	if m.Skill != nil {
		cp := *m.Skill
		sk = &cp
	}
	act := config.Action{Type: "agent", Agent: "member:" + spec.Member, Prompt: spec.Prompt, Checkout: "none", Name: "member:" + spec.Member}
	t := core.Trigger{
		Source: "member", Instance: "member", Kind: "member",
		Target: inbound.SyntheticTarget("member:"+spec.Member, spec.Session),
		Title:  "member " + spec.Member,
		Action: act,
	}
	step := config.Step{Type: "agent", Runtime: cfg.MemberHomeRuntime(spec.Member), Model: m.Home.Model, Mode: m.Home.Mode, Skill: sk, Checkout: "none",
		Labels:      map[string]string{"member": spec.Member, "member_role": config.MemberRoleHome, "member_session": spec.Session},
		BoundMember: spec.Member, BoundMemberRole: config.MemberRoleHome}
	model, provider := "", ""
	if h.Resolve != nil {
		model, _, provider = h.Resolve(ctx, step)
	}
	req := dispatch.Request{
		Trigger:       t,
		Action:        act,
		Step:          step,
		Identity:      "member:" + spec.Member,
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
	return ref.AgentID, nil
}

// Send implements HomeLauncher.
func (h *PaseoHome) Send(ctx context.Context, member, agentID, text string) error {
	d, err := h.disp(member)
	if err != nil {
		return err
	}
	return d.Send(ctx, agentID, text)
}

// State implements HomeLauncher. A "closed" agent (its process ended, e.g.
// across a paseo daemon restart) counts as gone: a send would resume it
// without the launch env, so without its conductor endpoint and token, and
// the member rotates to a fresh session instead.
func (h *PaseoHome) State(ctx context.Context, member, agentID string) (string, error) {
	d, err := h.disp(member)
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
func (h *PaseoHome) Retire(ctx context.Context, member, agentID, dispatchID, cwd string) error {
	if h.Unhold != nil {
		h.Unhold(agentID)
	}
	if h.Revoke != nil && dispatchID != "" {
		h.Revoke(dispatchID)
	}
	d, err := h.disp(member)
	if err != nil {
		return err
	}
	return d.Archive(ctx, agentID)
}
