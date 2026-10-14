package member

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
func (s *Service) AgentOp(ctx context.Context, c Actor, sub string, args map[string]any) (map[string]any, error) {
	if c.Member == "" {
		return nil, fmt.Errorf("this session is not a member session")
	}
	if _, err := s.member(c.Member); err != nil {
		return nil, err
	}
	switch sub {
	case "whoami":
		rec := s.Ledger.Member(c.Member)
		return map[string]any{"member": c.Member, "role": c.Role, "case": c.CaseID,
			"open_work": LedgerSummary(rec), "autonomy": AutonomySummary(s.Cfg(), c.Member)}, nil
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
		w, err := s.Spawn(ctx, c, SpawnRequest{CaseID: str(args, "case"), Title: str(args, "title"), Brief: str(args, "brief"), Handoff: boolArg(args, "handoff")})
		if err != nil {
			return nil, err
		}
		return map[string]any{"worker": w.ID, "case": w.CaseID, "status": w.Status,
			"message": "worker started; it reports back into your conversation when it finishes"}, nil
	case "handoff":
		sha, err := s.Handoff(ctx, c, str(args, "text"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"commit": sha}, nil
	case "propose_command":
		p, err := s.ProposeCommand(c, str(args, "tool"), strs(args, "argv"), strs(args, "files"), str(args, "summary"))
		if err != nil {
			return nil, err
		}
		return proposalAnswer(p, s.Cfg().Members[c.Member]), nil
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
		out["operators"] = cfg.MemberOperators(name)
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
	case "consolidate":
		w, err := s.Consolidate(ctx, name)
		if err != nil {
			return nil, err
		}
		return map[string]any{"worker": w.ID, "case": w.CaseID}, nil
	}
	return nil, fmt.Errorf("unknown member command %q", sub)
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
		"jailed": s.Gate != nil && s.Gate.jailed(), "memory_repo": cfg.MemberRepoDir(name),
		"open_cases": open, "pending_proposals": pending, "live_workers": workers,
		"home_runtime": cfg.MemberHomeRuntime(name), "worker_role": config.MemberRoleWorker,
	}
	if h := rec.Home; h != nil {
		out["home"] = map[string]any{"session": h.ID, "agent": h.AgentID, "cwd": h.Cwd,
			"started": h.Started.Format(time.RFC3339), "turns": h.Turns}
	}
	delete(out, "worker_role")
	return out
}
