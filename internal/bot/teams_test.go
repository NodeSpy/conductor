package bot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// teamFixture: team platform (lead, a, b — a narrower than the team on
// operators), team billing (c), and solo (no team).
type teamFixture struct {
	*svcFixture
	x      *Executor
	audits []map[string]any
	clk    *clock
}

func newTeamFixture(t *testing.T, edit func(*config.Config)) *teamFixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(store.Options{StatePath: filepath.Join(dir, "state", "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	bots := map[string]config.BotConfig{}
	for _, n := range []string{"lead", "a", "b", "c", "solo"} {
		m := testBot()
		m.MemoryRepo = filepath.Join(dir, "mem-"+n)
		m.Description = n + " of the fleet"
		switch n {
		case "c":
			m.Team = "billing"
		case "solo":
		default:
			m.Team = "platform"
		}
		bots[n] = m
	}
	a := bots["a"]
	a.Operators = []string{"op"}
	bots["a"] = a
	cfg := &config.Config{
		Bots: bots,
		Operators: map[string]config.OperatorConfig{
			"op": {SSHKey: "ssh-ed25519 AAAA", Aliases: []string{"slack:U1"}}, "op2": {SSHKey: "ssh-ed25519 BBBB"},
			"bop": {SSHKey: "ssh-ed25519 CCCC"}},
		Teams: map[string]config.TeamConfig{
			"platform": {Description: "platform services", Operators: []string{"op", "op2"},
				Knowledge: &config.TeamKnowledge{Repo: filepath.Join(dir, "team-platform")},
				Routing: &config.TeamRouting{Lead: "lead", Fallback: "a",
					Rules: []config.RoutingRule{{Name: "billing-svc", Match: map[string]string{"service": "billing-*", "source": "pd.*"}, To: "b"}}}},
			"billing": {Operators: []string{"bop"}},
		},
	}
	if edit != nil {
		edit(cfg)
	}
	if err := cfg.ResolveTeams(); err != nil {
		t.Fatal(err)
	}
	f := &teamFixture{svcFixture: &svcFixture{st: st, home: &fakeHome{}, workers: &fakeWorkers{}, cfg: cfg, root: filepath.Join(dir, "sessions")},
		clk: &clock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}}
	gate := &Gate{Cfg: func() *config.Config { return cfg }, Ledger: st, Sandbox: fixedJail(true), Now: f.clk.now}
	f.svc = &Service{Cfg: func() *config.Config { return cfg }, Ledger: st, Gate: gate, Home: f.home, Workers: f.workers,
		SessionRoot: f.root, HandoffTimeout: time.Second, Now: f.clk.now,
		Audit: func(e map[string]any) { f.audits = append(f.audits, e) }}
	gate.OnProposal = f.svc.OnProposal
	f.x = &Executor{Cfg: func() *config.Config { return cfg }, Ledger: st, Gate: gate, Now: f.clk.now,
		Verify:    func(context.Context, string, string, []byte, string) error { return nil },
		Knowledge: f.svc.LandApproved, Notify: f.svc.OnDecision}
	f.svc.Exec = f.x
	for _, n := range cfg.BotNames() {
		if err := f.svc.Repo(n).Init(context.Background()); err != nil {
			t.Fatal(err)
		}
		f.svc.Tick(context.Background(), n) // a home session each
	}
	return f
}

func (f *teamFixture) homeOf(bot string) Actor {
	h := f.st.Bot(bot).Home
	return Actor{Bot: bot, Role: config.BotRoleHome, Dispatch: h.Dispatch}
}

func (f *teamFixture) caseOf(t *testing.T, bot, id string) store.Case {
	t.Helper()
	c := f.st.Bot(bot).Cases[id]
	if c == nil {
		t.Fatalf("%s has no case %s", bot, id)
	}
	return *c
}

// triageWorker is the worker the lead's triage case started.
func (f *teamFixture) triageWorker(t *testing.T, caseID string) store.Worker {
	t.Helper()
	for _, w := range f.st.Bot("lead").Workers {
		if w.CaseID == caseID {
			return *w
		}
	}
	t.Fatalf("no triage worker on lead's case %s", caseID)
	return store.Worker{}
}

func (f *teamFixture) route(t *testing.T, ev TeamEvent) map[string]any {
	t.Helper()
	if ev.Team == "" {
		ev.Team = "platform"
	}
	out, err := f.svc.RouteTeam(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTeamRoutesByRule(t *testing.T) {
	f := newTeamFixture(t, nil)
	out := f.route(t, TeamEvent{Source: "pd.incident", Title: "billing api 5xx", Text: "error rate 9%",
		Fields: map[string]any{"service": "billing-api", "labels": map[string]any{"env": "prod"}}})
	if out["via"] != "rule" || out["bot"] != "b" || out["rule"] != "billing-svc" {
		t.Fatalf("rule routing: %v", out)
	}
	c := f.caseOf(t, "b", out["case"].(string))
	if c.Team != "platform" || c.Routing == nil || c.Routing.Via != "rule" || c.Routing.Rule != "billing-svc" || c.Routing.Assignee != "b" || c.Routing.AssigneeCase != c.ID {
		t.Fatalf("the ledger does not record the routing: %+v %+v", c, c.Routing)
	}
	if c.RequesterClass != ReqEvent || c.Source != "pd.incident" {
		t.Fatalf("an event lost its provenance: %+v", c)
	}
	// The rule is all-of: same service from another source falls through
	// to the lead.
	out = f.route(t, TeamEvent{Source: "chat.mention", Title: "billing?", Fields: map[string]any{"service": "billing-api"}})
	if out["via"] != "lead" {
		t.Fatalf("a partial match took the rule: %v", out)
	}
	if len(f.st.Bot("lead").Workers) != 1 {
		t.Fatal("the lead's triage worker did not start")
	}
	// A missing field never matches, nested fields are dotted.
	if ruleMatches(config.RoutingRule{Match: map[string]string{"labels.env": "prod"}}, TeamEvent{Fields: map[string]any{"labels": map[string]any{"env": "prod"}}}) != true ||
		ruleMatches(config.RoutingRule{Match: map[string]string{"labels.env": "*"}}, TeamEvent{Fields: map[string]any{}}) ||
		ruleMatches(config.RoutingRule{Match: map[string]string{"n": "3"}}, TeamEvent{Fields: map[string]any{"n": 3}}) != true ||
		ruleMatches(config.RoutingRule{Match: map[string]string{"labels": "*"}}, TeamEvent{Fields: map[string]any{"labels": map[string]any{}}}) ||
		ruleMatches(config.RoutingRule{}, TeamEvent{}) {
		t.Fatal("field matching")
	}
	// A flat dotted key, as event data names its own fields.
	if !ruleMatches(config.RoutingRule{Match: map[string]string{"pagerduty.service": "db-*"}}, TeamEvent{Fields: map[string]any{"pagerduty.service": "db-main"}}) {
		t.Fatal("a flat dotted field did not match")
	}
}

func TestTeamFallsBackWithoutALead(t *testing.T) {
	f := newTeamFixture(t, func(c *config.Config) {
		tc := c.Teams["platform"]
		r := *tc.Routing
		r.Lead = ""
		tc.Routing = &r
		c.Teams["platform"] = tc
	})
	out := f.route(t, TeamEvent{Source: "pd.incident", Title: "disk full", Fields: map[string]any{"service": "web"}})
	if out["via"] != "fallback" || out["bot"] != "a" {
		t.Fatalf("fallback: %v", out)
	}
	c := f.caseOf(t, "a", out["case"].(string))
	if c.Routing.Via != "fallback" || c.Routing.Reason != "no rule matched" {
		t.Fatalf("fallback not recorded: %+v", c.Routing)
	}
	if !strings.Contains(f.unreadText("a"), "Routed through team platform to you as its fallback") {
		t.Fatalf("the assignee is not told how the case reached it: %s", f.unreadText("a"))
	}
}

func (f *teamFixture) unreadText(bot string) string {
	var out []string
	for _, m := range f.st.Bot(bot).Inbox {
		if m.Delivered.IsZero() {
			out = append(out, m.Text)
		}
	}
	return strings.Join(out, "\n")
}

// The lead triages with a READ-ONLY worker and assigns; the assignee works
// the original request.
func TestTeamLeadTriagesAndAssigns(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	out := f.route(t, TeamEvent{Source: "chat.mention", Requester: "slack:U1", Title: "web latency", Text: "p99 is 4s", Urgent: true})
	lc := out["case"].(string)
	tc := f.caseOf(t, "lead", lc)
	if tc.Routing.State != "triage" || tc.Payload != "p99 is 4s" || tc.RequesterClass != ReqOperator || tc.Requester != "op" {
		t.Fatalf("triage case: %+v %+v", tc, tc.Routing)
	}
	w := f.triageWorker(t, lc)
	// (A worker conductor starts runs at most on the bot's own trust, so
	// an operator's request triages as bot.)
	if !w.ReadOnly || w.Parent != "conductor" || w.RequesterClass != ReqBot {
		t.Fatalf("the triage worker is not read-only on the request's provenance: %+v", w)
	}
	// Its writes are proposals, whatever the requester.
	if v, _ := f.svc.Gate.CheckVerb(ctx, childID("lead", w), "pd.ack", nil); v.Allow {
		t.Fatal("the triage worker ran a write")
	}
	if !strings.Contains(f.workers.fired[len(f.workers.fired)-1].inputs["prompt"].(string), "conductor bot assign "+lc) {
		t.Fatal("the triage brief does not say how to assign")
	}
	wa := childActor("lead", w)
	// Only the lead assigns; only to a teammate; only its own triage case.
	if _, err := f.svc.Assign(ctx, f.homeOf("a"), lc, "b", ""); err == nil || !strings.Contains(err.Error(), "only team platform's lead (lead)") {
		t.Fatalf("a non-lead assigned: %v", err)
	}
	if _, err := f.svc.Assign(ctx, wa, lc, "c", ""); err == nil {
		t.Fatal("assigned outside the team")
	}
	if _, err := f.svc.Assign(ctx, wa, lc, "solo", ""); err == nil {
		t.Fatal("assigned to a teamless bot")
	}
	other, _ := f.svc.CaseOpen(f.homeOf("lead"), "mine", "")
	if _, err := f.svc.Assign(ctx, wa, other.ID, "b", ""); err == nil {
		t.Fatal("the triage worker assigned a case it was not started on")
	}
	if _, err := f.svc.Assign(ctx, f.homeOf("lead"), other.ID, "b", ""); err == nil {
		t.Fatal("assigned a case that is not a triage case")
	}
	// A routed case the lead itself works (not a triage case) is not
	// re-assignable, even if it carries a routing record.
	routed, err := f.svc.deliverRouted(ctx, Event{Bot: "lead", Source: "pd.x", Title: "mine too"}, "", ReqEvent, store.Routing{Team: "platform", Via: "rule", Rule: "r"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Assign(ctx, f.homeOf("lead"), routed, "b", ""); err == nil || !strings.Contains(err.Error(), "is not a triage case") {
		t.Fatalf("a rule-routed case was re-assigned: %v", err)
	}
	// One triage worker cannot assign ANOTHER triage case.
	out2 := f.route(t, TeamEvent{Source: "pd.x", Title: "second"})
	if _, err := f.svc.Assign(ctx, wa, out2["case"].(string), "b", ""); err == nil || !strings.Contains(err.Error(), "only the triage case it was started on") {
		t.Fatalf("a triage worker assigned another triage case: %v", err)
	}
	res, err := f.svc.Assign(ctx, wa, lc, "b", "looks like the web tier")
	if err != nil {
		t.Fatal(err)
	}
	bc := f.caseOf(t, "b", res["case"].(string))
	if bc.Requester != "op" || bc.RequesterClass != ReqOperator || bc.Source != "chat.mention" || !bc.Urgent || bc.Title != "web latency" {
		t.Fatalf("the assignee's case is not the original request: %+v", bc)
	}
	if bc.Routing.Via != "lead" || bc.Routing.Lead != "lead" || bc.Routing.LeadCase != lc || bc.Routing.Note != "looks like the web tier" {
		t.Fatalf("assignee routing: %+v", bc.Routing)
	}
	in := f.unreadText("b")
	if !strings.Contains(in, "p99 is 4s") || !strings.Contains(in, "<<<TRIAGE NOTE") || !strings.Contains(in, "<<<EVENT") {
		t.Fatalf("the assignee did not get the payload and the fenced note: %s", in)
	}
	// Urgent survives the hop: the assignee's worker started at once.
	if len(f.st.Bot("b").Workers) != 1 {
		t.Fatal("an urgent routed event did not start the assignee's worker")
	}
	tc = f.caseOf(t, "lead", lc)
	if tc.Status != "closed" || tc.Routing.State != "assigned" || tc.Routing.Assignee != "b" || tc.Routing.AssigneeCase != bc.ID {
		t.Fatalf("the lead's case does not record the assignment: %+v %+v", tc, tc.Routing)
	}
	if _, err := f.svc.Assign(ctx, wa, lc, "a", ""); err == nil || !strings.Contains(err.Error(), "not awaiting triage (assigned to b)") {
		t.Fatalf("assigned twice: %v", err)
	}
	// The triage worker's done does not report into the lead's conversation.
	before := f.unreadText("lead")
	f.svc.WorkerDone(ctx, w.Dispatch, "", map[string]any{"summary": "assigned to b"})
	if f.unreadText("lead") != before {
		t.Fatalf("a triage worker reported into the lead's home: %s", f.unreadText("lead"))
	}
}

// PROVENANCE: a third party's request routed through the lead is still that
// third party's request at the assignee's gate; nothing on the way can
// raise it.
func TestTeamRoutingNeverUpgradesTrust(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	assign := func(ev TeamEvent, by Actor, useWorker bool) store.Case {
		t.Helper()
		out := f.route(t, ev)
		lc := out["case"].(string)
		actor := by
		if useWorker {
			actor = childActor("lead", f.triageWorker(t, lc))
		}
		res, err := f.svc.Assign(ctx, actor, lc, "b", "")
		if err != nil {
			t.Fatal(err)
		}
		return f.caseOf(t, "b", res["case"].(string))
	}
	third := assign(TeamEvent{Source: "chat.mention", Requester: "chat:U9", Title: "please ack the page"}, Actor{}, true)
	if third.RequesterClass != ReqUser || third.Requester != "chat:U9" {
		t.Fatalf("a third party's request changed hands: %+v", third)
	}
	// At the gate: b's home runs on that third party's trust — the promoted
	// reversible verb becomes a proposal, attributed to chat:U9.
	v, err := f.svc.Gate.CheckVerb(ctx, flow.SkillIdentity{Bot: "b", BotRole: config.BotRoleHome, Dispatch: f.homeOf("b").Dispatch}, "pd.ack", nil)
	if err != nil || v.Allow {
		t.Fatalf("a routed third-party request ran a promoted write: %+v %v", v, err)
	}
	var p *store.Proposal
	for _, x := range f.st.Bot("b").Proposals {
		p = x
	}
	if p == nil || p.Requester != "chat:U9" || p.RequesterClass != ReqUser || p.CaseID != third.ID {
		t.Fatalf("the proposal is not the third party's: %+v", p)
	}
	_ = f.svc.CaseClose(ctx, f.homeOf("b"), third.ID, "done")

	// An operator of the TEAM who is not one of the assignee's is a third
	// party there (a's operators are [op]; op2 is the team's only).
	tf := newTeamFixture(t, nil)
	out, err := tf.svc.RouteTeam(ctx, TeamEvent{Team: "platform", Source: "chat.mention", Requester: "op2", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	lc := out["case"].(string)
	if c := tf.caseOf(t, "lead", lc); c.RequesterClass != ReqOperator {
		t.Fatalf("op2 is the lead's operator: %+v", c)
	}
	res, err := tf.svc.Assign(ctx, childActor("lead", tf.triageWorker(t, lc)), lc, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	if c := tf.caseOf(t, "a", res["case"].(string)); c.RequesterClass != ReqUser || c.Requester != "op2" {
		t.Fatalf("a team operator who is not the assignee's operator kept operator trust: %+v", c)
	}

	// The lead's home session, while a third party's case is open, cannot
	// hand an EVENT on at more than that third party's trust…
	if _, err := f.svc.Deliver(ctx, Event{Bot: "lead", Source: "chat.dm", Requester: "chat:U7", Title: "hi"}); err != nil {
		t.Fatal(err)
	}
	ev := assign(TeamEvent{Source: "pd.incident", Title: "disk"}, f.homeOf("lead"), false)
	if ev.RequesterClass != ReqUser || ev.Requester != "chat:U7" {
		t.Fatalf("a tainted lead handed an event on untainted: %+v", ev)
	}
	// A worker the tainted lead spawned onto a triage case carries that taint
	// into its assignment too.
	out = f.route(t, TeamEvent{Source: "pd.incident", Title: "disk again"})
	lc = out["case"].(string)
	cw, err := f.svc.Spawn(ctx, f.homeOf("lead"), SpawnRequest{CaseID: lc, Brief: "help triage"})
	if err != nil {
		t.Fatal(err)
	}
	res, err = f.svc.Assign(ctx, childActor("lead", cw), lc, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if c := f.caseOf(t, "b", res["case"].(string)); c.RequesterClass != ReqUser || c.Requester != "chat:U7" {
		t.Fatalf("a tainted lead's child handed an event on untainted: %+v", c)
	}
	// …and an untainted lead never raises one: an event stays an event.
	g := newTeamFixture(t, nil)
	if _, err := g.svc.Deliver(ctx, Event{Bot: "lead", Source: "operator", Requester: "op", Title: "op asks"}); err != nil {
		t.Fatal(err)
	}
	out, _ = g.svc.RouteTeam(ctx, TeamEvent{Team: "platform", Source: "pd.incident", Title: "cpu"})
	lc = out["case"].(string)
	res, err = g.svc.Assign(ctx, g.homeOf("lead"), lc, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if c := g.caseOf(t, "b", res["case"].(string)); c.RequesterClass != ReqEvent || c.Requester != "" {
		t.Fatalf("assignment raised an event's trust: %+v", c)
	}
	// An operator's request assigned by an untainted lead home stays the
	// operator's (provenance is preserved, not reset to the lead's).
	out, _ = g.svc.RouteTeam(ctx, TeamEvent{Team: "platform", Source: "chat.mention", Requester: "slack:U1", Title: "op via chat"})
	lc = out["case"].(string)
	_ = g.svc.CaseClose(ctx, g.homeOf("lead"), firstOpen(g, "lead", "op asks"), "done")
	res, err = g.svc.Assign(ctx, g.homeOf("lead"), lc, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	if c := g.caseOf(t, "b", res["case"].(string)); c.RequesterClass != ReqOperator || c.Requester != "op" {
		t.Fatalf("an operator's routed request lost its requester: %+v", c)
	}
}

func firstOpen(f *teamFixture, bot, title string) string {
	for _, c := range f.st.Bot(bot).Cases {
		if c.Status == "open" && c.Title == title {
			return c.ID
		}
	}
	return ""
}

func TestTeamTriageFallsBack(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	// The triage worker finishes without assigning: the fallback gets it.
	out := f.route(t, TeamEvent{Source: "pd.incident", Requester: "chat:U9", Title: "one"})
	lc := out["case"].(string)
	w := f.triageWorker(t, lc)
	f.svc.WorkerDone(ctx, w.Dispatch, "", map[string]any{"summary": "not sure"})
	tc := f.caseOf(t, "lead", lc)
	if tc.Routing.State != "assigned" || tc.Routing.Assignee != "a" || !strings.Contains(tc.Routing.Reason, "without assigning") {
		t.Fatalf("no fallback after the triage worker ended: %+v", tc.Routing)
	}
	ac := f.caseOf(t, "a", tc.Routing.AssigneeCase)
	if ac.Routing.Via != "fallback" || ac.RequesterClass != ReqUser || ac.Requester != "chat:U9" {
		t.Fatalf("fallback case: %+v %+v", ac, ac.Routing)
	}
	// An assigned case's worker finishing changes nothing and logs nothing.
	var logs []string
	f.svc.Log = func(s string, a ...any) { logs = append(logs, fmt.Sprintf(s, a...)) }
	out = f.route(t, TeamEvent{Source: "pd.incident", Title: "one-b"})
	lc = out["case"].(string)
	w = f.triageWorker(t, lc)
	if _, err := f.svc.Assign(ctx, childActor("lead", w), lc, "b", ""); err != nil {
		t.Fatal(err)
	}
	f.svc.WorkerDone(ctx, w.Dispatch, "", map[string]any{"summary": "assigned"})
	for _, l := range logs {
		if strings.Contains(l, "fallback for") {
			t.Fatalf("an assigned case fell back: %s", l)
		}
	}
	if r := f.caseOf(t, "lead", lc).Routing; r.Assignee != "b" || r.Via != "lead" && r.Reason != "" {
		t.Fatalf("an assigned case was touched again: %+v", r)
	}
	// The lead sits on one past the timeout: the fallback gets it.
	out = f.route(t, TeamEvent{Source: "pd.incident", Title: "two"})
	lc = out["case"].(string)
	f.clk.add(config.DefaultTriageTimeout - time.Second)
	f.svc.Tick(ctx, "lead")
	if f.caseOf(t, "lead", lc).Routing.State != "triage" {
		t.Fatal("fell back before the timeout")
	}
	f.clk.add(time.Second)
	f.svc.Tick(ctx, "lead")
	if r := f.caseOf(t, "lead", lc).Routing; r.State != "assigned" || r.Assignee != "a" || !strings.Contains(r.Reason, "did not assign within 10m") {
		t.Fatalf("no fallback after the timeout: %+v", r)
	}
	// A former lead's open triage still falls back after a lead change.
	out = f.route(t, TeamEvent{Source: "pd.incident", Title: "orphan"})
	lc = out["case"].(string)
	tc2 := f.cfg.Teams["platform"]
	rt2 := *tc2.Routing
	rt2.Lead = "b"
	tc2.Routing = &rt2
	f.cfg.Teams["platform"] = tc2
	f.clk.add(config.DefaultTriageTimeout)
	f.svc.Tick(ctx, "lead")
	if r := f.caseOf(t, "lead", lc).Routing; r.State != "assigned" || r.Assignee != "a" {
		t.Fatalf("a former lead's triage was stranded: %+v", r)
	}

	// The triage worker cannot start: straight to the fallback.
	g := newTeamFixture(t, nil)
	lead := g.cfg.Bots["lead"]
	lead.Worker.MaxConcurrent = 1
	g.cfg.Bots["lead"] = lead
	if _, err := g.svc.Spawn(ctx, g.homeOf("lead"), SpawnRequest{Title: "busy", Brief: "x"}); err != nil {
		t.Fatal(err)
	}
	out, err := g.svc.RouteTeam(ctx, TeamEvent{Team: "platform", Source: "pd.incident", Title: "three"})
	if err != nil {
		t.Fatal(err)
	}
	if out["via"] != "fallback" || out["bot"] != "a" {
		t.Fatalf("an unstartable triage did not fall back: %v", out)
	}
	if c := g.caseOf(t, "a", out["case"].(string)); !strings.Contains(c.Routing.Reason, "triage could not start") {
		t.Fatalf("reason: %+v", c.Routing)
	}
}

// Inside a team bots are peers; across teams nothing changes.
func TestTeamPeersInsideNotAcross(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	if _, err := f.svc.Ask(ctx, f.homeOf("a"), "b", "is the deploy yours?", ""); err != nil {
		t.Fatalf("teammates cannot ask each other: %v", err)
	}
	var pc *store.Case
	for _, c := range f.st.Bot("b").Cases {
		pc = c
	}
	if pc == nil || pc.RequesterClass != ReqPeer || pc.Requester != "bot:a" || !strings.Contains(f.unreadText("b"), "<<<PEER MESSAGE") {
		t.Fatalf("a teammate's message is not a fenced peer case: %+v", pc)
	}
	if _, err := f.svc.Ask(ctx, f.homeOf("a"), "c", "hello billing", ""); err == nil || !strings.Contains(err.Error(), "not linked") {
		t.Fatalf("a cross-team ask passed without a link: %v", err)
	}
	if len(f.st.Bot("c").Cases) != 0 {
		t.Fatal("the other team received something")
	}
	if _, err := f.svc.Ask(ctx, f.homeOf("a"), "solo", "hello", ""); err == nil {
		t.Fatal("a teamless bot was reachable")
	}
	f.cfg.BotLinks = [][]string{{"a", "c"}}
	if _, err := f.svc.Ask(ctx, f.homeOf("a"), "c", "hello billing", ""); err != nil {
		t.Fatalf("an explicit cross-team link: %v", err)
	}
	// peers: none switches intra-team peering off.
	g := newTeamFixture(t, func(c *config.Config) {
		tc := c.Teams["platform"]
		tc.Peers = "none"
		c.Teams["platform"] = tc
	})
	if _, err := g.svc.Ask(ctx, g.homeOf("a"), "b", "hi", ""); err == nil {
		t.Fatal("peers: none still peers")
	}
	// A teammate's worker still cannot message anyone.
	w, err := f.svc.Spawn(ctx, f.homeOf("a"), SpawnRequest{Title: "x", Brief: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Ask(ctx, childActor("a", w), "b", "hi", ""); err == nil {
		t.Fatal("a child messaged a teammate")
	}
}

// KNOWLEDGE: a promotion is a proposal by default, an operator of the team
// approves it, and only then does it land — authored by the bot.
func TestTeamKnowledgePromotionIsAProposal(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	if _, err := f.svc.WriteMemory(ctx, f.homeOf("a"), "knowledge/db.md", "db-prod-1 is the primary", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.WriteMemory(ctx, f.homeOf("a"), "knowledge/private.md", "a's own notes", false, ""); err != nil {
		t.Fatal(err)
	}
	out, err := f.svc.Promote(ctx, f.homeOf("a"), "knowledge/db.md", "knowledge/databases.md", "the team keeps asking")
	if err != nil {
		t.Fatal(err)
	}
	if out["landed"] != false || out["status"] != "pending" {
		t.Fatalf("a promotion landed without approval: %v", out)
	}
	pid := out["proposal"].(string)
	p := f.st.Bot("a").Proposals[pid]
	if p.Kind != KindKnowledge || p.Team != "platform" || strings.Join(p.Approvers, ",") != "op,op2" || p.Knowledge.Text != "db-prod-1 is the primary\n" {
		t.Fatalf("knowledge proposal: %+v %+v", p, p.Knowledge)
	}
	if !strings.Contains(out["message"].(string), "An operator of team platform (op, op2)") {
		t.Fatalf("the bot is not told who approves: %v", out["message"])
	}
	if _, err := f.svc.TeamRead(ctx, "b", "knowledge/databases.md"); err == nil {
		t.Fatal("the promotion is readable before approval")
	}
	// The team's operators decide — op2 is the team's, not a's own.
	if _, err := f.x.Decide(ctx, Decision{Bot: "a", Proposal: pid, Operator: "bop", Decision: "approve", Signature: "x"}); err == nil ||
		!strings.Contains(err.Error(), "not an operator of team platform") {
		t.Fatalf("another team's operator approved: %v", err)
	}
	res, err := f.x.Decide(ctx, Decision{Bot: "a", Proposal: pid, Operator: "op2", Decision: "approve", Signature: "x"})
	if err != nil || res.Status != "executed" || res.Knowledge.Landed == "" {
		t.Fatalf("approved promotion: %+v %v", res, err)
	}
	// Every teammate reads it; the bot's other memory stays its own.
	if text, err := f.svc.TeamRead(ctx, "b", "knowledge/databases.md"); err != nil || !strings.Contains(text, "db-prod-1") {
		t.Fatalf("teammate read: %q %v", text, err)
	}
	if hits, _ := f.svc.TeamRecall(ctx, "lead", "primary"); len(hits) != 1 || hits[0]["path"] != "knowledge/databases.md" {
		t.Fatalf("team recall: %v", hits)
	}
	if hits, _ := f.svc.TeamRecall(ctx, "b", "own notes"); len(hits) != 0 {
		t.Fatal("unpromoted memory leaked into the team")
	}
	if _, err := f.svc.TeamRead(ctx, "c", "knowledge/databases.md"); err == nil {
		t.Fatal("another team read the knowledge")
	}
	if _, err := f.svc.TeamRead(ctx, "solo", "knowledge/databases.md"); err == nil {
		t.Fatal("a teamless bot read the knowledge")
	}
	// One commit, by the bot, with the approval in its trailers.
	repo := f.svc.TeamRepo("platform")
	log, err := repo.git(ctx, repo.Dir, "log", "-1", "--format=%an <%ae>%n%B")
	if err != nil {
		t.Fatal(err)
	}
	if first := strings.SplitN(log, "\n", 2)[0]; first != "bot a <a@bots.conductor.invalid>" {
		t.Errorf("the promotion is not authored by the bot: %q", first)
	}
	for _, want := range []string{"Approved-By: op2", "Bot-Proposal: " + pid, "Promoted-From: knowledge/db.md@"} {
		if !strings.Contains(log, want) {
			t.Errorf("commit lacks %q:\n%s", want, log)
		}
	}
	// A re-approval does nothing.
	if _, err := f.x.Decide(ctx, Decision{Bot: "a", Proposal: pid, Operator: "op", Decision: "approve", Signature: "x"}); err == nil {
		t.Fatal("approved twice")
	}
}

func TestTeamKnowledgePromotionGating(t *testing.T) {
	ctx := context.Background()
	direct := func(c *config.Config) {
		tc := c.Teams["platform"]
		k := *tc.Knowledge
		k.Promotion = "direct"
		tc.Knowledge = &k
		c.Teams["platform"] = tc
	}
	f := newTeamFixture(t, direct)
	write := func(f *teamFixture, path, text string) {
		t.Helper()
		if _, err := f.svc.WriteMemory(ctx, f.homeOf("a"), path, text, false, ""); err != nil {
			t.Fatal(err)
		}
	}
	write(f, "knowledge/a.md", "fact a")
	// direct + the bot's own initiative: lands now.
	out, err := f.svc.Promote(ctx, f.homeOf("a"), "knowledge/a.md", "", "")
	if err != nil || out["landed"] != true {
		t.Fatalf("direct promotion did not land: %v %v", out, err)
	}
	if text, _ := f.svc.TeamRead(ctx, "b", "knowledge/a.md"); !strings.Contains(text, "fact a") {
		t.Fatal("direct promotion not readable")
	}
	// direct, but a third party's case is open: a proposal.
	if _, err := f.svc.Deliver(ctx, Event{Bot: "a", Source: "chat.dm", Requester: "chat:U9", Title: "share this"}); err != nil {
		t.Fatal(err)
	}
	write(f, "knowledge/b.md", "fact b")
	out, err = f.svc.Promote(ctx, f.homeOf("a"), "knowledge/b.md", "", "")
	if err != nil || out["landed"] != false || out["status"] != "pending" {
		t.Fatalf("a tainted direct promotion landed: %v %v", out, err)
	}
	if p := f.st.Bot("a").Proposals[out["proposal"].(string)]; p.RequesterClass != ReqUser || !strings.Contains(p.Reason, "user") {
		t.Fatalf("held proposal provenance: %+v", p)
	}
	// direct, from a read-only worker: a proposal.
	g := newTeamFixture(t, direct)
	write(g, "knowledge/c.md", "fact c")
	w, err := g.svc.Spawn(ctx, g.homeOf("a"), SpawnRequest{Title: "ro", Brief: "x", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	out, err = g.svc.Promote(ctx, childActor("a", w), "knowledge/c.md", "", "")
	if err != nil || out["landed"] != false {
		t.Fatalf("a read-only worker landed a direct promotion: %v %v", out, err)
	}
	// Shadow mode: a shadow proposal, never approvable.
	h := newTeamFixture(t, func(c *config.Config) {
		direct(c)
		m := c.Bots["a"]
		m.Autonomy.Shadow = boolp(true)
		c.Bots["a"] = m
	})
	write(h, "knowledge/d.md", "fact d")
	out, err = h.svc.Promote(ctx, h.homeOf("a"), "knowledge/d.md", "", "")
	if err != nil || out["status"] != "shadow" || out["landed"] != false {
		t.Fatalf("shadow promotion: %v %v", out, err)
	}
	if _, err := h.x.Decide(ctx, Decision{Bot: "a", Proposal: out["proposal"].(string), Operator: "op", Decision: "approve", Signature: "x"}); err == nil {
		t.Fatal("a shadow promotion was approved")
	}
	// What can be promoted, and where to.
	k := newTeamFixture(t, nil)
	write(k, "runbooks/r.md", "step 1")
	if _, err := k.svc.Episode(ctx, k.homeOf("a"), "", "x"); err == nil {
		t.Fatal("fixture: episode without a case")
	}
	hsha, err := k.svc.Handoff(ctx, k.homeOf("a"), "where I left off")
	if err != nil || hsha == "" {
		t.Fatal(err)
	}
	hp, _ := k.svc.Repo("a").LatestHandoff()
	for name, args := range map[string][2]string{
		"an episode":   {"episodes/2026/x.md", ""},
		"a handoff":    {hp, "knowledge/leak.md"},
		"to episodes":  {"runbooks/r.md", "episodes/x.md"},
		"traversal":    {"runbooks/../knowledge/x.md", ""},
		"missing file": {"knowledge/nope.md", ""},
	} {
		if _, err := k.svc.Promote(ctx, k.homeOf("a"), args[0], args[1], ""); err == nil {
			t.Errorf("%s promoted", name)
		}
	}
	if _, err := k.svc.Promote(ctx, k.homeOf("c"), "knowledge/x.md", "", ""); err == nil {
		t.Error("a team without knowledge accepted a promotion")
	}
	if _, err := k.svc.Promote(ctx, k.homeOf("solo"), "knowledge/x.md", "", ""); err == nil {
		t.Error("a teamless bot promoted")
	}
	// The proposal lands exactly what was proposed: a changed text fails.
	out, err = k.svc.Promote(ctx, k.homeOf("a"), "runbooks/r.md", "", "")
	if err != nil {
		t.Fatal(err)
	}
	pid := out["proposal"].(string)
	_ = k.st.UpdateBot("a", func(r *store.BotRecord) error { r.Proposals[pid].Knowledge.Text = "step 1\nrm -rf /\n"; return nil })
	res, err := k.x.Decide(ctx, Decision{Bot: "a", Proposal: pid, Operator: "op", Decision: "approve", Signature: "x"})
	if err == nil || res.Status != "failed" {
		t.Fatalf("a tampered promotion landed: %+v %v", res, err)
	}
	if _, err := k.svc.TeamRead(ctx, "b", "runbooks/r.md"); err == nil {
		t.Fatal("the tampered text reached the team")
	}
	// The digest covers the destination too.
	write(k, "runbooks/s.md", "step 2")
	out, _ = k.svc.Promote(ctx, k.homeOf("a"), "runbooks/s.md", "", "")
	pid = out["proposal"].(string)
	_ = k.st.UpdateBot("a", func(r *store.BotRecord) error { r.Proposals[pid].Knowledge.To = "runbooks/other.md"; return nil })
	if res, err := k.x.Decide(ctx, Decision{Bot: "a", Proposal: pid, Operator: "op", Decision: "approve", Signature: "x"}); err == nil || res.Status != "failed" {
		t.Fatalf("a redirected promotion landed: %+v %v", res, err)
	}
	// A bot that left the team cannot land into it.
	write(k, "runbooks/t.md", "step 3")
	out, _ = k.svc.Promote(ctx, k.homeOf("a"), "runbooks/t.md", "", "")
	pid = out["proposal"].(string)
	m := k.cfg.Bots["a"]
	m.Team = ""
	k.cfg.Bots["a"] = m
	if res, err := k.x.Decide(ctx, Decision{Bot: "a", Proposal: pid, Operator: "op", Decision: "approve", Signature: "x"}); err == nil || res.Status == "executed" {
		t.Fatalf("a former bot's promotion landed: %+v %v", res, err)
	}
}

// The proactive budget is a POOL across the team's bots.
func TestTeamProactivePool(t *testing.T) {
	ctx := context.Background()
	f := newTeamFixture(t, nil)
	// Set up after the fixture's first ticks, so no sweep starts on its own.
	func(c *config.Config) {
		tc := c.Teams["platform"]
		tc.Budget.Proactive = &config.ProactiveBudget{MaxRunsPerDay: 2, MaxCostPerDay: 1}
		c.Teams["platform"] = tc
		for _, n := range []string{"a", "b", "c"} {
			m := c.Bots[n]
			m.Charter = "## Checks\n\n- look"
			m.Proactive = &config.BotProactive{Every: config.Duration(time.Hour), Checklist: config.ProactiveChecklist{Section: "Checks"},
				Budget: config.ProactiveBudget{MaxRunsPerDay: 5}}
			if n != "c" {
				m.Proactive.Budget = config.ProactiveBudget{} // inherit the pool
			}
			c.Bots[n] = m
		}
		// c is in another team: its pool is not platform's.
	}(f.cfg)
	if err := f.cfg.ResolveTeams(); err != nil {
		t.Fatal(err)
	}
	if b := f.cfg.Bots["a"].Proactive.Budget; b.MaxRunsPerDay != 2 {
		t.Fatalf("the pool was not inherited: %+v", b)
	}
	finish := func(bot string) {
		for _, w := range f.st.Bot(bot).Workers {
			if w.Sweep != "" && liveWorker(w) {
				f.svc.WorkerDone(ctx, w.Dispatch, "", map[string]any{"summary": "quiet"})
			}
		}
	}
	// Another team's bot sweeping first is not charged to this pool.
	if _, err := f.svc.StartSweep(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	finish("c")
	if _, err := f.svc.StartSweep(ctx, "a", true); err != nil {
		t.Fatal(err)
	}
	finish("a")
	if _, err := f.svc.StartSweep(ctx, "b", true); err != nil {
		t.Fatalf("another team's sweep counted against this pool: %v", err)
	}
	finish("b")
	// a has used 1 of its own 2, but the team has used 2 of 2.
	_, err := f.svc.StartSweep(ctx, "a", true)
	if err == nil || !strings.Contains(err.Error(), "team platform's budget: its bots ran 2 sweeps") {
		t.Fatalf("the pool did not stop a teammate: %v", err)
	}
	if _, err := f.svc.StartSweep(ctx, "c", true); err != nil {
		t.Fatalf("another team's bot was charged to this pool: %v", err)
	}
	finish("c")
	// The window rolls.
	f.clk.add(SweepWindow)
	if _, err := f.svc.StartSweep(ctx, "a", true); err != nil {
		t.Fatalf("the pool did not roll over: %v", err)
	}
	finish("a")
	// Cost counts across the team too.
	_ = f.st.UpdateBot("a", func(r *store.BotRecord) error {
		for _, sw := range r.Sweeps {
			if f.clk.now().Sub(sw.Started) < SweepWindow {
				sw.CostUSD = 1
			}
		}
		return nil
	})
	if _, err := f.svc.StartSweep(ctx, "b", true); err == nil || !strings.Contains(err.Error(), "cost $1.00") {
		t.Fatalf("pool cost: %v", err)
	}
}

func TestTeamIsolationIsEnforced(t *testing.T) {
	ctx := context.Background()
	f := newTeamFixture(t, func(c *config.Config) {
		tc := c.Teams["platform"]
		tc.Isolation = config.BotIsolation{Sandbox: "required", HandoffWorkers: boolp(false)}
		c.Teams["platform"] = tc
		m := c.Bots["b"]
		m.Charter = "## Checks\n\n- look"
		m.Proactive = &config.BotProactive{Every: config.Duration(time.Hour), Checklist: config.ProactiveChecklist{Section: "Checks"},
			Budget: config.ProactiveBudget{MaxRunsPerDay: 5}}
		c.Bots["b"] = m
	})
	// Sandboxed: sessions start; hand-off workers stay off.
	if f.st.Bot("a").Home == nil {
		t.Fatal("a sandboxed bot did not start")
	}
	if _, err := f.svc.Spawn(ctx, f.homeOf("a"), SpawnRequest{Title: "x", Brief: "x", Handoff: true}); err == nil || !strings.Contains(err.Error(), "isolation.handoff_workers: false, team platform") {
		t.Fatalf("a hand-off worker started: %v", err)
	}
	if _, err := f.svc.Spawn(ctx, f.homeOf("a"), SpawnRequest{Title: "x", Brief: "x"}); err != nil {
		t.Fatalf("a cli worker: %v", err)
	}
	// Not sandboxed: nothing of theirs starts.
	f.svc.Gate.Sandbox = fixedJail(false)
	if _, err := f.svc.Spawn(ctx, f.homeOf("a"), SpawnRequest{Title: "y", Brief: "y"}); err == nil || !strings.Contains(err.Error(), "requires conductor's agent sandbox") {
		t.Fatalf("a worker started unsandboxed: %v", err)
	}
	if err := f.svc.Rotate(ctx, "a", "test"); err == nil {
		t.Fatal("a home session started unsandboxed")
	}
	if _, err := f.svc.StartSweep(ctx, "b", true); err == nil || !strings.Contains(err.Error(), "requires conductor's agent sandbox") {
		t.Fatalf("a sweep started unsandboxed: %v", err)
	}
	// The bot outside the team is unaffected.
	if _, err := f.svc.Spawn(ctx, f.homeOf("solo"), SpawnRequest{Title: "z", Brief: "z", Handoff: true}); err != nil {
		t.Fatalf("a teamless bot was held to the team's isolation: %v", err)
	}
	// A tick never launches an unsandboxed required bot.
	k := newTeamFixture(t, nil)
	k.svc.Gate.Sandbox = fixedJail(false)
	m := k.cfg.Bots["lead"]
	m.Isolation.Sandbox = "required"
	k.cfg.Bots["lead"] = m
	_ = k.st.UpdateBot("lead", func(r *store.BotRecord) error { r.Home = nil; return nil })
	var logs []string
	k.svc.Log = func(s string, a ...any) { logs = append(logs, fmt.Sprintf(s, a...)) }
	k.svc.Tick(ctx, "lead")
	k.clk.add(2 * LaunchBackoff)
	k.svc.Tick(ctx, "lead")
	if k.st.Bot("lead").Home != nil {
		t.Fatal("a tick launched a bot that requires the sandbox")
	}
	// …and says so once, not every tick.
	if len(logs) != 1 || !strings.Contains(logs[0], "requires conductor's agent sandbox") {
		t.Fatalf("sandbox hold logged %d times: %q", len(logs), logs)
	}
}

// Every proposal says which operators may decide it, and its team.
func TestProposalsNameTheirApprovers(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	if _, err := f.svc.Deliver(ctx, Event{Bot: "a", Source: "chat.dm", Requester: "chat:U9", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	v, err := f.svc.Gate.CheckVerb(ctx, flow.SkillIdentity{Bot: "a", BotRole: config.BotRoleHome, Dispatch: f.homeOf("a").Dispatch}, "kv.set", nil)
	if err != nil || v.Allow {
		t.Fatal("fixture")
	}
	var p *store.Proposal
	for _, x := range f.st.Bot("a").Proposals {
		p = x
	}
	if p.Team != "platform" || strings.Join(p.Approvers, ",") != "op" {
		t.Fatalf("a bot proposal names the bot's operators: %+v", p)
	}
	if !strings.Contains(v.Output["message"].(string), "An operator (op)") {
		t.Fatalf("message: %v", v.Output["message"])
	}
	if got := Approvers(f.cfg, "a", *p); strings.Join(got, ",") != "op" {
		t.Fatalf("approvers: %v", got)
	}
	if _, err := f.x.Decide(ctx, Decision{Bot: "a", Proposal: p.ID, Operator: "op2", Decision: "deny", Signature: "x"}); err == nil {
		t.Fatal("a team operator who is not the bot's decided a bot proposal")
	}
	// The operator's view.
	out, err := f.svc.OperatorOp(ctx, "proposal", map[string]any{"bot": "a", "id": p.ID})
	if err != nil || out["team"] != "platform" {
		t.Fatalf("operator proposal view: %v %v", out, err)
	}
	ts, err := f.svc.OperatorOp(ctx, "team", map[string]any{"team": "platform"})
	if err != nil || strings.Join(ts["bots"].([]string), ",") != "a,b,lead" {
		t.Fatalf("team view: %v %v", ts, err)
	}
	if k, ok := ts["knowledge"].(map[string]any); !ok || k["promotion"] != "proposal" {
		t.Fatalf("team view knowledge: %v", ts["knowledge"])
	}
	sctx, cancel := context.WithCancel(context.Background())
	f.svc.Start(sctx)
	cancel()
	if _, err := os.Stat(filepath.Join(f.cfg.TeamKnowledgeDir("platform"), ".git")); err != nil {
		t.Fatalf("Start did not create the knowledge repo: %v", err)
	}
}
