package bot

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/store"
)

// recordingSpend records which tickets were closed on the spend ledger.
type recordingSpend struct{ closed []string }

func (r *recordingSpend) SpentOnTicket(string, string) (int64, error) { return 0, nil }
func (r *recordingSpend) CloseTicket(ticketID string) error {
	r.closed = append(r.closed, ticketID)
	return nil
}

// Closing a ticket prunes its purchase records from the spend ledger: a closed
// ticket's committing purchases count toward no open cap, so they are reclaimed
// rather than retained forever (#1). The prune is keyed by the ticket's stable
// id.
func TestCloseTicketPrunesPurchases(t *testing.T) {
	f := newTeamFixture(t, nil)
	f.svc.Audit = func(map[string]any) {}
	spend := &recordingSpend{}
	f.svc.Spend = spend
	ctx := context.Background()

	tk := f.ticket(t, opActor, TicketNew{Title: "close me", Assignee: "bot:a"})
	if _, err := f.svc.TicketSetStatus(ctx, opActor, tk.Ticket, store.TicketDeclined, "not needed"); err != nil {
		t.Fatal(err)
	}

	if len(spend.closed) != 1 || spend.closed[0] != tk.Ticket {
		t.Fatalf("closing the ticket did not prune its purchases by ticket id: %v (want [%s])", spend.closed, tk.Ticket)
	}
}
