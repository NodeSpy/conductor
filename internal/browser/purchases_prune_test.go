package browser

import (
	"fmt"
	"testing"
	"time"
)

// The purchase ledger is bounded: terminal records past the retention count
// are evicted oldest-first, the spend log is trimmed to its cap, and pending
// or approved purchases are never dropped — so a run of lapsed proposals
// cannot grow the file (or its per-op marshal) without limit.
func TestPurchaseLedgerPrunes(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	f := &purchasesFile{Purchases: map[string]*Purchase{}}
	for i := 0; i < keepTerminalPurchases+50; i++ {
		id := fmt.Sprintf("t%d", i)
		f.Purchases[id] = &Purchase{ID: id, Status: PurchaseExpired, Created: base.Add(time.Duration(i) * time.Minute)}
	}
	f.Purchases["pend"] = &Purchase{ID: "pend", Status: PurchasePending, Created: base}
	f.Purchases["appr"] = &Purchase{ID: "appr", Status: PurchaseApproved, Created: base}

	pruneLocked(f)

	terminal := 0
	for _, p := range f.Purchases {
		if purchaseTerminal(p.Status) {
			terminal++
		}
	}
	if terminal != keepTerminalPurchases {
		t.Fatalf("terminal records kept %d, want %d", terminal, keepTerminalPurchases)
	}
	if f.Purchases["pend"] == nil || f.Purchases["appr"] == nil {
		t.Fatal("a pending or approved purchase was evicted")
	}
	if f.Purchases["t0"] != nil {
		t.Fatal("the oldest terminal record was not evicted")
	}
	if f.Purchases[fmt.Sprintf("t%d", keepTerminalPurchases+49)] == nil {
		t.Fatal("the newest terminal record was evicted")
	}

	// The spend log is trimmed through the real write path, newest kept.
	l := &Purchases{Dir: t.TempDir(), Now: func() time.Time { return base }}
	if err := l.update(func(f *purchasesFile) error {
		for i := 0; i < keepSpendEntries+100; i++ {
			f.Spend = append(f.Spend, SpendEntry{Card: "c", Amount: 1, At: base.Add(time.Duration(i) * time.Second), Purchase: fmt.Sprintf("p%d", i)})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := l.loadLocked()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Spend) != keepSpendEntries {
		t.Fatalf("spend entries kept %d, want %d", len(got.Spend), keepSpendEntries)
	}
	if got.Spend[0].Purchase != fmt.Sprintf("p%d", 100) {
		t.Fatalf("oldest spend not trimmed (first is %s)", got.Spend[0].Purchase)
	}
}
