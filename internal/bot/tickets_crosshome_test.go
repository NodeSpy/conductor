package bot

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/store"
)

// Two concurrent cross-home reassignments of one unclaimed draft must settle to
// exactly one live copy — never two independently dispatchable copies of the
// same ticket. The hookLedger interposes a competing assign (→ c) at the first
// assign's (→ b) first write, reproducing the race deterministically. Regression
// guard for the round-3 TicketAssign, which hardened only the same-home path and
// left the cross-home draft branch working off a pre-lock snapshot (#3).
func TestTicketCrossHomeAssignAtomic(t *testing.T) {
	f := newTeamFixture(t, nil)
	f.svc.Audit = func(map[string]any) {}
	ctx := context.Background()

	// A draft held by bot a.
	tk := f.ticket(t, opActor, TicketNew{Title: "route me somewhere", Assignee: "bot:a"})
	if tk.Status != store.TicketDraft {
		t.Fatalf("setup: ticket is %s, want draft", tk.Status)
	}

	// The competing assign runs on a second service over the raw store so it
	// does not re-enter the hook ledger.
	raw := &Service{Cfg: f.svc.Cfg, Ledger: f.st, Gate: f.svc.Gate, Home: f.home, Workers: f.workers,
		SessionRoot: f.root, Now: f.svc.Now, Audit: func(map[string]any) {}}
	f.svc.Ledger = &hookLedger{Store: f.st, hook: func() {
		_, _ = raw.TicketAssign(ctx, opActor, tk.Ticket, "bot:c")
	}}

	_, _ = f.svc.TicketAssign(ctx, opActor, tk.Ticket, "bot:b")

	live := 0
	where := ""
	for _, b := range []string{"lead", "a", "b", "c", "solo"} {
		for _, c := range f.st.Bot(b).Cases {
			if c.Ticket == tk.Ticket && c.MovedTo == "" {
				live++
				where += " bot:" + b
			}
		}
	}
	if q, ok := f.svc.Ledger.(queueLedger); ok {
		for _, name := range q.QueueNames() {
			for _, c := range q.Queue(name) {
				if c.Ticket == tk.Ticket && c.MovedTo == "" {
					live++
					where += " queue:" + name
				}
			}
		}
	}
	if live != 1 {
		t.Fatalf("after two concurrent cross-home assigns, %d live copies exist (%s); want exactly 1 — a duplicate, independently dispatchable ticket", live, where)
	}
}
