package store

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// A bot's ledger is bounded on write: the spend log is trimmed to its cap
// (newest kept) and closed cases past the retention count are evicted
// (oldest first), while active cases and in-flight spend stay — so a
// long-lived busy bot does not pay an ever-growing marshal and case scan.
func TestBotLedgerPrunes(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{StatePath: filepath.Join(dir, "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := s.UpdateBot("sre", func(r *BotRecord) error {
		for i := 0; i < keepBotSpend+100; i++ {
			r.Spend = append(r.Spend, SpendEntry{Amount: 1, At: base.Add(time.Duration(i) * time.Second), Proposal: fmt.Sprintf("s%d", i)})
		}
		if r.Cases == nil {
			r.Cases = map[string]*Case{}
		}
		for i := 0; i < keepClosedCases+50; i++ {
			id := fmt.Sprintf("c%d", i)
			r.Cases[id] = &Case{ID: id, Ticket: fmt.Sprintf("T-%d", i), Status: TicketDone, Updated: base.Add(time.Duration(i) * time.Minute)}
		}
		for _, id := range []string{"live1", "live2", "live3"} {
			r.Cases[id] = &Case{ID: id, Ticket: "T-" + id, Status: TicketInProgress, Updated: base}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	rec := s.Bot("sre")
	if len(rec.Spend) != keepBotSpend {
		t.Fatalf("spend kept %d, want %d", len(rec.Spend), keepBotSpend)
	}
	if rec.Spend[0].Proposal != fmt.Sprintf("s%d", 100) {
		t.Fatalf("oldest spend not trimmed (first is %s)", rec.Spend[0].Proposal)
	}

	closed := 0
	for _, c := range rec.Cases {
		if Closed(c.Status) {
			closed++
		}
	}
	if closed != keepClosedCases {
		t.Fatalf("closed cases kept %d, want %d", closed, keepClosedCases)
	}
	for _, id := range []string{"live1", "live2", "live3"} {
		if rec.Cases[id] == nil {
			t.Fatalf("active case %s was evicted", id)
		}
	}
	if rec.Cases["c0"] != nil {
		t.Fatal("the oldest closed case was not evicted")
	}
	if rec.Cases[fmt.Sprintf("c%d", keepClosedCases+49)] == nil {
		t.Fatal("the newest closed case was evicted")
	}
}
