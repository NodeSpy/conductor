package browser

import (
	"testing"
	"time"
)

// M1 (correctness) / L1 (security): the ticket spend view was permanently blind
// because bot.SpendLedger had no production implementation. *Purchases now
// implements it, so a committed purchase on a bot's case shows up in the
// ticket's spend. Counts the committing statuses (pending/approved/used/done),
// in the asked currency, for exactly that bot's case.
func TestPurchasesSpentOnCase(t *testing.T) {
	l := &Purchases{Dir: t.TempDir()}
	mk := func(bot, caseID string, total float64, cur string) Purchase {
		p, err := l.Propose(Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: total, Currency: cur,
			Identity: "agent:" + bot + "@home", Bot: bot, CaseID: caseID}, time.Hour)
		if err != nil {
			t.Fatalf("propose: %v", err)
		}
		return p
	}

	// Two pending purchases on bot a / case cA, plus unrelated ones.
	mk("a", "cA", 40, "USD")
	mk("a", "cA", 35.50, "USD")
	mk("a", "cA", 1000, "EUR") // other currency: excluded
	mk("a", "cB", 99, "USD")   // other case: excluded
	mk("b", "cA", 99, "USD")   // other bot: excluded

	cents, err := l.SpentOnCase("a", "cA", "USD")
	if err != nil {
		t.Fatal(err)
	}
	if cents != 7550 {
		t.Fatalf("committed on a/cA in USD: got %d cents, want 7550", cents)
	}

	// Currency match is case-insensitive and the EUR total is separate.
	if c, _ := l.SpentOnCase("a", "cA", "eur"); c != 100000 {
		t.Fatalf("a/cA in EUR: got %d cents, want 100000", c)
	}
	// An unknown case has spent nothing (not an error).
	if c, err := l.SpentOnCase("a", "nope", "USD"); err != nil || c != 0 {
		t.Fatalf("unknown case: %d %v", c, err)
	}
}

// A refused or denied purchase does not count toward spend — only the
// committing statuses the budget reserves against do.
func TestPurchasesSpentOnCaseExcludesNonCommitting(t *testing.T) {
	l := &Purchases{Dir: t.TempDir()}
	p, err := l.Propose(Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 50, Currency: "USD",
		Identity: "agent:a@home", Bot: "a", CaseID: "cA"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// Pending counts.
	if c, _ := l.SpentOnCase("a", "cA", "USD"); c != 5000 {
		t.Fatalf("pending should count: %d", c)
	}
	// Denied no longer counts.
	if _, err := l.Deny(p.ID, "op", "changed my mind"); err != nil {
		t.Fatal(err)
	}
	if c, _ := l.SpentOnCase("a", "cA", "USD"); c != 0 {
		t.Fatalf("denied should not count: %d", c)
	}
}
