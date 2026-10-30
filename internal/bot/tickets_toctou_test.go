package bot

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/NodeSpy/conductor/internal/store"
)

// hookLedger wraps the store and runs hook once, right as the first UpdateBot
// begins — the instant between a ticket op's lock-free snapshot read and its
// locked write. It lets a test interpose a competing committed write there
// deterministically, reproducing the TOCTOU without racing the scheduler.
type hookLedger struct {
	*store.Store
	hook func()
	once sync.Once
}

func (h *hookLedger) UpdateBot(name string, fn func(*store.BotRecord) error) error {
	h.once.Do(func() {
		if h.hook != nil {
			h.hook()
		}
	})
	return h.Store.UpdateBot(name, fn)
}

// Ticket writes validate-and-write atomically under the lock, not against a
// pre-lock snapshot: concurrent terminal transitions settle to exactly one
// (no state-machine bypass), and a reassignment does not clobber a comment
// committed between its read and its write (no lost update).
func TestTicketConcurrentWritesAtomic(t *testing.T) {
	// --- No state-machine bypass: a transition validated against a stale
	// snapshot must re-validate against the live record and be refused when a
	// competing write closed the ticket in between. ---
	t.Run("invalid transition refused", func(t *testing.T) {
		f := newTeamFixture(t, nil)
		f.svc.Audit = func(map[string]any) {}
		ctx := context.Background()
		// A held ticket in an active, non-terminal state (its moves go through
		// updateTicket, the path the recheck guards).
		tk := f.ticket(t, opActor, TicketNew{Title: "x", Assignee: "bot:a", Ready: true})
		if v, _ := f.svc.TicketShow(tk.Ticket); v.Status != store.TicketInProgress {
			t.Fatalf("setup: ticket is %s, want in_progress", v.Status)
		}
		// Between TicketSetStatus's snapshot read and its locked write, a
		// competing write closes the ticket to done.
		f.svc.Ledger = &hookLedger{Store: f.st, hook: func() {
			_ = f.st.UpdateBot("a", func(r *store.BotRecord) error {
				for _, c := range r.Cases {
					if c.Ticket == tk.Ticket {
						c.Status = store.TicketDone
					}
				}
				return nil
			})
		}}
		// in_progress → waiting passed the pre-lock check on the stale
		// snapshot; the live record is now done, from which waiting is invalid.
		if _, err := f.svc.TicketSetStatus(ctx, opActor, tk.Ticket, store.TicketWaiting, ""); err == nil {
			t.Fatal("a transition validated only against the stale snapshot overwrote the live terminal state")
		}
		if v, _ := f.svc.TicketShow(tk.Ticket); v.Status != store.TicketDone {
			t.Fatalf("the live done status was overwritten to %s", v.Status)
		}
	})

	// --- No lost update: a reassignment keeps concurrent comments. ---
	t.Run("no lost comment", func(t *testing.T) {
		f := newTeamFixture(t, nil)
		f.svc.Audit = func(map[string]any) {} // the fixture's sink is not concurrency-safe
		ctx := context.Background()
		tk := f.ticket(t, opActor, TicketNew{Title: "rotate keys", Assignee: "bot:a"})

		const comments = 200
		var ok int64
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < comments; i++ {
				if _, err := f.svc.TicketComment(ctx, opActor, tk.Ticket, "note", nil); err == nil {
					atomic.AddInt64(&ok, 1)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < comments; i++ {
				// Same-home draft reassign: the field-level write path.
				_, _ = f.svc.TicketAssign(ctx, opActor, tk.Ticket, "bot:a")
			}
		}()
		wg.Wait()

		v, err := f.svc.TicketShow(tk.Ticket)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(v.Comments)) != ok {
			t.Fatalf("kept %d comments, but %d were committed — a reassignment clobbered some", len(v.Comments), ok)
		}
	})
}
