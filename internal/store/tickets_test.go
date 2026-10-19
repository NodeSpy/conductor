package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MIGRATION IN PLACE: a ledger written before tickets loads with every case
// a ticket — open → in_progress, closed → done, an id each (oldest first),
// an assignee, a priority from its urgency, who opened it — written back
// once; a second boot changes nothing and mints nothing.
func TestTicketMigration(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	old := `{"sre": {"name": "sre", "cases": {
	  "c2": {"id": "c2", "title": "db down", "status": "open", "urgent": true, "source": "pd.incident", "requester": "ops", "requester_class": "operator", "created": "2026-10-01T10:00:00Z", "updated": "2026-10-01T10:00:00Z"},
	  "c1": {"id": "c1", "title": "old page", "status": "closed", "source": "pd.incident", "requester": "", "requester_class": "event", "created": "2026-09-01T10:00:00Z", "updated": "2026-09-02T10:00:00Z"},
	  "c3": {"id": "c3", "title": "from a peer", "status": "open", "source": "bot:web", "requester": "bot:web", "requester_class": "peer", "created": "2026-10-02T10:00:00Z", "updated": "2026-10-02T10:00:00Z"}
	}}}`
	if err := os.WriteFile(filepath.Join(dir, "bots.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(Options{StatePath: p})
	if err != nil {
		t.Fatal(err)
	}
	r := s.Bot("sre")
	c1, c2, c3 := r.Cases["c1"], r.Cases["c2"], r.Cases["c3"]
	if c1.Status != TicketDone || c2.Status != TicketInProgress || c3.Status != TicketInProgress {
		t.Fatalf("statuses: %s %s %s", c1.Status, c2.Status, c3.Status)
	}
	if c1.Ticket != "T-1" || c2.Ticket != "T-2" || c3.Ticket != "T-3" {
		t.Fatalf("ids, oldest first: %s %s %s", c1.Ticket, c2.Ticket, c3.Ticket)
	}
	if c2.Priority != "urgent" || c1.Priority != "normal" || c2.Assignee != "bot:sre" {
		t.Fatalf("priority/assignee: %+v", c2)
	}
	if c2.CreatedBy != "operator:ops" || c1.CreatedBy != "pd.incident" || c3.CreatedBy != "bot:web" {
		t.Fatalf("created by: %s %s %s", c2.CreatedBy, c1.CreatedBy, c3.CreatedBy)
	}
	if len(c2.History) != 1 || c2.History[0].What != "migrated from a case (open → in_progress)" || c2.History[0].By != "operator:ops" {
		t.Fatalf("migration history: %+v", c2.History)
	}
	disk, _ := os.ReadFile(filepath.Join(dir, "bots.json"))
	if strings.Contains(string(disk), `"status": "open"`) || !strings.Contains(string(disk), `"ticket": "T-2"`) {
		t.Fatalf("not written back:\n%s", disk)
	}
	// A second boot: the same ids, nothing minted.
	s2, err := Open(Options{StatePath: p})
	if err != nil {
		t.Fatal(err)
	}
	if s2.Bot("sre").Cases["c2"].Ticket != "T-2" {
		t.Fatal("ids changed across a reopen")
	}
	if id, _ := s2.NextTicket(); id != "T-4" {
		t.Fatalf("the counter after migration: %s", id)
	}
	// A case written the old way after boot is migrated as it is stored.
	_ = s2.UpdateBot("sre", func(r *BotRecord) error {
		r.Cases["c9"] = &Case{ID: "c9", Title: "new", Status: "open", Source: "x"}
		return nil
	})
	if c := s2.Bot("sre").Cases["c9"]; c.Ticket != "T-5" || c.Status != TicketInProgress {
		t.Fatalf("a new case: %+v", c)
	}
}

// Queues hold the tickets no bot has yet, and persist.
func TestTicketQueues(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "state.json")
	s, _ := Open(Options{StatePath: p})
	_ = s.UpdateQueue("team:platform", func(q map[string]*Case) error {
		q["T-1"] = &Case{ID: "T-1", Ticket: "T-1", Title: "idea", Status: TicketDraft}
		return nil
	})
	s2, _ := Open(Options{StatePath: p})
	if q := s2.Queue("team:platform"); q["T-1"] == nil || q["T-1"].Title != "idea" {
		t.Fatalf("queue after reopen: %v", q)
	}
	if n := s2.QueueNames(); len(n) != 1 || n[0] != "team:platform" {
		t.Fatalf("queue names: %v", n)
	}
	for _, st := range []string{TicketReady, TicketInProgress, TicketWaiting} {
		if !Active(st) || Closed(st) {
			t.Errorf("%s", st)
		}
	}
	for _, st := range []string{TicketDone, TicketDeclined} {
		if Active(st) || !Closed(st) {
			t.Errorf("%s", st)
		}
	}
	if Active(TicketDraft) || Closed(TicketDraft) {
		t.Error("draft")
	}
}
