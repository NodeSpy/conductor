package main

import (
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser"
)

// M1 (correctness) / L1 (security): bot.SpendLedger is wired in production to
// the daemon's live browser purchase ledger. Before this it was never assigned,
// so a ticket's Spent view was permanently blind. spendLedger resolves the live
// manager (wired after the bots) at call time and reports its committed spend;
// with no browser it says so rather than silently reporting zero.
func TestSpendLedgerReadsLiveBrowser(t *testing.T) {
	// No browser on this daemon: the ledger reports unknown, not a bogus zero.
	browserMgr.Store(nil)
	if _, err := (spendLedger{}).SpentOnTicket("T-1", "USD"); err == nil {
		t.Fatal("with no browser the spend ledger must report it cannot tell, not zero")
	}

	// A committed purchase under the ticket's stable budget key.
	purch := &browser.Purchases{Dir: t.TempDir()}
	if _, err := purch.Propose(browser.Purchase{Card: "visa", Merchant: "https://shop.example.com",
		Total: 42.50, Currency: "USD", Identity: "agent:a@home", Bot: "a", CaseID: "cA", BudgetKey: "T-1"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	browserMgr.Store(&browser.Manager{Purchases: purch})
	defer browserMgr.Store(nil)

	cents, err := (spendLedger{}).SpentOnTicket("T-1", "USD")
	if err != nil {
		t.Fatal(err)
	}
	if cents != 4250 {
		t.Fatalf("a committed purchase must show up in the ticket's spend: got %d cents, want 4250", cents)
	}
}
