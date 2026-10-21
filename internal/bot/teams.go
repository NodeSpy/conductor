package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// TEAMS at runtime (config: internal/config/teams.go). Three things a team
// adds to its bots, each built from what bots already have:
//
//   - ROUTING. `bot: { team: … }` hands an event to the team; its rules
//     pick a bot deterministically, else the LEAD triages it (a
//     read-only worker of the lead that runs `conductor bot assign`), else
//     the fallback takes it. The event's requester is classified once, at
//     intake, and every hop can only lower that trust, never raise it: the
//     assignee's case is that requester's request, gated as such — assignment
//     never upgrades trust.
//   - PEERING. Teammates are linked (Config.BotLinked): `ask` between them
//     is the ordinary peer message — data, fenced, audited, rate-limited.
//   - KNOWLEDGE. An optional team repo every bot reads through conductor.
//     Nothing writes it but a PROMOTION from a bot's own memory — a
//     proposal an operator of the team approves (the default), or, with
//     promotion: direct, a commit at once when the bot's provenance would
//     let a promoted verb run. Bot memory stays the bot's.

// KindKnowledge is a team knowledge promotion proposal.
const KindKnowledge = "knowledge"

// promoteVerb is how a knowledge promotion is named in the gate's terms.
const promoteVerb = "team.promote"

// maxPayload bounds an event text a triage case keeps for its assignee.
const maxPayload = 16 << 10

// TeamEvent is an event routed to a team.
type TeamEvent struct {
	Team, Source, Requester, Title, Text string
	Urgent                               bool
	// Fields are the event's own fields, for routing rules.
	Fields map[string]any
	// files fetches the event's files (flow.BotRoute.Files).
	files func(ctx context.Context, maxFiles int, maxFile int64) ([]connector.File, error)
	// ticket is the ready ticket this routing picks up.
	ticket *store.Case
}

// TeamRepo is a team's knowledge repo handle (nil without knowledge:).
func (s *Service) TeamRepo(team string) *Repo {
	cfg := s.Cfg()
	dir := cfg.TeamKnowledgeDir(team)
	if dir == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repos == nil {
		s.repos = map[string]*Repo{}
	}
	key := "team:" + team // bot names have no ':'
	r := s.repos[key]
	if r == nil {
		r = &Repo{Dir: dir, Bot: team, Team: true, Now: s.Now, Seed: cfg.TeamSeedDir(team),
			IndexPath: cfg.TeamIndexPath(team),
			LimitsFn:  func() config.MemoryLimits { return s.Cfg().TeamLimits(team) },
			KindsFn: func() *Kinds {
				return NewKinds(true, s.Cfg().Teams[team].Collections).WithLimits(s.Cfg().TeamLimits(team))
			}}
		s.repos[key] = r
	}
	return r
}

// classifyAgainst is RequesterClass for an explicit operator list.
func classifyAgainst(cfg *config.Config, ops []string, requester string) (string, string) {
	r := requester
	if strings.TrimSpace(r) == "" {
		return "", ReqEvent
	}
	for _, op := range ops {
		if r == op || r == "operator:"+op {
			return op, ReqOperator
		}
		for _, a := range cfg.Operators[op].Aliases {
			if r == a {
				return op, ReqOperator
			}
		}
	}
	return r, ReqUser
}

// atBot bounds an intake attribution by what the receiving bot makes
// of the same requester: an operator of the team who is not one of this
// bot's is a third party here. Never more trust than either.
func atBot(cfg *config.Config, bot, who, class string) (string, string) {
	if class != ReqOperator {
		return who, class // event / user / peer / proactive: nothing here can raise them
	}
	// Re-classifying an operator here gives that operator or a third party:
	// never more than it had.
	return RequesterClass(cfg, bot, who)
}

// RouteTeam routes an event to a team: the first matching rule, else the
// lead's triage, else the fallback.
func (s *Service) RouteTeam(ctx context.Context, ev TeamEvent) (map[string]any, error) {
	cfg := s.Cfg()
	t, ok := cfg.Teams[ev.Team]
	if !ok {
		return nil, fmt.Errorf("unknown team %q", ev.Team)
	}
	r := t.Routing
	if r == nil {
		return nil, fmt.Errorf("team %s has no routing:", ev.Team)
	}
	who, class := classifyAgainst(cfg, cfg.TeamOperators(ev.Team), ev.Requester)
	rt := store.Routing{Team: ev.Team, At: s.now()}
	for i, rule := range r.Rules {
		if ruleMatches(rule, ev) {
			rt.Via, rt.Rule, rt.Assignee = "rule", rule.Label(i), rule.To
			break
		}
	}
	if rt.Via == "" && r.Lead != "" {
		cs, err := s.startTriage(ctx, ev, r.Lead, who, class)
		if err == nil {
			s.audit(map[string]any{"event": "bot_team_route", "team": ev.Team, "via": "lead", "lead": r.Lead, "case": cs.ID,
				"source": ev.Source, "requester": cs.Requester, "requester_class": cs.RequesterClass})
			return map[string]any{"team": ev.Team, "via": "lead", "bot": r.Lead, "case": cs.ID, "status": "triage"}, nil
		}
		s.logf("team %s: lead %s could not triage: %v — routing to the fallback", ev.Team, r.Lead, err)
		rt.Reason = "the lead's triage could not start: " + oneLine(err.Error(), 200)
	}
	if rt.Via == "" {
		rt.Via, rt.Assignee = "fallback", r.Fallback
		if rt.Reason == "" {
			rt.Reason = "no rule matched"
		}
	}
	ev2 := Event{Bot: rt.Assignee, Source: ev.Source, Title: ev.Title, Text: ev.Text, Urgent: ev.Urgent, files: ev.files, ticket: ev.ticket}
	id, err := s.deliverRouted(ctx, ev2, who, class, rt, "")
	if err != nil {
		return nil, err
	}
	s.audit(map[string]any{"event": "bot_team_route", "team": ev.Team, "via": rt.Via, "rule": rt.Rule, "bot": rt.Assignee,
		"case": id, "source": ev.Source, "reason": rt.Reason})
	out := map[string]any{"team": ev.Team, "via": rt.Via, "bot": rt.Assignee, "case": id}
	if rt.Rule != "" {
		out["rule"] = rt.Rule
	}
	return out, nil
}

// ruleMatches reports whether every match: field of a rule holds.
func ruleMatches(rule config.RoutingRule, ev TeamEvent) bool {
	if len(rule.Match) == 0 {
		return false
	}
	for k, pat := range rule.Match {
		v, ok := eventField(ev, k)
		if !ok {
			return false
		}
		if m, err := path.Match(pat, v); err != nil || !m {
			return false
		}
	}
	return true
}

// eventField reads a rule's field: source, title, text, requester, or a
// scalar of the event's own fields (dotted for nested maps).
func eventField(ev TeamEvent, k string) (string, bool) {
	switch k {
	case "source":
		return ev.Source, true
	case "title":
		return ev.Title, true
	case "text":
		return ev.Text, true
	case "requester":
		return ev.Requester, true
	}
	// A flat key first — event data often carries dotted names of its own
	// ("pagerduty.service") — then a dotted path into nested maps.
	if v, ok := ev.Fields[k]; ok {
		return scalarField(v)
	}
	var cur any = ev.Fields
	for _, seg := range strings.Split(k, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = m[seg]; !ok {
			return "", false
		}
	}
	return scalarField(cur)
}

// scalarField renders a rule-matchable value; maps, lists and nil never match.
func scalarField(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case nil, map[string]any, []any:
		return "", false
	default:
		return fmt.Sprint(x), true
	}
}

// deliverRouted opens a case in bot for a routed event: the requester as
// the team classified it, bounded by what this bot makes of it (never
// more), with the routing decision on the case.
func (s *Service) deliverRouted(ctx context.Context, ev Event, who, class string, rt store.Routing, note string) (string, error) {
	if _, err := s.bot(ev.Bot); err != nil {
		return "", err
	}
	who, class = atBot(s.Cfg(), ev.Bot, who, class)
	rt.Assignee = ev.Bot
	return s.deliverCase(ctx, ev, who, class, &rt, note)
}

// startTriage opens a triage case in the lead and starts its read-only
// triage worker.
func (s *Service) startTriage(ctx context.Context, ev TeamEvent, lead, who, class string) (store.Case, error) {
	cfg := s.Cfg()
	if _, err := s.bot(lead); err != nil {
		return store.Case{}, err
	}
	who, class = atBot(cfg, lead, who, class)
	now := s.now()
	title := eventTitle(ev.Title, ev.Text, ev.Source)
	payload := ev.Text
	if len(payload) > maxPayload {
		payload = payload[:maxPayload]
	}
	if ev.files != nil {
		// Triage is asynchronous and the event's files would have to be
		// fetched now for a bot not chosen yet: they are not carried.
		payload += "\n\n(This event came with files, which a lead's triage does not pass on: route events with files to a bot, or with a rule.)"
	}
	var cs store.Case
	err := s.Ledger.UpdateBot(lead, func(r *store.BotRecord) error {
		id := r.NextID("c")
		if r.Cases == nil {
			r.Cases = map[string]*store.Case{}
		}
		c := &store.Case{ID: id, Title: title, Status: store.TicketInProgress, Urgent: ev.Urgent, Source: srcOr(ev.Source),
			Requester: who, RequesterClass: class, Team: ev.Team, Payload: payload, Created: now, Updated: now,
			Routing: &store.Routing{Team: ev.Team, Via: "lead", Lead: lead, LeadCase: id, State: "triage", At: now}}
		if t := ev.ticket; t != nil {
			// The ticket itself goes to the lead's triage.
			carryTicket(c, t, lead, now)
		}
		r.Cases[id] = c
		cs = *c
		return nil
	})
	if err != nil {
		return store.Case{}, err
	}
	brief := TriageBrief(cfg, ev.Team, lead, cs, payload)
	if _, err := s.Spawn(ctx, Actor{Bot: lead, Role: config.BotRoleHome}, SpawnRequest{CaseID: cs.ID, Brief: brief, ReadOnly: true, byConductor: true}); err != nil {
		_ = s.Ledger.UpdateBot(lead, func(r *store.BotRecord) error {
			if c := r.Cases[cs.ID]; c != nil {
				c.Status, c.Outcome, c.Updated = store.TicketDone, "triage could not start: "+oneLine(err.Error(), 200), s.now()
				c.Routing.State = "failed"
			}
			return nil
		})
		return store.Case{}, err
	}
	return cs, nil
}

func srcOr(src string) string {
	if src == "" {
		return "unknown"
	}
	return src
}

func eventTitle(title, text, src string) string {
	t := oneLine(title, 200)
	if t == "" {
		t = oneLine(text, 120)
	}
	if t == "" {
		t = "event from " + srcOr(src)
	}
	return t
}

// Assign is the lead's `conductor bot assign <case> <bot>`: hand a
// triage case to a teammate. Only the team's lead assigns, only its own
// triage cases, only to a teammate, and only once. What the
// assignee gets is the ORIGINAL request — its requester, its source, its
// text — at no more trust than it had, and no more than the assigning
// session's own: a lead cannot launder a third party's request into an
// operator's by routing it.
func (s *Service) Assign(ctx context.Context, c Actor, caseID, to, note string) (map[string]any, error) {
	cfg := s.Cfg()
	team, t, ok := cfg.TeamOf(c.Bot)
	if !ok {
		return nil, fmt.Errorf("bot %s is not in a team", c.Bot)
	}
	if t.Routing == nil || t.Routing.Lead != c.Bot {
		lead := "none"
		if t.Routing != nil && t.Routing.Lead != "" {
			lead = t.Routing.Lead
		}
		return nil, fmt.Errorf("only team %s's lead (%s) assigns its work", team, lead)
	}
	rec := s.Ledger.Bot(c.Bot)
	var callerClass, callerWho string
	switch c.Role {
	case config.BotRoleHome:
		callerClass, callerWho, _ = homeAttribution(&rec)
	case config.BotRoleWorker:
		w := workerByDispatch(rec, c.Dispatch)
		if w == nil || w.CaseID != caseID {
			return nil, fmt.Errorf("a worker assigns only the triage case it was started on")
		}
		callerClass, callerWho = w.RequesterClass, w.Requester
		if callerClass == "" {
			callerClass = ReqUser
		}
	default:
		return nil, errors.New("only the lead's home session or its triage worker assigns")
	}
	if len(note) > MaxPeerMessage {
		return nil, fmt.Errorf("note too long (%d bytes, max %d)", len(note), MaxPeerMessage)
	}
	return s.assignTriage(ctx, team, c.Bot, caseID, to, "lead", strings.TrimSpace(note), "", callerClass, callerWho)
}

// assignTriage moves a lead's triage case to a teammate, exactly once.
func (s *Service) assignTriage(ctx context.Context, team, lead, caseID, to, via, note, reason, callerClass, callerWho string) (map[string]any, error) {
	cfg := s.Cfg()
	mc, ok := cfg.Bots[to]
	if !ok || mc.Disabled || string(mc.Team) != team {
		return nil, fmt.Errorf("%q is not a bot of team %s (bots: %s)", to, team, strings.Join(cfg.TeamBots(team), ", "))
	}
	var cs store.Case
	err := s.Ledger.UpdateBot(lead, func(r *store.BotRecord) error {
		c := r.Cases[caseID]
		if c == nil || c.Routing == nil || c.Routing.LeadCase != caseID {
			return fmt.Errorf("case %s is not a triage case of team %s", caseID, team)
		}
		if c.Routing.State != "triage" || !store.Active(c.Status) {
			return fmt.Errorf("case %s is not awaiting triage (%s%s)", caseID, c.Routing.State, assignedTo(c.Routing))
		}
		c.Routing.State, c.Routing.Assignee = "assigned", to
		cs = *c
		cp := *c.Routing
		cs.Routing = &cp
		return nil
	})
	if err != nil {
		return nil, err
	}
	// The request keeps its own provenance, bounded by the assigning
	// session's: the less trusted of the two, never more. A session acting
	// on its own initiative (bot: no less-trusted request open) adds no
	// taint — the request stays exactly whose it was.
	who, class := cs.Requester, cs.RequesterClass
	if callerClass != ReqBot {
		class, who = lessTrusted(cs.RequesterClass, cs.Requester, callerClass, callerWho)
	}
	rt := store.Routing{Team: team, Via: via, Lead: lead, LeadCase: caseID, Note: note, Reason: reason, At: s.now()}
	msg := ""
	if note != "" {
		msg = fmt.Sprintf("Team %s's lead %s assigned this to you, with a note:\n%s", team, lead, fence("TRIAGE NOTE", note))
	}
	ev := Event{Bot: to, Source: cs.Source, Title: cs.Title, Text: cs.Payload, Urgent: cs.Urgent}
	if cs.Ticket != "" {
		// The same ticket moves on to the teammate; the lead's copy closes,
		// pointing where it went.
		t := cs
		ev.ticket = &t
	}
	id, err := s.deliverRouted(ctx, ev, who, class, rt, msg)
	if err != nil {
		_ = s.Ledger.UpdateBot(lead, func(r *store.BotRecord) error {
			if c := r.Cases[caseID]; c != nil && c.Routing != nil {
				c.Routing.State, c.Routing.Assignee = "triage", ""
			}
			return nil
		})
		return nil, err
	}
	_ = s.Ledger.UpdateBot(lead, func(r *store.BotRecord) error {
		if c := r.Cases[caseID]; c != nil && c.Routing != nil {
			c.Routing.AssigneeCase = id
			c.Routing.Reason = reason
			c.Status, c.Updated = store.TicketDone, s.now()
			c.Outcome = fmt.Sprintf("assigned to %s (case %s) via %s", to, id, via)
			if c.Ticket != "" {
				c.MovedTo = "bot:" + to + "/" + id
			}
		}
		return nil
	})
	s.audit(map[string]any{"event": "bot_team_route", "team": team, "via": via, "lead": lead, "lead_case": caseID,
		"bot": to, "case": id, "reason": reason, "requester": who, "requester_class": class})
	s.logf("team %s: case %s of %s assigned to %s (case %s, via %s)", team, caseID, lead, to, id, via)
	return map[string]any{"assigned": true, "team": team, "to": to, "case": id, "via": via}, nil
}

func assignedTo(rt *store.Routing) string {
	if rt.Assignee == "" {
		return ""
	}
	return " to " + rt.Assignee
}

// triageFallback hands an unassigned triage case to the team's fallback.
func (s *Service) triageFallback(ctx context.Context, lead, caseID, reason string) {
	team, t, ok := s.Cfg().TeamOf(lead)
	if !ok || t.Routing == nil {
		return
	}
	// Callers pick triage-state cases; assignTriage re-checks the state
	// inside its transaction, so a race with an assignment is refused there.
	cs := s.Ledger.Bot(lead).Cases[caseID]
	if cs == nil || cs.Routing == nil {
		return
	}
	if _, err := s.assignTriage(ctx, team, lead, caseID, t.Routing.Fallback, "fallback", "", reason, cs.RequesterClass, cs.Requester); err != nil {
		s.logf("team %s: fallback for %s's case %s: %v", team, lead, caseID, err)
	}
}

// expireTriage falls back on triage cases left past the timeout — a former
// lead's included, so a lead change in config strands nothing.
func (s *Service) expireTriage(ctx context.Context, name string) {
	_, t, ok := s.Cfg().TeamOf(name)
	if !ok || t.Routing == nil {
		return
	}
	var due []string
	for _, c := range s.Ledger.Bot(name).Cases {
		if store.Active(c.Status) && c.Routing != nil && c.Routing.State == "triage" && s.now().Sub(c.Created) >= t.Routing.TimeoutD() {
			due = append(due, c.ID)
		}
	}
	sort.Strings(due)
	for _, id := range due {
		s.triageFallback(ctx, name, id, fmt.Sprintf("the lead did not assign within %s", t.Routing.TimeoutD()))
	}
}

// isTriage reports whether a case is a lead's (still or once) triage case.
func isTriage(c store.Case) bool {
	return c.Routing != nil && c.Routing.LeadCase == c.ID && c.Routing.Via == "lead"
}

// ---- knowledge ----

// TeamInfo is what `conductor bot team` tells a bot about its team.
func (s *Service) TeamInfo(ctx context.Context, bot string) (map[string]any, error) {
	cfg := s.Cfg()
	team, t, ok := cfg.TeamOf(bot)
	if !ok {
		return nil, fmt.Errorf("bot %s is not in a team", bot)
	}
	var ms []any
	for _, n := range cfg.TeamBots(team) {
		e := map[string]any{"bot": n, "description": cfg.Bots[n].Description, "peer": cfg.BotLinked(bot, n)}
		if t.Routing != nil && t.Routing.Lead == n {
			e["lead"] = true
		}
		ms = append(ms, e)
	}
	out := map[string]any{"team": team, "description": t.Description, "bots": ms, "operators": cfg.TeamOperators(team)}
	if t.Peers == "none" {
		out["peers"] = "none — teammates message each other only where an operator linked them"
	} else {
		out["peers"] = "team — ask any teammate (`conductor bot ask <bot> \"…\"`)"
	}
	if t.Routing != nil {
		out["routing"] = map[string]any{"lead": t.Routing.Lead, "fallback": t.Routing.Fallback, "rules": len(t.Routing.Rules)}
	}
	if repo := s.TeamRepo(team); repo != nil {
		out["knowledge"] = map[string]any{"promotion": t.Knowledge.PromotionMode(), "head": repo.Head(ctx),
			"files": teamFiles(repo)}
	}
	return out, nil
}

// teamRepoOf is a bot's team's knowledge repo, or an error.
func (s *Service) teamRepoOf(bot string) (string, config.TeamConfig, *Repo, error) {
	team, t, ok := s.Cfg().TeamOf(bot)
	if !ok {
		return "", t, nil, fmt.Errorf("bot %s is not in a team", bot)
	}
	repo := s.TeamRepo(team)
	if repo == nil {
		return "", t, nil, fmt.Errorf("team %s has no knowledge repo (teams.%s.knowledge)", team, team)
	}
	return team, t, repo, nil
}

// TeamRead reads a team knowledge file.
func (s *Service) TeamRead(ctx context.Context, bot, rel string) (string, error) {
	_, _, repo, err := s.teamRepoOf(bot)
	if err != nil {
		return "", err
	}
	if err := repo.Init(ctx); err != nil {
		return "", err
	}
	return repo.Read(rel)
}

// TeamRecall ranks team knowledge against a query.
func (s *Service) TeamRecall(ctx context.Context, bot, query string) ([]Hit, error) {
	team, _, repo, err := s.teamRepoOf(bot)
	if err != nil {
		return nil, err
	}
	if err := repo.Init(ctx); err != nil {
		return nil, err
	}
	return repo.Recall(ctx, query, s.Cfg().TeamLimits(team).RecallLimitOr(), "")
}

// knowledgeOptions are what a promotion's digest covers.
func knowledgeOptions(k store.KnowledgeChange) map[string]any {
	return map[string]any{"team": k.Team, "from": k.From, "to": k.To, "sha256": k.SHA256}
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Promote is `conductor bot team promote --path <memory file> [--to
// <team path>]`: propose putting one of the bot's own knowledge or
// runbook files into the team's knowledge. With promotion: proposal (the
// default) it is held for an operator of the team; with direct, it lands at
// once — but only on a provenance a promoted verb could run on (an event's,
// the bot's own, an operator's), only outside shadow mode, and never
// from a read-only worker; otherwise it is a proposal too.
func (s *Service) Promote(ctx context.Context, c Actor, from, to, note string) (map[string]any, error) {
	m, err := s.bot(c.Bot)
	if err != nil {
		return nil, err
	}
	team, t, repo, err := s.teamRepoOf(c.Bot)
	if err != nil {
		return nil, err
	}
	own := s.Repo(c.Bot)
	src, err := own.cleanPath(from)
	if err != nil {
		return nil, err
	}
	if sk, _ := own.Kinds().Of(src); sk.Name == KindLog || sk.Name == KindHandoff {
		return nil, fmt.Errorf("promote a document of your memory (knowledge, runbooks, watch, …), not your %s", sk.Name)
	}
	if strings.TrimSpace(to) == "" {
		to = src
	}
	text, err := own.Read(src)
	if err != nil {
		return nil, fmt.Errorf("read %s from your memory: %w", src, err)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%s is empty", src)
	}
	if len(text) > 256<<10 {
		return nil, fmt.Errorf("%s is larger than 256KiB", src)
	}
	if len(note) > MaxPeerMessage {
		return nil, fmt.Errorf("note too long (%d bytes, max %d)", len(note), MaxPeerMessage)
	}
	// The destination's kind, path and schema are checked now, as a
	// promotion would land — a malformed document is refused here, with its
	// schema errors, not at approval time.
	if to, err = CleanPath(to); err != nil {
		return nil, err
	}
	if _, err := repo.Kinds().CheckWrite(to, ensureNL(text), WriterProposal, ""); err != nil {
		return nil, fmt.Errorf("team %s would not take it: %w", team, err)
	}
	k := store.KnowledgeChange{Team: team, From: src, To: to, SHA256: sha256Hex(text), Text: text, Source: own.Head(ctx), Note: strings.TrimSpace(note)}
	if err := repo.Init(ctx); err != nil {
		return nil, err
	}
	var cl callerInfo
	if s.Gate != nil {
		cl = s.Gate.caller(flow.SkillIdentity{Bot: c.Bot, BotRole: c.Role, Dispatch: c.Dispatch})
	} else {
		cl = callerInfo{class: ReqUser}
	}
	shadow := m.Autonomy.ShadowMode()
	if t.Knowledge.PromotionMode() == "direct" && !shadow && autonomyClass[cl.class] && !cl.readOnly() {
		sha, err := s.landKnowledge(ctx, c.Bot, k, "", "")
		if err != nil {
			return nil, err
		}
		return map[string]any{"landed": true, "team": team, "path": to, "commit": sha,
			"message": fmt.Sprintf("Promoted %s into team %s's knowledge as %s (commit %s).", src, team, to, sha)}, nil
	}
	reason := ""
	if t.Knowledge.PromotionMode() == "direct" && !shadow {
		reason = fmt.Sprintf("direct promotion is held for approval: this session acts on a %s's request", cl.class)
		if cl.readOnly() {
			reason = "direct promotion is held for approval: a read-only worker proposed it"
		}
	}
	p, err := s.proposeKnowledge(c, m, cl, k, reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"landed": false, "team": team, "proposal": p.ID, "status": p.Status, "approvers": p.Approvers,
		"message": proposalAnswer(p, m)["message"]}, nil
}

// proposeKnowledge records a knowledge promotion proposal.
func (s *Service) proposeKnowledge(c Actor, m config.BotConfig, cl callerInfo, k store.KnowledgeChange, reason string) (store.Proposal, error) {
	cfg := s.Cfg()
	opts := knowledgeOptions(k)
	digest, err := ActionDigest(c.Bot, KindKnowledge, promoteVerb, opts)
	if err != nil {
		return store.Proposal{}, err
	}
	now := s.now()
	status := "pending"
	if m.Autonomy.ShadowMode() {
		status = "shadow"
	}
	var created store.Proposal
	err = s.Ledger.UpdateBot(c.Bot, func(r *store.BotRecord) error {
		kc := k
		p := &store.Proposal{ID: r.NextID("p"), Kind: KindKnowledge, Tier: config.TierPropose, Uses: promoteVerb,
			Options: opts, Digest: digest, Knowledge: &kc, Role: c.Role, Agent: c.Agent, Dispatch: c.Dispatch,
			Summary:   fmt.Sprintf("promote %s into team %s's knowledge as %s", k.From, k.Team, k.To),
			Requester: cl.who, RequesterClass: cl.class, Status: status, Reason: reason,
			Team: k.Team, Approvers: cfg.TeamOperators(k.Team), Created: now, Expires: now.Add(m.Autonomy.TTL())}
		if cl.worker != nil {
			p.Worker = cl.worker.ID
		}
		if cs := r.Cases[cl.caseID]; cs != nil {
			p.CaseID = cs.ID
			cs.Proposals = append(cs.Proposals, p.ID)
			cs.Updated = now
		}
		if r.Proposals == nil {
			r.Proposals = map[string]*store.Proposal{}
		}
		r.Proposals[p.ID] = p
		created = *p
		return nil
	})
	if err != nil {
		return store.Proposal{}, err
	}
	s.OnProposal(c.Bot, created)
	return created, nil
}

// landKnowledge commits a promotion into the team repo, authored by the
// bot, with the proposal and approver as trailers.
func (s *Service) landKnowledge(ctx context.Context, bot string, k store.KnowledgeChange, proposal, approver string) (string, error) {
	if sha256Hex(k.Text) != k.SHA256 {
		return "", errors.New("the promoted text does not match its recorded hash — not landed")
	}
	repo := s.TeamRepo(k.Team)
	if repo == nil {
		return "", fmt.Errorf("team %s has no knowledge repo", k.Team)
	}
	if err := repo.Init(ctx); err != nil {
		return "", err
	}
	tr := map[string]string{"Bot": bot, "Bot-Team": k.Team, "Promoted-From": k.From + "@" + k.Source,
		"Bot-Proposal": proposal, "Approved-By": approver}
	// Approved with changes, the content is the operator's: the operator
	// authors the commit, and the trailers name the proposing bot and what
	// it proposed.
	author, subject := BotIdent(bot), fmt.Sprintf("knowledge: %s (promoted by bot %s)", k.To, bot)
	if k.EditedBy != "" {
		author = OperatorIdent(k.EditedBy)
		subject = fmt.Sprintf("knowledge: %s (promoted by bot %s, edited by operator %s)", k.To, bot, k.EditedBy)
		tr["Edited-By"], tr["Proposed-SHA256"], tr["Operator"] = k.EditedBy, k.ProposedSHA256, k.EditedBy
	}
	sha, err := repo.WriteIdent(ctx, author, WriterProposal, k.To, k.Text, "replace", subject, tr)
	if err != nil {
		return "", err
	}
	s.audit(map[string]any{"event": "bot_knowledge", "bot": bot, "team": k.Team, "from": k.From, "path": k.To,
		"commit": sha, "proposal": proposal, "approver": approver, "edited_by": k.EditedBy})
	return sha, nil
}

// LandApproved is the executor's hook for an approved knowledge proposal.
func (s *Service) LandApproved(ctx context.Context, bot string, p store.Proposal) (string, error) {
	if p.Knowledge == nil {
		return "", errors.New("not a knowledge proposal")
	}
	if team, _, ok := s.Cfg().TeamOf(bot); !ok || team != p.Knowledge.Team {
		return "", fmt.Errorf("bot %s is no longer in team %s", bot, p.Knowledge.Team)
	}
	return s.landKnowledge(ctx, bot, *p.Knowledge, p.ID, p.Approver)
}

// ---- budgets and isolation ----

// teamSweeps snapshots a bot's teammates' ledgers for the pool check.
// Read OUTSIDE the bot's own ledger transaction (the store lock is held
// inside it); the team lock StartSweep holds makes the snapshot current —
// no teammate records a sweep while it is held.
func (s *Service) teamSweeps(name string) []store.BotRecord {
	cfg := s.Cfg()
	team, t, ok := cfg.TeamOf(name)
	if !ok || t.Budget.Proactive == nil {
		return nil
	}
	var out []store.BotRecord
	for _, n := range cfg.TeamBots(team) {
		if n != name {
			out = append(out, s.Ledger.Bot(n))
		}
	}
	return out
}

// teamPool reports why a sweep would overrun its team's pool ("" = fits):
// the sweeps of every bot on the team over the rolling window, this one's included.
func (s *Service) teamPool(name string, own store.BotRecord, others []store.BotRecord) string {
	team, t, ok := s.Cfg().TeamOf(name)
	if !ok || t.Budget.Proactive == nil {
		return ""
	}
	if why := t.Budget.Proactive.Exceeded(sweepUsage(s.now(), append([]store.BotRecord{own}, others...)...)); why != "" {
		return fmt.Sprintf("team %s's budget (budget.proactive, all its bots' sweeps): %s", team, why)
	}
	return ""
}

// sandboxGate refuses a session start the bot's isolation forbids.
func (s *Service) sandboxGate(name string, m config.BotConfig) error {
	if m.Isolation.SandboxRequired() && !(s.Gate != nil && s.Gate.sandboxed()) {
		return fmt.Errorf("bot %s requires conductor's agent sandbox (isolation.sandbox: required%s), and it is not active on this daemon", name, teamSuffix(m))
	}
	return nil
}

func teamSuffix(m config.BotConfig) string {
	if m.Team == "" {
		return ""
	}
	return ", team " + string(m.Team)
}

func ensureNL(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

// teamFiles lists a team repo's documents (every kind's home).
func teamFiles(repo *Repo) []string {
	var out []string
	for _, k := range repo.Kinds().List() {
		out = append(out, repo.List(k.Dir)...)
	}
	return out
}
