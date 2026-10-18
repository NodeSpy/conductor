package bot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/store"
)

// The two op tables. AgentOp serves a bot SESSION (the agent socket,
// authorized by its token to exactly one bot and role); OperatorOp serves
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

// listArg is a list argument, comma-separated entries split.
func listArg(args map[string]any, k string) []string {
	var out []string
	for _, v := range strs(args, k) {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// filesArg decodes a message's files from the control socket:
// [{name, data: base64}].
func filesArg(args map[string]any) ([]AttachmentIn, error) {
	raw, _ := args["attachments"].([]any)
	var out []AttachmentIn
	for i, x := range raw {
		m, _ := x.(map[string]any)
		data, err := base64.StdEncoding.DecodeString(str(m, "data"))
		if err != nil {
			return nil, fmt.Errorf("attachment %d: %w", i+1, err)
		}
		out = append(out, AttachmentIn{Name: str(m, "name"), Data: data})
	}
	return out, nil
}

// attachmentView is an attachment as the operator surfaces show it.
func attachmentView(a store.Attachment) map[string]any {
	v := toMap(a)
	if a.Removed.IsZero() {
		delete(v, "removed")
	}
	return v
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

// AgentOp serves one `conductor bot <sub>` call from a bot session.
// A worker's call also hands it any unread instructions from its parent.
func (s *Service) AgentOp(ctx context.Context, c Actor, sub string, args map[string]any) (map[string]any, error) {
	if c.Bot == "" {
		return nil, fmt.Errorf("this session is not a bot session")
	}
	if _, err := s.bot(c.Bot); err != nil {
		return nil, err
	}
	out, err := s.agentOp(ctx, c, sub, args)
	if c.Role == config.BotRoleWorker {
		if texts := s.takeInstructions(c.Bot, c.Dispatch); len(texts) > 0 {
			if out == nil {
				out = map[string]any{}
			}
			out["instructions_from_parent"] = InstructionsText(c.Bot, texts)
		}
	}
	return out, err
}

func (s *Service) agentOp(ctx context.Context, c Actor, sub string, args map[string]any) (map[string]any, error) {
	if operatorOnlyOps[sub] {
		return nil, fmt.Errorf("a bot session cannot %s a repo directly — only an operator does, on the operator control socket; propose team knowledge with `conductor bot team promote`, and write your own memory with `conductor bot remember`", sub)
	}
	switch sub {
	case "whoami":
		rec := s.Ledger.Bot(c.Bot)
		out := map[string]any{"bot": c.Bot, "role": c.Role, "case": c.CaseID,
			"open_work": LedgerSummary(rec), "autonomy": AutonomySummary(s.Cfg(), c.Bot)}
		if team, _, ok := s.Cfg().TeamOf(c.Bot); ok {
			out["team"] = team
		}
		return out, nil
	case "remember":
		path, sha, err := s.WriteMemory(ctx, c, MemoryWrite{Kind: str(args, "kind"), Path: str(args, "path"), Slug: str(args, "slug"),
			Title: str(args, "title"), Text: str(args, "text"), Append: boolArg(args, "append"), CaseID: str(args, "case")})
		if err != nil {
			return nil, err
		}
		return map[string]any{"commit": sha, "path": path}, nil
	case "kinds":
		return kindsInfo(s.Repo(c.Bot).Kinds(), str(args, "kind"))
	case "episode":
		sha, err := s.Episode(ctx, c, str(args, "case"), str(args, "text"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"commit": sha}, nil
	case "recall":
		return memRecall(ctx, s.Repo(c.Bot), args, "conductor bot read", "conductor bot index")
	case "read":
		return memRead(ctx, s.Repo(c.Bot), args)
	case "index":
		m := s.Cfg().Bots[c.Bot]
		return memIndex(ctx, s.Repo(c.Bot), args, IndexView{Budget: m.Memory.IndexBudgetOr(config.DefaultIndexBudget)})
	case "cases":
		return map[string]any{"cases": toList(s.Cases(c.Bot, boolArg(args, "all")))}, nil
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
			Handoff: boolArg(args, "handoff"), Group: str(args, "group"), Verbs: verbList(args), ReadOnly: boolArg(args, "read_only"),
			Attachments: listArg(args, "attachments")})
		if err != nil {
			return nil, err
		}
		out := map[string]any{"worker": w.ID, "case": w.CaseID, "status": w.Status,
			"message": "worker started; it reports back into your conversation when it finishes"}
		if w.Group != "" {
			out["group"] = w.Group
			out["message"] = fmt.Sprintf("worker started in group %s; `conductor bot join --case %s --group %s` waits for the whole group and returns every result together (the group also reports here once, when its last worker finishes)", w.Group, w.CaseID, str(args, "group"))
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
	case "delegate":
		return s.Delegate(ctx, c, str(args, "to"), str(args, "task"), str(args, "case"))
	case "decline":
		return s.Decline(ctx, c, str(args, "case"), str(args, "reason"))
	case "delegations":
		return s.Delegations(c.Bot, boolArg(args, "all")), nil
	case "tell":
		return s.Tell(ctx, c, str(args, "to"), str(args, "text"))
	case "report":
		return map[string]any{"ok": true}, s.Report(c, str(args, "text"))
	case "instructions":
		// The delivery itself happens in AgentOp; this op only asks.
		return map[string]any{"ok": true}, nil
	case "peers":
		return map[string]any{"peers": s.Peers(c.Bot)}, nil
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
			return nil, fmt.Errorf("no bot has terraform: configured on this daemon")
		}
		p, err := s.Terraform.Propose(ctx, c, TerraformRequest{Repo: str(args, "repo"), Commit: str(args, "commit"), Dir: str(args, "dir"),
			Workspace: str(args, "workspace"), VarFiles: strs(args, "var_files"), Targets: strs(args, "targets"), Summary: str(args, "summary")})
		if err != nil {
			return nil, err
		}
		return map[string]any{"proposal": p.ID, "status": p.Status, "executed": false,
			"message": fmt.Sprintf("Conductor is planning %s@%s (%s) in its own checkout. When the plan is ready you and your operator will see its summary; an operator approves it with `conductor bot approve %s %s`, and conductor applies exactly that plan. `conductor bot wait %s` blocks until it is decided.", p.Terraform.Repo, short(p.Terraform.Commit), p.Terraform.Dir, c.Bot, p.ID, p.ID)}, nil
	case "replan":
		if s.Terraform == nil {
			return nil, fmt.Errorf("no bot has terraform: configured on this daemon")
		}
		p, err := s.Terraform.Replan(ctx, c, str(args, "proposal"))
		if err != nil {
			return nil, err
		}
		return map[string]any{"proposal": p.ID, "status": p.Status, "replan_of": str(args, "proposal")}, nil
	case "proposals":
		return map[string]any{"proposals": toList(s.Proposals(c.Bot, boolArg(args, "all")))}, nil
	case "wait":
		// Bounded per call (the socket client has a short deadline); the
		// CLI loops until decided or its own timeout.
		p, err := s.Wait(ctx, c.Bot, str(args, "proposal"), 20*time.Second)
		if err != nil {
			return nil, err
		}
		return toMap(p), nil
	case "approve", "deny", "decide":
		return nil, fmt.Errorf("a bot session cannot decide proposals — only an operator can, with a signed `conductor bot approve` on the operator socket")
	}
	return nil, fmt.Errorf("unknown bot op %q", sub)
}

// teamOp serves `conductor bot team [read|recall|promote]`.
func (s *Service) teamOp(ctx context.Context, c Actor, args map[string]any) (map[string]any, error) {
	switch a := str(args, "action"); a {
	case "", "info":
		return s.TeamInfo(ctx, c.Bot)
	case "read", "recall", "index":
		team, _, repo, err := s.teamRepoOf(c.Bot)
		if err != nil {
			return nil, err
		}
		if err := repo.Init(ctx); err != nil {
			return nil, err
		}
		switch a {
		case "read":
			return memRead(ctx, repo, args)
		case "recall":
			return memRecall(ctx, repo, args, "conductor bot team read", "conductor bot team index")
		}
		return memIndex(ctx, repo, args, IndexView{Budget: s.Cfg().TeamLimits(team).IndexBudgetOr(config.DefaultTeamIndexBudget),
			Heading: "## Team " + team + "'s knowledge index", Recall: "conductor bot team recall", IndexCmd: "conductor bot team index"})
	case "promote":
		return s.Promote(ctx, c, str(args, "path"), str(args, "to"), str(args, "note"))
	default:
		return nil, fmt.Errorf("unknown team action %q (info, read, recall, index, promote)", a)
	}
}

// Proposals lists a bot's proposals (pending first, newest first).
func (s *Service) Proposals(bot string, all bool) []store.Proposal {
	rec := s.Ledger.Bot(bot)
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
	name := str(args, "bot")
	need := func() error {
		if _, err := s.bot(name); err != nil {
			return err
		}
		return nil
	}
	switch sub {
	case "edit":
		// An operator's direct write (opedit.go): a team's knowledge
		// (team:) or a bot's memory (bot:), one commit against base.
		return s.OperatorEdit(ctx, str(args, "team"), name, str(args, "as"), str(args, "case"), Edit{
			Op: str(args, "op"), Path: str(args, "path"), To: str(args, "to"), Text: str(args, "text"),
			Kind: str(args, "kind"), Create: boolArg(args, "create"),
			Commit: str(args, "commit"), Base: str(args, "base"), Subject: str(args, "message")})
	case "new":
		return s.OperatorNew(ctx, str(args, "team"), name, str(args, "as"), str(args, "case"), NewDoc{Kind: str(args, "kind"),
			Title: str(args, "title"), Slug: str(args, "slug"), Text: str(args, "text"), Base: str(args, "base"),
			Subject: str(args, "message"), Fields: fieldArgs(args)})
	case "file":
		return s.RepoFile(ctx, str(args, "team"), name, str(args, "path"), str(args, "kind"))
	case "kinds":
		repo, _, _, err := s.editTarget(str(args, "team"), name)
		if err != nil {
			return nil, err
		}
		return kindsInfo(repo.Kinds(), str(args, "kind"))
	case "mem_index", "mem_recall":
		team := str(args, "team")
		repo, _, _, err := s.editTarget(team, name)
		if err != nil {
			return nil, err
		}
		if err := repo.Init(ctx); err != nil {
			return nil, err
		}
		if sub == "mem_recall" {
			face, target := "memory", name
			if team != "" {
				face, target = "knowledge", team
			}
			return memRecall(ctx, repo, args, "conductor bot "+face+" cat "+target, "conductor bot "+face+" index "+target)
		}
		v := IndexView{Budget: cfg.Bots[name].Memory.IndexBudgetOr(config.DefaultIndexBudget)}
		if team != "" {
			v = IndexView{Budget: cfg.TeamLimits(team).IndexBudgetOr(config.DefaultTeamIndexBudget), Heading: "## Team " + team + "'s knowledge index"}
		}
		return memIndex(ctx, repo, args, v)
	case "template_check":
		if err := need(); err != nil {
			return nil, err
		}
		id, err := s.CheckTemplate(ctx, name)
		if err != nil {
			return nil, err
		}
		return map[string]any{"proposal": id, "template": toMap(s.Ledger.Bot(name).Template)}, nil
	case "history":
		n, _ := strconv.Atoi(str(args, "limit"))
		return s.RepoHistory(ctx, str(args, "team"), name, str(args, "path"), n)
	case "list":
		var out []any
		for _, n := range cfg.BotNames() {
			out = append(out, s.statusOf(n))
		}
		return map[string]any{"bots": out}, nil
	case "show":
		if err := need(); err != nil {
			return nil, err
		}
		st := s.statusOf(name)
		st["memory_log"] = s.Repo(name).Log(ctx, 10)
		st["open_work"] = LedgerSummary(s.Ledger.Bot(name))
		st["charter"] = CharterText(cfg, name)
		return st, nil
	case "cases":
		if err := need(); err != nil {
			return nil, err
		}
		return map[string]any{"cases": toList(s.Cases(name, boolArg(args, "all")))}, nil
	case "delegations":
		if err := need(); err != nil {
			return nil, err
		}
		return s.Delegations(name, boolArg(args, "all")), nil
	case "workers":
		if err := need(); err != nil {
			return nil, err
		}
		rec := s.Ledger.Bot(name)
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
		p := s.Ledger.Bot(name).Proposals[str(args, "id")]
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
		if cp.Template != nil {
			out["diff"] = TemplateDiff(cp.Template)
		}
		return out, nil
	case "decide":
		if err := need(); err != nil {
			return nil, err
		}
		if s.Exec == nil {
			return nil, fmt.Errorf("no approval executor")
		}
		p, err := s.Exec.Decide(ctx, Decision{Bot: name, Proposal: str(args, "id"), Operator: str(args, "operator"),
			Decision: str(args, "decision"), Signature: str(args, "signature"), Reason: str(args, "reason"), Edit: str(args, "edit")})
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
		files, err := filesArg(args)
		if err != nil {
			return nil, err
		}
		atts, err := s.SayWith(name, str(args, "text"), files, str(args, "case"))
		if err != nil {
			return nil, err
		}
		out := map[string]any{"queued": true}
		if len(atts) > 0 {
			var vs []any
			for _, a := range atts {
				vs = append(vs, attachmentView(*a))
			}
			out["attachments"] = vs
		}
		return out, nil
	case "deliver":
		files, err := filesArg(args)
		if err != nil {
			return nil, err
		}
		id, err := s.Deliver(ctx, Event{Bot: name, Source: "operator", Requester: str(args, "requester"),
			Title: str(args, "title"), Text: str(args, "text"), Urgent: boolArg(args, "urgent"), Attachments: files})
		if err != nil {
			return nil, err
		}
		return map[string]any{"case": id}, nil
	case "attachments":
		if err := need(); err != nil {
			return nil, err
		}
		var vs []any
		for _, a := range s.Attachments(name) {
			if a.Removed.IsZero() || boolArg(args, "all") {
				vs = append(vs, attachmentView(a))
			}
		}
		return map[string]any{"attachments": vs}, nil
	case "attachment":
		if err := need(); err != nil {
			return nil, err
		}
		a, data, err := s.AttachmentBytes(name, str(args, "id"))
		if err != nil {
			return nil, err
		}
		v := attachmentView(a)
		if boolArg(args, "data") {
			v["data"] = base64.StdEncoding.EncodeToString(data)
		}
		return v, nil
	case "replan":
		if err := need(); err != nil {
			return nil, err
		}
		if s.Terraform == nil {
			return nil, fmt.Errorf("no terraform runner on this daemon")
		}
		p, err := s.Terraform.Replan(ctx, Actor{Bot: name, Role: config.BotRoleHome}, str(args, "id"))
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
		sws := s.Sweeps(name, 20)
		for i := range sws {
			sws[i] = sweepForDisplay(sws[i])
		}
		return map[string]any{"sweeps": toList(sws)}, nil
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
	return nil, fmt.Errorf("unknown bot command %q", sub)
}

// teamStatus is a team for the operator: bots, lead, routing, knowledge,
// the proactive pool's use, and (full) recent routed cases and the
// knowledge log.
func (s *Service) teamStatus(ctx context.Context, team string, full bool) map[string]any {
	cfg := s.Cfg()
	t := cfg.Teams[team]
	peers := "team"
	if t.Peers == "none" {
		peers = "none"
	}
	out := map[string]any{"team": team, "description": t.Description, "bots": cfg.TeamBots(team),
		"operators": cfg.TeamOperators(team), "peers": peers}
	if r := t.Routing; r != nil {
		rules := make([]any, 0, len(r.Rules))
		for i, rule := range r.Rules {
			rules = append(rules, map[string]any{"rule": rule.Label(i), "match": rule.Match, "to": rule.To})
		}
		out["routing"] = map[string]any{"lead": r.Lead, "fallback": r.Fallback, "rules": rules, "timeout": r.TimeoutD().String()}
	}
	if pool := t.Budget.Proactive; pool != nil {
		var recs []store.BotRecord
		for _, n := range cfg.TeamBots(team) {
			recs = append(recs, s.Ledger.Bot(n))
		}
		out["proactive_pool"] = poolForDisplay(*pool, s.now(), recs)
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
		for _, n := range cfg.TeamBots(team) {
			for _, c := range s.Cases(n, true) {
				if c.Team != team || c.Routing == nil {
					continue
				}
				routed = append(routed, map[string]any{"bot": n, "case": c.ID, "title": c.Title, "status": c.Status,
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
		for _, n := range cfg.TeamBots(team) {
			for _, p := range s.Proposals(n, false) {
				if p.Kind == KindKnowledge {
					pending = append(pending, map[string]any{"bot": n, "proposal": p.ID, "status": p.Status, "action": ProposalAction(p)})
				}
			}
		}
		out["knowledge_proposals"] = pending
		var edges []store.Delegation
		for _, n := range cfg.TeamBots(team) {
			for _, d := range s.Ledger.Bot(n).Delegations {
				edges = append(edges, *d)
			}
		}
		sort.Slice(edges, func(i, j int) bool { return edges[i].Created.After(edges[j].Created) })
		var ds []any
		for _, d := range edges {
			if len(ds) == 30 {
				break
			}
			ds = append(ds, map[string]any{"delegation": d.ID, "from": d.From, "from_case": d.FromCase, "to": d.To, "to_case": d.ToCase,
				"title": d.Title, "status": d.Status, "requester": d.Requester, "requester_class": d.RequesterClass, "depth": d.Depth})
		}
		out["delegations"] = ds
	}
	return out
}

func (s *Service) statusOf(name string) map[string]any {
	cfg := s.Cfg()
	m := cfg.Bots[name]
	rec := s.Ledger.Bot(name)
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
		"bot": name, "description": m.Description, "shadow": m.Autonomy.ShadowMode(),
		"sandboxed": s.Gate != nil && s.Gate.sandboxed(), "memory_repo": cfg.BotRepoDir(name),
		"open_cases": open, "pending_proposals": pending, "live_workers": workers,
		"home_runtime": cfg.BotHomeRuntime(name), "worker_role": config.BotRoleWorker,
		"identity": m.Identity.View(name, m.IdentityFrom),
	}
	if o := m.Origin; o != nil {
		t := toMap(o)
		t["ref"] = o.Ref()
		if st := rec.Template; st != nil {
			t["applied_version"] = st.Version
			t["since"] = st.Since.Format(time.RFC3339)
		}
		for _, p := range rec.Proposals {
			if p.Kind == KindTemplate && p.Status == "pending" {
				t["pending_update"] = p.ID
			}
		}
		out["template"] = t
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

// The usage a bot listing shows follows usage.mode (cost.DisplayMode): off,
// no usage figures; subscription, what conductor observed per runtime and
// model, no dollars; api, with dollars. The budget's caps (config) are
// always shown — enforcement does not depend on the mode.

// sweepForDisplay strips a sweep's usage figures the mode does not show.
func sweepForDisplay(sw store.Sweep) store.Sweep {
	if !cost.ShowUsage() {
		sw.CostUSD, sw.Tokens, sw.Turns, sw.Usage = 0, 0, 0, nil
		return sw
	}
	// Per runtime/model only: the totals sum across providers.
	sw.Tokens, sw.Turns = 0, 0
	if !cost.ShowDollars() {
		sw.CostUSD = 0
		sw.Usage = withoutDollars(sw.Usage)
	}
	return sw
}

func withoutDollars(us []store.ModelUsage) []store.ModelUsage {
	out := make([]store.ModelUsage, len(us))
	for i, u := range us {
		u.CostUSD = 0
		out[i] = u
	}
	return out
}

// poolForDisplay is a team's proactive pool: its caps, and (when the mode
// shows usage) what its bots' sweeps used in the window, per runtime/model.
func poolForDisplay(pool config.ProactiveBudget, now time.Time, recs []store.BotRecord) map[string]any {
	out := map[string]any{"max_runs_per_day": pool.MaxRunsPerDay, "max_turns_per_day": pool.MaxTurnsPerDay,
		"max_tokens_per_day": int(pool.MaxTokensPerDay), "max_cost_per_day": pool.MaxCostPerDay}
	if !cost.ShowUsage() {
		return out
	}
	byModel := map[[2]string]*store.ModelUsage{}
	runs := 0
	for _, rec := range recs {
		for _, sw := range rec.Sweeps {
			if now.Sub(sw.Started) >= SweepWindow {
				continue
			}
			runs++
			for _, u := range sw.Usage {
				k := [2]string{u.Runtime, u.Model}
				m := byModel[k]
				if m == nil {
					m = &store.ModelUsage{Runtime: u.Runtime, Model: u.Model}
					byModel[k] = m
				}
				m.Runs += u.Runs
				m.Turns += u.Turns
				m.Tokens += u.Tokens
				m.CostUSD += u.CostUSD
				m.Approximate = m.Approximate || u.Approximate
			}
		}
	}
	var ms []store.ModelUsage
	for _, m := range byModel {
		ms = append(ms, *m)
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].Runtime+"\x00"+ms[i].Model < ms[j].Runtime+"\x00"+ms[j].Model })
	if !cost.ShowDollars() {
		ms = withoutDollars(ms)
	}
	out["observed_24h"] = map[string]any{"sweeps": runs, "by_model": toList(ms)}
	return out
}

// kindsInfo describes a repo's kinds (or one), for a session or an operator.
func kindsInfo(ks *Kinds, one string) (map[string]any, error) {
	if one != "" {
		k, err := ks.Get(one)
		if err != nil {
			return nil, err
		}
		return k.Describe(), nil
	}
	var out []any
	for _, k := range ks.List() {
		out = append(out, k.Describe())
	}
	return map[string]any{"kinds": out}, nil
}

// fieldArgs reads `fields` — a map, or k=v strings (the CLI's --field).
func fieldArgs(args map[string]any) map[string]string {
	out := map[string]string{}
	switch v := args["fields"].(type) {
	case map[string]any:
		for k, x := range v {
			out[k] = fmt.Sprint(x)
		}
	default:
		for _, kv := range strs(args, "fields") {
			if k, val, ok := strings.Cut(kv, "="); ok {
				out[strings.TrimSpace(k)] = val
			}
		}
	}
	return out
}
