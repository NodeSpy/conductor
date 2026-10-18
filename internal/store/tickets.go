package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TICKETS (docs/wiki/Tickets.md). Every case is a ticket: a bot's ledger
// holds the tickets assigned to it; tickets.json holds the rest — a team's
// tickets no bot has picked up yet (its drafts), and the triage queue of
// unassigned ones — and the conductor-wide ticket counter.
//
//	draft → ready → in_progress → waiting → done | declined   (and reopen)
//
// Draft is the ideas space: nothing starts on a draft — no worker, routing
// or delegation. Ready, with an assignee, makes it the assignee's to pick up.

// Ticket statuses.
const (
	TicketDraft      = "draft"
	TicketReady      = "ready"
	TicketInProgress = "in_progress"
	TicketWaiting    = "waiting"
	TicketDone       = "done"
	TicketDeclined   = "declined"
)

// TicketStatuses are every status, in order.
var TicketStatuses = []string{TicketDraft, TicketReady, TicketInProgress, TicketWaiting, TicketDone, TicketDeclined}

// Active reports a status work may happen on: ready, in progress, waiting.
func Active(status string) bool {
	return status == TicketReady || status == TicketInProgress || status == TicketWaiting
}

// Closed reports a finished ticket: done or declined.
func Closed(status string) bool { return status == TicketDone || status == TicketDeclined }

// Ticket priorities, lowest first.
var TicketPriorities = []string{"low", "normal", "high", "urgent"}

// TriageQueue is the queue of unassigned tickets.
const TriageQueue = "triage"

// ticketFile is tickets.json.
type ticketFile struct {
	Next   int                         `json:"next"`
	Queues map[string]map[string]*Case `json:"queues,omitempty"` // queue ("team:<name>" | "triage") → id → ticket
}

func (s *Store) ticketsPath() string {
	if s.path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.path), "tickets.json")
}

func (s *Store) loadTickets() {
	s.tickets = ticketFile{}
	p := s.ticketsPath()
	if p == "" {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &s.tickets); err != nil {
		_ = os.Rename(p, fmt.Sprintf("%s.corrupt-%d", p, time.Now().Unix()))
		s.tickets = ticketFile{}
	}
}

func (s *Store) saveTickets() error {
	return s.persist(func() ([]byte, string, error) {
		b, err := json.MarshalIndent(s.tickets, "", " ")
		return b, s.ticketsPath(), err
	})
}

// nextTicketLocked mints the next ticket id. s.mu held.
func (s *Store) nextTicketLocked() string {
	s.tickets.Next++
	return "T-" + strconv.Itoa(s.tickets.Next)
}

// NextTicket mints a conductor-wide ticket id (persisted).
func (s *Store) NextTicket() (string, error) {
	s.mu.Lock()
	id := s.nextTicketLocked()
	s.mu.Unlock()
	return id, s.saveTickets()
}

// Queue is a copy of one queue's tickets.
func (s *Store) Queue(name string) map[string]*Case {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]*Case{}
	for id, c := range s.tickets.Queues[name] {
		cp := cloneCase(c)
		out[id] = cp
	}
	return out
}

// QueueNames lists the queues that hold tickets, sorted.
func (s *Store) QueueNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for q, m := range s.tickets.Queues {
		if len(m) > 0 {
			out = append(out, q)
		}
	}
	sort.Strings(out)
	return out
}

// UpdateQueue edits a copy of one queue under its lock and persists it.
func (s *Store) UpdateQueue(name string, fn func(q map[string]*Case) error) error {
	ml := s.botLock("queue:" + name)
	ml.Lock()
	defer ml.Unlock()
	s.mu.Lock()
	work := map[string]*Case{}
	for id, c := range s.tickets.Queues[name] {
		work[id] = cloneCase(c)
	}
	if err := fn(work); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.tickets.Queues == nil {
		s.tickets.Queues = map[string]map[string]*Case{}
	}
	s.tickets.Queues[name] = work
	s.mu.Unlock()
	return s.saveTickets()
}

func cloneCase(c *Case) *Case {
	b, _ := json.Marshal(c)
	var out Case
	_ = json.Unmarshal(b, &out)
	return &out
}

// migrateTickets brings stored cases to tickets, in place, once: open →
// in_progress, closed → done, a ticket id and assignee for each, the
// priority from its urgency. A ledger written before tickets loads, and the
// next save writes it in the new form; run at Open, before any writer.
func (s *Store) migrateTickets() error {
	changed := false
	names := make([]string, 0, len(s.bots))
	for n := range s.bots {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := s.bots[n]
		ids := make([]string, 0, len(r.Cases))
		for id := range r.Cases {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return caseOrder(r.Cases[ids[i]], r.Cases[ids[j]]) })
		for _, id := range ids {
			if migrateCase(r.Cases[id], n, s.nextTicketLocked) {
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	if err := s.saveBots(); err != nil {
		return err
	}
	return s.saveTickets()
}

func caseOrder(a, b *Case) bool {
	if !a.Created.Equal(b.Created) {
		return a.Created.Before(b.Created)
	}
	return a.ID < b.ID
}

// migrateCase brings one pre-ticket case to a ticket (true when it changed).
func migrateCase(c *Case, bot string, next func() string) bool {
	changed := false
	switch c.Status {
	case "open":
		c.Status, changed = TicketInProgress, true
	case "closed":
		c.Status, changed = TicketDone, true
	}
	if c.Ticket == "" {
		c.Ticket, changed = next(), true
	}
	if c.Assignee == "" {
		c.Assignee, changed = "bot:"+bot, true
	}
	if c.Priority == "" {
		c.Priority = "normal"
		if c.Urgent {
			c.Priority = "urgent"
		}
		changed = true
	}
	if c.CreatedBy == "" {
		c.CreatedBy, changed = createdByOf(c), true
	}
	return changed
}

// createdByOf names who opened a pre-ticket case.
func createdByOf(c *Case) string {
	switch {
	case c.RequesterClass == "operator" && c.Requester != "":
		return "operator:" + c.Requester
	case strings.HasPrefix(c.Requester, "bot:"):
		return c.Requester
	case c.Source != "":
		return c.Source
	}
	return "conductor"
}
