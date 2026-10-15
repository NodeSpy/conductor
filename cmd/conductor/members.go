package main

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/engine"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/member"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
	"github.com/NodeSpy/conductor/internal/store"
)

// memberStack is the daemon's member wiring (docs/design/members.md).
type memberStack struct {
	svc  *member.Service
	gate *member.Gate
}

// buildMembers assembles the member service from the daemon's existing
// parts: the ledger (store), the flow runner's skill chokepoint (gate +
// approved execution), the paseo dispatcher of the home runtime (home
// sessions), the engine's manual-trigger path (workers), and — for members
// with a terraform: block — conductor's own terraform runner. nil when no
// member is configured.
func buildMembers(cfg *config.Config, st *store.Store, runner *flow.Runner, reg *controller.Registry,
	hold *dispatch.HoldSet, eng *engine.Engine, logf func(string, ...any)) *memberStack {
	if len(cfg.MemberNames()) == 0 || runner == nil {
		return nil
	}
	cfgFn := func() *config.Config { return cfg }
	sandbox := sandboxSeam()
	gate := &member.Gate{Cfg: cfgFn, Ledger: st, Sandbox: sandbox}
	audit := func(e map[string]any) { st.Audit(e) }
	var tf *member.Terraform
	for _, n := range cfg.MemberNames() {
		if cfg.Members[n].Terraform != nil {
			tf = &member.Terraform{Cfg: cfgFn, Ledger: st, Gate: gate, Log: logf}
			break
		}
	}
	exec := &member.Executor{Cfg: cfgFn, Ledger: st, Gate: gate, RunVerb: runner.RunSkillVerb, Audit: audit, Terraform: tf}
	home := &member.PaseoHome{
		Cfg: cfgFn,
		For: func(rt string) (member.PaseoDispatcher, error) {
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
	svc := &member.Service{
		Cfg: cfgFn, Ledger: st, Gate: gate, Exec: exec, Home: home, Terraform: tf,
		Workers: workerFirer(func(ctx context.Context, trig, hid string, inputs map[string]any) error {
			_, err := runManualTrigger(ctx, manual, controlRequest{Name: trig, Inputs: inputs, HistoryID: hid}, eng.Emit)
			return err
		}),
		Log: logf, Audit: audit, RevokeToken: revokeDispatch,
	}
	registerSandboxSessions(svc)
	gate.OnProposal = svc.OnProposal
	exec.Notify = svc.OnDecision
	if tf != nil {
		tf.OnPlanned = svc.OnProposal
	}
	runner.Members = gate
	runner.MemberRouter = svc
	if !sandbox.Active() {
		logf("members: %s — sessions are NOT sandboxed until conductor's agent sandbox is on this daemon; promotion beyond observe/propose is refused without it", strings.Join(cfg.MemberNames(), ", "))
	}
	return &memberStack{svc: svc, gate: gate}
}

// sandboxSeam is conductor's agent sandbox as members see it. Until it is on
// main this is NoSandbox — member sessions run unconfined and promotion past
// observe/propose is refused. The switch, once it lands:
//
//	return member.SandboxFuncs{
//		IsActive: func() bool { return <the sandbox is enforcing agent launches> },
//		Provider: func(base string) (string, bool) { return <the conductor-owned paseo provider entry `conductor paseo setup` made for base, e.g. claude-sandboxed> },
//	}
func sandboxSeam() member.Sandbox { return member.NoSandbox{} }

// registerSandboxSessions hands the sandbox endpoint members' cwd → session
// lookup, so `conductor sandbox exec` launching a member's home agent (or a
// hand-off worker) mints that launch's token with the member binding and
// dispatch. A no-op until the sandbox is on main; then, roughly:
//
//	sandbox.SetSessionLookup(func(cwd string) (sandbox.Session, bool) { b, ok := svc.SessionForCwd(cwd); … })
func registerSandboxSessions(svc *member.Service) { _ = svc.SessionForCwd }

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
func (m *memberStack) start(ctx context.Context, events *flow.EventHub) {
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

// wrapStepOps puts the member service in front of the engine's step.done:
// a worker's done is recorded in its case, the memory repo and the home
// inbox first; a HOME session's done is refused (a home session does not
// finish — it is rotated).
func (m *memberStack) wrapStepOps(base func() *connector.StepOps) func() *connector.StepOps {
	if m == nil {
		return base
	}
	return func() *connector.StepOps {
		inner := base()
		if inner == nil {
			return nil
		}
		return &connector.StepOps{Done: func(ctx context.Context, dispatchID, agentID, reason string, output any, hasOutput bool) error {
			if mem, role := m.svc.IsMemberDispatch(dispatchID); mem != "" {
				if role == config.MemberRoleHome {
					return fmt.Errorf("step.done: the home session of member %s does not finish — it is rotated (`conductor member rotate %s`)", mem, mem)
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

// agentOp authorizes a member-session call by its token.
func (m *memberStack) agentOp(sb *skill.Broker) func(ctx context.Context, token, sub string, args map[string]any, peer memory.Peer) (map[string]any, error) {
	return func(ctx context.Context, token, sub string, args map[string]any, peer memory.Peer) (map[string]any, error) {
		id, err := sb.Authorize(token, skill.Peer{PID: peer.PID, StartTime: peer.StartTime, UID: peer.UID, Valid: peer.Valid})
		if err != nil {
			return nil, err
		}
		if id.Member == "" {
			return nil, fmt.Errorf("member: this session is not a member session")
		}
		if args == nil {
			args = map[string]any{}
		}
		return m.svc.AgentOp(ctx, member.Actor{Member: id.Member, Role: id.MemberRole, CaseID: id.CaseID,
			Dispatch: id.Dispatch, Agent: id.Agent}, sub, args)
	}
}

// memberCtl is the running daemon's member service, for the control socket
// (nil when no member is configured).
var memberCtl *member.Service

// handleMemberControl serves `conductor member …` on the operator control
// socket. This socket is the operator's: it is 0600 in a 0700 state dir,
// and with the agent sandbox on it is outside every session. A decide (approve/deny)
// additionally carries the operator's SSH signature, verified against the
// configured key — the socket alone never approves anything.
func handleMemberControl(ctx context.Context, conn net.Conn, req controlRequest, log func(string, ...any)) {
	svc := memberCtl
	if svc == nil {
		writeControlResp(conn, controlResponse{Error: "no members are configured on this daemon"})
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
		name := fmt.Sprint(args["member"])
		go func() {
			if _, err := svc.OperatorOp(context.WithoutCancel(ctx), "rotate", args); err != nil {
				log("member %s: rotate: %v", name, err)
			}
		}()
		writeControlResp(conn, controlResponse{OK: true, Msg: "rotation of " + name + " started — `conductor member show " + name + "` reports the new session"})
		return
	}
	out, err := svc.OperatorOp(ctx, req.Sub, args)
	if err != nil {
		writeControlResp(conn, controlResponse{Error: err.Error()})
		return
	}
	writeControlResp(conn, controlResponse{OK: true, Result: out})
}
