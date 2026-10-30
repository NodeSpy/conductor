package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/engine"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/notify"
	"github.com/NodeSpy/conductor/internal/paseosetup"
	"github.com/NodeSpy/conductor/internal/skill"
	"github.com/NodeSpy/conductor/internal/store"
)

// botStack is the daemon's bot wiring (docs/design/bots.md).
type botStack struct {
	svc  *bot.Service
	gate *bot.Gate

	avatarsOnce sync.Once
	avatars     *avatarPlans
}

// buildBots assembles the bot service from the daemon's existing
// parts: the ledger (store), the flow runner's skill chokepoint (gate +
// approved execution), the paseo dispatcher of the home runtime (home
// sessions), the engine's manual-trigger path (workers), and — for bots
// with a terraform: block — conductor's own terraform runner. nil when no
// bot is configured.
func buildBots(cfg *config.Config, st *store.Store, runner *flow.Runner, reg *controller.Registry,
	hold *dispatch.HoldSet, eng *engine.Engine, notifier *notify.Notifier, logf func(string, ...any)) *botStack {
	if len(cfg.BotNames()) == 0 || runner == nil {
		return nil
	}
	cfgFn := func() *config.Config { return cfg }
	sandbox := sandboxSeam(cfg)
	gate := &bot.Gate{Cfg: cfgFn, Ledger: st, Sandbox: sandbox}
	audit := func(e map[string]any) { st.Audit(e) }
	var tf *bot.Terraform
	for _, n := range cfg.BotNames() {
		if cfg.Bots[n].Terraform != nil {
			tf = &bot.Terraform{Cfg: cfgFn, Ledger: st, Gate: gate, Log: logf}
			break
		}
	}
	// An approved browser.purchase approves the browser's purchase record
	// (the card is filled later, once, by pay); every other verb runs
	// through the skill chokepoint.
	runVerb := func(ctx context.Context, id flow.SkillIdentity, uses string, options map[string]any) (map[string]any, error) {
		if uses == BrowserPurchaseVerb {
			return runBotPurchaseApproved(ctx, gate, id, options)
		}
		return runner.RunSkillVerb(ctx, id, uses, options)
	}
	exec := &bot.Executor{Cfg: cfgFn, Ledger: st, Gate: gate, RunVerb: runVerb, Audit: audit, Terraform: tf}
	browserBotGate.Store(gate)
	home := &bot.PaseoHome{
		Cfg: cfgFn,
		For: func(rt string) (bot.PaseoDispatcher, error) {
			r, err := reg.RunnerFor(rt)
			if err != nil {
				return nil, err
			}
			d, ok := r.(*dispatch.Dispatcher)
			if !ok {
				return nil, fmt.Errorf("runtime %q is not a paseo runtime", rt)
			}
			return d, nil
		},
		Hold: hold.Add, Unhold: hold.Remove, Sandbox: sandbox, Resolve: eng.ResolveModel,
		Revoke: revokeDispatch,
	}
	manual := manualTriggersByName(cfg)
	svc := &bot.Service{
		Cfg: cfgFn, Ledger: st, Gate: gate, Exec: exec, Home: home, Terraform: tf, Blobs: runner.Blobs,
		Workers: workerFirer(func(ctx context.Context, trig, hid string, inputs map[string]any) error {
			_, err := runManualTrigger(ctx, manual, controlRequest{Name: trig, Inputs: inputs, HistoryID: hid}, eng.Emit)
			return err
		}),
		Log: logf, Audit: audit, RevokeToken: revokeDispatch,
		RunUsage: func(hid string) (bot.RunUsage, bool) {
			h, ok := st.GetHistory(hid)
			return bot.RunUsage{CostUSD: h.CostUSD, Tokens: h.Tokens, Turns: h.Turns, ByModel: h.Usage}, ok
		},
		// A ticket's spend view reads the browser's live purchase ledger: it is
		// wired after buildBots (wireBrowser), so resolve it per call rather than
		// capture a nil at construction. Without a browser on this daemon the
		// view reports spend unknown, as before.
		Spend: spendLedger{},
	}
	if notifier != nil {
		// A proactive finding over the threshold is also a conductor.*
		// lifecycle event, so a trigger can alert through any connector.
		svc.Notify = func(ctx context.Context, n bot.Notice) {
			notifier.Publish(ctx, bot.NotifyEvent, core.Trigger{Title: n.Title},
				fmt.Sprintf("bot %s: proactive sweep %s found %s — %s (%s)", n.Bot, n.Sweep, strings.ToUpper(n.Severity), n.Title, n.Text),
				map[string]any{"bot": n.Bot, "bot_name": botDisplayName(runner, n.Bot), "sweep": n.Sweep, "case": n.CaseID, "severity": n.Severity, "key": n.Key})
		}
	}
	registerSandboxSessions(svc)
	gate.OnProposal = svc.OnProposal
	exec.Knowledge = svc.LandApproved
	exec.Template = svc.LandTemplate
	exec.Notify = svc.OnDecision
	if tf != nil {
		tf.OnPlanned = svc.OnProposal
	}
	runner.Bots = gate
	runner.BotRouter = svc
	if !sandbox.Active() {
		logf("bots: %s — the bot write-promotion seam is off, so promotion beyond observe/propose is refused (home/worker sessions still run under conductor's agent sandbox when it is on)", strings.Join(cfg.BotNames(), ", "))
	}
	return &botStack{svc: svc, gate: gate}
}

// agentSandboxBuildable reports whether this box can actually build the agent
// sandbox (an unprivileged user namespace on Linux, sandbox-exec on macOS).
// Overridable in tests.
var agentSandboxBuildable = func() bool { return agentsandbox.Probe() == "" }

// sandboxSeam is conductor's agent sandbox as bots see it. Bots are confined
// by DEFAULT — sandbox.agents defaults to on (config.AgentSandboxOn), and the
// operator opts out explicitly with sandbox.agents: off. The seam is enforcing
// only when the sandbox is on AND this box can actually build it: if the
// platform cannot sandbox, Active() is false and the gate refuses autonomous
// promotion (fail closed) rather than let an agent run unconfined — the
// dispatch launch itself is refused too (wireAgentSandbox / Probe).
//
// IsActive is the gate's precondition for promotion past observe/propose.
// Provider names the conductor-owned paseo provider entry `conductor paseo
// setup` writes for a base provider (claude → claude-sandboxed), which runs
// the home session through `conductor sandbox exec`.
func sandboxSeam(cfg *config.Config) bot.Sandbox {
	enforcing := cfg.AgentSandboxOn() && agentSandboxBuildable()
	return bot.SandboxFuncs{
		IsActive: func() bool { return enforcing },
		Provider: func(base string) (string, bool) {
			if !enforcing {
				return "", false
			}
			if strings.TrimSpace(base) == "" {
				base = dispatch.PaseoDefaultProvider
			}
			if strings.HasSuffix(base, paseosetup.Suffix) {
				return base, true
			}
			return base + paseosetup.Suffix, true
		},
	}
}

// spendLedger is bot.SpendLedger over the daemon's live browser purchase
// ledger, so a ticket's spend view (conductor ticket show, GET /v1/tickets/{id})
// reports its real committed spend against its lifetime cap. The browser is
// wired after the bots (wireBrowser) and may be off, so it resolves the ledger
// at call time: with no browser the view reports spend unknown, as before.
type spendLedger struct{}

func (spendLedger) SpentOnTicket(ticketID, currency string) (int64, error) {
	m := browserMgr.Load()
	if m == nil || m.Purchases == nil {
		return 0, fmt.Errorf("no browser purchase ledger on this daemon")
	}
	// The ticket's stable id is the budget key its purchases accrue under, so
	// the committed total spans every copy — the same key the lifetime cap is
	// enforced against (H2) and durable across the bot ledger's case prune.
	return m.Purchases.CommittedCents(ticketID, currency)
}

// CloseTicket reclaims a closed ticket's purchase records from the live ledger
// (its budget key is the ticket's stable id), bounding the ledger's growth (#1).
// With no browser there is nothing to prune.
func (spendLedger) CloseTicket(ticketID string) error {
	m := browserMgr.Load()
	if m == nil || m.Purchases == nil {
		return nil
	}
	_, err := m.Purchases.PruneTicket(ticketID)
	return err
}

// botSessionLookup resolves a sandboxed launch's cwd to the live bot session
// running in it (home or hand-off worker). wireAgentSandbox hands it to the
// agent sandbox resolver. buildBots runs before wireAgentSandbox, so the
// lookup is published through this package var rather than set on the resolver
// directly. nil when no bots are configured.
var botSessionLookup func(cwd string) (agentsandbox.Entry, bool)

// registerSandboxSessions publishes the cwd → session lookup the agent sandbox
// consults: `conductor sandbox exec` launching a bot's home agent (or a
// hand-off worker) then mints that launch's token with the bot binding, its
// granted skill verbs and the dispatch — instead of the verb-less `dir:`
// identity a workspace conductor never registered as a worktree would
// otherwise get (the default-on agent sandbox stripping a bot's identity).
func registerSandboxSessions(svc *bot.Service) {
	botSessionLookup = func(cwd string) (agentsandbox.Entry, bool) {
		b, ok := svc.SessionForCwd(cwd)
		if !ok {
			return agentsandbox.Entry{}, false
		}
		return botSessionEntry(svc.Cfg(), b), true
	}
}

// botSessionEntry is the agent-sandbox entry for a live bot session: its own
// per-session identity and tool state, the paths bound read-only (a home's
// attachments, a worker's passed files), and a skill identity carrying the
// bot binding and its granted verbs — the same grant the non-sandboxed launch
// path mints via dispatch.SkillIdentityFor, so a bot acts identically with the
// sandbox on. A hand-off worker additionally gets handoff.done (interactive).
func botSessionEntry(cfg *config.Config, b bot.SessionBinding) agentsandbox.Entry {
	pol := dispatch.EffectiveSkillPolicy(cfg.Bots[b.Bot].Skill, b.Role == config.BotRoleWorker)
	return agentsandbox.Entry{
		Identity: b.Identity,
		ReadOnly: b.ReadOnly,
		Skill: &skill.Identity{
			Agent:     "bot:" + b.Bot,
			Repo:      "bot:" + b.Bot,
			Policy:    pol,
			Bot:       b.Bot,
			BotRole:   b.Role,
			CaseID:    b.CaseID,
			Dispatch:  b.Dispatch,
			Requester: skill.RequesterEvent,
		},
	}
}

// revokeDispatch kills every conductor session token minted for a dispatch
// (the broker's hand-off release path), durable ones included.
func revokeDispatch(d string) {
	if b := skill.Active(); b != nil {
		b.RevokeDispatch(d)
	}
}

type workerFirer func(ctx context.Context, trig, hid string, inputs map[string]any) error

func (f workerFirer) Fire(ctx context.Context, trig, hid string, inputs map[string]any) error {
	return f(ctx, trig, hid, inputs)
}

// start runs the service and feeds it worker run events.
func (m *botStack) start(ctx context.Context, events *flow.EventHub) {
	if m == nil {
		return
	}
	if events != nil {
		ch, cancel := events.Subscribe("")
		go func() {
			defer cancel()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					m.svc.OnRunEvent(ctx, ev.Type, ev.Run, ev.Status, ev.Detail)
				}
			}
		}()
	}
	m.svc.Start(ctx)
}

// wrapStepOps puts the bot service in front of the engine's step.done:
// a worker's done is recorded in its case, the memory repo and the home
// inbox first; a HOME session's done is refused (a home session does not
// finish — it is rotated).
func (m *botStack) wrapStepOps(base func() *connector.StepOps) func() *connector.StepOps {
	if m == nil {
		return base
	}
	return func() *connector.StepOps {
		inner := base()
		if inner == nil {
			return nil
		}
		return &connector.StepOps{Done: func(ctx context.Context, dispatchID, agentID, reason string, output any, hasOutput bool) error {
			if mem, role := m.svc.IsBotDispatch(dispatchID); mem != "" {
				if role == config.BotRoleHome {
					return fmt.Errorf("step.done: the home session of bot %s does not finish — it is rotated (`conductor bot rotate %s`)", mem, mem)
				}
				// A child cannot finish past instructions it has not read.
				if err := m.svc.CheckDone(dispatchID); err != nil {
					return err
				}
				var out any
				if hasOutput {
					out = output
				}
				m.svc.WorkerDone(ctx, dispatchID, reason, out)
			}
			return inner.Done(ctx, dispatchID, agentID, reason, output, hasOutput)
		}}
	}
}

// agentOp authorizes a bot-session call by its token.
func (m *botStack) agentOp(sb *skill.Broker) func(ctx context.Context, token, sub string, args map[string]any, peer memory.Peer) (map[string]any, error) {
	return func(ctx context.Context, token, sub string, args map[string]any, peer memory.Peer) (map[string]any, error) {
		id, err := sb.Authorize(token, skill.Peer{PID: peer.PID, StartTime: peer.StartTime, UID: peer.UID, Valid: peer.Valid})
		if err != nil {
			return nil, err
		}
		if id.Bot == "" {
			return nil, fmt.Errorf("bot: this session is not a bot session")
		}
		if args == nil {
			args = map[string]any{}
		}
		return m.svc.AgentOp(ctx, bot.Actor{Bot: id.Bot, Role: id.BotRole, CaseID: id.CaseID,
			Dispatch: id.Dispatch, Agent: id.Agent}, sub, args)
	}
}

// botDisplayName is a bot's display name, or its config id.
func botDisplayName(r *flow.Runner, name string) string {
	if r != nil && r.Cfg != nil {
		return r.Cfg.Bots[name].Identity.Attributes(name)["name"]
	}
	return name
}

// botCtl is the running daemon's bot service, for the control socket
// (nil when no bot is configured).
var botCtl *bot.Service

// handleBotControl serves `conductor bot …` on the operator control
// socket. This socket is the operator's: it is 0600 in a 0700 state dir,
// and with the agent sandbox on it is outside every session. A decide (approve/deny)
// additionally carries the operator's SSH signature, verified against the
// configured key — the socket alone never approves anything.
func handleBotControl(ctx context.Context, conn net.Conn, req controlRequest, log func(string, ...any)) {
	switch req.Sub {
	case "templates", "template", "template_plan", "grant_plan", "packs":
		// The catalog and the plans need no bot to exist yet: the dashboard's
		// New bot flow starts here.
		templateControl(conn, req)
		return
	case "identity_options":
		// The avatar, color and tone lists, for the dashboard's pickers.
		writeControlResp(conn, controlResponse{OK: true, Result: config.IdentityOptions()})
		return
	}
	svc := botCtl
	if svc == nil {
		writeControlResp(conn, controlResponse{Error: "no bots are configured on this daemon"})
		return
	}
	_ = conn.SetDeadline(time.Now().Add(3 * time.Minute))
	args := req.Args
	if args == nil {
		args = map[string]any{}
	}
	if req.Sub == "rotate" {
		// A rotation waits on the old session's handoff turn: run it in the
		// background and say so.
		name := fmt.Sprint(args["bot"])
		go func() {
			if _, err := svc.OperatorOp(context.WithoutCancel(ctx), "rotate", args); err != nil {
				log("bot %s: rotate: %v", name, err)
			}
		}()
		writeControlResp(conn, controlResponse{OK: true, Msg: "rotation of " + name + " started — `conductor bot show " + name + "` reports the new session"})
		return
	}
	out, err := svc.OperatorOp(ctx, req.Sub, args)
	if err != nil {
		writeControlResp(conn, controlResponse{Error: err.Error()})
		return
	}
	writeControlResp(conn, controlResponse{OK: true, Result: out})
}
