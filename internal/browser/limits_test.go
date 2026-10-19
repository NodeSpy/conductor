package browser

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

const shopURL = "https://shop.example.com"

var botCaller = Principal{Identity: "agent:shopper@home", Bot: "shopper", Groups: []string{"bot:shopper", "team:ops"}, CaseID: "c3"}

func stackRules(card Card, budget *CaseBudget) SpendRules {
	return SpendRules{Card: card, Who: botCaller, Bot: "shopper", Team: "ops",
		BotLimits:  &config.PurchaseLimits{CardLimits: config.CardLimits{PerPurchase: 40, PerDay: 200, PerMonth: 300}},
		TeamLimits: &config.PurchaseLimits{CardLimits: config.CardLimits{PerPurchase: 100, PerDay: 150, PerMonth: 400}},
		Budget:     budget}
}

func bigCard() Card {
	return Card{Name: "visa", Merchants: []string{shopURL}, Identities: []string{"bot:shopper"},
		Limits: config.CardLimits{PerPurchase: 500, PerDay: 1000, PerMonth: 2000}}
}

func buyP(total float64) Purchase {
	return Purchase{Card: "visa", Merchant: shopURL, Total: total, Currency: "USD", Identity: botCaller.Identity, Bot: "shopper",
		CaseRef: "bot:shopper/c3", BudgetKey: "bot:shopper/c3"}
}

func binding(ls []LayerState) LayerState {
	for _, l := range ls {
		if l.Binding {
			return l
		}
	}
	return LayerState{}
}

// Every layer applies and the lowest wins: a card that allows 500 a
// purchase, a team 100, a bot 40 — 40 decides; with a ticket's 120, an 85
// purchase is refused by the bot even with budget left; the binding layer
// is named.
func TestLowestLayerWins(t *testing.T) {
	l, _ := ledger(t)
	budget := &CaseBudget{Key: "bot:shopper/c3", Amount: 120, Currency: "USD"}
	r := stackRules(bigCard(), budget)
	_, err := l.Evaluate(r, buyP(85), "")
	if err == nil || !strings.Contains(err.Error(), "over bot shopper's 40.00 USD per-purchase limit") {
		t.Fatalf("the bot's 40 per purchase: %v", err)
	}
	ls, err := l.Evaluate(r, buyP(40), "")
	if err != nil {
		t.Fatal(err)
	}
	if b := binding(ls); b.Layer != "bot shopper" || b.Window != "purchase" {
		t.Fatalf("binding: %+v", b)
	}
	// Without the bot's cap, the team's 100 per purchase decides.
	r.BotLimits = nil
	if _, err := l.Evaluate(r, buyP(100.01), ""); err == nil || !strings.Contains(err.Error(), "team ops's 100.00 USD per-purchase") {
		t.Fatalf("the team: %v", err)
	}
	// And the ticket's 120 decides a 100 after 85 elsewhere on it.
	if _, err := l.ProposeWithin(buyP(85), time.Hour, r); err != nil {
		t.Fatal(err)
	}
	_, err = l.Evaluate(r, buyP(50), "")
	if err == nil || !strings.Contains(err.Error(), "over this ticket's 120.00 USD limit: 35.00 USD left") {
		t.Fatalf("the ticket: %v", err)
	}
	// A higher layer can never widen a lower one: a bot cap above the card's
	// changes nothing.
	small := bigCard()
	small.Limits = config.CardLimits{PerPurchase: 10, PerDay: 20, PerMonth: 30}
	r2 := stackRules(small, nil)
	if _, err := l.Evaluate(r2, buyP(11), ""); err == nil || !strings.Contains(err.Error(), "card visa's 10.00 USD per-purchase") {
		t.Fatalf("the card's 10 under a bot's 40: %v", err)
	}
}

// A ticket's budget counts everything proposed under it over its whole
// life — pending, approved, paid — and frees what is refused, denied or
// expires; never another ticket's.
func TestTicketBudgetAccumulates(t *testing.T) {
	l, ck := ledger(t)
	budget := &CaseBudget{Key: "bot:shopper/c3", Amount: 120, Currency: "USD"}
	r := stackRules(bigCard(), budget)
	r.BotLimits = nil
	a, err := l.ProposeWithin(buyP(85), time.Hour, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.ProposeWithin(buyP(50), time.Hour, r); err == nil || !strings.Contains(err.Error(), "35.00 USD left") {
		t.Fatalf("a pending purchase counts: %v", err)
	}
	if ps, _ := l.List(""); len(ps) != 1 {
		t.Fatalf("a refused proposal recorded nothing: %+v", ps)
	}
	// Approved, then paid: still counted.
	if _, err := l.ApproveWithin(a.ID, a.Digest, "op", time.Hour, false, r); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ProposeWithin(buyP(50), time.Hour, r); err == nil || !strings.Contains(err.Error(), "35.00 USD left") {
		t.Fatalf("an approved purchase counts: %v", err)
	}
	if _, err := l.Consume(a.ID, botCaller, r); err != nil {
		t.Fatal(err)
	}
	if used, ids := l.Committed("bot:shopper/c3"); used != 85 || len(ids) != 1 {
		t.Fatalf("paid counts: %v %v", used, ids)
	}
	if _, err := l.Complete(a.ID, Receipt{OrderNumber: "FS-1", Total: 85}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ProposeWithin(buyP(50), time.Hour, r); err == nil || !strings.Contains(err.Error(), "35.00 USD left") {
		t.Fatalf("a completed purchase counts: %v", err)
	}
	if _, err := l.ProposeWithin(buyP(35.01), time.Hour, r); err == nil {
		t.Fatal("35.01 after 85 paid")
	}
	b, err := l.ProposeWithin(buyP(35), time.Hour, r)
	if err != nil {
		t.Fatalf("exactly the rest: %v", err)
	}
	// Denied: freed.
	if _, err := l.Deny(b.ID, "op", "no"); err != nil {
		t.Fatal(err)
	}
	if used, ids := l.Committed("bot:shopper/c3"); used != 85 || len(ids) != 1 {
		t.Fatalf("a denied purchase is not committed: %v %v", used, ids)
	}
	c, err := l.ProposeWithin(buyP(35), time.Hour, r)
	if err != nil {
		t.Fatalf("after a denial: %v", err)
	}
	// Expired: freed.
	ck.t = ck.t.Add(time.Hour)
	d, err := l.ProposeWithin(buyP(35), time.Hour, r)
	if err != nil {
		t.Fatalf("after an expiry: %v", err)
	}
	// Refused at the fill (the checkout stopped): freed by Unspend.
	l.ApproveWithin(d.ID, d.Digest, "op", time.Hour, false, r)
	if _, err := l.Consume(d.ID, botCaller, r); err != nil {
		t.Fatal(err)
	}
	l.Unspend(d.ID, "the total changed")
	if _, err := l.ProposeWithin(buyP(35), time.Hour, r); err != nil {
		t.Fatalf("after a refused checkout: %v", err)
	}
	_ = c
	// Another ticket's budget is its own: unspent budget never transfers.
	other := &CaseBudget{Key: "bot:shopper/c4", Amount: 10, Currency: "USD"}
	r4 := stackRules(bigCard(), other)
	r4.BotLimits = nil
	p4 := buyP(10.01)
	p4.CaseRef, p4.BudgetKey = "bot:shopper/c4", "bot:shopper/c4"
	if _, err := l.ProposeWithin(p4, time.Hour, r4); err == nil || !strings.Contains(err.Error(), "10.00 USD left") {
		t.Fatalf("ticket c4's own 10: %v", err)
	}
}

// A ticket's budget above a 30-day cap of the layers over it is clamped
// there, and says so.
func TestTicketBudgetClamped(t *testing.T) {
	l, _ := ledger(t)
	r := stackRules(bigCard(), &CaseBudget{Key: "bot:shopper/c3", Amount: 500, Currency: "USD"})
	r.BotLimits.PerPurchase = 500
	ls, err := l.Evaluate(r, buyP(10), "")
	if err != nil {
		t.Fatal(err)
	}
	var ticket LayerState
	for _, s := range ls {
		if s.Window == "ticket" {
			ticket = s
		}
	}
	if ticket.Limit != 300 || !strings.Contains(ticket.Note, "clamped from 500.00 to bot shopper's 30-day limit 300.00") {
		t.Fatalf("clamped: %+v", ticket)
	}
	if limit, note := clampBudget(100, ls); limit != 100 || note != "" {
		t.Fatalf("a budget inside every cap is its own: %v %q", limit, note)
	}
}

// The fill-time check (Consume) honours the ticket budget as it stands
// then: lowered after the approval, the approval is spent on a refusal.
func TestFillTimeHonoursTheTicket(t *testing.T) {
	l, _ := ledger(t)
	budget := &CaseBudget{Key: "bot:shopper/c3", Amount: 120, Currency: "USD"}
	r := stackRules(bigCard(), budget)
	r.BotLimits = nil
	a, _ := l.ProposeWithin(buyP(85), time.Hour, r)
	if _, err := l.ApproveWithin(a.ID, a.Digest, "op", time.Hour, false, r); err != nil {
		t.Fatal(err)
	}
	budget.Amount = 80
	if _, err := l.Consume(a.ID, botCaller, r); err == nil || !strings.Contains(err.Error(), "over this ticket's 80.00 USD limit: 80.00 USD left") {
		t.Fatalf("lowered after the approval: %v", err)
	}
	got, _, _ := l.Get(a.ID)
	if got.Status != PurchaseRefused || got.Checked != "paid" || !strings.Contains(got.Binding, "ticket") {
		t.Fatalf("recorded: %+v", got)
	}
	// The approval itself re-checks too.
	b, _ := l.ProposeWithin(buyP(70), time.Hour, r)
	budget.Amount = 60
	if _, err := l.ApproveWithin(b.ID, b.Digest, "op", time.Hour, false, r); err == nil || !strings.Contains(err.Error(), "this ticket") {
		t.Fatalf("the approval: %v", err)
	}
}

// A ticket's budget may name a card, merchants and a currency; a purchase
// outside them is refused by the ticket layer.
func TestTicketBudgetRestrictions(t *testing.T) {
	l, _ := ledger(t)
	for name, c := range map[string]struct {
		b    CaseBudget
		want string
	}{
		"card":     {CaseBudget{Key: "k", Amount: 100, Currency: "USD", Card: "amex"}, "for card amex, not visa"},
		"merchant": {CaseBudget{Key: "k", Amount: 100, Currency: "USD", Merchants: []string{"https://other.example.com"}}, "does not cover https://shop.example.com"},
		"currency": {CaseBudget{Key: "k", Amount: 100, Currency: "EUR"}, "budget is in EUR"},
	} {
		b := c.b
		r := stackRules(bigCard(), &b)
		if _, err := l.Evaluate(r, buyP(5), ""); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	ok := CaseBudget{Key: "k", Amount: 100, Currency: "USD", Card: "visa", Merchants: []string{shopURL + "/"}}
	if _, err := l.Evaluate(stackRules(bigCard(), &ok), buyP(5), ""); err != nil {
		t.Fatalf("inside its card and merchants: %v", err)
	}
	// No budget: the purchase falls back to the other layers.
	if _, err := l.Evaluate(stackRules(bigCard(), nil), buyP(40), ""); err != nil {
		t.Fatalf("no ticket budget: %v", err)
	}
}

// The bot and team layers count that bot's and that team's spend, across
// cards, and per card where set; the day window at its boundary.
func TestBotAndTeamLayersCountTheirOwn(t *testing.T) {
	l, ck := ledger(t)
	r := stackRules(bigCard(), nil)
	r.BotLimits = &config.PurchaseLimits{CardLimits: config.CardLimits{PerPurchase: 40, PerDay: 60, PerMonth: 300},
		Cards: map[string]config.CardLimits{"visa": {PerPurchase: 40, PerDay: 50, PerMonth: 100}}}
	spend := func(rr SpendRules, who Principal, amt float64) error {
		p := buyP(amt)
		p.Identity, p.Bot = who.Identity, who.Bot
		q, err := l.ProposeWithin(p, time.Hour, rr)
		if err != nil {
			return err
		}
		if _, err := l.ApproveWithin(q.ID, q.Digest, "op", time.Hour, false, rr); err != nil {
			return err
		}
		_, err = l.Consume(q.ID, who, rr)
		return err
	}
	if err := spend(r, botCaller, 40); err != nil {
		t.Fatal(err)
	}
	if err := spend(r, botCaller, 10.01); err == nil || !strings.Contains(err.Error(), "bot shopper on card visa's 50.00 USD daily limit: 10.00 USD left") {
		t.Fatalf("the bot's per-card day: %v", err)
	}
	// Another bot's spend is not this bot's; the team's is shared.
	other := Principal{Identity: "agent:helper@home", Bot: "helper", Groups: []string{"bot:helper", "team:ops"}}
	ro := stackRules(bigCard(), nil)
	ro.Who, ro.Bot, ro.BotLimits = other, "helper", nil
	if err := spend(ro, other, 100); err != nil {
		t.Fatal(err)
	}
	if err := spend(ro, other, 10.01); err == nil || !strings.Contains(err.Error(), "team ops's 150.00 USD daily limit: 10.00 USD left") {
		t.Fatalf("the team's day, shared: %v", err)
	}
	ck.t = ck.t.Add(24*time.Hour - time.Second)
	if err := spend(r, botCaller, 10.01); err == nil {
		t.Fatal("a second before the day ends")
	}
	ck.t = ck.t.Add(time.Second)
	if err := spend(r, botCaller, 40); err != nil {
		t.Fatalf("the next day: %v", err)
	}
}

func TestOverMessages(t *testing.T) {
	for s, want := range map[LayerState]string{
		{Layer: "ticket bot:x/c3", Window: "ticket", Limit: 120, Remaining: 35, Currency: "USD"}: "over this ticket's 120.00 USD limit: 35.00 USD left",
		{Layer: "bot x", Window: "purchase", Limit: 40, Remaining: 40, Currency: "USD"}:          "over bot x's 40.00 USD per-purchase limit",
		{Layer: "team t", Window: "day", Limit: 50, Remaining: -3, Currency: "USD"}:              "over team t's 50.00 USD daily limit: 0.00 USD left",
		{Layer: "card v", Window: "30 days", Limit: 300, Remaining: 1, Currency: "EUR"}:          "over card v's 300.00 EUR 30-day limit: 1.00 EUR left",
	} {
		if got := overMessage(s); got != want {
			t.Errorf("%q", got)
		}
	}
	if CaseRef("b", "") != "" || CaseRef("", "c") != "" || CaseRef("b", "c") != "bot:b/c" {
		t.Fatal("CaseRef")
	}
	for _, bad := range []string{"0", "-1", "x", "NaN", "Inf", ""} {
		if _, err := ParseSpendLimit(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if v, err := ParseSpendLimit("120.005"); err != nil || v != 120.01 {
		t.Fatalf("rounded: %v %v", v, err)
	}
}

type fakeBudgets map[string]CaseBudget

func (f fakeBudgets) CaseBudget(ref string) (CaseBudget, bool) { b, ok := f[ref]; return b, ok }

// A purchase proposed by a bot session under a case: the case's ticket
// budget is a layer, the binding layer is recorded, and a refusal names
// the ticket with what is left.
func TestProposeUnderATicket(t *testing.T) {
	card := bigCard()
	st := Settings{Allow: []string{"shop.example.com"}, Cards: map[string]Card{"visa": card}, PurchaseTTL: time.Minute,
		BotPurchases:  map[string]*config.PurchaseLimits{"shopper": {CardLimits: config.CardLimits{PerPurchase: 90, PerDay: 200, PerMonth: 300}}},
		TeamPurchases: map[string]*config.PurchaseLimits{"ops": {CardLimits: config.CardLimits{PerPurchase: 100, PerDay: 300, PerMonth: 400}}},
		BotTeam:       map[string]string{"shopper": "ops"}}
	m := &Manager{Settings: func() Settings { return st }, Purchases: &Purchases{Dir: t.TempDir()},
		Budgets:     fakeBudgets{"bot:shopper/c3": {Key: "bot:shopper/c3", Amount: 120, Currency: "USD"}},
		BotPurchase: func(ctx context.Context, c Principal, p Purchase) (string, bool, error) { return "p1", false, nil }}
	args := func(total string) map[string]any {
		return map[string]any{"card": "visa", "merchant": shopURL, "total": total}
	}
	p, err := m.proposePurchase(context.Background(), botCaller, st, args("85"), map[string]any{})
	if err != nil || p.CaseRef != "bot:shopper/c3" || p.BudgetKey != "bot:shopper/c3" || p.Checked != "proposed" || !strings.HasPrefix(p.Binding, "bot shopper (purchase)") || p.Layers[len(p.Layers)-1].Layer != "ticket bot:shopper/c3" {
		t.Fatalf("under the ticket: %+v %v", p, err)
	}
	// The bot's team is a layer too, read from the settings.
	hasTeam := false
	for _, s := range p.Layers {
		hasTeam = hasTeam || s.Layer == "team ops"
	}
	if !hasTeam {
		t.Fatalf("no team layer: %+v", p.Layers)
	}
	// 90 left under the bot's cap, 35 under the ticket's: the ticket binds.
	if _, err := m.proposePurchase(context.Background(), botCaller, st, args("50"), map[string]any{}); err == nil || !strings.Contains(err.Error(), "over this ticket's 120.00 USD limit: 35.00 USD left") {
		t.Fatalf("the second: %v", err)
	}
	// The bot's own cap decides an 91 even with the ticket's budget left.
	c2 := botCaller
	c2.CaseID = "c9"
	if _, err := m.proposePurchase(context.Background(), c2, st, args("91"), map[string]any{}); err == nil || !strings.Contains(err.Error(), "bot shopper's 90.00 USD per-purchase") {
		t.Fatalf("the bot's cap: %v", err)
	}
}

// Each layer's window holds its own: a bot's, that bot's spend; a team's,
// its bots' (not an outside bot's); a team's cap on one card, its bots'
// spend on that card. A paid purchase's view does not count itself.
func TestLayerWindowsHoldTheirOwn(t *testing.T) {
	l, _ := ledger(t)
	team := &config.PurchaseLimits{CardLimits: config.CardLimits{PerPurchase: 100, PerDay: 500, PerMonth: 900},
		Cards: map[string]config.CardLimits{"visa": {PerPurchase: 60, PerDay: 80, PerMonth: 200}}}
	rulesOf := func(who Principal, teamName string) SpendRules {
		r := SpendRules{Card: bigCard(), Who: who, Bot: who.Bot, Team: teamName,
			BotLimits: &config.PurchaseLimits{CardLimits: config.CardLimits{PerPurchase: 100, PerDay: 300, PerMonth: 600}}}
		if teamName != "" {
			r.TeamLimits = team
		}
		return r
	}
	pay := func(who Principal, teamName string, amt float64) Purchase {
		t.Helper()
		r := rulesOf(who, teamName)
		p := buyP(amt)
		p.Identity, p.Bot, p.CaseRef, p.BudgetKey = who.Identity, who.Bot, "", ""
		q, err := l.ProposeWithin(p, time.Hour, r)
		if err == nil {
			_, err = l.ApproveWithin(q.ID, q.Digest, "op", time.Hour, false, r)
		}
		if err == nil {
			q, err = l.Consume(q.ID, who, r)
		}
		if err != nil {
			t.Fatal(err)
		}
		return q
	}
	helper := Principal{Identity: "agent:helper@home", Bot: "helper", Groups: []string{"bot:helper", "team:ops"}}
	outsider := Principal{Identity: "agent:solo@home", Bot: "solo", Groups: []string{"bot:solo"}}
	mine := pay(botCaller, "ops", 30)
	pay(helper, "ops", 20)
	pay(outsider, "", 70)
	used := func(states []LayerState, layer, window string) float64 {
		for _, s := range states {
			if s.Layer == layer && s.Window == window {
				return s.Used
			}
		}
		t.Fatalf("no %s (%s) in %+v", layer, window, states)
		return 0
	}
	states, err := l.Evaluate(rulesOf(botCaller, "ops"), buyP(5), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		layer string
		want  float64
	}{{"card visa", 120}, {"bot shopper", 30}, {"team ops", 50}, {"team ops on card visa", 50}} {
		if got := used(states, c.layer, "day"); got != c.want {
			t.Errorf("%s day: used %.2f, want %.2f", c.layer, got, c.want)
		}
	}
	// The team's 80 a day on this card: 30 left of it decides a 31.
	if _, err := l.Evaluate(rulesOf(botCaller, "ops"), buyP(31), ""); err == nil || !strings.Contains(err.Error(), "team ops on card visa's 80.00 USD daily limit: 30.00 USD left") {
		t.Fatalf("the team's per-card day: %v", err)
	}
	// A paid purchase's own view leaves itself out.
	states, err = l.Evaluate(rulesOf(botCaller, "ops"), mine, mine.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := used(states, "bot shopper", "day"); got != 0 {
		t.Errorf("a paid purchase counted itself: %.2f", got)
	}
}
