package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

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
	"github.com/NodeSpy/conductor/internal/skill"
	"github.com/NodeSpy/conductor/internal/store"
)

// botStack is the daemon's bot wiring (docs/design/bots.md).
type botStack struct {
	svc  *bot.Service
	gate *bot.Gate
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
	sandbox := sandboxSeam()
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
		Cfg: cfgFn, Ledger: st, Gate: gate, Exec: exec, Home: home, Terraform: tf,
		Workers: workerFirer(func(ctx context.Context, trig, hid string, inputs map[string]any) error {
			_, err := runManualTrigger(ctx, manual, controlRequest{Name: trig, Inputs: inputs, HistoryID: hid}, eng.Emit)
			return err
		}),
		Log: logf, Audit: audit, RevokeToken: revokeDispatch,
		RunUsage: func(hid string) (bot.RunUsage, bool) {
			h, ok := st.GetHistory(hid)
			return bot.RunUsage{CostUSD: h.CostUSD, Tokens: h.Tokens, Turns: h.Turns, ByModel: h.Usage}, ok
		},
	}
	if notifier != nil {
		// A proactive finding over the threshold is also a conductor.*
		// lifecycle event, so a trigger can alert through any connector.
		svc.Notify = func(ctx context.Context, n bot.Notice) {
			notifier.Publish(ctx, bot.NotifyEvent, core.Trigger{Title: n.Title},
				fmt.Sprintf("bot %s: proactive sweep %s found %s — %s (%s)", n.Bot, n.Sweep, strings.ToUpper(n.Severity), n.Title, n.Text),
				map[string]any{"bot": n.Bot, "sweep": n.Sweep, "case": n.CaseID, "severity": n.Severity, "key": n.Key})
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
		logf("bots: %s — sessions are NOT sandboxed until conductor's agent sandbox is on this daemon; promotion beyond observe/propose is refused without it", strings.Join(cfg.BotNames(), ", "))
	}
	return &botStack{svc: svc, gate: gate}
}

// sandboxSeam is conductor's agent sandbox as bots see it. Until it is on
// main this is NoSandbox — bot sessions run unconfined and promotion past
// observe/propose is refused. The switch, once it lands:
//
//	return bot.SandboxFuncs{
//		IsActive: func() bool { return <the sandbox is enforcing agent launches> },
//		Provider: func(base string) (string, bool) { return <the conductor-owned paseo provider entry `conductor paseo setup` made for base, e.g. claude-sandboxed> },
//	}
func sandboxSeam() bot.Sandbox { return bot.NoSandbox{} }

// registerSandboxSessions hands the sandbox endpoint bots' cwd → session
// lookup, so `conductor sandbox exec` launching a bot's home agent (or a
// hand-off worker) mints that launch's token with the bot binding and
// dispatch. A no-op until the sandbox is on main; then, roughly:
//
//	sandbox.SetSessionLookup(func(cwd string) (sandbox.Session, bool) { b, ok := svc.SessionForCwd(cwd); … })
func registerSandboxSessions(svc *bot.Service) { _ = svc.SessionForCwd }

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
	case "templates", "template", "template_plan", "grant_plan":
		// The catalog and the plans need no bot to exist yet: the dashboard's
		// New bot flow starts here.
		templateControl(conn, req)
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
