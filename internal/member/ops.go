package member

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// The two op tables. AgentOp serves a member SESSION (the agent socket,
// authorized by its token to exactly one member and role); OperatorOp serves
// the operator control socket. Approval lives only in the second.

func str(args map[string]any, k string) string {
	if v, ok := args[k]; ok && v != nil {
		if s, ok := v.(string); ok {
			return s
		}
		return fmt.Sprint(v)
	}
	return ""
}

func boolArg(args map[string]any, k string) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	}
	return false
}

func strs(args map[string]any, k string) []string {
	switch v := args[k].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	}
	return nil
}

// verbList reads --verbs as a comma-separated list or a list.
func verbList(args map[string]any) []string {
	var out []string
	for _, v := range strs(args, "verbs") {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func toMap(v any) map[string]any {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func toList[T any](xs []T) []any {
	out := make([]any, 0, len(xs))
	for _, x := range xs {
		out = append(out, toMap(x))
	}
	return out
}

// AgentOp serves one `conductor member <sub>` call from a member session.
// A worker's call also hands it any unread instructions from its parent.
func (s *Service) AgentOp(ctx context.Context, c Actor, sub string, args map[string]any) (map[string]any, error) {
	if c.Member == "" {
		return nil, fmt.Errorf("this session is not a member session")
	}
	if _, err := s.member(c.Member); err != nil {
		return nil, err
	}
	out, err := s.agentOp(ctx, c, sub, args)
	if c.Role == config.MemberRoleWorker {
		if texts := s.takeInstructions(c.Member, c.Dispatch); len(texts) > 0 {
			if out == nil {
				out = map[string]any{}
			}
			out["instructions_from_parent"] = InstructionsText(c.Member, texts)
		}
	}
	return out, err
}

func (s *Service) agentOp(ctx context.Context, c Actor, sub string, args map[string]any) (map[string]any, error) {
	switch sub {
	case "whoami":
		rec := s.Ledger.Member(c.Member)
		out := map[string]any{"member": c.Member, "role": c.Role, "case": c.CaseID,
			"open_work": LedgerSummary(rec), "autonomy": AutonomySummary(s.Cfg(), c.Member)}
		if team, _, ok := s.Cfg().TeamOf(c.Member); ok {
			out["team"] = team
		}
		return out, nil
	case "remember":
		sha, err := s.WriteMemory(ctx, c, str(args, "path"), str(args, "text"), boolArg(args, "append"), str(args, "case"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"commit": sha, "path": str(args, "path")}, nil
	case "episode":
		sha, err := s.Episode(ctx, c, str(args, "case"), str(args, "text"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"commit": sha}, nil
	case "recall":
		hits := s.Repo(c.Member).Search(str(args, "query"), 20)
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, map[string]any{"path": h["path"], "line": h["line"]})
		}
		return map[string]any{"hits": out, "count": len(out)}, nil
	case "read":
		text, err := s.Repo(c.Member).Read(str(args, "path"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": str(args, "path"), "text": text}, nil
	case "cases":
		return map[string]any{"cases": toList(s.Cases(c.Member, boolArg(args, "all")))}, nil
	case "case_open":
		cs, err := s.CaseOpen(c, str(args, "title"), str(args, "summary"))
		if err != nil {
			return nil, err
		}
		return toMap(cs), nil
	case "case_note":
		return map[string]any{"ok": true}, s.CaseNote(c, str(args, "case"), str(args, "text"))
	case "case_close":
		return map[string]any{"ok": true}, s.CaseClose(ctx, c, str(args, "case"), str(args, "outcome"))
	case "spawn":
		w, err := s.Spawn(ctx, c, SpawnRequest{CaseID: str(args, "case"), Title: str(args, "title"), Brief: str(args, "brief"),
			Handoff: boolArg(args, "handoff"), Group: str(args, "group"), Verbs: verbList(args), ReadOnly: boolArg(args, "read_only")})
		if err != nil {
			return nil, err
		}
		out := map[string]any{"worker": w.ID, "case": w.CaseID, "status": w.Status,
			"message": "worker started; it reports back into your conversation when it finishes"}
		if w.Group != "" {
			out["group"] = w.Group
			out["message"] = fmt.Sprintf("worker started in group %s; `conductor member join --case %s --group %s` waits for the whole group and returns every result together (the group also reports here once, when its last worker finishes)", w.Group, w.CaseID, str(args, "group"))
		}
		return out, nil
	case "join":
		// Bounded per call like wait; the CLI loops.
		res, err := s.Join(ctx, c, str(args, "id"), str(args, "case"), str(args, "group"), 20*time.Second)
		if err != nil {
			return nil, err
		}
		return toMap(res), nil
	case "ask":
		return s.Ask(ctx, c, str(args, "to"), str(args, "text"), str(args, "case"))
	case "tell":
		return s.Tell(ctx, c, str(args, "to"), str(args, "text"))
	case "report":
		return map[string]any{"ok": true}, s.Report(c, str(args, "text"))
	case "instructions":
		// The delivery itself happens in AgentOp; this op only asks.
		return map[string]any{"ok": true}, nil
	case "peers":
		return map[string]any{"peers": s.Peers(c.Member)}, nil
	case "team":
		return s.teamOp(ctx, c, args)
	case "assign":
		return s.Assign(ctx, c, str(args, "case"), str(args, "to"), str(args, "note"))
	case "handoff":
		sha, err := s.Handoff(ctx, c, str(args, "text"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"commit": sha}, nil
	case "propose_terraform":
		if s.Terraform == nil {
			return nil, fmt.Errorf("no member has terraform: configured on this daemon")
		}
		p, err := s.Terraform.Propose(ctx, c, TerraformRequest{Repo: str(args, "repo"), Commit: str(args, "commit"), Dir: str(args, "dir"),
			Workspace: str(args, "workspace"), VarFiles: strs(args, "var_files"), Targets: strs(args, "targets"), Summary: str(args, "summary")})
		if err != nil {
			return nil, err
		}
		return map[string]any{"proposal": p.ID, "status": p.Status, "executed": false,
			"message": fmt.Sprintf("Conductor is planning %s@%s (%s) in its own checkout. When the plan is ready you and your operator will see its summary; an operator approves it with `conductor member approve %s %s`, and conductor applies exactly that plan. `conductor member wait %s` blocks until it is decided.", p.Terraform.Repo, short(p.Terraform.Commit), p.Terraform.Dir, c.Member, p.ID, p.ID)}, nil
	case "replan":
		if s.Terraform == nil {
			return nil, fmt.Errorf("no member has terraform: configured on this daemon")
		}
		p, err := s.Terraform.Replan(ctx, c, str(args, "proposal"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"proposal": p.ID, "status": p.Status, "replan_of": str(args, "proposal")}, nil
	case "proposals":
		return map[string]any{"proposals": toList(s.Proposals(c.Member, boolArg(args, "all")))}, nil
	case "wait":
		// Bounded per call (the socket client has a short deadline); the
		// CLI loops until decided or its own timeout.
		p, err := s.Wait(ctx, c.Member, str(args, "proposal"), 20*time.Second)
		if err != nil {
			return nil, err
		}
		return toMap(p), nil
	case "approve", "deny", "decide":
		return nil, fmt.Errorf("a member session cannot decide proposals — only an operator can, with a signed `conductor member approve` on the operator socket")
	}
	return nil, fmt.Errorf("unknown member op %q", sub)
}

// teamOp serves `conductor member team [read|recall|promote]`.
func (s *Service) teamOp(ctx context.Context, c Actor, args map[string]any) (map[string]any, error) {
	switch a := str(args, "action"); a {
	case "", "info":
		return s.TeamInfo(ctx, c.Member)
	case "read":
		text, err := s.TeamRead(ctx, c.Member, str(args, "path"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": str(args, "path"), "text": text}, nil
	case "recall":
		hits, err := s.TeamRecall(ctx, c.Member, str(args, "query"))
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, len(hits))
		for _, h := range hits {
			out = append(out, map[string]any{"path": h["path"], "line": h["line"]})
		}
		return map[string]any{"hits": out, "count": len(out)}, nil
	case "promote":
		return s.Promote(ctx, c, str(args, "path"), str(args, "to"), str(args, "note"))
	default:
		return nil, fmt.Errorf("unknown team action %q (info, read, recall, promote)", a)
	}
}

// Proposals lists a member's proposals (pending first, newest first).
func (s *Service) Proposals(member string, all bool) []store.Proposal {
	rec := s.Ledger.Member(member)
	var out []store.Proposal
	for _, p := range rec.Proposals {
		if all || p.Status == "pending" || p.Status == "shadow" {
			cp := *p
			cp.Identity = nil // daemon-side only
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// OperatorOp serves one operator control-socket call.
func (s *Service) OperatorOp(ctx context.Context, sub string, args map[string]any) (map[string]any, error) {
	cfg := s.Cfg()
	name := str(args, "member")
	need := func() error {
		if _, err := s.member(name); err != nil {
			return err
		}
		return nil
	}
	switch sub {
	case "list":
		var out []any
		for _, n := range cfg.MemberNames() {
			out = append(out, s.statusOf(n))
		}
		return map[string]any{"members": out}, nil
	case "show":
		if err := need(); err != nil {
			return nil, err
		}
		st := s.statusOf(name)
		st["memory_log"] = s.Repo(name).Log(ctx, 10)
		st["open_work"] = LedgerSummary(s.Ledger.Member(name))
		st["charter"] = CharterText(cfg, name)
		return st, nil
	case "cases":
		if err := need(); err != nil {
			return nil, err
		}
		return map[string]any{"cases": toList(s.Cases(name, boolArg(args, "all")))}, nil
	case "workers":
		if err := need(); err != nil {
			return nil, err
		}
		rec := s.Ledger.Member(name)
		var ws []store.Worker
		for _, w := range rec.Workers {
			ws = append(ws, *w)
		}
		sort.Slice(ws, func(i, j int) bool { return ws[i].Started.After(ws[j].Started) })
		return map[string]any{"workers": toList(ws)}, nil
	case "proposals":
		if err := need(); err != nil {
			return nil, err
		}
		return map[string]any{"proposals": toList(s.Proposals(name, boolArg(args, "all")))}, nil
	case "proposal":
		if err := need(); err != nil {
			return nil, err
		}
		p := s.Ledger.Member(name).Proposals[str(args, "id")]
		if p == nil {
			return nil, fmt.Errorf("no proposal %s", str(args, "id"))
		}
		cp := *p
		cp.Identity = nil
		out := toMap(cp)
		out["action"] = ProposalAction(cp)
		out["operators"] = Approvers(cfg, name, cp)
		if cp.Knowledge != nil {
			out["text"] = cp.Knowledge.Text
		}
		return out, nil
	case "decide":
		if err := need(); err != nil {
			return nil, err
		}
		if s.Exec == nil {
			return nil, fmt.Errorf("no approval executor")
		}
		p, err := s.Exec.Decide(ctx, Decision{Member: name, Proposal: str(args, "id"), Operator: str(args, "operator"),
			Decision: str(args, "decision"), Signature: str(args, "signature"), Reason: str(args, "reason")})
		if err != nil {
			return nil, err
		}
		p.Identity = nil
		return toMap(p), nil
	case "rotate":
		if err := need(); err != nil {
			return nil, err
		}
		if err := s.Rotate(ctx, name, "operator request"); err != nil {
			return nil, err
		}
		return s.statusOf(name), nil
	case "say":
		if err := s.Say(name, str(args, "text")); err != nil {
			return nil, err
		}
		return map[string]any{"queued": true}, nil
	case "deliver":
		id, err := s.Deliver(ctx, Event{Member: name, Source: "operator", Requester: str(args, "requester"),
			Title: str(args, "title"), Text: str(args, "text"), Urgent: boolArg(args, "urgent")})
		if err != nil {
			return nil, err
		}
		return map[string]any{"case": id}, nil
	case "replan":
		if err := need(); err != nil {
			return nil, err
		}
		if s.Terraform == nil {
			return nil, fmt.Errorf("no terraform runner on this daemon")
		}
		p, err := s.Terraform.Replan(ctx, Actor{Member: name, Role: config.MemberRoleHome}, str(args, "id"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"proposal": p.ID, "status": p.Status, "replan_of": str(args, "id")}, nil
	case "sweep":
		if err := need(); err != nil {
			return nil, err
		}
		sw, err := s.StartSweep(ctx, name, true)
		if err != nil {
			return nil, err
		}
		return map[string]any{"sweep": sw.ID, "worker": sw.Worker, "status": sw.Status}, nil
	case "sweeps":
		if err := need(); err != nil {
			return nil, err
		}
		return map[string]any{"sweeps": toList(s.Sweeps(name, 20))}, nil
	case "teams":
		var out []any
		for _, t := range cfg.TeamNames() {
			out = append(out, s.teamStatus(ctx, t, false))
		}
		return map[string]any{"teams": out}, nil
	case "team":
		t := str(args, "team")
		if _, ok := cfg.Teams[t]; !ok {
			return nil, fmt.Errorf("unknown team %q", t)
		}
		return s.teamStatus(ctx, t, true), nil
	case "consolidate":
		w, err := s.Consolidate(ctx, name)
		if err != nil {
			return nil, err
		}
		if w.ID == "" {
			return map[string]any{"skipped": "no new episodes since the last consolidation"}, nil
		}
		return map[string]any{"worker": w.ID, "case": w.CaseID}, nil
	}
	return nil, fmt.Errorf("unknown member command %q", sub)
}

// teamStatus is a team for the operator: members, lead, routing, knowledge,
// the proactive pool's use, and (full) recent routed cases and the
// knowledge log.
func (s *Service) teamStatus(ctx context.Context, team string, full bool) map[string]any {
	cfg := s.Cfg()
	t := cfg.Teams[team]
	peers := "team"
	if t.Peers == "none" {
		peers = "none"
	}
	out := map[string]any{"team": team, "description": t.Description, "members": cfg.TeamMembers(team),
		"operators": cfg.TeamOperators(team), "peers": peers}
	if r := t.Routing; r != nil {
		rules := make([]any, 0, len(r.Rules))
		for i, rule := range r.Rules {
			rules = append(rules, map[string]any{"rule": rule.Label(i), "match": rule.Match, "to": rule.To})
		}
		out["routing"] = map[string]any{"lead": r.Lead, "fallback": r.Fallback, "rules": rules, "timeout": r.TimeoutD().String()}
	}
	if pool := t.Budget.Proactive; pool != nil {
		runs, cost := 0, 0.0
		for _, n := range cfg.TeamMembers(team) {
			for _, sw := range s.Ledger.Member(n).Sweeps {
				if s.now().Sub(sw.Started) < SweepWindow {
					runs++
					cost += sw.CostUSD
				}
			}
		}
		out["proactive_pool"] = map[string]any{"runs_24h": runs, "cost_24h": cost, "max_runs_per_day": pool.MaxRunsPerDay, "max_cost_per_day": pool.MaxCostPerDay}
	}
	if repo := s.TeamRepo(team); repo != nil {
		k := map[string]any{"repo": repo.Dir, "promotion": t.Knowledge.PromotionMode()}
		if full {
			k["log"] = repo.Log(ctx, 10)
		}
		out["knowledge"] = k
	}
	if full {
		var routed []any
		for _, n := range cfg.TeamMembers(team) {
			for _, c := range s.Cases(n, true) {
				if c.Team != team || c.Routing == nil {
					continue
				}
				routed = append(routed, map[string]any{"member": n, "case": c.ID, "title": c.Title, "status": c.Status,
					"via": c.Routing.Via, "rule": c.Routing.Rule, "lead": c.Routing.Lead, "lead_case": c.Routing.LeadCase,
					"assignee": c.Routing.Assignee, "assignee_case": c.Routing.AssigneeCase, "state": c.Routing.State,
					"requester": c.Requester, "requester_class": c.RequesterClass, "created": c.Created.Format(time.RFC3339)})
			}
		}
		if len(routed) > 30 {
			routed = routed[:30]
		}
		out["routed"] = routed
		var pending []any
		for _, n := range cfg.TeamMembers(team) {
			for _, p := range s.Proposals(n, false) {
				if p.Kind == KindKnowledge {
					pending = append(pending, map[string]any{"member": n, "proposal": p.ID, "status": p.Status, "action": ProposalAction(p)})
				}
			}
		}
		out["knowledge_proposals"] = pending
	}
	return out
}

func (s *Service) statusOf(name string) map[string]any {
	cfg := s.Cfg()
	m := cfg.Members[name]
	rec := s.Ledger.Member(name)
	open, pending, workers := 0, 0, 0
	for _, c := range rec.Cases {
		if c.Status == "open" {
			open++
		}
	}
	for _, p := range rec.Proposals {
		if p.Status == "pending" {
			pending++
		}
	}
	for _, w := range rec.Workers {
		if w.Status == "starting" || w.Status == "running" {
			workers++
		}
	}
	out := map[string]any{
		"member": name, "description": m.Description, "shadow": m.Autonomy.ShadowMode(),
		"sandboxed": s.Gate != nil && s.Gate.sandboxed(), "memory_repo": cfg.MemberRepoDir(name),
		"open_cases": open, "pending_proposals": pending, "live_workers": workers,
		"home_runtime": cfg.MemberHomeRuntime(name), "worker_role": config.MemberRoleWorker,
	}
	if h := rec.Home; h != nil {
		out["home"] = map[string]any{"session": h.ID, "agent": h.AgentID, "cwd": h.Cwd,
			"started": h.Started.Format(time.RFC3339), "turns": h.Turns}
	}
	if p := m.Proactive; p != nil {
		pr := map[string]any{"every": p.Every.D().String(), "notify_at": p.NotifyLevel()}
		if sw := s.Sweeps(name, 1); len(sw) > 0 {
			pr["last_sweep"] = map[string]any{"id": sw[0].ID, "status": sw[0].Status, "started": sw[0].Started.Format(time.RFC3339), "summary": sw[0].Summary}
		}
		out["proactive"] = pr
	}
	if peers := s.Peers(name); len(peers) > 0 {
		out["peers"] = peers
	}
	if m.Team != "" {
		out["team"] = string(m.Team)
	}
	if m.Isolation.SandboxRequired() {
		out["sandbox_required"] = true
	}
	if m.Browser != nil {
		out["browser"] = map[string]any{"logins": m.Browser.Logins, "credentials": m.Browser.Credentials}
	}
	delete(out, "worker_role")
	return out
}
