package browser

import (
	"fmt"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// A ticket's spend_limit is a LIFETIME cap: everything committed under it over
// its whole life counts, forever. The ledger prune bounds terminal records,
// but a used/completed purchase that still counts against an open ticket's cap
// must never be evicted — dropping it silently frees that spend and lets the
// ticket overspend. Regression guard for the round-3 prune (#1).
func TestPurchasePruneKeepsTicketCappedSpend(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := &Purchases{Dir: t.TempDir(), Now: func() time.Time { return base.Add(2 * time.Hour) }}

	const key = "T-1"
	if err := l.update(func(f *purchasesFile) error {
		// A completed $100 purchase committed under ticket T-1's lifetime cap,
		// old enough to be the oldest terminal record.
		f.Purchases["done"] = &Purchase{ID: "done", Status: PurchaseDone, Card: "visa",
			Currency: "USD", Total: 100, BudgetKey: key, Created: base}
		// Enough newer terminal records to push the ledger past its retention
		// bound, so the prune on this write evicts the oldest.
		for i := 0; i < keepTerminalPurchases+50; i++ {
			id := fmt.Sprintf("x%d", i)
			f.Purchases[id] = &Purchase{ID: id, Status: PurchaseExpired, Created: base.Add(time.Duration(i+1) * time.Minute)}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The prune must not have dropped the ticket-capped purchase: its $100
	// still counts against the lifetime cap.
	if used, ids := l.Committed(key); used != 100 || len(ids) != 1 {
		t.Fatalf("after prune, ticket %s committed = %.2f via %v; the capped purchase was evicted and its spend lost", key, used, ids)
	}

	// And a further purchase that would exceed the $120 lifetime cap is refused.
	card := Card{Name: "visa", Merchants: []string{"https://shop.example.com"},
		Limits: config.CardLimits{PerPurchase: 1000, PerDay: 1000, PerMonth: 1000}}
	budget := CaseBudget{Key: key, Amount: 120, Currency: "USD", Card: "visa"}
	next := Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 30, Currency: "USD",
		Identity: "agent:a@home", BudgetKey: key, CaseRef: "bot:a/cA"}
	if _, err := l.ProposeWithin(next, time.Hour, SpendRules{Card: card, Budget: &budget}); err == nil {
		t.Fatalf("a $30 purchase on top of the $100 already committed was allowed — the $120 lifetime cap was defeated by the prune")
	}
}
