package bot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/store"
)

// fakeSpend is a purchase ledger: cents committed per ticket/currency (the
// durable total, by the ticket's stable id).
type fakeSpend struct {
	cents map[string]int64
	err   error
}

func (f *fakeSpend) SpentOnTicket(ticketID, currency string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	return f.cents[ticketID+"/"+currency], nil
}

func (f *fakeSpend) CloseTicket(string) error { return nil }

func limit(amount string, merchants ...string) *store.SpendLimit {
	return &store.SpendLimit{Amount: amount, Merchants: merchants}
}

func lp(l *store.SpendLimit) **store.SpendLimit { return &l }

// THE FIELD ROUND-TRIPS: set at creation, canonical, on the ticket record,
// through the ledger file and back, and carried when the ticket moves.
func TestTicketSpendLimitRoundTrips(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	v := f.ticket(t, opActor, TicketNew{Title: "buy a hard drive", Assignee: "bot:a",
		SpendLimit: &store.SpendLimit{Amount: "120", Currency: "usd", Card: "visa", Merchants: []string{"https://Shop.example.com/", "https://shop.example.com"}}})
	want := store.SpendLimit{Amount: "120.00", Currency: "USD", Card: "visa", Merchants: []string{"https://shop.example.com"}}
	if v.SpendLimit == nil || spendText(v.SpendLimit) != spendText(&want) {
		t.Fatalf("created: %+v", v.SpendLimit)
	}
	if c := f.caseOf(t, "a", v.ID); c.SpendLimit == nil || c.SpendLimit.Amount != "120.00" || c.SpendLimit.Card != "visa" {
		t.Fatalf("stored: %+v", c.SpendLimit)
	}
	raw, err := json.Marshal(f.caseOf(t, "a", v.ID))
	if err != nil || !strings.Contains(string(raw), `"spend_limit":{"amount":"120.00","currency":"USD","card":"visa","merchants":["https://shop.example.com"]}`) {
		t.Fatalf("on disk: %s %v", raw, err)
	}
	var back store.Case
	if err := json.Unmarshal(raw, &back); err != nil || !sameSpendLimit(back.SpendLimit, &want) {
		t.Fatalf("read back: %+v %v", back.SpendLimit, err)
	}
	if raw, _ := json.Marshal(store.Case{Title: "x"}); strings.Contains(string(raw), "spend_limit") {
		t.Fatalf("no limit is written as none: %s", raw)
	}
	if got, err := f.svc.TicketShow(v.Ticket); err != nil || !sameSpendLimit(got.SpendLimit, &want) {
		t.Fatalf("show: %+v %v", got.SpendLimit, err)
	}
	if !strings.Contains(strings.Join(f.historyOf(t, v.Ticket), "\n"), "spend limit 120.00 USD on card visa at https://shop.example.com") {
		t.Fatalf("history: %v", f.historyOf(t, v.Ticket))
	}
	moved, err := f.svc.TicketAssign(ctx, opActor, v.Ticket, "bot:b")
	if err != nil || !sameSpendLimit(moved.SpendLimit, &want) || !sameSpendLimit(f.caseOf(t, "b", moved.ID).SpendLimit, &want) {
		t.Fatalf("moved: %+v %v", moved.SpendLimit, err)
	}
	cleared, err := f.svc.TicketEdit(ctx, opActor, v.Ticket, TicketChanges{SpendLimit: lp(nil)})
	if err != nil || cleared.SpendLimit != nil {
		t.Fatalf("the operator clears it: %+v %v", cleared.SpendLimit, err)
	}
	for _, c := range []struct {
		l    store.SpendLimit
		want string
	}{
		{store.SpendLimit{Amount: "0"}, "more than zero"},
		{store.SpendLimit{Amount: "-5"}, "a positive number"},
		{store.SpendLimit{Amount: "1.999"}, "at most two decimals"},
		{store.SpendLimit{Amount: "10", Currency: "dollars"}, "three-letter code"},
		{store.SpendLimit{Amount: "10", Card: "My Card"}, "a card's name"},
		{store.SpendLimit{Amount: "10", Merchants: []string{"http://shop.example.com"}}, "an https origin"},
		{store.SpendLimit{Amount: "10", Merchants: []string{"https://shop.example.com/cart"}}, "an https origin"},
	} {
		l := c.l
		if _, err := f.svc.TicketCreate(ctx, opActor, TicketNew{Title: "x", SpendLimit: &l}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v", c.l, err)
		}
	}
}

// ONLY OPERATORS RAISE: a bot can't set, raise, clear or widen a limit; it
// may only lower one on a ticket it holds or wrote. An operator's raise is
// recorded as a widening.
func TestTicketSpendLimitOnlyOperatorsRaise(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	a := TicketActor{Bot: f.homeOf("a")}
	if _, err := f.svc.TicketCreate(ctx, a, TicketNew{Title: "x", Assignee: "bot:a", SpendLimit: limit("5")}); err == nil || !strings.Contains(err.Error(), "only an operator sets or raises a ticket's spend limit (a spend limit set)") {
		t.Fatalf("a bot setting a limit at creation: %v", err)
	}
	idea := f.ticket(t, a, TicketNew{Title: "its idea", Assignee: "bot:a"})
	if _, err := f.svc.TicketEdit(ctx, a, idea.Ticket, TicketChanges{SpendLimit: lp(limit("5"))}); err == nil || !strings.Contains(err.Error(), "a bot may only lower it") {
		t.Fatalf("a bot setting a limit on its own idea: %v", err)
	}
	v := f.ticket(t, opActor, TicketNew{Title: "buy a hard drive", Assignee: "bot:a", SpendLimit: &store.SpendLimit{Amount: "120", Card: "visa", Merchants: []string{"https://shop.example.com", "https://other.example.com"}}})
	base := *v.SpendLimit
	with := func(edit func(*store.SpendLimit)) *store.SpendLimit {
		l := base
		l.Merchants = append([]string(nil), base.Merchants...)
		edit(&l)
		return &l
	}
	for _, c := range []struct {
		name string
		l    *store.SpendLimit
		why  string
	}{
		{"raise", with(func(l *store.SpendLimit) { l.Amount = "120.01" }), "the spend limit raised 120.00 → 120.01 USD"},
		{"clear", nil, "the spend limit cleared"},
		{"currency", with(func(l *store.SpendLimit) { l.Currency = "EUR"; l.Amount = "1" }), "currency changed"},
		{"card", with(func(l *store.SpendLimit) { l.Card = "amex" }), "card changed"},
		{"no card", with(func(l *store.SpendLimit) { l.Card = "" }), "card changed"},
		{"merchant added", with(func(l *store.SpendLimit) { l.Merchants = append(l.Merchants, "https://third.example.com") }), "merchant https://third.example.com added"},
		{"merchants dropped", with(func(l *store.SpendLimit) { l.Merchants = nil }), "merchant restriction dropped"},
	} {
		if _, err := f.svc.TicketEdit(ctx, a, v.Ticket, TicketChanges{SpendLimit: lp(c.l)}); err == nil || !strings.Contains(err.Error(), c.why) || !strings.Contains(err.Error(), "a bot may only lower it") {
			t.Errorf("a bot widening (%s): %v", c.name, err)
		}
	}
	if got := f.caseOf(t, "a", v.ID).SpendLimit; !sameSpendLimit(got, &base) {
		t.Fatalf("a refused change still landed: %+v", got)
	}
	// Lowering, on its own ticket: a smaller amount, fewer merchants.
	low := with(func(l *store.SpendLimit) { l.Amount = "99.5"; l.Merchants = l.Merchants[:1] })
	got, err := f.svc.TicketEdit(ctx, a, v.Ticket, TicketChanges{SpendLimit: lp(low)})
	if err != nil || got.SpendLimit.Amount != "99.50" || len(got.SpendLimit.Merchants) != 1 {
		t.Fatalf("a bot lowering its own ticket's limit: %+v %v", got.SpendLimit, err)
	}
	// Then it can't put it back.
	if _, err := f.svc.TicketEdit(ctx, a, v.Ticket, TicketChanges{SpendLimit: lp(&base)}); err == nil {
		t.Fatal("a bot raised the limit back")
	}
	// Not another's ticket, even lowering.
	if _, err := f.svc.TicketEdit(ctx, TicketActor{Bot: f.homeOf("c")}, v.Ticket, TicketChanges{SpendLimit: lp(limit("1"))}); err == nil {
		t.Fatal("a bot lowered a ticket it neither holds nor wrote")
	}
	// The operator raises; the raise is recorded as a widening.
	up, err := f.svc.TicketEdit(ctx, opActor, v.Ticket, TicketChanges{SpendLimit: lp(limit("500"))})
	if err != nil || up.SpendLimit.Amount != "500.00" {
		t.Fatalf("the operator raising it: %+v %v", up.SpendLimit, err)
	}
	if h := strings.Join(f.historyOf(t, v.Ticket), "\n"); !strings.Contains(h, "a WIDENING: the spend limit raised 99.50 → 500.00 USD") {
		t.Fatalf("the raise is not recorded as a widening: %s", h)
	}
	if w, _ := spendLimitWidens(limit("10", "https://a.example"), limit("10", "https://a.example")); w {
		t.Fatal("the same limit counted as a widening")
	}
}

// SPENT SO FAR: read the ticket's durable committed total (by its stable id,
// spanning every copy past and present); without a ledger, unknown (and said
// so).
func TestTicketSpentSoFar(t *testing.T) {
	f := newTeamFixture(t, nil)
	ctx := context.Background()
	// Active, so moving it leaves a's copy (and what a spent on it) behind.
	v := f.ticket(t, opActor, TicketNew{Title: "buy a hard drive", Assignee: "bot:a", Ready: true, SpendLimit: limit("120")})
	if got, _ := f.svc.TicketShow(v.Ticket); got.Spent == nil || got.Spent.Known || got.Spent.Why != "no purchase ledger on this daemon" {
		t.Fatalf("no ledger: %+v", got.Spent)
	}
	moved, err := f.svc.TicketAssign(ctx, opActor, v.Ticket, "bot:b")
	if err != nil {
		t.Fatal(err)
	}
	led := &fakeSpend{cents: map[string]int64{v.Ticket + "/USD": 7550, v.Ticket + "/EUR": 99999}}
	f.svc.Spend = led
	got, _ := f.svc.TicketShow(v.Ticket)
	if got.Spent == nil || !got.Spent.Known || got.Spent.Spent != "75.50" || got.Spent.Remaining != "44.50" || got.Spent.Currency != "USD" {
		t.Fatalf("spent for the ticket: %+v", got.Spent)
	}
	lim, st, ok := f.svc.TicketSpend("b", moved.ID)
	if !ok || lim.Amount != "120.00" || st.Spent != "75.50" {
		t.Fatalf("the accessor payments call: %+v %+v %v", lim, st, ok)
	}
	if _, _, ok := f.svc.TicketSpend("a", v.ID); !ok {
		t.Fatal("the moved-away copy still names the ticket's limit")
	}
	led.cents[v.Ticket+"/USD"] = 20000
	if got, _ := f.svc.TicketShow(v.Ticket); got.Spent.Remaining != "0.00" {
		t.Fatalf("over the limit: %+v", got.Spent)
	}
	led.err = errors.New("ledger unreadable")
	if got, _ := f.svc.TicketShow(v.Ticket); got.Spent.Known || got.Spent.Why != "ledger unreadable" {
		t.Fatalf("a ledger error: %+v", got.Spent)
	}
	plain := f.ticket(t, opActor, TicketNew{Title: "no limit", Assignee: "bot:a"})
	if got, _ := f.svc.TicketShow(plain.Ticket); got.Spent != nil {
		t.Fatalf("no limit, no spent line: %+v", got.Spent)
	}
	if _, _, ok := f.svc.TicketSpend("a", plain.ID); ok {
		t.Fatal("a ticket without a limit reported one")
	}
	if _, _, ok := f.svc.TicketSpend("a", "nope"); ok {
		t.Fatal("an unknown case reported a limit")
	}
}

// THE API and ops: spend_limit as an object, or an amount beside currency,
// card and merchants; null or "none" clears it; a bot raising through an op
// is refused.
func TestTicketSpendLimitAPI(t *testing.T) {
	f := newTeamFixture(t, nil)
	srv := httptest.NewServer(OperatorAPI(f.svc))
	defer srv.Close()
	do := func(method, path, body string) (int, map[string]any) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	code, out := do("POST", "/v1/tickets", `{"title":"buy a hard drive","assignee":"bot:a","operator":"op","spend_limit":{"amount":"50","currency":"usd","card":"visa","merchants":["https://shop.example.com"]}}`)
	l, _ := out["spend_limit"].(map[string]any)
	if code != http.StatusCreated || l["amount"] != "50.00" || l["currency"] != "USD" || l["card"] != "visa" {
		t.Fatalf("create: %d %v", code, out)
	}
	tid := out["ticket"].(string)
	code, out = do("GET", "/v1/tickets/"+tid+"?operator=op", "")
	if sp, _ := out["spent"].(map[string]any); code != 200 || sp == nil || sp["known"] != false {
		t.Fatalf("show carries spent: %d %v", code, out)
	}
	code, out = do("PATCH", "/v1/tickets/"+tid, `{"spend_limit":"75","currency":"USD","operator":"op"}`)
	if l, _ := out["spend_limit"].(map[string]any); code != 200 || l["amount"] != "75.00" || l["card"] != nil {
		t.Fatalf("patch amount: %d %v", code, out)
	}
	code, out = do("PATCH", "/v1/tickets/"+tid, `{"spend_limit":null,"operator":"op"}`)
	if code != 200 || out["spend_limit"] != nil {
		t.Fatalf("patch clear: %d %v", code, out)
	}
	if code, out = do("PATCH", "/v1/tickets/"+tid, `{"spend_limit":["x"],"operator":"op"}`); code != http.StatusUnprocessableEntity && code != http.StatusBadRequest {
		t.Fatalf("a malformed limit: %d %v", code, out)
	}
	// Agent ops: the same args, a bot actor.
	ctx := context.Background()
	a := TicketActor{Bot: f.homeOf("a")}
	v := f.ticket(t, opActor, TicketNew{Title: "x", Assignee: "bot:a", SpendLimit: limit("20")})
	if _, err := f.svc.ticketOp(ctx, a, "ticket_edit", map[string]any{"ticket": v.Ticket, "spend_limit": "25"}); err == nil || !strings.Contains(err.Error(), "a bot may only lower it") {
		t.Fatalf("a bot raising through the op: %v", err)
	}
	if _, err := f.svc.ticketOp(ctx, a, "ticket_edit", map[string]any{"ticket": v.Ticket, "spend_limit": "none"}); err == nil {
		t.Fatal("a bot cleared the limit through the op")
	}
	if _, err := f.svc.ticketOp(ctx, a, "ticket_edit", map[string]any{"ticket": v.Ticket, "spend_limit": "15"}); err != nil {
		t.Fatalf("a bot lowering through the op: %v", err)
	}
	if c := f.caseOf(t, "a", v.ID); c.SpendLimit.Amount != "15.00" {
		t.Fatalf("lowered: %+v", c.SpendLimit)
	}
}

// historyOf is a ticket's history lines, as the operator sees them.
func (f *teamFixture) historyOf(t *testing.T, tid string) []string {
	t.Helper()
	v, err := f.svc.TicketShow(tid)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, h := range v.History {
		out = append(out, h.What)
	}
	return out
}
