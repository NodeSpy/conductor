package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// H2: a ticket's spend_limit is a LIFETIME cap — "everything proposed under it
// over its whole life ... never more than the amount". A ticket moves between
// bots as separate case copies (TicketAssign → placeTicket mints a fresh case
// id, carryTicket copies the SpendLimit). Enforcement must therefore key the
// budget to the ticket's STABLE id, not the per-copy case reference, or the cap
// resets on every reassignment and the ticket can overspend N×.
func TestTicketSpendCapHoldsAcrossReassignment(t *testing.T) {
	st, err := store.Open(store.Options{StatePath: filepath.Join(t.TempDir(), "s.json")})
	if err != nil {
		t.Fatal(err)
	}
	// Ticket T-1, $120 cap, lived on bot a (case cA), then reassigned to bot b
	// (case cB). Both copies carry the same stable ticket id and the same cap;
	// a's copy is the one left behind (MovedTo), b's is live.
	limit := &store.SpendLimit{Amount: "120.00", Currency: "USD"}
	_ = st.UpdateBot("a", func(r *store.BotRecord) error {
		r.Cases = map[string]*store.Case{"cA": {ID: "cA", Ticket: "T-1", SpendLimit: limit, MovedTo: "bot:b/cB"}}
		return nil
	})
	_ = st.UpdateBot("b", func(r *store.BotRecord) error {
		r.Cases = map[string]*store.Case{"cB": {ID: "cB", Ticket: "T-1", SpendLimit: limit}}
		return nil
	})
	browserLedger.Store(st)
	defer browserLedger.Store(nil)

	budgets := ledgerBudgets{}
	bA, ok := budgets.CaseBudget("bot:a/cA")
	if !ok {
		t.Fatal("no budget on copy A")
	}
	bB, ok := budgets.CaseBudget("bot:b/cB")
	if !ok {
		t.Fatal("no budget on copy B")
	}
	// The two copies of one ticket must share a single accrual key.
	if bA.Key != bB.Key {
		t.Fatalf("a moved ticket's budget is keyed per-copy, not by its stable id: A=%q B=%q", bA.Key, bB.Key)
	}

	// A card with room to spare, so only the ticket layer can bind.
	card := browser.Card{Name: "visa", Merchants: []string{"https://shop.example.com"},
		Limits: config.CardLimits{PerPurchase: 1000, PerDay: 1000, PerMonth: 1000}}
	purch := &browser.Purchases{Dir: t.TempDir()}

	// Copy A spent $100 of the $120 (a committed proposal under its budget key).
	spent := browser.Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 100, Currency: "USD",
		Identity: "agent:a@home", Bot: "a", CaseID: "cA", CaseRef: "bot:a/cA", BudgetKey: bA.Key}
	if _, err := purch.ProposeWithin(spent, time.Hour, browser.SpendRules{Card: card, Budget: &bA}); err != nil {
		t.Fatalf("copy A's $100: %v", err)
	}

	// After reassignment, copy B proposes another $100. The ticket's lifetime
	// cap is $120 with $100 already committed, so only $20 is left: refuse. If
	// the budget reset on the move, this is allowed and the ticket overspends to
	// $200 against its $120 cap.
	again := browser.Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 100, Currency: "USD",
		Identity: "agent:b@home", Bot: "b", CaseID: "cB", CaseRef: "bot:b/cB", BudgetKey: bB.Key}
	_, err = purch.ProposeWithin(again, time.Hour, browser.SpendRules{Card: card, Budget: &bB})
	if err == nil {
		t.Fatalf("copy B's second $100 was allowed — the ticket's $120 lifetime cap reset on reassignment (budget key A=%q B=%q)", bA.Key, bB.Key)
	}
	if !strings.Contains(err.Error(), "ticket") {
		t.Fatalf("copy B was refused, but not by the ticket budget: %v", err)
	}

	// And the committed total under the ticket never exceeds the cap.
	if committed, _ := purch.Committed(bB.Key); committed > 120 {
		t.Fatalf("committed %.2f exceeds the $120 lifetime cap", committed)
	}
}
