package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

func (f *teamFixture) deliver(t *testing.T, ev Event) string {
	t.Helper()
	id, err := f.svc.Deliver(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *teamFixture) delegate(t *testing.T, from, to, caseID, task string) (store.Case, store.Delegation) {
	t.Helper()
	out, err := f.svc.Delegate(context.Background(), f.homeOf(from), to, task, caseID)
	if err != nil {
		t.Fatal(err)
	}
	d := f.st.Bot(from).Delegations[out["delegation"].(string)]
	if d == nil {
		t.Fatalf("%s kept no edge for %v", from, out)
	}
	return f.caseOf(t, to, out["peer_case"].(string)), *d
}

func (f *teamFixture) auditsOf(event, direction string) []map[string]any {
	var out []map[string]any
	for _, a := range f.audits {
		if a["event"] == event && a["direction"] == direction {
			out = append(out, a)
		}
	}
	return out
}

func addPlatformBots(names ...string) func(*config.Config) {
	return func(c *config.Config) {
		for _, n := range names {
			m := testBot()
			m.MemoryRepo = c.Bots["a"].MemoryRepo + "-" + n
			m.Team = "platform"
			c.Bots[n] = m
		}
	}
}

// A delegates to teammate B; B works the case and its outcome returns to
// A's case, fenced, exactly once; the edge records who handed what to whom.
func TestDelegateWorkReturnsToTheDelegatorsCase(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	ac := f.deliver(t, Event{Bot: "a", Source: "pd.incident", Title: "db latency"})
	bc, d := f.delegate(t, "a", "b", ac, "check the replica lag on db-main")

	if bc.Delegation == nil || bc.Delegation.From != "a" || bc.Delegation.FromCase != ac || bc.Delegation.ID != d.ID {
		t.Fatalf("the delegated case is not linked to the delegator's: %+v", bc)
	}
	if bc.Source != "bot:a" || bc.RequesterClass != ReqEvent || bc.Status != "open" || bc.Payload != "check the replica lag on db-main" {
		t.Fatalf("delegated case: %+v", bc)
	}
	if d.From != "a" || d.To != "b" || d.ToCase != bc.ID || d.FromCase != ac || d.Status != DelegationOpen || d.Depth != 1 || strings.Join(d.Chain, ",") != "a" {
		t.Fatalf("edge: %+v", d)
	}
	in := f.unreadText("b")
	if !strings.Contains(in, "<<<DELEGATED TASK") || !strings.Contains(in, "check the replica lag") || !strings.Contains(in, "conductor bot decline "+bc.ID) {
		t.Fatalf("the recipient was not told, fenced: %s", in)
	}
	if !strings.Contains(f.caseOf(t, "a", ac).Summary, "delegated to b ("+d.ID+")") {
		t.Fatalf("the delegator's case does not note it: %+v", f.caseOf(t, "a", ac))
	}
	if len(f.auditsOf("bot_delegation", "sent")) != 1 || len(f.auditsOf("bot_delegation", "received")) != 1 {
		t.Fatalf("not audited on both sides: %v", f.audits)
	}
	if r := f.auditsOf("bot_delegation", "received")[0]; r["bot"] != "b" || r["case"] != bc.ID || r["text"] != "check the replica lag on db-main" {
		t.Fatalf("received audit: %v", r)
	}

	if err := f.svc.CaseClose(ctx, f.homeOf("b"), bc.ID, "lag is 3s, caused by a long vacuum"); err != nil {
		t.Fatal(err)
	}
	if d := f.st.Bot("a").Delegations[d.ID]; d.Status != DelegationDone || d.Result != "lag is 3s, caused by a long vacuum" {
		t.Fatalf("edge after the result: %+v", d)
	}
	var msgs []*store.InboxMsg
	for _, m := range f.st.Bot("a").Inbox {
		if strings.Contains(m.Text, "DELEGATION RESULT") {
			msgs = append(msgs, m)
		}
	}
	if len(msgs) != 1 || msgs[0].CaseID != ac || !strings.Contains(msgs[0].Text, "<<<DELEGATION RESULT") || !strings.Contains(msgs[0].Text, "long vacuum") {
		t.Fatalf("the result did not come back to a's case, fenced: %+v", msgs)
	}
	if len(f.auditsOf("bot_delegation", "done")) != 1 || len(f.auditsOf("bot_delegation", "done_received")) != 1 {
		t.Fatalf("the result is not audited on both sides: %v", f.audits)
	}
	// Once: closing again reports nothing more.
	_ = f.svc.CaseClose(ctx, f.homeOf("b"), bc.ID, "again")
	if n := strings.Count(f.unreadText("a"), "<<<DELEGATION RESULT"); n != 1 {
		t.Fatalf("the result was reported %d times", n)
	}
	// An ordinary case's close reports nowhere.
	own, _ := f.svc.CaseOpen(f.homeOf("b"), "mine", "")
	before := len(f.st.Bot("a").Inbox)
	_ = f.svc.CaseClose(ctx, f.homeOf("b"), own.ID, "done")
	if len(f.st.Bot("a").Inbox) != before {
		t.Fatal("an ordinary case reported to a peer")
	}
	// The listings: a's out-edge, b's in-case.
	if out := f.svc.Delegations("a", true)["out"].([]any); len(out) != 1 {
		t.Fatalf("a's delegations: %v", out)
	}
	if in := f.svc.Delegations("b", true)["in"].([]any); len(in) != 1 || in[0].(map[string]any)["from"] != "a" {
		t.Fatalf("b's delegations: %v", in)
	}
	ts := f.svc.teamStatus(ctx, "platform", true)
	if ds := ts["delegations"].([]any); len(ds) != 1 || ds[0].(map[string]any)["from"] != "a" || ds[0].(map[string]any)["to"] != "b" {
		t.Fatalf("the team view does not show the edge: %v", ts["delegations"])
	}
}

// A delegation without --case opens one in the delegator.
func TestDelegateWithoutACaseOpensOne(t *testing.T) {
	f := newTeamFixture(t, nil)
	bc, d := f.delegate(t, "a", "b", "", "rotate the cache keys")
	ac := f.caseOf(t, "a", d.FromCase)
	if ac.Status != "open" || ac.Source != "bot" || !strings.Contains(ac.Title, "delegated to b") || bc.Delegation.FromCase != ac.ID {
		t.Fatalf("delegator case: %+v / %+v", ac, bc)
	}
}

// The recipient declines with a reason, which goes back fenced.
func TestDelegateDecline(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	ac := f.deliver(t, Event{Bot: "a", Source: "pd.incident", Title: "x"})
	bc, d := f.delegate(t, "a", "b", ac, "resize the web tier")
	if _, err := f.svc.Decline(ctx, f.homeOf("b"), bc.ID, ""); err == nil {
		t.Fatal("a decline without a reason")
	}
	w, err := f.svc.Spawn(ctx, f.homeOf("b"), SpawnRequest{CaseID: bc.ID, Brief: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Decline(ctx, childActor("b", w), bc.ID, "no"); err == nil {
		t.Fatal("a worker declined")
	}
	own, _ := f.svc.CaseOpen(f.homeOf("b"), "mine", "")
	if _, err := f.svc.Decline(ctx, f.homeOf("b"), own.ID, "no"); err == nil {
		t.Fatal("declined a case nobody delegated")
	}
	if _, err := f.svc.Decline(ctx, f.homeOf("b"), bc.ID, "web is not in my scope — billing-sre owns it"); err != nil {
		t.Fatal(err)
	}
	if c := f.caseOf(t, "b", bc.ID); c.Status != "closed" || !strings.HasPrefix(c.Outcome, "declined: ") {
		t.Fatalf("declined case: %+v", c)
	}
	if e := f.st.Bot("a").Delegations[d.ID]; e.Status != DelegationDeclined || !strings.Contains(e.Result, "billing-sre owns it") {
		t.Fatalf("edge after decline: %+v", e)
	}
	in := f.unreadText("a")
	if !strings.Contains(in, "<<<DELEGATION DECLINED") || !strings.Contains(in, "billing-sre owns it") || strings.Contains(in, "DELEGATION RESULT") {
		t.Fatalf("the refusal did not come back fenced: %s", in)
	}
	if _, err := f.svc.Decline(ctx, f.homeOf("b"), bc.ID, "again"); err == nil {
		t.Fatal("declined a closed case")
	}
	if len(f.auditsOf("bot_delegation", "declined")) != 1 || len(f.auditsOf("bot_delegation", "declined_received")) != 1 {
		t.Fatalf("decline not audited on both sides: %v", f.audits)
	}
}

// PROVENANCE: the recipient's case carries the less trusted of the
// delegator's case's requester and the delegator's own trust — delegation
// never upgrades.
func TestDelegationNeverUpgradesTrust(t *testing.T) {
	ctx := context.Background()
	gateAt := func(f *teamFixture, bot string) flow.BotVerdict {
		t.Helper()
		v, err := f.svc.Gate.CheckVerb(ctx, flow.SkillIdentity{Bot: bot, BotRole: config.BotRoleHome, Dispatch: f.homeOf(bot).Dispatch}, "pd.ack", nil)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	// A third party's request delegated is that third party's at B's gate,
	// and stays it when B delegates it onward.
	f := newTeamFixture(t, nil)
	ac := f.deliver(t, Event{Bot: "a", Source: "chat.mention", Requester: "chat:U9", Title: "ack the page"})
	bc, d := f.delegate(t, "a", "b", ac, "ack the page")
	if bc.RequesterClass != ReqUser || bc.Requester != "chat:U9" || d.RequesterClass != ReqUser {
		t.Fatalf("a third party's request changed hands: %+v", bc)
	}
	if v := gateAt(f, "b"); v.Allow {
		t.Fatalf("a delegated third-party request ran a promoted write at the recipient: %+v", v)
	}
	var p *store.Proposal
	for _, x := range f.st.Bot("b").Proposals {
		p = x
	}
	if p == nil || p.Requester != "chat:U9" || p.RequesterClass != ReqUser || p.CaseID != bc.ID {
		t.Fatalf("the recipient's proposal is not the third party's: %+v", p)
	}
	lc, _ := f.delegate(t, "b", "lead", bc.ID, "ack the page")
	if lc.RequesterClass != ReqUser || lc.Requester != "chat:U9" {
		t.Fatalf("delegated onward, the third party's request was upgraded: %+v", lc)
	}
	// A worker the recipient spawns on it carries the same.
	w, err := f.svc.Spawn(ctx, f.homeOf("b"), SpawnRequest{CaseID: bc.ID, Brief: "ack"})
	if err != nil {
		t.Fatal(err)
	}
	if w.RequesterClass != ReqUser || w.Requester != "chat:U9" {
		t.Fatalf("the recipient's child was untainted: %+v", w)
	}

	// A tainted delegator (a third party's case open) cannot hand an EVENT
	// on at more than that third party's trust.
	g := newTeamFixture(t, nil)
	g.deliver(t, Event{Bot: "a", Source: "chat.dm", Requester: "chat:U7", Title: "hi"})
	ev := g.deliver(t, Event{Bot: "a", Source: "pd.incident", Title: "disk"})
	bc, _ = g.delegate(t, "a", "b", ev, "free some disk")
	if bc.RequesterClass != ReqUser || bc.Requester != "chat:U7" {
		t.Fatalf("a tainted delegator handed an event on untainted: %+v", bc)
	}
	// …nor work of its own initiative.
	bc, _ = g.delegate(t, "a", "lead", "", "look at the dashboards")
	if bc.RequesterClass != ReqUser || bc.Requester != "chat:U7" {
		t.Fatalf("a tainted delegator's own task was untainted: %+v", bc)
	}

	// A delegator tainted by something MORE trusted than the case's requester
	// (a proactive finding is open) still hands on the third party's class.
	pf := newTeamFixture(t, nil)
	if err := pf.st.UpdateBot("a", func(r *store.BotRecord) error {
		if r.Cases == nil {
			r.Cases = map[string]*store.Case{}
		}
		r.Cases["c900"] = &store.Case{ID: "c900", Title: "finding", Status: "open", Source: "proactive", Requester: "sweep x1", RequesterClass: ReqProactive}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	uc := pf.deliver(t, Event{Bot: "a", Source: "chat.mention", Requester: "chat:U3", Title: "restart web"})
	bc, _ = pf.delegate(t, "a", "b", uc, "restart web")
	if bc.RequesterClass != ReqUser || bc.Requester != "chat:U3" {
		t.Fatalf("a third party's request took the delegator's (more trusted) taint: %+v", bc)
	}

	// An untainted delegator keeps an operator's request the operator's,
	// re-checked at the recipient (op is an operator of both a and b)…
	h := newTeamFixture(t, nil)
	oc := h.deliver(t, Event{Bot: "a", Source: "operator", Requester: "op", Title: "check db"})
	bc, _ = h.delegate(t, "a", "b", oc, "check db")
	if bc.RequesterClass != ReqOperator || bc.Requester != "op" {
		t.Fatalf("an operator's request lost its requester: %+v", bc)
	}
	// …but an operator of the delegator who is not the recipient's is a
	// third party there (a's operators are [op]; op2 is b's, not a's).
	o2 := h.deliver(t, Event{Bot: "b", Source: "operator", Requester: "op2", Title: "x"})
	if c := h.caseOf(t, "b", o2); c.RequesterClass != ReqOperator {
		t.Fatalf("op2 is b's operator: %+v", c)
	}
	_ = h.svc.CaseClose(ctx, h.homeOf("b"), bc.ID, "done")
	ac2, _ := h.delegate(t, "b", "a", o2, "x")
	if ac2.RequesterClass != ReqUser || ac2.Requester != "op2" {
		t.Fatalf("another bot's operator kept operator trust at the recipient: %+v", ac2)
	}
	// An untainted bot's own task is a PEER's request at the recipient:
	// a bot's own trust never crosses.
	k := newTeamFixture(t, nil)
	bc, _ = k.delegate(t, "a", "b", "", "tidy the runbooks")
	if bc.RequesterClass != ReqPeer || bc.Requester != "bot:a" {
		t.Fatalf("a bot's own initiative crossed as the recipient's own: %+v", bc)
	}
	if v := gateAt(k, "b"); v.Allow {
		t.Fatalf("a peer's task ran a promoted write unapproved: %+v", v)
	}
	// An event stays an event.
	e := newTeamFixture(t, nil)
	ec := e.deliver(t, Event{Bot: "a", Source: "pd.incident", Title: "cpu"})
	bc, _ = e.delegate(t, "a", "b", ec, "cpu")
	if bc.RequesterClass != ReqEvent || bc.Requester != "" {
		t.Fatalf("delegation changed an event's trust: %+v", bc)
	}
}

func TestDelegatedProvenance(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		caseWho, caseClass, pc, pw string
		wantWho, wantClass         string
	}{
		{"operator, untainted", "op", ReqOperator, ReqBot, "", "op", ReqOperator},
		{"operator, tainted by a user", "op", ReqOperator, ReqUser, "chat:U1", "chat:U1", ReqUser},
		{"event, tainted by a peer", "", ReqEvent, ReqPeer, "bot:x", "bot:x", ReqPeer},
		{"user, untainted", "chat:U2", ReqUser, ReqBot, "", "chat:U2", ReqUser},
		{"user, tainted by something more trusted", "chat:U2", ReqUser, ReqProactive, "sweep x1", "chat:U2", ReqUser},
		{"peer, tainted by an event", "bot:x", ReqPeer, ReqEvent, "", "bot:x", ReqPeer},
		{"own initiative", "", ReqBot, ReqBot, "", "bot:a", ReqPeer},
		{"own initiative, proactive taint", "", ReqBot, ReqProactive, "sweep x1", "sweep x1", ReqProactive},
		{"unknown class", "who", "weird", ReqBot, "", "who", ReqUser},
	} {
		who, class := delegatedProvenance("a", tc.caseWho, tc.caseClass, tc.pc, tc.pw)
		if who != tc.wantWho || class != tc.wantClass {
			t.Errorf("%s: got %s/%s, want %s/%s", tc.name, who, class, tc.wantWho, tc.wantClass)
		}
	}
}

// SCOPE: teammates by default; across teams only with an operator's link;
// peers: none turns delegation off with ask.
func TestDelegationScope(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "c", "hello billing", ""); err == nil || !strings.Contains(err.Error(), "may not delegate") {
		t.Fatalf("a cross-team delegation passed without a link: %v", err)
	}
	if len(f.st.Bot("c").Cases) != 0 {
		t.Fatal("the other team received a case")
	}
	if r := f.auditsOf("bot_delegation", "refused"); len(r) != 1 || r[0]["peer"] != "c" {
		t.Fatalf("the refusal is not audited: %v", r)
	}
	if len(f.st.Bot("a").Delegations) != 0 {
		t.Fatal("a refused delegation left an edge")
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "solo", "x", ""); err == nil {
		t.Fatal("a teamless bot was reachable")
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "a", "x", ""); err == nil {
		t.Fatal("a bot delegated to itself")
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "nobody", "x", ""); err == nil {
		t.Fatal("delegated to an unknown bot")
	}
	f.cfg.BotLinks = [][]string{{"a", "c"}}
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "c", "hello billing", ""); err != nil {
		t.Fatalf("an explicit cross-team link: %v", err)
	}
	// A worker cannot delegate.
	w, err := f.svc.Spawn(ctx, f.homeOf("a"), SpawnRequest{Title: "x", Brief: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Delegate(ctx, childActor("a", w), "b", "x", ""); err == nil {
		t.Fatal("a child delegated")
	}
	// Bounds on the text.
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "b", "  ", ""); err == nil {
		t.Fatal("an empty task")
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "b", strings.Repeat("x", MaxPeerMessage+1), ""); err == nil {
		t.Fatal("an oversized task")
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "b", "x", "c999"); err == nil {
		t.Fatal("delegated off a case that does not exist")
	}
	// peers: none switches both off between teammates.
	g := newTeamFixture(t, func(c *config.Config) {
		tc := c.Teams["platform"]
		tc.Peers = "none"
		c.Teams["platform"] = tc
	})
	if _, err := g.svc.Delegate(ctx, g.homeOf("a"), "b", "x", ""); err == nil {
		t.Fatal("peers: none still delegates")
	}
	if _, err := g.svc.Ask(ctx, g.homeOf("a"), "b", "x", ""); err == nil {
		t.Fatal("peers: none still asks")
	}
	if len(g.st.Bot("b").Cases) != 0 {
		t.Fatal("peers: none delivered something")
	}
}

// NO LOOPS: a chain cannot reach a bot already in it — not even through a
// fresh case — and cannot go deeper than either side's max_depth.
func TestDelegationLoopsAndDepth(t *testing.T) {
	ctx := context.Background()
	f := newTeamFixture(t, addPlatformBots("d", "e"))
	ac := f.deliver(t, Event{Bot: "a", Source: "pd.incident", Title: "x"})
	bc, _ := f.delegate(t, "a", "b", ac, "hop 1")
	if _, err := f.svc.Delegate(ctx, f.homeOf("b"), "a", "back to you", bc.ID); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("a delegation routed back to its delegator: %v", err)
	}
	// A fresh case does not reset the chain: b's home still holds a's case.
	if _, err := f.svc.Delegate(ctx, f.homeOf("b"), "a", "back to you", ""); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("a fresh case laundered a loop: %v", err)
	}
	if n := len(f.st.Bot("a").Cases); n != 1 {
		t.Fatalf("a loop delivered a case back to a (%d cases)", n)
	}
	lc, ld := f.delegate(t, "b", "lead", bc.ID, "hop 2")
	if ld.Depth != 2 || strings.Join(ld.Chain, ",") != "a,b" {
		t.Fatalf("hop 2 edge: %+v", ld)
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("lead"), "a", "x", lc.ID); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("a chain reached an ancestor two hops back: %v", err)
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("lead"), "b", "x", lc.ID); err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("a chain reached its delegator: %v", err)
	}
	dc, dd := f.delegate(t, "lead", "d", lc.ID, "hop 3") // the default max
	if dd.Depth != 3 || dc.Delegation.Depth != 3 {
		t.Fatalf("hop 3: %+v", dd)
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("d"), "e", "hop 4", dc.ID); err == nil || !strings.Contains(err.Error(), "too deep") {
		t.Fatalf("a chain went past the default depth: %v", err)
	}
	if len(f.st.Bot("e").Cases) != 0 {
		t.Fatal("hop 4 delivered")
	}
	// Once b closes a's case, b may hand a work again (no loop).
	_ = f.svc.CaseClose(ctx, f.homeOf("b"), bc.ID, "done")
	if _, err := f.svc.Delegate(ctx, f.homeOf("b"), "a", "your turn", ""); err != nil {
		t.Fatalf("a closed chain still blocks: %v", err)
	}

	// The team's max_depth.
	g := newTeamFixture(t, func(c *config.Config) {
		tc := c.Teams["platform"]
		tc.Delegation = &config.TeamDelegation{MaxDepth: 1}
		c.Teams["platform"] = tc
	})
	bc, _ = g.delegate(t, "a", "b", "", "hop 1")
	if _, err := g.svc.Delegate(ctx, g.homeOf("b"), "lead", "hop 2", bc.ID); err == nil || !strings.Contains(err.Error(), "too deep") {
		t.Fatalf("max_depth 1 allowed hop 2: %v", err)
	}
	// The recipient's team's max applies too: platform default 3, billing 1.
	h := newTeamFixture(t, func(c *config.Config) {
		tc := c.Teams["billing"]
		tc.Delegation = &config.TeamDelegation{MaxDepth: 1}
		c.Teams["billing"] = tc
		c.BotLinks = [][]string{{"b", "c"}}
	})
	bc, _ = h.delegate(t, "a", "b", "", "hop 1")
	if _, err := h.svc.Delegate(ctx, h.homeOf("b"), "c", "hop 2", bc.ID); err == nil || !strings.Contains(err.Error(), "too deep") {
		t.Fatalf("the recipient team's max_depth was ignored: %v", err)
	}
}

// RATE LIMIT: per direction per hour.
func TestDelegationRateLimit(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	for i := 0; i < MaxDelegationsPerHour; i++ {
		if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "b", "task", ""); err != nil {
			t.Fatalf("delegation %d: %v", i, err)
		}
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "b", "task", ""); err == nil || !strings.Contains(err.Error(), "last hour") {
		t.Fatalf("the rate limit did not hold: %v", err)
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "lead", "task", ""); err != nil {
		t.Fatalf("the limit is per direction: %v", err)
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("b"), "lead", "task", ""); err != nil {
		t.Fatalf("the limit is per sender: %v", err)
	}
	f.clk.add(time.Hour)
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "b", "task", ""); err != nil {
		t.Fatalf("an hour later: %v", err)
	}
}

func TestDelegationEdgesStayBounded(t *testing.T) {
	r := &store.BotRecord{Delegations: map[string]*store.Delegation{}}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < delegationRetention+5; i++ {
		id := r.NextID("d")
		st := DelegationDone
		if i < 3 {
			st = DelegationOpen
		}
		r.Delegations[id] = &store.Delegation{ID: id, Status: st, Updated: t0.Add(time.Duration(i) * time.Minute)}
	}
	pruneDelegations(r)
	if len(r.Delegations) != delegationRetention {
		t.Fatalf("kept %d", len(r.Delegations))
	}
	for _, id := range []string{"d1", "d2", "d3"} {
		if r.Delegations[id] == nil {
			t.Fatalf("an open edge %s was dropped", id)
		}
	}
	if r.Delegations["d4"] != nil || r.Delegations["d8"] != nil || r.Delegations["d9"] == nil {
		t.Fatal("not the oldest finished edges dropped")
	}
}

// The home session's prompts and handoffs know about open delegations.
func TestDelegationInTheLedgerSummary(t *testing.T) {
	f := newTeamFixture(t, nil)
	_, d := f.delegate(t, "a", "b", "", "rotate keys")
	if s := LedgerSummary(f.st.Bot("a")); !strings.Contains(s, "delegation "+d.ID+" to b") {
		t.Fatalf("summary: %s", s)
	}
}
