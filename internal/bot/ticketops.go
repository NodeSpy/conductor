package bot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/store"
)

// ticketOp serves the ticket ops for an operator (the control socket) and a
// bot's session (its token) alike; the actor decides what is allowed.
//
//	ticket_new     title, description, assignee, priority, due, labels, ready, attachments, spend_limit
//	ticket_list    assignee, holder, status, priority, label, q
//	ticket_show    ticket
//	ticket_edit    ticket, title, description, priority, due ("" clears), labels, spend_limit
//	ticket_assign  ticket, to
//	ticket_comment ticket, text, attachments
//	ticket_status  ticket, to (ready | in_progress | waiting | done | declined | draft | reopen), note
//
// spend_limit is {amount, currency, card, merchants}, or an amount with
// currency, card and merchants beside it; null or "none" clears it.
func (s *Service) ticketOp(ctx context.Context, a TicketActor, sub string, args map[string]any) (map[string]any, error) {
	view := func(v TicketView, err error) (map[string]any, error) {
		if err != nil {
			return nil, err
		}
		return toMap(v), nil
	}
	switch sub {
	case "ticket_new":
		due, err := dueArg(args)
		if err != nil {
			return nil, err
		}
		files, err := filesArg(args)
		if err != nil {
			return nil, err
		}
		_, limit, err := spendArg(args)
		if err != nil {
			return nil, err
		}
		return view(s.TicketCreate(ctx, a, TicketNew{Title: str(args, "title"), Description: str(args, "description"),
			Assignee: str(args, "assignee"), Priority: str(args, "priority"), Due: due, Labels: listArg(args, "labels"),
			Ready: boolArg(args, "ready"), Files: files, SpendLimit: limit}))
	case "ticket_list":
		f := TicketFilter{Assignee: str(args, "assignee"), Holder: str(args, "holder"), Priority: str(args, "priority"),
			Label: str(args, "label"), Query: str(args, "q"), Statuses: listArg(args, "status")}
		if f.Assignee != "" && f.Assignee != "none" {
			as, err := s.parseAssignee(f.Assignee)
			if err != nil {
				return nil, err
			}
			f.Assignee = as
		}
		ts, err := s.Tickets(f)
		if err != nil {
			return nil, err
		}
		var out []any
		for _, t := range ts {
			// A bot sees the tickets it holds or wrote.
			if a.isBot() && t.Holder != a.Bot.Bot && t.CreatedBy != a.String() {
				continue
			}
			out = append(out, toMap(t))
		}
		return map[string]any{"tickets": out}, nil
	case "ticket_show":
		v, err := s.TicketShow(str(args, "ticket"))
		if err != nil {
			return nil, err
		}
		if a.isBot() && v.Holder != a.Bot.Bot && v.CreatedBy != a.String() {
			return nil, fmt.Errorf("ticket %s is not bot %s's: it neither holds it nor wrote it", v.Ticket, a.Bot.Bot)
		}
		return toMap(v), nil
	case "ticket_edit":
		var ch TicketChanges
		for _, k := range []string{"title", "description", "priority"} {
			if _, ok := args[k]; ok {
				v := str(args, k)
				switch k {
				case "title":
					ch.Title = &v
				case "description":
					ch.Description = &v
				case "priority":
					ch.Priority = &v
				}
			}
		}
		if _, ok := args["due"]; ok {
			due, err := dueArg(args)
			if err != nil {
				return nil, err
			}
			ch.Due = &due
		}
		if _, ok := args["labels"]; ok {
			ls := listArg(args, "labels")
			ch.Labels = &ls
		}
		present, limit, err := spendArg(args)
		if err != nil {
			return nil, err
		}
		if present {
			ch.SpendLimit = &limit
		}
		return view(s.TicketEdit(ctx, a, str(args, "ticket"), ch))
	case "ticket_assign":
		return view(s.TicketAssign(ctx, a, str(args, "ticket"), str(args, "to")))
	case "ticket_comment":
		files, err := filesArg(args)
		if err != nil {
			return nil, err
		}
		return view(s.TicketComment(ctx, a, str(args, "ticket"), str(args, "text"), files))
	case "ticket_status":
		return view(s.TicketSetStatus(ctx, a, str(args, "ticket"), str(args, "to"), str(args, "note")))
	}
	return nil, fmt.Errorf("unknown ticket op %q", sub)
}

// spendArg reads a spend limit: spend_limit as an object {amount, currency,
// card, merchants}, or as an amount with currency, card and merchants beside
// it; null or "none" clears it. present false: not mentioned.
func spendArg(args map[string]any) (present bool, limit *store.SpendLimit, err error) {
	v, ok := args["spend_limit"]
	if !ok {
		return false, nil, nil
	}
	switch x := v.(type) {
	case nil:
		return true, nil, nil
	case map[string]any:
		return true, &store.SpendLimit{Amount: str(x, "amount"), Currency: str(x, "currency"), Card: str(x, "card"), Merchants: strs(x, "merchants")}, nil
	case string, float64, int:
		amt := strings.TrimSpace(fmt.Sprint(x))
		if amt == "" || amt == "none" {
			return true, nil, nil
		}
		return true, &store.SpendLimit{Amount: amt, Currency: str(args, "currency"), Card: str(args, "card"), Merchants: listArg(args, "merchants")}, nil
	}
	return false, nil, fmt.Errorf("spend_limit: an amount, or {amount, currency, card, merchants}")
}

// isTicketOp reports a ticket op.
func isTicketOp(sub string) bool { return strings.HasPrefix(sub, "ticket_") }

// dueArg reads a due date: YYYY-MM-DD (end of that day, UTC), RFC 3339, or
// "" for none.
func dueArg(args map[string]any) (*time.Time, error) {
	v := strings.TrimSpace(str(args, "due"))
	if v == "" || v == "none" {
		return nil, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		t = t.Add(24*time.Hour - time.Second)
		return &t, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, fmt.Errorf("due %q: want YYYY-MM-DD", v)
	}
	return &t, nil
}

// operatorActor is the operator acting on the control socket: the named
// one, the only one, or an unnamed operator when several are configured.
func (s *Service) operatorActor(as string) (TicketActor, error) {
	op, err := s.ChatOperator(as)
	if err != nil {
		if as != "" {
			return TicketActor{}, err
		}
		op = ""
	}
	return TicketActor{Operator: op}, nil
}
