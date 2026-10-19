package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

var opActor = TicketActor{Operator: "op"}

func (f *teamFixture) ticket(t *testing.T, a TicketActor, n TicketNew) TicketView {
	t.Helper()
	v, err := f.svc.TicketCreate(context.Background(), a, n)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (f *teamFixture) inboxCount(bot string) int { return len(f.st.Bot(bot).Inbox) }

// THE DISPATCH GATE: a draft starts nothing — it reaches no inbox, no
// routing runs, and no worker or delegation can hang off it.
func TestTicketDraftStartsNothing(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	before := map[string]int{"lead": f.inboxCount("lead"), "a": f.inboxCount("a"), "b": f.inboxCount("b")}
	d := f.ticket(t, opActor, TicketNew{Title: "rotate the cache keys", Description: "before Friday", Assignee: "bot:a"})
	if d.Status != store.TicketDraft || d.Holder != "a" || d.Ticket == "" || d.CreatedBy != "operator:op" || d.Priority != "normal" {
		t.Fatalf("a draft for a: %+v", d)
	}
	td := f.ticket(t, opActor, TicketNew{Title: "audit the dashboards", Assignee: "team:platform"})
	if td.Queue != "team:platform" || td.Holder != "" {
		t.Fatalf("a team's draft sits in the team's queue: %+v", td)
	}
	un := f.ticket(t, opActor, TicketNew{Title: "someone look at this"})
	if un.Queue != store.TriageQueue || un.Assignee != "" {
		t.Fatalf("an unassigned ticket sits in triage: %+v", un)
	}
	for b, n := range before {
		if f.inboxCount(b) != n {
			t.Errorf("a draft reached %s's inbox", b)
		}
	}
	if len(f.st.Bot("lead").Workers) != 0 {
		t.Fatal("a draft started the lead's triage")
	}
	if _, err := f.svc.Spawn(ctx, f.homeOf("a"), SpawnRequest{CaseID: d.ID, Brief: "do it"}); err == nil || !strings.Contains(err.Error(), "is a draft: nothing starts on it") {
		t.Fatalf("a worker on a draft: %v", err)
	}
	if _, err := f.svc.Delegate(ctx, f.homeOf("a"), "b", "you do it", d.ID); err == nil || !strings.Contains(err.Error(), "is a draft") {
		t.Fatalf("a delegation off a draft: %v", err)
	}
	if err := dispatchable(&store.Case{Ticket: "T-9", Status: store.TicketDone}); err == nil {
		t.Fatal("a closed ticket is dispatchable")
	}
	for _, st := range []string{store.TicketReady, store.TicketInProgress, store.TicketWaiting} {
		if err := dispatchable(&store.Case{Status: st}); err != nil {
			t.Fatalf("%s: %v", st, err)
		}
	}
}

// READY HANDS IT OVER: a bot assignee picks it up at once (in progress, in
// its inbox); a team's routing picks the bot, and the ticket leaves the
// queue — the same ticket, wherever it goes.
func TestTicketReadyDispatches(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	d := f.ticket(t, opActor, TicketNew{Title: "rotate the cache keys", Description: "before Friday", Assignee: "a", Labels: []string{"cache", "Ops"}})
	v, err := f.svc.TicketSetStatus(ctx, opActor, d.Ticket, store.TicketReady, "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != store.TicketInProgress || v.ReadyBy != "operator:op" || v.Holder != "a" || v.ID != d.ID {
		t.Fatalf("picked up: %+v", v)
	}
	if got := f.unreadText("a"); !strings.Contains(got, "ticket "+d.Ticket+" | priority normal") || !strings.Contains(got, "rotate the cache keys") ||
		!strings.Contains(got, "labels cache, ops") || !strings.Contains(got, "conductor bot ticket done "+d.Ticket) {
		t.Fatalf("a's delivery:\n%s", got)
	}
	// A team ticket: the lead's triage picks it up, out of the queue.
	td := f.ticket(t, opActor, TicketNew{Title: "audit the dashboards", Assignee: "team:platform"})
	tv, err := f.svc.TicketSetStatus(ctx, opActor, td.Ticket, store.TicketReady, "")
	if err != nil {
		t.Fatal(err)
	}
	if tv.Holder != "lead" || tv.Status != store.TicketInProgress || tv.Assignee != "team:platform" || tv.Routing == nil || tv.Routing.State != "triage" {
		t.Fatalf("a team ticket at the lead: %+v", tv)
	}
	if q := f.st.Queue("team:platform"); len(q) != 0 {
		t.Fatalf("the ticket stayed in the queue: %v", q)
	}
	// Assigned on by the lead's triage: still the same ticket.
	if _, err := f.svc.Assign(ctx, f.homeOf("lead"), tv.ID, "b", "dashboards are yours"); err != nil {
		t.Fatal(err)
	}
	moved, err := f.svc.TicketShow(td.Ticket)
	if err != nil || moved.Holder != "b" || moved.Assignee != "team:platform" || moved.Title != "audit the dashboards" {
		t.Fatalf("assigned on: %+v %v", moved, err)
	}
	if c := f.caseOf(t, "lead", tv.ID); c.MovedTo != "bot:b/"+moved.ID || !store.Closed(c.Status) {
		t.Fatalf("the lead's copy: %+v", c)
	}
	// Ready needs an assignee.
	un := f.ticket(t, opActor, TicketNew{Title: "someone look"})
	if _, err := f.svc.TicketSetStatus(ctx, opActor, un.Ticket, store.TicketReady, ""); err == nil || !strings.Contains(err.Error(), "needs an assignee") {
		t.Fatalf("ready without an assignee: %v", err)
	}
	// Created ready: dispatched at once.
	r := f.ticket(t, opActor, TicketNew{Title: "now", Assignee: "bot:b", Ready: true})
	if r.Status != store.TicketInProgress || r.Holder != "b" {
		t.Fatalf("created ready: %+v", r)
	}
	// An event's case is a ticket too, created ready and picked up.
	cid, _ := f.svc.Deliver(ctx, Event{Bot: "a", Source: "pd.incident", Title: "db down"})
	ec := f.caseOf(t, "a", cid)
	if ec.Ticket == "" || ec.Status != store.TicketInProgress || len(ec.History) == 0 || !strings.Contains(ec.History[0].What, "created ready and picked up") {
		t.Fatalf("an event's ticket: %+v", ec)
	}
}

// WHO MAY MARK READY: an operator; a bot only its own tickets, only with
// tickets.mark_own_ready — never another's.
func TestTicketWhoMarksReady(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	a := TicketActor{Bot: f.homeOf("a")}
	idea := f.ticket(t, a, TicketNew{Title: "add a disk alert", Assignee: "bot:a"})
	if idea.Status != store.TicketDraft || idea.CreatedBy != "bot:a" {
		t.Fatalf("a bot's idea: %+v", idea)
	}
	if _, err := f.svc.TicketSetStatus(ctx, a, idea.Ticket, store.TicketReady, ""); err == nil || !strings.Contains(err.Error(), "tickets.mark_own_ready is off") {
		t.Fatalf("a bot marking its own idea ready, knob off: %v", err)
	}
	if _, err := f.svc.TicketCreate(ctx, a, TicketNew{Title: "x", Assignee: "bot:a", Ready: true}); err == nil || !strings.Contains(err.Error(), "only an operator marks a ticket ready") {
		t.Fatalf("a bot creating a ready ticket: %v", err)
	}
	m := f.cfg.Bots["a"]
	m.Tickets.MarkOwnReady = boolp(true)
	f.cfg.Bots["a"] = m
	b := f.cfg.Bots["b"]
	b.Tickets.MarkOwnReady = boolp(true)
	f.cfg.Bots["b"] = b
	if v, err := f.svc.TicketSetStatus(ctx, a, idea.Ticket, store.TicketReady, ""); err != nil || v.Status != store.TicketInProgress || v.ReadyBy != "bot:a" {
		t.Fatalf("its own idea, knob on: %+v %v", v, err)
	}
	// Another's ticket: never, knob or not.
	opT := f.ticket(t, opActor, TicketNew{Title: "operator's", Assignee: "bot:b"})
	if _, err := f.svc.TicketSetStatus(ctx, TicketActor{Bot: f.homeOf("b")}, opT.Ticket, store.TicketReady, ""); err == nil || !strings.Contains(err.Error(), "only an operator marks this ticket ready (bot b did not write it)") {
		t.Fatalf("a bot marking an operator's ticket ready: %v", err)
	}
	aForB := f.ticket(t, a, TicketNew{Title: "for b", Assignee: "bot:b"})
	if _, err := f.svc.TicketSetStatus(ctx, TicketActor{Bot: f.homeOf("b")}, aForB.Ticket, store.TicketReady, ""); err == nil || !strings.Contains(err.Error(), "did not write it") {
		t.Fatalf("b marking a's idea ready: %v", err)
	}
	if _, err := f.svc.TicketSetStatus(ctx, opActor, aForB.Ticket, store.TicketReady, ""); err != nil {
		t.Fatalf("the operator marking it ready: %v", err)
	}
	// A worker never writes or moves tickets.
	if _, err := f.svc.TicketCreate(ctx, TicketActor{Bot: Actor{Bot: "a", Role: config.BotRoleWorker}}, TicketNew{Title: "x"}); err == nil {
		t.Fatal("a worker wrote a ticket")
	}
}

// ASSIGNMENT AUTHORITY: an operator assigns anywhere; a bot only to itself,
// a bot it may delegate to, or its own team (or leaves it unassigned), and
// only tickets it holds or wrote.
func TestTicketAssignmentAuthority(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	a := TicketActor{Bot: f.homeOf("a")}
	for _, ok := range []string{"bot:a", "bot:b", "bot:lead", "team:platform", ""} {
		if _, err := f.svc.TicketCreate(ctx, a, TicketNew{Title: "x", Assignee: ok}); err != nil {
			t.Errorf("a → %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"bot:c", "team:billing", "bot:solo"} {
		if _, err := f.svc.TicketCreate(ctx, a, TicketNew{Title: "x", Assignee: bad}); err == nil || !strings.Contains(err.Error(), "may not assign work to "+bad) {
			t.Errorf("a → %s: %v", bad, err)
		}
	}
	mine := f.ticket(t, a, TicketNew{Title: "mine", Assignee: "bot:a"})
	if _, err := f.svc.TicketAssign(ctx, a, mine.Ticket, "bot:c"); err == nil || !strings.Contains(err.Error(), "may not assign") {
		t.Fatalf("reassigning past the rules: %v", err)
	}
	if v, err := f.svc.TicketAssign(ctx, a, mine.Ticket, "bot:b"); err != nil || v.Holder != "b" || v.Status != store.TicketDraft {
		t.Fatalf("a draft moves to b, still a draft: %+v %v", v, err)
	}
	if n := len(f.st.Bot("a").Cases); f.st.Bot("a").Cases[mine.ID] != nil && n > 0 {
		t.Fatal("the draft stayed in a's ledger too")
	}
	opT := f.ticket(t, opActor, TicketNew{Title: "op's", Assignee: "bot:b"})
	if _, err := f.svc.TicketAssign(ctx, a, opT.Ticket, "bot:a"); err == nil || !strings.Contains(err.Error(), "neither holds it nor wrote it") {
		t.Fatalf("a touching b's operator ticket: %v", err)
	}
	if _, err := f.svc.TicketEdit(ctx, a, opT.Ticket, TicketChanges{Labels: &[]string{"x"}}); err == nil {
		t.Fatal("a edited a ticket it neither holds nor wrote")
	}
	if v, err := f.svc.TicketAssign(ctx, opActor, opT.Ticket, "bot:c"); err != nil || v.Holder != "c" {
		t.Fatalf("the operator assigns anywhere: %+v %v", v, err)
	}
	// An active ticket goes through dispatch; never back to triage.
	act := f.ticket(t, opActor, TicketNew{Title: "active", Assignee: "bot:a", Ready: true})
	if _, err := f.svc.TicketAssign(ctx, opActor, act.Ticket, "none"); err == nil || !strings.Contains(err.Error(), "can't go back to the triage queue") {
		t.Fatalf("an active ticket to triage: %v", err)
	}
	v, err := f.svc.TicketAssign(ctx, opActor, act.Ticket, "bot:b")
	if err != nil || v.Holder != "b" || v.Status != store.TicketInProgress || !strings.Contains(f.unreadText("b"), act.Ticket) {
		t.Fatalf("an active ticket reassigned: %+v %v", v, err)
	}
	if c := f.caseOf(t, "a", act.ID); c.MovedTo == "" || !store.Closed(c.Status) {
		t.Fatalf("the copy left behind: %+v", c)
	}
}

// PROVENANCE: a ticket keeps its requester as it moves; an operator who
// does not run a bot is a third party to it; a bot's idea carries its own
// trust at most, and the operator's ready never raises it.
func TestTicketProvenance(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	op2 := TicketActor{Operator: "op2"}
	v := f.ticket(t, op2, TicketNew{Title: "x", Assignee: "bot:a"}) // a's operators are [op]
	if v.RequesterClass != ReqUser || v.Requester != "op2" {
		t.Fatalf("op2 to a: %+v", v)
	}
	idea := f.ticket(t, TicketActor{Bot: f.homeOf("b")}, TicketNew{Title: "b's idea", Assignee: "bot:b"})
	if idea.RequesterClass != ReqBot {
		t.Fatalf("a bot's idea: %+v", idea)
	}
	r, _ := f.svc.TicketSetStatus(ctx, opActor, idea.Ticket, store.TicketReady, "")
	if r.RequesterClass != ReqBot || r.ReadyBy != "operator:op" {
		t.Fatalf("ready raised the trust: %+v", r)
	}
	// Reassigned by the operator to a bot op runs: the requester stays.
	m, err := f.svc.TicketAssign(ctx, opActor, v.Ticket, "bot:b")
	if err != nil || m.Requester != "op2" || m.RequesterClass != ReqUser {
		t.Fatalf("moved: %+v %v", m, err)
	}
}

// THE STATUS MACHINE: only its transitions; ready is picked up, not set in
// progress by hand; reopen brings a ticket back (an operator's to ready,
// a bot's to draft); closing a held ticket writes its episode and reports a
// delegation back.
func TestTicketStatusMachine(t *testing.T) {
	for _, c := range []struct {
		from, to string
		ok       bool
	}{
		{"draft", "ready", true}, {"draft", "declined", true}, {"draft", "in_progress", false}, {"draft", "done", false}, {"draft", "waiting", false},
		{"ready", "draft", true}, {"ready", "done", true},
		{"in_progress", "waiting", true}, {"in_progress", "done", true}, {"in_progress", "declined", true}, {"in_progress", "draft", false}, {"in_progress", "ready", false},
		{"waiting", "in_progress", true}, {"waiting", "done", true},
		{"done", "reopen", true}, {"done", "ready", false}, {"done", "in_progress", false}, {"declined", "reopen", true}, {"draft", "reopen", false},
	} {
		if TicketCanMove(c.from, c.to) != c.ok {
			t.Errorf("%s → %s: %v", c.from, c.to, !c.ok)
		}
	}
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	v := f.ticket(t, opActor, TicketNew{Title: "x", Assignee: "bot:a"})
	if _, err := f.svc.TicketSetStatus(ctx, opActor, v.Ticket, store.TicketDone, ""); err == nil || !strings.Contains(err.Error(), "is draft: it can't go to done") {
		t.Fatalf("draft → done: %v", err)
	}
	f.svc.TicketSetStatus(ctx, opActor, v.Ticket, store.TicketReady, "")
	a := TicketActor{Bot: f.homeOf("a")}
	if w, err := f.svc.TicketSetStatus(ctx, a, v.Ticket, store.TicketWaiting, "need the prod creds"); err != nil || w.Status != store.TicketWaiting {
		t.Fatalf("waiting: %+v %v", w, err)
	}
	if w, err := f.svc.TicketSetStatus(ctx, a, v.Ticket, store.TicketInProgress, ""); err != nil || w.Status != store.TicketInProgress {
		t.Fatalf("back in progress: %+v %v", w, err)
	}
	d, err := f.svc.TicketSetStatus(ctx, a, v.Ticket, store.TicketDone, "rotated")
	if err != nil || d.Status != store.TicketDone || d.Outcome != "rotated" {
		t.Fatalf("done: %+v %v", d, err)
	}
	if log := strings.Join(f.svc.Repo("a").Log(ctx, 5), "\n"); !strings.Contains(log, "case "+d.ID+" closed") {
		t.Fatalf("no episode for the closed ticket:\n%s", log)
	}
	// Reopen: the bot's to draft; the operator's to ready, dispatched.
	if r, err := f.svc.TicketSetStatus(ctx, a, v.Ticket, "reopen", "not quite"); err != nil || r.Status != store.TicketDraft {
		t.Fatalf("a bot's reopen: %+v %v", r, err)
	}
	f.svc.TicketSetStatus(ctx, opActor, v.Ticket, store.TicketReady, "")
	f.svc.TicketSetStatus(ctx, a, v.Ticket, store.TicketDeclined, "can't")
	if r, err := f.svc.TicketSetStatus(ctx, opActor, v.Ticket, "reopen", ""); err != nil || r.Status != store.TicketInProgress {
		t.Fatalf("an operator's reopen: %+v %v", r, err)
	}
	// Ready is picked up, not set by hand.
	q := f.ticket(t, opActor, TicketNew{Title: "q", Assignee: "team:billing"})
	if _, err := f.svc.TicketSetStatus(ctx, opActor, q.Ticket, store.TicketInProgress, ""); err == nil {
		t.Fatal("draft → in_progress by hand")
	}
	// Every change is in the history, with who made it.
	h, _ := f.svc.TicketShow(v.Ticket)
	var whats []string
	for _, x := range h.History {
		whats = append(whats, x.By+": "+x.What)
	}
	got := strings.Join(whats, "\n")
	for _, want := range []string{"operator:op: created (bot:a, draft)", "operator:op: draft → ready", "bot:a: in_progress → waiting: need the prod creds",
		"bot:a: in_progress → done: rotated", "bot:a: done → draft: not quite", "bot:a: in_progress → declined: can't", "operator:op: declined → ready"} {
		if !strings.Contains(got, want) {
			t.Errorf("history lacks %q:\n%s", want, got)
		}
	}
}

// Delegation off a ticket: the member works it as its own ticket, comments,
// closes, and the outcome comes back to the delegator's ticket.
func TestTicketDelegatedWorkComesBack(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	v := f.ticket(t, opActor, TicketNew{Title: "check replica lag", Assignee: "bot:lead", Ready: true})
	out, err := f.svc.Delegate(ctx, f.homeOf("lead"), "b", "check the replica lag on db-main", v.ID)
	if err != nil {
		t.Fatal(err)
	}
	bc := f.caseOf(t, "b", out["peer_case"].(string))
	b := TicketActor{Bot: f.homeOf("b")}
	if _, err := f.svc.TicketComment(ctx, b, bc.Ticket, "lag is 2s and falling", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.TicketSetStatus(ctx, b, bc.Ticket, store.TicketDone, "lag back under 100ms"); err != nil {
		t.Fatal(err)
	}
	lead, _ := f.svc.TicketShow(v.Ticket)
	if dl, _ := lead.Links["delegations"].([]string); len(dl) != 1 || !strings.Contains(dl[0], "("+DelegationDone+")") {
		t.Fatalf("the lead's ticket links: %+v", lead.Links)
	}
	if !strings.Contains(f.unreadText("lead"), "lag back under 100ms") {
		t.Fatal("the outcome did not come back to the lead")
	}
	bt, _ := f.svc.TicketShow(bc.Ticket)
	if len(bt.Comments) != 1 || bt.Comments[0].By != "bot:b" || bt.Links["delegated_from"] != "lead/"+v.ID {
		t.Fatalf("b's ticket: %+v", bt)
	}
}

// Comments: an operator's on an active ticket reaches the bot that holds it;
// on a draft it is only a note. Edits are checked and recorded.
func TestTicketCommentsAndEdits(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	d := f.ticket(t, opActor, TicketNew{Title: "x", Assignee: "bot:a"})
	n := f.inboxCount("a")
	if _, err := f.svc.TicketComment(ctx, opActor, d.Ticket, "a note for later", nil); err != nil || f.inboxCount("a") != n {
		t.Fatalf("a comment on a draft reached the bot: %v", err)
	}
	f.svc.TicketSetStatus(ctx, opActor, d.Ticket, store.TicketReady, "")
	if _, err := f.svc.TicketComment(ctx, opActor, d.Ticket, "use the staging key first", nil); err != nil || !strings.Contains(f.unreadText("a"), "use the staging key first") {
		t.Fatalf("an active ticket's comment: %v", err)
	}
	if _, err := f.svc.TicketComment(ctx, TicketActor{Bot: f.homeOf("b")}, d.Ticket, "x", nil); err == nil {
		t.Fatal("a bot commented on another's ticket")
	}
	due, _ := dueArg(map[string]any{"due": "2026-11-01"})
	title, prio := "rotate keys", "high"
	labels := []string{"Security", "ops", "ops"}
	v, err := f.svc.TicketEdit(ctx, opActor, d.Ticket, TicketChanges{Title: &title, Priority: &prio, Due: &due, Labels: &labels})
	if err != nil || v.Title != title || v.Priority != "high" || v.Due == nil || strings.Join(v.Labels, ",") != "ops,security" {
		t.Fatalf("edit: %+v %v", v, err)
	}
	for _, c := range []struct {
		ch   TicketChanges
		want string
	}{
		{TicketChanges{Priority: ptr("asap")}, `priority "asap" is not one of low, normal, high, urgent`},
		{TicketChanges{Title: ptr("")}, "needs a title"},
		{TicketChanges{Title: ptr("two\nlines")}, "title: one line"},
		{TicketChanges{Labels: &[]string{"Has Space"}}, `label "has space"`},
		{TicketChanges{}, "nothing to change"},
	} {
		if _, err := f.svc.TicketEdit(ctx, opActor, d.Ticket, c.ch); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.ch, err)
		}
	}
	if _, err := dueArg(map[string]any{"due": "next week"}); err == nil {
		t.Fatal("a bad due date")
	}
}

func ptr(s string) *string { return &s }

// Listing: by assignee, holder, status, priority, label, text; done and
// declined only when asked.
func TestTicketList(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	f.ticket(t, opActor, TicketNew{Title: "alpha disk", Assignee: "bot:a", Labels: []string{"disk"}, Priority: "high"})
	f.ticket(t, opActor, TicketNew{Title: "team dashboards", Assignee: "team:platform"})
	f.ticket(t, opActor, TicketNew{Title: "nobody's"})
	done := f.ticket(t, opActor, TicketNew{Title: "finished", Assignee: "bot:b", Ready: true})
	f.svc.TicketSetStatus(ctx, opActor, done.Ticket, store.TicketDone, "ok")
	titles := func(fl TicketFilter) string {
		ts, err := f.svc.Tickets(fl)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range ts {
			out = append(out, x.Title)
		}
		return strings.Join(out, "|")
	}
	if got := titles(TicketFilter{}); strings.Contains(got, "finished") || !strings.Contains(got, "alpha disk") || !strings.Contains(got, "nobody's") {
		t.Fatalf("default: %s", got)
	}
	for fl, want := range map[*TicketFilter]string{
		{Assignee: "bot:a"}:                       "alpha disk",
		{Assignee: "team:platform"}:               "team dashboards",
		{Assignee: "none"}:                        "nobody's",
		{Label: "disk"}:                           "alpha disk",
		{Priority: "high"}:                        "alpha disk",
		{Query: "DASH"}:                           "team dashboards",
		{Statuses: []string{store.TicketDone}}:    "finished",
		{Holder: "b", Statuses: []string{"done"}}: "finished",
	} {
		if got := titles(*fl); got != want {
			t.Errorf("%+v: %q, want %q", *fl, got, want)
		}
	}
	if _, err := f.svc.Tickets(TicketFilter{Statuses: []string{"open"}}); err == nil {
		t.Fatal("an unknown status")
	}
}

// The operator API: create, ready, read, comment, list — and the stream.
func TestTicketAPI(t *testing.T) {
	f := newTeamFixture(t, nil)
	srv := httptest.NewServer(OperatorAPI(f.svc))
	defer srv.Close()
	post := func(method, path, body string) (*http.Response, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}
	ch, cancel := f.svc.SubscribeTickets()
	defer cancel()
	resp, out := post("POST", "/v1/tickets", `{"title":"rotate keys","assignee":"bot:a","labels":["ops"],"due":"2026-11-01","operator":"op"}`)
	if resp.StatusCode != http.StatusCreated || out["status"] != "draft" {
		t.Fatalf("create: %d %v", resp.StatusCode, out)
	}
	tid := out["ticket"].(string)
	if ev := <-ch; ev.Ticket != tid {
		t.Fatalf("stream: %+v", ev)
	}
	resp, out = post("PATCH", "/v1/tickets/"+tid, `{"priority":"high","status":"ready","operator":"op"}`)
	if resp.StatusCode != 200 || out["status"] != "in_progress" || out["priority"] != "high" {
		t.Fatalf("patch: %d %v", resp.StatusCode, out)
	}
	resp, out = post("POST", "/v1/tickets/"+tid+"/comments", `{"text":"thanks","operator":"op"}`)
	if resp.StatusCode != http.StatusCreated || len(out["comments"].([]any)) != 1 {
		t.Fatalf("comment: %d %v", resp.StatusCode, out)
	}
	r, _ := http.Get(srv.URL + "/v1/tickets?assignee=bot:a&operator=op")
	var list struct {
		Tickets []map[string]any `json:"tickets"`
	}
	_ = json.NewDecoder(r.Body).Decode(&list)
	if len(list.Tickets) != 1 || list.Tickets[0]["ticket"] != tid {
		t.Fatalf("list: %+v", list)
	}
	if r, _ := http.Get(srv.URL + "/v1/tickets/T-999"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown: %d", r.StatusCode)
	}
	if resp, out = post("PATCH", "/v1/tickets/"+tid, `{"status":"draft","operator":"op"}`); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("an illegal transition: %d %v", resp.StatusCode, out)
	}
	var b bytes.Buffer
	b.WriteString(`{"title":""}`)
	if r, _ := http.Post(srv.URL+"/v1/tickets", "application/json", &b); r.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("no title: %d", r.StatusCode)
	}
}

// A bot's prompt lists its drafts as ideas nothing has started on, apart
// from its open work.
func TestLedgerSummaryShowsDrafts(t *testing.T) {
	f := newTeamFixture(t, nil)
	d := f.ticket(t, TicketActor{Bot: f.homeOf("a")}, TicketNew{Title: "add a disk alert", Assignee: "bot:a"})
	r := f.ticket(t, opActor, TicketNew{Title: "rotate keys", Assignee: "bot:a", Ready: true})
	s := LedgerSummary(f.st.Bot("a"))
	if !strings.Contains(s, "- draft "+d.Ticket+": add a disk alert (written by bot:a; an idea — nothing starts on it until it is marked ready)") ||
		!strings.Contains(s, "("+r.Ticket+", in progress)") || strings.Contains(s, "case "+d.ID+" (") {
		t.Fatalf("summary:\n%s", s)
	}
}

// The guards behind the gate, tested directly: dispatch refuses anything
// not ready; a ticket left ready (its dispatch failed) is still not set in
// progress by hand; a refused ready create leaves nothing behind.
func TestTicketGateGuards(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	d := f.ticket(t, opActor, TicketNew{Title: "x", Assignee: "bot:a"})
	if err := f.svc.dispatchTicket(ctx, d.Ticket); err == nil || !strings.Contains(err.Error(), "is draft, not ready") {
		t.Fatalf("dispatching a draft: %v", err)
	}
	if f.caseOf(t, "a", d.ID).Status != store.TicketDraft {
		t.Fatal("a refused dispatch changed the ticket")
	}
	_ = f.st.UpdateBot("a", func(r *store.BotRecord) error { r.Cases[d.ID].Status = store.TicketReady; return nil })
	if _, err := f.svc.TicketSetStatus(ctx, opActor, d.Ticket, store.TicketInProgress, ""); err == nil || !strings.Contains(err.Error(), "picked up by its assignee") {
		t.Fatalf("ready → in_progress by hand: %v", err)
	}
	before, _ := f.svc.Tickets(TicketFilter{Statuses: store.TicketStatuses})
	if _, err := f.svc.TicketCreate(ctx, TicketActor{Bot: f.homeOf("a")}, TicketNew{Title: "sneaky", Assignee: "bot:a", Ready: true}); err == nil {
		t.Fatal("a bot created a ready ticket")
	}
	if after, _ := f.svc.Tickets(TicketFilter{Statuses: store.TicketStatuses}); len(after) != len(before) {
		t.Fatalf("a refused ready create left a ticket behind (%d → %d)", len(before), len(after))
	}
}
