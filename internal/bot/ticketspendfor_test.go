package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/store"
)

// lastAudit is the newest audit row of a bot_ticket action.
func (f *teamFixture) lastAudit(action string) map[string]any {
	for i := len(f.audits) - 1; i >= 0; i-- {
		if e := f.audits[i]; e["event"] == "bot_ticket" && e["action"] == action {
			return e
		}
	}
	return nil
}

// A SPEND LIMIT IS BOUND TO ITS HOLDER: an operator confirms a limit for a
// bot, and only that bot spends under it. A bot that moves the ticket on
// leaves the limit held where it lands until an operator confirms it there;
// confirming it for another bot (an assignment, a restated limit, a reopen)
// is a widening the dashboard steps up for, named for the bot; a closed
// ticket's limit allows nothing.
func TestTicketSpendLimitIsBoundToItsHolder(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	usd50 := map[string]any{"amount": "50", "currency": "USD"}
	held := func(bot string, tid string) string {
		t.Helper()
		c, loc, err := f.svc.findTicket(tid)
		if err != nil || loc.Bot != bot {
			t.Fatalf("ticket %s: at %+v, want bot %s: %v", tid, loc, bot, err)
		}
		return SpendHeld(bot, &c)
	}

	// A new ticket's limit is confirmed for its bot, and the step-up for it
	// says which.
	if w, why := f.svc.TicketSpendWidens("", map[string]any{"title": "x", "assignee": "bot:a", "spend_limit": usd50}, false); !w || !strings.Contains(why, "confirmed for bot:a") {
		t.Fatalf("a new ticket's limit: %v %q", w, why)
	}
	v := f.ticket(t, opActor, TicketNew{Title: "buy a drive", Assignee: "bot:a", Ready: true, SpendLimit: &store.SpendLimit{Amount: "50", Currency: "USD"}})
	if h := held("a", v.Ticket); h != "" {
		t.Fatalf("the bot it was confirmed for may not spend it: %s", h)
	}
	if e := f.lastAudit("create"); e["spend_for"] != "a" {
		t.Fatalf("the create audit: %v", e)
	}

	// The bot hands it on: the limit moves, held.
	stub := v.ID
	if _, err := f.svc.TicketAssign(ctx, TicketActor{Bot: f.homeOf("a")}, v.Ticket, "bot:b"); err != nil {
		t.Fatal(err)
	}
	if h := held("b", v.Ticket); !strings.Contains(h, "held") || !strings.Contains(h, "confirmed it for bot:a, not bot:b") {
		t.Fatalf("a bot's handoff carried a spendable limit: %q", h)
	}
	if c := f.caseOf(t, "a", stub); !strings.Contains(SpendHeld("a", &c), "moved to bot:b") {
		t.Fatalf("the copy left behind still spends: %q", SpendHeld("a", &c))
	}
	if e := f.lastAudit("assign"); e["spend_limit_moved"] != true || e["spend_for"] != "a" || e["to"] != "bot:b" {
		t.Fatalf("the assign audit doesn't say the limit moved: %v", e)
	}
	if sv, _ := f.svc.TicketShow(v.Ticket); !strings.Contains(sv.SpendHeld, "held") {
		t.Fatalf("show doesn't say it's held: %+v", sv.SpendHeld)
	}
	if h := strings.Join(f.historyOf(t, v.Ticket), "\n"); !strings.Contains(h, "spend limit held") {
		t.Fatalf("history: %s", h)
	}

	// Confirming it for b — the same limit, restated — widens, named for b.
	if w, why := f.svc.TicketSpendWidens(v.Ticket, map[string]any{"spend_limit": usd50}, true); !w || !strings.Contains(why, "moves from bot:a to bot:b") {
		t.Fatalf("restating the limit for b: %v %q", w, why)
	}
	if w, why := f.svc.TicketSpendWidens(v.Ticket, map[string]any{"title": "y"}, true); w {
		t.Fatalf("a title edit widened: %q", why)
	}
	if _, err := f.svc.TicketEdit(ctx, opActor, v.Ticket, TicketChanges{SpendLimit: lp(&store.SpendLimit{Amount: "50", Currency: "USD"})}); err != nil {
		t.Fatal(err)
	}
	if h := held("b", v.Ticket); h != "" {
		t.Fatalf("confirmed for b, still held: %s", h)
	}
	if e := f.lastAudit("edit"); e["spend_for"] != "b" {
		t.Fatalf("the edit audit: %v", e)
	}

	// An operator's reassignment confirms it for the new bot: a widening,
	// named; to the bot it's already confirmed for, not one.
	if w, why := f.svc.TicketSpendWidens(v.Ticket, map[string]any{"assignee": "bot:b"}, true); w {
		t.Fatalf("assigning to the same bot widened: %q", why)
	}
	if w, why := f.svc.TicketSpendWidens(v.Ticket, map[string]any{"assignee": "bot:c"}, true); !w || !strings.Contains(why, "moves from bot:b to bot:c") {
		t.Fatalf("reassigning to c: %v %q", w, why)
	}
	if _, err := f.svc.TicketAssign(ctx, opActor, v.Ticket, "bot:c"); err != nil {
		t.Fatal(err)
	}
	if h := held("c", v.Ticket); h != "" {
		t.Fatalf("the operator's assignment didn't confirm it for c: %s", h)
	}
	if e := f.lastAudit("assign"); e["spend_limit_moved"] != true || e["spend_for"] != "c" {
		t.Fatalf("the assign audit: %v", e)
	}

	// Done: nothing more is spent. A bot's reopen leaves it held; the
	// operator's reopen is a widening, named for the bot.
	c := TicketActor{Bot: f.homeOf("c")}
	if _, err := f.svc.TicketSetStatus(ctx, c, v.Ticket, store.TicketDone, "bought"); err != nil {
		t.Fatal(err)
	}
	if h := held("c", v.Ticket); !strings.Contains(h, "is done") {
		t.Fatalf("a done ticket's budget is spendable: %q", h)
	}
	if w, why := f.svc.TicketSpendWidens(v.Ticket, map[string]any{"status": "reopen"}, true); !w || !strings.Contains(why, "reopened") || !strings.Contains(why, "bot:c") {
		t.Fatalf("an operator's reopen: %v %q", w, why)
	}
	if _, err := f.svc.TicketSetStatus(ctx, c, v.Ticket, "reopen", ""); err != nil {
		t.Fatal(err)
	}
	if h := held("c", v.Ticket); !strings.Contains(h, "held") {
		t.Fatalf("a bot's reopen revived the budget: %q", h)
	}

	w2 := f.ticket(t, opActor, TicketNew{Title: "again", Assignee: "bot:c", Ready: true, SpendLimit: &store.SpendLimit{Amount: "5", Currency: "USD"}})
	if _, err := f.svc.TicketSetStatus(ctx, opActor, w2.Ticket, store.TicketDone, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.TicketSetStatus(ctx, opActor, w2.Ticket, "reopen", ""); err != nil {
		t.Fatal(err)
	}
	if h := held("c", w2.Ticket); h != "" {
		t.Fatalf("the operator's reopen didn't confirm it again: %s", h)
	}
	if e := f.lastAudit("status"); e["spend_for"] != "c" {
		t.Fatalf("the reopen audit: %v", e)
	}

	// A team's ticket: whichever bot its routing (here, the lead's triage)
	// lands it on holds it held.
	tv := f.ticket(t, opActor, TicketNew{Title: "team buy", Assignee: "team:platform", Ready: true, SpendLimit: &store.SpendLimit{Amount: "5", Currency: "USD"}})
	if w, why := f.svc.TicketSpendWidens("", map[string]any{"title": "t", "assignee": "team:platform", "spend_limit": usd50}, false); !w || strings.Contains(why, "bot:") {
		t.Fatalf("a team ticket's limit names no bot: %v %q", w, why)
	}
	cc, loc, err := f.svc.findTicket(tv.Ticket)
	if err != nil || loc.Bot == "" {
		t.Fatalf("the team's ticket: %+v %v", loc, err)
	}
	if h := SpendHeld(loc.Bot, &cc); !strings.Contains(h, "confirmed it for no bot") {
		t.Fatalf("team routing gave bot %s the budget: %q", loc.Bot, h)
	}
}
