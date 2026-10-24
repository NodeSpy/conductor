package bot

import (
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/store"
)

// purchasesLedger is the real durable SpendLedger over a browser purchase
// ledger, keyed by the ticket's stable id.
type purchasesLedger struct{ p *browser.Purchases }

func (x purchasesLedger) SpentOnTicket(ticketID, currency string) (int64, error) {
	return x.p.CommittedCents(ticketID, currency)
}

// A ticket's spend view must read the durable per-ticket total, not sum the
// ticket's per-bot case copies: a reassigned ticket's old copy is closed and
// eventually pruned from the bot ledger, but its spend stays in the purchase
// ledger under the ticket's stable id. Regression guard for #6 (the display
// under-reported after a closed-case prune) — ties to #1 (the purchase stays).
func TestTicketSpendReadsDurableTotal(t *testing.T) {
	f := newTeamFixture(t, nil)
	f.svc.Audit = func(map[string]any) {}

	// A ticket now held by bot b, with a $120 lifetime cap.
	tk := f.ticket(t, opActor, TicketNew{Title: "buy a drive", Assignee: "bot:b",
		SpendLimit: &store.SpendLimit{Amount: "120", Currency: "USD"}})

	// $100 was committed while the ticket lived on bot a; a's copy has since been
	// pruned from the bot ledger (closed-case eviction), so no store case for
	// that copy survives — but the purchase remains under the ticket's stable id.
	purch := &browser.Purchases{Dir: t.TempDir()}
	if _, err := purch.Propose(browser.Purchase{Card: "visa", Merchant: "https://shop.example.com",
		Total: 100, Currency: "USD", Identity: "agent:a@home", Bot: "a", CaseID: "old-a", BudgetKey: tk.Ticket}, time.Hour); err != nil {
		t.Fatal(err)
	}
	f.svc.Spend = purchasesLedger{p: purch}

	_, spent, ok := f.svc.TicketSpend("b", tk.ID)
	if !ok {
		t.Fatal("no spend status for the ticket")
	}
	if !spent.Known || spent.Spent != "100.00" || spent.Remaining != "20.00" {
		t.Fatalf("spend under-reported after the old copy was pruned: spent=%q remaining=%q (want 100.00 / 20.00)", spent.Spent, spent.Remaining)
	}
}
