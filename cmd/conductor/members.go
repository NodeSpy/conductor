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
	"github.com/NodeSpy/conductor/internal/grant"
	"github.com/NodeSpy/conductor/internal/member"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/skill"
	"github.com/NodeSpy/conductor/internal/store"
)

// memberStack is the daemon's member wiring (docs/design/members.md).
type memberStack struct {
	svc    *member.Service
	gate   *member.Gate
	grants grant.Store
}

// grantStore is the daemon's one grant store, shared by the member gate
// (which mints on approval) and — once #154 lands — the jail's broker
// (which consumes). In-memory by design: a grant is a short-lived approval,
// and a restart dropping it is the safe failure (the case keeps the approval
// record; an operator re-approves).
var grantStore grant.Store = grant.NewMemStore()

// buildMembers assembles the member service from the daemon's existing
// parts: the ledger (store), the flow runner's skill chokepoint (gate +
// approved execution), the paseo dispatcher of the home runtime (home
// sessions), and the engine's manual-trigger path (workers). nil when no
// member is configured.
func buildMembers(cfg *config.Config, st *store.Store, runner *flow.Runner, reg *controller.Registry,
	hold *dispatch.HoldSet, eng *engine.Engine, logf func(string, ...any)) *memberStack {
	if len(cfg.MemberNames()) == 0 || runner == nil {
		return nil
	}
	cfgFn := func() *config.Config { return cfg }
	jail := jailSeam()
	gate := &member.Gate{Cfg: cfgFn, Ledger: st, Jail: jail}
	audit := func(e map[string]any) { st.Audit(e) }
	exec := &member.Executor{Cfg: cfgFn, Ledger: st, Gate: gate, Grants: grantStore, RunVerb: runner.RunSkillVerb, Audit: audit}
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
		Hold: hold.Add, Unhold: hold.Remove, Jail: jail, Resolve: eng.ResolveModel,
		Revoke: func(d string) {
			if b := skill.Active(); b != nil {
				b.RevokeDispatch(d)
			}
		},
	}
	manual := manualTriggersByName(cfg)
	svc := &member.Service{
		Cfg: cfgFn, Ledger: st, Gate: gate, Exec: exec, Home: home,
		Workers: workerFirer(func(ctx context.Context, trig, hid string, inputs map[string]any) error {
			_, err := runManualTrigger(ctx, manual, controlRequest{Name: trig, Inputs: inputs, HistoryID: hid}, eng.Emit)
			return err
		}),
		Log: logf, Audit: audit,
	}
	registerJailIdentity(svc)
	gate.OnProposal = svc.OnProposal
	exec.Notify = svc.OnDecision
	runner.Members = gate
	runner.MemberRouter = svc
	if len(cfg.MemberNames()) > 0 {
		logf("members: %s — sessions are NOT jailed until #154 lands; promotion beyond observe/propose is refused without the jail", strings.Join(cfg.MemberNames(), ", "))
	}
	return &memberStack{svc: svc, gate: gate, grants: grantStore}
}

// jailSeam is the agent jail as members see it. Until #160 (agent jail) and
// #175 (its paseo integration) are on main it is NoJail — member sessions run
// unjailed and promotion past observe/propose is refused. The switch, once
// they land:
//
//	return member.JailFuncs{
//		IsActive: func() bool { return <the jail is enabled for paseo sessions> },
//		Archive:  controller.ArchivePaseoCwd,
//	}
func jailSeam() member.Jail { return member.NoJail{} }

// registerJailIdentity hands the jail members' cwd→identity lookup. A no-op
// until #175 is on main; then:
//
//	controller.SetPaseoIdentity(svc.IdentityForCwd)
func registerJailIdentity(svc *member.Service) { _ = svc.IdentityForCwd }

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
// and once #154 lands it is outside every jail. A decide (approve/deny)
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
