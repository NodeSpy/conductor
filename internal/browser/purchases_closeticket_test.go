package browser

import (
	"fmt"
	"testing"
	"time"
)

// A ticket's committing (used/completed) purchases carry its lifetime-cap spend,
// so the ordinary prune keeps them forever (90e695bb) — but once the ticket is
// CLOSED that spend counts toward no open cap, and keeping a record per purchase
// for every ticket ever grows the ledger (and its per-op marshal) without bound.
// PruneTicket drops a closed ticket's terminal records, leaving every OTHER
// ticket's spend intact.
func TestPruneTicketDropsClosedTicketPurchases(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := &Purchases{Dir: t.TempDir(), Now: func() time.Time { return base }}
	const open, closed = "T-open", "T-closed"

	const perTicket = 500
	if err := l.update(func(f *purchasesFile) error {
		for i := 0; i < perTicket; i++ {
			for _, k := range []string{open, closed} {
				id := fmt.Sprintf("%s-%d", k, i)
				f.Purchases[id] = &Purchase{ID: id, Status: PurchaseDone, Card: "visa",
					Currency: "USD", Total: 1, BudgetKey: k, Created: base}
			}
		}
		// A still-pending purchase under the closed ticket must survive the close
		// (only terminal records are pruned).
		f.Purchases["pending-closed"] = &Purchase{ID: "pending-closed", Status: PurchasePending,
			Card: "visa", Currency: "USD", Total: 7, BudgetKey: closed, Created: base}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Before close: the committing records are retained (the ordinary prune never
	// evicts a committing+budget-key record) — the #1 keep-through-prune.
	if used, _ := l.Committed(closed); used == 0 {
		t.Fatal("setup: the closed ticket should have committed spend before close")
	}

	n, err := l.PruneTicket(closed)
	if err != nil {
		t.Fatal(err)
	}
	if n != perTicket {
		t.Fatalf("PruneTicket dropped %d records, want %d (the terminal ones)", n, perTicket)
	}

	// The closed ticket's terminal records are gone; the pending one survives.
	cnt := map[string]int{}
	if err := l.update(func(f *purchasesFile) error {
		for _, p := range f.Purchases {
			cnt[p.BudgetKey]++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if cnt[closed] != 1 {
		t.Fatalf("after close, %d records remain for the closed ticket, want 1 (the pending one)", cnt[closed])
	}
	if cnt[open] != perTicket {
		t.Fatalf("pruning the closed ticket dropped the OPEN ticket's records: %d of %d remain", cnt[open], perTicket)
	}
	// The open ticket's lifetime spend is untouched — the cap still holds.
	if used, _ := l.Committed(open); used != float64(perTicket) {
		t.Fatalf("the open ticket's committed spend changed to %.2f, want %d", used, perTicket)
	}
	if used, _ := l.Committed(closed); used != 0 {
		t.Fatalf("the closed ticket still reports %.2f committed", used)
	}
}
