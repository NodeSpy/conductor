package bot

import (
	"context"
	"errors"
	"testing"

	"github.com/NodeSpy/conductor/internal/store"
)

// failBotLedger fails the nth UpdateBot call for one bot, to simulate a store
// write failing mid-reassign (the destination place, or the source removal).
type failBotLedger struct {
	*store.Store
	bot   string
	nth   int // 1-based: fail the nth UpdateBot(bot); 0 = never
	calls int
}

func (h *failBotLedger) UpdateBot(name string, fn func(*store.BotRecord) error) error {
	if name == h.bot {
		h.calls++
		if h.calls == h.nth {
			return errors.New("simulated store write failure")
		}
	}
	return h.Store.UpdateBot(name, fn)
}

func liveCopies(f *teamFixture, tid string) int {
	n := 0
	for _, b := range []string{"lead", "a", "b", "c", "solo"} {
		for _, c := range f.st.Bot(b).Cases {
			if c.Ticket == tid && c.MovedTo == "" {
				n++
			}
		}
	}
	if q, ok := f.svc.Ledger.(queueLedger); ok {
		for _, name := range q.QueueNames() {
			for _, c := range q.Queue(name) {
				if c.Ticket == tid && c.MovedTo == "" {
					n++
				}
			}
		}
	}
	return n
}

func stubCount(f *teamFixture, tid string) int {
	n := 0
	for _, b := range []string{"lead", "a", "b", "c", "solo"} {
		for _, c := range f.st.Bot(b).Cases {
			if c.Ticket == tid && c.MovedTo == ticketMoving {
				n++
			}
		}
	}
	return n
}

// A cross-home draft reassign whose destination PLACE fails must release the
// claim: the source goes back to live (no stuck "(reassigning)" stub), and no
// destination copy exists. Regression guard for #3 gap (a) in-process.
func TestTicketReassignPlaceFailureReleasesClaim(t *testing.T) {
	f := newTeamFixture(t, nil)
	f.svc.Audit = func(map[string]any) {}
	ctx := context.Background()

	tk := f.ticket(t, opActor, TicketNew{Title: "place will fail", Assignee: "bot:a"})
	if tk.Status != store.TicketDraft {
		t.Fatalf("setup: %s, want draft", tk.Status)
	}
	// placeTicket is the first UpdateBot("b") after the swap.
	f.svc.Ledger = &failBotLedger{Store: f.st, bot: "b", nth: 1}

	if _, err := f.svc.TicketAssign(ctx, opActor, tk.Ticket, "bot:b"); err == nil {
		t.Fatal("a reassign whose place failed must report an error")
	}
	if n := stubCount(f, tk.Ticket); n != 0 {
		t.Fatalf("a failed place left %d stuck (reassigning) claims; want 0", n)
	}
	if n := liveCopies(f, tk.Ticket); n != 1 {
		t.Fatalf("after a failed place, %d live copies exist; want exactly 1 (the source, recovered)", n)
	}
}

// A cross-home draft reassign whose place SUCCEEDS but whose source removal
// fails must not leave a stuck "(reassigning)" claim, and must not leave two
// live copies — the destination copy is live, the source is finalized to a
// moved stub. Regression guard for #3 gap (b).
func TestTicketReassignPostPlaceFailureNoStuckClaim(t *testing.T) {
	f := newTeamFixture(t, nil)
	f.svc.Audit = func(map[string]any) {}
	ctx := context.Background()

	tk := f.ticket(t, opActor, TicketNew{Title: "removal will fail", Assignee: "bot:a"})
	// On "a": #1 = claim (ok), #2 = the post-place delete (fails), #3 = the
	// finalize (ok). placeTicket writes "b", unaffected.
	f.svc.Ledger = &failBotLedger{Store: f.st, bot: "a", nth: 2}

	_, _ = f.svc.TicketAssign(ctx, opActor, tk.Ticket, "bot:b")

	if n := stubCount(f, tk.Ticket); n != 0 {
		t.Fatalf("a failed source removal left %d stuck (reassigning) claims; want 0", n)
	}
	if n := liveCopies(f, tk.Ticket); n != 1 {
		t.Fatalf("after a failed source removal, %d live copies exist; want exactly 1 (the destination)", n)
	}
	// The one live copy is the destination (bot b).
	found := false
	for _, c := range f.st.Bot("b").Cases {
		if c.Ticket == tk.Ticket && c.MovedTo == "" {
			found = true
		}
	}
	if !found {
		t.Fatal("the live copy is not the destination (bot b)")
	}
}

// Startup reconciliation recovers a crash mid-reassign: a dangling
// "(reassigning)" claim whose destination copy never landed is cleared (the
// source goes live again); one whose destination copy IS present is dropped
// (the move completed, the stub is leftover). Regression guard for #3.
func TestReconcileTicketClaims(t *testing.T) {
	t.Run("dest absent → source recovered", func(t *testing.T) {
		f := newTeamFixture(t, nil)
		tk := f.ticket(t, opActor, TicketNew{Title: "recover me", Assignee: "bot:a"})
		aid := ""
		for id, c := range f.st.Bot("a").Cases {
			if c.Ticket == tk.Ticket {
				aid = id
			}
		}
		if aid == "" {
			t.Fatal("setup: no case on a")
		}
		// Plant a dangling claim with no destination copy anywhere.
		if err := f.st.UpdateBot("a", func(r *store.BotRecord) error {
			r.Cases[aid].MovedTo = ticketMoving
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := f.svc.findTicket(tk.Ticket); err == nil {
			t.Fatal("precondition: a claimed stub must be invisible to findTicket")
		}

		f.svc.reconcileTicketClaims()

		c := f.st.Bot("a").Cases[aid]
		if c == nil || c.MovedTo != "" {
			t.Fatalf("a dangling claim with no destination was not recovered: %+v", c)
		}
		if n := liveCopies(f, tk.Ticket); n != 1 {
			t.Fatalf("after recovery, %d live copies; want 1", n)
		}
	})

	t.Run("dest present → stub dropped", func(t *testing.T) {
		f := newTeamFixture(t, nil)
		tk := f.ticket(t, opActor, TicketNew{Title: "already moved", Assignee: "bot:a"})
		aid := ""
		for id, c := range f.st.Bot("a").Cases {
			if c.Ticket == tk.Ticket {
				aid = id
			}
		}
		// Plant the dangling claim on a, and a live destination copy on b.
		if err := f.st.UpdateBot("a", func(r *store.BotRecord) error {
			r.Cases[aid].MovedTo = ticketMoving
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := f.st.UpdateBot("b", func(r *store.BotRecord) error {
			if r.Cases == nil {
				r.Cases = map[string]*store.Case{}
			}
			r.Cases["cdest"] = &store.Case{ID: "cdest", Ticket: tk.Ticket, Status: store.TicketDraft}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		f.svc.reconcileTicketClaims()

		if c := f.st.Bot("a").Cases[aid]; c != nil {
			t.Fatalf("a completed move's leftover stub was not dropped: %+v", c)
		}
		if n := liveCopies(f, tk.Ticket); n != 1 {
			t.Fatalf("after dropping the stub, %d live copies; want exactly 1 (the destination)", n)
		}
	})
}
