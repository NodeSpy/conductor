package bot

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// TICKETS (docs/wiki/Tickets.md). Every case is a ticket: a title and a
// description, a status, a priority, a due date, labels, an assignee (a
// team, a bot, or nobody yet — the triage queue), comments, links to the
// work it caused, and a history of who changed what.
//
//	draft → ready → in_progress → waiting → done | declined   (and reopen)
//
// Draft is the ideas space: operators and bots write and edit tickets there,
// and NOTHING STARTS on a draft — no worker, no routing, no delegation
// (dispatchable is the one gate). Marking a ticket ready, with an assignee,
// is what makes it the assignee's: a bot assignee picks it up at once
// (in_progress, in its inbox), a team's routing picks the bot. Only an
// operator marks a ticket ready, unless the bot's tickets.mark_own_ready
// lets it mark the ones it created itself. Events and workflows create
// tickets ready, as they always did.

// Ticket bounds.
const (
	MaxTicketTitle       = 200
	MaxTicketDescription = 16 << 10
	MaxTicketComment     = 16 << 10
	MaxTicketLabels      = 10
	ticketHistoryCap     = 200
	ticketCommentCap     = 500
)

// ticketMoving marks a draft claimed for a cross-home reassignment: it is set
// on the source under its lock before the copy is placed, so a concurrent
// assign of the same draft sees it as already moved and backs off. In the happy
// path it does not persist — the claimed stub is dropped once the copy is placed
// and the claim released if the place fails. A crash between the persisted claim
// and the place (or a post-place source-removal failure) can leave it behind;
// reconcileTicketClaims sweeps those on startup.
const ticketMoving = "(reassigning)"

var labelRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,39}$`)

// dispatchable is THE DISPATCH GATE: work — a worker, a delegation, a
// routed delivery — starts only on a ready (or already active) ticket.
func dispatchable(c *store.Case) error {
	switch {
	case c.Status == store.TicketDraft:
		return fmt.Errorf("ticket %s is a draft: nothing starts on it until it is marked ready", ticketRef(c))
	case !store.Active(c.Status):
		return fmt.Errorf("ticket %s is %s", ticketRef(c), c.Status)
	}
	return nil
}

func ticketRef(c *store.Case) string {
	if c.Ticket != "" {
		return c.Ticket
	}
	return c.ID
}

// TicketActor is who acts on a ticket: an operator, or a bot's session.
type TicketActor struct {
	Operator string
	Bot      Actor
}

func (a TicketActor) String() string {
	if a.Bot.Bot != "" {
		return "bot:" + a.Bot.Bot
	}
	if a.Operator == "" {
		return "operator"
	}
	return "operator:" + a.Operator
}

func (a TicketActor) isBot() bool { return a.Bot.Bot != "" }

// ticketLoc is where a ticket lives: a bot's ledger, or a queue.
type ticketLoc struct {
	Bot, Queue, ID string
}

func (l ticketLoc) String() string {
	if l.Bot != "" {
		return "bot:" + l.Bot + "/" + l.ID
	}
	return "queue:" + l.Queue + "/" + l.ID
}

// TicketView is a ticket as the operator surfaces show it: where it is, and
// what it caused.
type TicketView struct {
	store.Case
	Holder string         `json:"holder,omitempty"` // the bot that holds it ("" in a queue)
	Queue  string         `json:"queue,omitempty"`
	Links  map[string]any `json:"links,omitempty"`
	// Spent is what has gone against its spend limit (set when it has one).
	Spent *SpendStatus `json:"spent,omitempty"`
	// SpendHeld says why the bot holding it may not spend under its limit
	// (SpendHeld), when it may not.
	SpendHeld string `json:"spend_held,omitempty"`
}

// reconcileTicketClaims recovers cross-home reassignments interrupted by a
// crash. The claim (MovedTo == ticketMoving) is persisted to the source before
// the destination copy is placed and the source removed, so an interruption can
// leave the sentinel behind: the source is invisible to findTicket, and the
// ticket is lost if no destination copy landed. On startup, for each stub: if a
// live copy exists elsewhere the move completed — drop the leftover stub; if
// none exists the move never landed — clear the claim so the source is live
// again. Run once from Start, before any dispatch.
func (s *Service) reconcileTicketClaims() {
	type stub struct {
		loc    ticketLoc
		ticket string
	}
	var stubs []stub
	for _, b := range s.ticketBots() {
		for id, c := range s.Ledger.Bot(b).Cases {
			if c.MovedTo == ticketMoving {
				stubs = append(stubs, stub{ticketLoc{Bot: b, ID: id}, c.Ticket})
			}
		}
	}
	if q := s.queues(); q != nil {
		for _, name := range q.QueueNames() {
			for id, c := range q.Queue(name) {
				if c.MovedTo == ticketMoving {
					stubs = append(stubs, stub{ticketLoc{Queue: name, ID: id}, c.Ticket})
				}
			}
		}
	}
	for _, st := range stubs {
		if _, _, err := s.findTicket(st.ticket); err == nil {
			// A live destination copy exists: the move completed, drop the stub.
			if st.loc.Queue != "" {
				_ = s.removeFromQueue(st.loc)
			} else {
				_ = s.Ledger.UpdateBot(st.loc.Bot, func(r *store.BotRecord) error {
					delete(r.Cases, st.loc.ID)
					return nil
				})
			}
			continue
		}
		// No destination copy landed: recover the source.
		_ = s.updateTicket(st.loc, func(cc *store.Case) error {
			if cc.MovedTo == ticketMoving {
				cc.MovedTo = ""
			}
			return nil
		})
	}
}

// findTicket finds the live copy of a ticket (never a moved-away stub).
func (s *Service) findTicket(tid string) (store.Case, ticketLoc, error) {
	for _, b := range s.ticketBots() {
		for id, c := range s.Ledger.Bot(b).Cases {
			if c.Ticket == tid && c.MovedTo == "" {
				return *c, ticketLoc{Bot: b, ID: id}, nil
			}
		}
	}
	if q := s.queues(); q != nil {
		for _, name := range q.QueueNames() {
			for id, c := range q.Queue(name) {
				if c.Ticket == tid && c.MovedTo == "" {
					return *c, ticketLoc{Queue: name, ID: id}, nil
				}
			}
		}
	}
	return store.Case{}, ticketLoc{}, fmt.Errorf("no ticket %s", tid)
}

// ticketBots are the bots whose ledgers hold tickets: the configured ones.
func (s *Service) ticketBots() []string { return s.Cfg().BotNames() }

// queueLedger is the store's queue side.
type queueLedger interface {
	Queue(name string) map[string]*store.Case
	QueueNames() []string
	UpdateQueue(name string, fn func(q map[string]*store.Case) error) error
	NextTicket() (string, error)
}

func (s *Service) queues() queueLedger {
	q, _ := s.Ledger.(queueLedger)
	return q
}

// updateTicket edits a ticket where it lives.
func (s *Service) updateTicket(loc ticketLoc, fn func(c *store.Case) error) error {
	if loc.Bot != "" {
		return s.Ledger.UpdateBot(loc.Bot, func(r *store.BotRecord) error {
			c := r.Cases[loc.ID]
			if c == nil {
				return fmt.Errorf("ticket moved")
			}
			return fn(c)
		})
	}
	return s.queues().UpdateQueue(loc.Queue, func(q map[string]*store.Case) error {
		c := q[loc.ID]
		if c == nil {
			return fmt.Errorf("ticket moved")
		}
		return fn(c)
	})
}

func addHistory(c *store.Case, at time.Time, by, what string) {
	c.History = append(c.History, store.TicketChange{At: at, By: by, What: what})
	if n := len(c.History); n > ticketHistoryCap {
		c.History = c.History[n-ticketHistoryCap:]
	}
	c.Updated = at
}

// parseAssignee checks an assignee: "bot:<name>", "team:<name>", a bare
// bot or team name, or "" (the triage queue).
func (s *Service) parseAssignee(a string) (string, error) {
	cfg := s.Cfg()
	a = strings.TrimSpace(a)
	if a == "" || a == "none" {
		return "", nil
	}
	kind, name, ok := strings.Cut(a, ":")
	if !ok {
		switch {
		case cfg.Bots[a].Description != "" || hasBot(cfg, a):
			return "bot:" + a, nil
		case cfg.Teams[a].Routing != nil || hasTeam(cfg, a):
			return "team:" + a, nil
		}
		return "", fmt.Errorf("assignee %q is not a bot or a team", a)
	}
	switch kind {
	case "bot":
		if !hasBot(cfg, name) {
			return "", fmt.Errorf("assignee %q: unknown bot", a)
		}
	case "team":
		if !hasTeam(cfg, name) {
			return "", fmt.Errorf("assignee %q: unknown team", a)
		}
	default:
		return "", fmt.Errorf("assignee %q: want bot:<name> or team:<name>", a)
	}
	return kind + ":" + name, nil
}

func hasBot(cfg *config.Config, n string) bool {
	m, ok := cfg.Bots[n]
	return ok && !m.Disabled
}

func hasTeam(cfg *config.Config, n string) bool { _, ok := cfg.Teams[n]; return ok }

// mayAssign is ASSIGNMENT AUTHORITY: an operator assigns anywhere; a bot
// only within its delegation rules — itself, a bot it may delegate to (its
// teammates, unless the team says peers: none, or an operator's link), or
// its own team — and may leave a ticket unassigned (the operators' triage).
func (s *Service) mayAssign(a TicketActor, assignee string) error {
	if !a.isBot() || assignee == "" {
		return nil
	}
	cfg := s.Cfg()
	me := a.Bot.Bot
	kind, name, _ := strings.Cut(assignee, ":")
	switch {
	case kind == "bot" && name == me:
		return nil
	case kind == "bot" && cfg.BotLinked(me, name):
		return nil
	case kind == "team" && string(cfg.Bots[me].Team) == name:
		return nil
	}
	return fmt.Errorf("bot %s may not assign work to %s: only itself, a bot it may delegate to, or its own team", me, assignee)
}

// TicketNew is a new ticket.
type TicketNew struct {
	Title, Description, Assignee, Priority string
	Due                                    *time.Time
	Labels                                 []string
	Ready                                  bool
	Files                                  []AttachmentIn
	SpendLimit                             *store.SpendLimit
}

func checkTicketText(title, desc string) error {
	if strings.TrimSpace(title) == "" {
		return errors.New("a ticket needs a title")
	}
	if utf8.RuneCountInString(title) > MaxTicketTitle || strings.ContainsAny(title, "\n\r") {
		return fmt.Errorf("title: one line, at most %d characters", MaxTicketTitle)
	}
	if len(desc) > MaxTicketDescription {
		return fmt.Errorf("description: %d bytes, over the cap of %d", len(desc), MaxTicketDescription)
	}
	return nil
}

func checkLabels(ls []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, l := range ls {
		l = strings.ToLower(strings.TrimSpace(l))
		if l == "" || seen[l] {
			continue
		}
		if !labelRE.MatchString(l) {
			return nil, fmt.Errorf("label %q: lowercase letters, digits, . _ -, at most 40", l)
		}
		seen[l] = true
		out = append(out, l)
	}
	if len(out) > MaxTicketLabels {
		return nil, fmt.Errorf("%d labels, at most %d", len(out), MaxTicketLabels)
	}
	sort.Strings(out)
	return out, nil
}

func checkPriority(p string) error {
	for _, x := range store.TicketPriorities {
		if x == p {
			return nil
		}
	}
	return fmt.Errorf("priority %q is not one of %s", p, strings.Join(store.TicketPriorities, ", "))
}

// requesterFor is whose request a new ticket is: an operator's (classed
// against the assignee bot, as any request is), or a bot's — at the trust
// of what its session is handling now, never more.
func (s *Service) requesterFor(a TicketActor, assignee string) (string, string) {
	cfg := s.Cfg()
	if a.isBot() {
		rec := s.Ledger.Bot(a.Bot.Bot)
		class, who, _ := homeAttribution(&rec)
		class, who = lessTrusted(class, who, ReqBot, "bot:"+a.Bot.Bot)
		return who, class
	}
	if a.Operator == "" {
		return "", ReqOperator
	}
	if kind, name, _ := strings.Cut(assignee, ":"); kind == "bot" {
		return RequesterClass(cfg, name, a.Operator)
	} else if kind == "team" {
		return classifyAgainst(cfg, cfg.TeamOperators(name), a.Operator)
	}
	return a.Operator, ReqOperator
}

// TicketCreate writes a new ticket: a draft, unless an operator (or a bot
// whose mark_own_ready allows it) asks for it ready — then it is dispatched
// to its assignee at once.
func (s *Service) TicketCreate(ctx context.Context, a TicketActor, n TicketNew) (TicketView, error) {
	if a.isBot() && a.Bot.Role != config.BotRoleHome {
		return TicketView{}, errors.New("only a bot's home session writes tickets (a worker reports to its parent)")
	}
	if err := checkTicketText(n.Title, n.Description); err != nil {
		return TicketView{}, err
	}
	assignee, err := s.parseAssignee(n.Assignee)
	if err != nil {
		return TicketView{}, err
	}
	if err := s.mayAssign(a, assignee); err != nil {
		return TicketView{}, err
	}
	labels, err := checkLabels(n.Labels)
	if err != nil {
		return TicketView{}, err
	}
	cfg := s.Cfg()
	if n.Priority == "" {
		n.Priority = cfg.TicketDefaultPriority(assignee)
	}
	if err := checkPriority(n.Priority); err != nil {
		return TicketView{}, err
	}
	limit, err := NormalizeSpendLimit(n.SpendLimit)
	if err != nil {
		return TicketView{}, err
	}
	if _, err := checkSpendChange(a, nil, limit); err != nil {
		return TicketView{}, err
	}
	if n.Ready {
		if err := s.mayMarkReady(a, a.String(), assignee); err != nil {
			return TicketView{}, err
		}
	}
	kind, holder, _ := strings.Cut(assignee, ":")
	var types []string
	if len(n.Files) > 0 {
		if kind != "bot" {
			return TicketView{}, errors.New("files go on a ticket a bot holds: assign it to a bot, or attach them in a comment once one has it")
		}
		if types, err = checkAttachments(cfg.Bots[holder].Attachments, n.Files); err != nil {
			return TicketView{}, err
		}
	}
	q := s.queues()
	if q == nil {
		return TicketView{}, errors.New("no ticket store on this daemon")
	}
	tid, err := q.NextTicket()
	if err != nil {
		return TicketView{}, err
	}
	now := s.now()
	who, class := s.requesterFor(a, assignee)
	c := store.Case{Ticket: tid, Title: strings.TrimSpace(n.Title), Description: n.Description, Status: store.TicketDraft,
		Priority: n.Priority, Due: n.Due, Labels: labels, Assignee: assignee, CreatedBy: a.String(),
		Source: "ticket", Requester: who, RequesterClass: class, Created: now, Updated: now, SpendLimit: limit}
	addHistory(&c, now, a.String(), "created ("+orNone(assignee)+", draft)")
	if limit != nil {
		// Only an operator sets a limit (checkSpendChange), and it is
		// confirmed for the bot the ticket is assigned to.
		c.SpendFor = spendHolder(assignee)
		addHistory(&c, now, a.String(), "spend limit "+spendText(limit)+", "+spendForText(c.SpendFor))
	}
	loc, err := s.placeTicket(c, assignee)
	if err != nil {
		return TicketView{}, err
	}
	if len(n.Files) > 0 {
		mid, err := s.reserveMessage(holder)
		if err != nil {
			return TicketView{}, err
		}
		atts, err := s.storeAttachments(holder, mid, loc.ID, n.Files, types, attachOrigin{Source: "ticket", Requester: who, Class: class})
		if err != nil {
			return TicketView{}, err
		}
		_ = s.updateTicket(loc, func(c *store.Case) error {
			c.Comments = append(c.Comments, store.TicketComment{At: now, By: a.String(), Text: "(files with the ticket)", Attachments: attachmentIDs(atts)})
			return nil
		})
	}
	ev := map[string]any{"event": "bot_ticket", "action": "create", "ticket": tid, "by": a.String(), "assignee": assignee,
		"requester": who, "requester_class": class}
	if limit != nil {
		ev["spend_limit"], ev["spend_for"] = spendText(limit), c.SpendFor
	}
	s.auditCtx(ctx, ev)
	if n.Ready {
		return s.TicketSetStatus(ctx, a, tid, store.TicketReady, "")
	}
	s.publishTicket(tid)
	return s.TicketShow(tid)
}

// placeTicket stores a ticket where its assignee keeps it: a bot's ledger,
// the team's queue, or the triage queue.
func (s *Service) placeTicket(c store.Case, assignee string) (ticketLoc, error) {
	kind, name, _ := strings.Cut(assignee, ":")
	if kind == "bot" {
		var id string
		err := s.Ledger.UpdateBot(name, func(r *store.BotRecord) error {
			id = r.NextID("c")
			c.ID = id
			if r.Cases == nil {
				r.Cases = map[string]*store.Case{}
			}
			cp := c
			r.Cases[id] = &cp
			return nil
		})
		return ticketLoc{Bot: name, ID: id}, err
	}
	queue := store.TriageQueue
	if kind == "team" {
		queue = assignee
	}
	id := c.Ticket
	err := s.queues().UpdateQueue(queue, func(q map[string]*store.Case) error {
		c.ID = id
		cp := c
		q[id] = &cp
		return nil
	})
	return ticketLoc{Queue: queue, ID: id}, err
}

// removeFromQueue drops a ticket that left a queue (a queue keeps no stubs:
// the ticket's history says where it went).
func (s *Service) removeFromQueue(loc ticketLoc) error {
	if loc.Queue == "" {
		return nil
	}
	return s.queues().UpdateQueue(loc.Queue, func(q map[string]*store.Case) error {
		delete(q, loc.ID)
		return nil
	})
}

// mayMarkReady is WHO MAY MARK READY: an operator; a bot only for a ticket
// it created itself, and only with tickets.mark_own_ready. Either way the
// ticket needs an assignee.
func (s *Service) mayMarkReady(a TicketActor, createdBy, assignee string) error {
	if assignee == "" {
		return errors.New("a ticket needs an assignee before it is ready: who would pick it up?")
	}
	if !a.isBot() {
		return nil
	}
	if createdBy != a.String() {
		return fmt.Errorf("only an operator marks this ticket ready (bot %s did not write it)", a.Bot.Bot)
	}
	if !s.Cfg().BotMarksOwnReady(a.Bot.Bot) {
		return fmt.Errorf("only an operator marks a ticket ready (bot %s's tickets.mark_own_ready is off)", a.Bot.Bot)
	}
	return nil
}

// may checks a bot's authority over a ticket it is not holding or did not
// write: none. An operator may act on any ticket.
func (s *Service) mayTouch(a TicketActor, c store.Case, loc ticketLoc) error {
	if !a.isBot() {
		return nil
	}
	if a.Bot.Role != config.BotRoleHome {
		return errors.New("only a bot's home session acts on tickets (a worker reports to its parent)")
	}
	if loc.Bot == a.Bot.Bot || c.CreatedBy == a.String() {
		return nil
	}
	return fmt.Errorf("ticket %s is not bot %s's: it neither holds it nor wrote it", c.Ticket, a.Bot.Bot)
}

// The status machine: which transitions exist.
var ticketTransitions = map[string][]string{
	store.TicketDraft:      {store.TicketReady, store.TicketDeclined},
	store.TicketReady:      {store.TicketDraft, store.TicketInProgress, store.TicketDone, store.TicketDeclined},
	store.TicketInProgress: {store.TicketWaiting, store.TicketDone, store.TicketDeclined},
	store.TicketWaiting:    {store.TicketInProgress, store.TicketDone, store.TicketDeclined},
	store.TicketDone:       {"reopen"},
	store.TicketDeclined:   {"reopen"},
}

// TicketCanMove reports whether the machine has the transition from → to.
func TicketCanMove(from, to string) bool {
	for _, x := range ticketTransitions[from] {
		if x == to {
			return true
		}
	}
	return false
}

// TicketSetStatus moves a ticket through the machine. "reopen" brings a
// done or declined ticket back: an operator's to ready (dispatched again)
// when it has an assignee, otherwise — and always for a bot — to draft.
func (s *Service) TicketSetStatus(ctx context.Context, a TicketActor, tid, to, note string) (TicketView, error) {
	c, loc, err := s.findTicket(tid)
	if err != nil {
		return TicketView{}, err
	}
	if err := s.mayTouch(a, c, loc); err != nil {
		return TicketView{}, err
	}
	if !TicketCanMove(c.Status, to) {
		return TicketView{}, fmt.Errorf("ticket %s is %s: it can't go to %s (from %s: %s)", tid, c.Status, to, c.Status, strings.Join(ticketTransitions[c.Status], ", "))
	}
	target := to
	switch to {
	case "reopen":
		target = store.TicketDraft
		if !a.isBot() && c.Assignee != "" {
			target = store.TicketReady
		}
	case store.TicketReady:
		if err := s.mayMarkReady(a, c.CreatedBy, c.Assignee); err != nil {
			return TicketView{}, err
		}
	case store.TicketInProgress:
		// Picking a ticket up is the dispatch, not an edit (and waiting →
		// in progress is the holder's).
		if c.Status == store.TicketReady {
			return TicketView{}, errors.New("a ready ticket is picked up by its assignee, not set in progress by hand")
		}
	}
	if a.isBot() && (target == store.TicketDraft && c.Status == store.TicketReady) && c.CreatedBy != a.String() {
		return TicketView{}, errors.New("only an operator, or the bot that wrote it, takes a ticket back to draft")
	}
	if store.Closed(target) && loc.Bot != "" {
		// A held ticket closes the way a case always has: its episode in
		// memory, and a delegated one's outcome back to its delegator.
		cs, err := s.closeCase(ctx, loc.Bot, loc.ID, strings.TrimSpace(note), target, a.String())
		if cs.ID != "" {
			st := DelegationDone
			if target == store.TicketDeclined {
				st = DelegationDeclined
			}
			s.reportDelegation(loc.Bot, cs, st, strings.TrimSpace(note))
			s.auditCtx(ctx, map[string]any{"event": "bot_ticket", "action": "status", "ticket": tid, "by": a.String(), "from": c.Status, "to": target})
			s.publishTicket(tid)
		}
		if err != nil && cs.ID == "" {
			return TicketView{}, err
		}
		return s.TicketShow(tid)
	}
	now := s.now()
	spendFor, rebound := "", false
	err = s.updateTicket(loc, func(cc *store.Case) error {
		// Re-validate the transition against the LIVE status, not the pre-lock
		// snapshot: two concurrent moves both pass the earlier check, but the
		// second sees the first's committed status here and is refused — no
		// terminal-state overwrite (approve.go does the same inside its
		// transaction).
		if !TicketCanMove(cc.Status, to) {
			return fmt.Errorf("ticket %s is %s: it can't go to %s (from %s: %s)", tid, cc.Status, to, cc.Status, strings.Join(ticketTransitions[cc.Status], ", "))
		}
		what := cc.Status + " → " + target
		if note = strings.TrimSpace(note); note != "" {
			what += ": " + oneLine(note, 300)
		}
		cc.Status = target
		if target == store.TicketReady {
			cc.ReadyBy = a.String()
		}
		if store.Closed(target) && note != "" {
			cc.Outcome = oneLine(note, 500)
		}
		addHistory(cc, now, a.String(), what)
		if cc.SpendLimit != nil {
			// Closing ends a limit's confirmation; an operator's reopen
			// confirms it again, for the bot assigned. A bot's reopen leaves
			// it held.
			switch {
			case store.Closed(target):
				cc.SpendFor = ""
			case to == "reopen" && !a.isBot():
				cc.SpendFor = spendHolder(cc.Assignee)
				spendFor, rebound = cc.SpendFor, true
				addHistory(cc, now, a.String(), "spend limit "+spendText(cc.SpendLimit)+" "+spendForText(cc.SpendFor))
			}
		}
		return nil
	})
	if err != nil {
		return TicketView{}, err
	}
	ev := map[string]any{"event": "bot_ticket", "action": "status", "ticket": tid, "by": a.String(), "from": c.Status, "to": target}
	if rebound {
		ev["spend_limit"], ev["spend_for"] = spendText(c.SpendLimit), spendFor
	}
	s.auditCtx(ctx, ev)
	if target == store.TicketReady {
		if err := s.dispatchTicket(ctx, tid); err != nil {
			return TicketView{}, err
		}
	}
	s.publishTicket(tid)
	return s.TicketShow(tid)
}

// dispatchTicket hands a ready ticket to its assignee: a bot picks it up at
// once (in progress, delivered to its inbox); a team's routing (rules, the
// lead's triage, the fallback) picks the bot. Called only on the transition
// to ready.
func (s *Service) dispatchTicket(ctx context.Context, tid string) error {
	c, loc, err := s.findTicket(tid)
	if err != nil {
		return err
	}
	if c.Status != store.TicketReady {
		return fmt.Errorf("ticket %s is %s, not ready", tid, c.Status)
	}
	kind, name, _ := strings.Cut(c.Assignee, ":")
	switch kind {
	case "bot":
		if loc.Bot == name {
			// Already in its ledger: picked up where it is.
			now := s.now()
			err := s.updateTicket(loc, func(cc *store.Case) error {
				cc.Status = store.TicketInProgress
				addHistory(cc, now, "bot:"+name, "ready → in_progress: picked up")
				return nil
			})
			if err != nil {
				return err
			}
			cc := c
			cc.Status = store.TicketInProgress
			s.enqueue(name, loc.ID, TicketMessage(cc, "ready for you"))
			return nil
		}
		_, err := s.deliverCase(ctx, Event{Bot: name, Source: "ticket", Title: c.Title, Text: c.Description, ticket: &c}, c.Requester, c.RequesterClass, nil, "")
		if err != nil {
			return err
		}
	case "team":
		_, err := s.RouteTeam(ctx, TeamEvent{Team: name, Source: "ticket", Requester: c.Requester, Title: c.Title, Text: c.Description,
			Fields: map[string]any{"title": c.Title, "text": c.Description, "priority": c.Priority, "labels": strings.Join(c.Labels, ",")}, ticket: &c})
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("ticket %s has no assignee", tid)
	}
	if loc.Queue != "" {
		return s.removeFromQueue(loc)
	}
	return s.markMoved(loc, tid)
}

// markMoved closes the copy a ticket left behind in a bot's ledger.
func (s *Service) markMoved(loc ticketLoc, tid string) error {
	if loc.Bot == "" {
		return nil
	}
	_, to, err := s.findTicketExcept(tid, loc)
	if err != nil {
		return nil
	}
	return s.Ledger.UpdateBot(loc.Bot, func(r *store.BotRecord) error {
		if c := r.Cases[loc.ID]; c != nil {
			c.MovedTo, c.Status, c.Updated = to.String(), store.TicketDone, s.now()
		}
		return nil
	})
}

// findTicketExcept finds a ticket's live copy other than at loc.
func (s *Service) findTicketExcept(tid string, not ticketLoc) (store.Case, ticketLoc, error) {
	for _, b := range s.ticketBots() {
		for id, c := range s.Ledger.Bot(b).Cases {
			if c.Ticket == tid && c.MovedTo == "" && !(b == not.Bot && id == not.ID) {
				return *c, ticketLoc{Bot: b, ID: id}, nil
			}
		}
	}
	return store.Case{}, ticketLoc{}, fmt.Errorf("no other copy of %s", tid)
}

// carryTicket gives a case delivered for a ticket the ticket's own fields:
// it is the same ticket, now in this bot's ledger.
func carryTicket(dst *store.Case, src *store.Case, bot string, at time.Time) {
	dst.Ticket, dst.Description, dst.Priority, dst.Due = src.Ticket, src.Description, src.Priority, src.Due
	dst.Labels, dst.CreatedBy, dst.ReadyBy = src.Labels, src.CreatedBy, src.ReadyBy
	dst.Comments, dst.History, dst.SpendLimit, dst.SpendFor = src.Comments, src.History, src.SpendLimit, src.SpendFor
	// A team's ticket stays the team's, whichever member works it; a bot's
	// is the bot's that now holds it.
	dst.Assignee = src.Assignee
	if !strings.HasPrefix(src.Assignee, "team:") {
		dst.Assignee = "bot:" + bot
	}
	what := src.Status + " → in_progress: picked up by bot:" + bot
	if src.Status == store.TicketInProgress {
		what = "moved to bot:" + bot
	}
	addHistory(dst, at, "bot:"+bot, what)
	if src.SpendLimit != nil && src.SpendFor != bot {
		// A limit reaches a bot it was not confirmed for (a team's routing,
		// a handoff): it allows nothing until an operator confirms it.
		addHistory(dst, at, "conductor", "spend limit held: "+SpendHeld(bot, dst))
	}
}

// TicketMessage is a ticket as the bot that picks it up sees it.
func TicketMessage(c store.Case, why string) string {
	meta := fmt.Sprintf("ticket %s | priority %s | requester %s (%s)", c.Ticket, c.Priority, orDash(c.Requester), c.RequesterClass)
	if c.Due != nil {
		meta += " | due " + c.Due.UTC().Format("2006-01-02")
	}
	if len(c.Labels) > 0 {
		meta += " | labels " + strings.Join(c.Labels, ", ")
	}
	if c.SpendLimit != nil {
		meta += " | spend limit " + spendText(c.SpendLimit)
	}
	return fmt.Sprintf("[conductor: %s — %s, case %s]\n%s\nWork it, then:\n  conductor bot ticket comment %s \"…\"     # progress, a question (your operator reads it)\n  conductor bot ticket wait %s \"…\"        # waiting on your operator\n  conductor bot ticket done %s \"…\"        # finished (or: decline %s \"why\")",
		meta, why, c.ID, fence("TICKET", c.Title+"\n\n"+c.Description), c.Ticket, c.Ticket, c.Ticket, c.Ticket)
}

// TicketEdit changes a ticket's title, description, priority, due date or
// labels. A bot edits only tickets it holds or wrote; every change goes in
// the history, with who made it.
type TicketChanges struct {
	Title, Description, Priority *string
	Due                          **time.Time
	Labels                       *[]string
	// SpendLimit sets (or, pointing at nil, clears) the spend limit.
	SpendLimit **store.SpendLimit
}

func (s *Service) TicketEdit(ctx context.Context, a TicketActor, tid string, ch TicketChanges) (TicketView, error) {
	c, loc, err := s.findTicket(tid)
	if err != nil {
		return TicketView{}, err
	}
	if err := s.mayTouch(a, c, loc); err != nil {
		return TicketView{}, err
	}
	title, desc := c.Title, c.Description
	if ch.Title != nil {
		title = strings.TrimSpace(*ch.Title)
	}
	if ch.Description != nil {
		desc = *ch.Description
	}
	if err := checkTicketText(title, desc); err != nil {
		return TicketView{}, err
	}
	if ch.Priority != nil {
		if err := checkPriority(*ch.Priority); err != nil {
			return TicketView{}, err
		}
	}
	var labels []string
	if ch.Labels != nil {
		if labels, err = checkLabels(*ch.Labels); err != nil {
			return TicketView{}, err
		}
	}
	var limit *store.SpendLimit
	widening := ""
	if ch.SpendLimit != nil {
		if limit, err = NormalizeSpendLimit(*ch.SpendLimit); err != nil {
			return TicketView{}, err
		}
		if widening, err = checkSpendChange(a, c.SpendLimit, limit); err != nil {
			return TicketView{}, err
		}
	}
	now := s.now()
	spendFor, bound := "", false
	err = s.updateTicket(loc, func(cc *store.Case) error {
		var what []string
		// An operator who sets a limit confirms it for the bot holding the
		// ticket now (none in a queue): restating the same limit is how a
		// limit held after a move is confirmed. A bot's narrowing keeps it.
		forBot := cc.SpendFor
		if ch.SpendLimit != nil && !a.isBot() {
			forBot = ""
			if limit != nil {
				forBot = loc.Bot
			}
		}
		if ch.SpendLimit != nil && (!sameSpendLimit(cc.SpendLimit, limit) || forBot != cc.SpendFor) {
			w := "spend limit " + spendText(limit)
			if widening != "" {
				w += " (a WIDENING: " + widening + ")"
			}
			if limit != nil {
				w += ", " + spendForText(forBot)
			}
			cc.SpendLimit, cc.SpendFor = limit, forBot
			spendFor, bound = forBot, true
			what = append(what, w)
		}
		if ch.Title != nil && title != cc.Title {
			cc.Title = title
			what = append(what, "title")
		}
		if ch.Description != nil && desc != cc.Description {
			cc.Description = desc
			what = append(what, "description")
		}
		if ch.Priority != nil && *ch.Priority != cc.Priority {
			what = append(what, "priority "+cc.Priority+" → "+*ch.Priority)
			cc.Priority = *ch.Priority
		}
		if ch.Due != nil {
			cc.Due = *ch.Due
			what = append(what, "due "+dueText(cc.Due))
		}
		if ch.Labels != nil {
			cc.Labels = labels
			what = append(what, "labels ["+strings.Join(labels, ", ")+"]")
		}
		if len(what) == 0 {
			return errors.New("nothing to change")
		}
		addHistory(cc, now, a.String(), "edited: "+strings.Join(what, "; "))
		return nil
	})
	if err != nil {
		return TicketView{}, err
	}
	ev := map[string]any{"event": "bot_ticket", "action": "edit", "ticket": tid, "by": a.String(), "widening": widening}
	if bound {
		ev["spend_limit"], ev["spend_for"] = spendText(limit), spendFor
	}
	s.auditCtx(ctx, ev)
	s.publishTicket(tid)
	return s.TicketShow(tid)
}

func dueText(d *time.Time) string {
	if d == nil {
		return "none"
	}
	return d.UTC().Format("2006-01-02")
}

func orNone(s string) string {
	if s == "" {
		return "unassigned"
	}
	return s
}

// TicketAssign (re)assigns a ticket, within ASSIGNMENT AUTHORITY. A draft
// just moves (nothing starts). An active ticket goes to its new assignee
// through the ordinary delivery and routing — the requester kept, the trust
// never raised (an operator's request is a third party's to a bot that
// operator does not run, and a bot's reassignment carries its own trust at
// most) — and the copy it left is closed, pointing where it went.
func (s *Service) TicketAssign(ctx context.Context, a TicketActor, tid, to string) (TicketView, error) {
	c, loc, err := s.findTicket(tid)
	if err != nil {
		return TicketView{}, err
	}
	if err := s.mayTouch(a, c, loc); err != nil {
		return TicketView{}, err
	}
	assignee, err := s.parseAssignee(to)
	if err != nil {
		return TicketView{}, err
	}
	if err := s.mayAssign(a, assignee); err != nil {
		return TicketView{}, err
	}
	if store.Closed(c.Status) {
		return TicketView{}, fmt.Errorf("ticket %s is %s: reopen it first", tid, c.Status)
	}
	if store.Active(c.Status) && assignee == "" {
		return TicketView{}, errors.New("an active ticket can't go back to the triage queue: assign it to a bot or a team")
	}
	now := s.now()
	who, class := c.Requester, c.RequesterClass
	if a.isBot() {
		rec := s.Ledger.Bot(a.Bot.Bot)
		pc, pw, _ := homeAttribution(&rec)
		class, who = lessTrusted(class, who, pc, pw)
	}
	// A spend limit moves with the ticket. An operator's assignment confirms
	// it for the bot assigned (none for a team or the queue: the bot that
	// picks it up is confirmed afterwards); a bot's leaves it confirmed for
	// whom it was, so it is held wherever it lands until an operator
	// confirms it there.
	spendFor := c.SpendFor
	if !a.isBot() {
		spendFor = spendHolder(assignee)
	}
	spendNote := func(cc *store.Case) {
		if cc.SpendLimit == nil {
			return
		}
		note := spendForText(spendFor)
		if h := spendHolder(assignee); h != "" && h != spendFor {
			note = "held until an operator confirms it for bot:" + h
		}
		cc.SpendFor = spendFor
		addHistory(cc, now, a.String(), "spend limit "+spendText(cc.SpendLimit)+" moves with it, "+note)
	}
	// assignOnto applies the reassignment to the live record under the lock,
	// mutating only the fields assignment owns — so a comment or edit a
	// concurrent op committed between the read above and this write is not
	// lost (the old code overwrote the whole record with a pre-lock snapshot).
	assignOnto := func(cc *store.Case, toStatus string) error {
		if store.Closed(cc.Status) {
			return fmt.Errorf("ticket %s is %s: reopen it first", tid, cc.Status)
		}
		if store.Active(cc.Status) && assignee == "" {
			return errors.New("an active ticket can't go back to the triage queue: assign it to a bot or a team")
		}
		prev := cc.Assignee
		cc.Assignee, cc.Requester, cc.RequesterClass = assignee, who, class
		if toStatus != "" {
			cc.Status = toStatus
		}
		addHistory(cc, now, a.String(), "assigned "+orNone(prev)+" → "+orNone(assignee))
		spendNote(cc)
		return nil
	}
	if c.Status == store.TicketDraft {
		if sameHome(loc, assignee) {
			err = s.updateTicket(loc, func(cc *store.Case) error { return assignOnto(cc, "") })
		} else {
			moved := c
			moved.Assignee, moved.Requester, moved.RequesterClass = assignee, who, class
			moved.MovedTo = ""
			addHistory(&moved, now, a.String(), "assigned "+orNone(c.Assignee)+" → "+orNone(assignee))
			spendNote(&moved)
			// Claim the source draft before placing the copy: mark it moving
			// under its own lock, so a second assign of the same draft racing to
			// another home finds it already gone (findTicket skips a moved copy)
			// and backs off — one live copy ever exists, never two. Place the
			// copy, then drop the claimed stub; if placing fails, release the
			// claim so the draft is live again.
			claimed := false
			err = s.updateTicket(loc, func(cc *store.Case) error {
				if cc.Status != store.TicketDraft || cc.MovedTo != "" {
					return fmt.Errorf("ticket %s was reassigned concurrently", tid)
				}
				cc.MovedTo = ticketMoving
				claimed = true
				return nil
			})
			if err == nil {
				var dest ticketLoc
				if dest, err = s.placeTicket(moved, assignee); err == nil {
					// The destination copy is live. Remove the source stub; if
					// the removal fails, the move still succeeded, so finalize the
					// stub to a real moved-away marker rather than leave the
					// transient claim dangling forever (findTicket skips both, but
					// only the sentinel is swept on startup). A lingering source
					// draft (claim released to "") here would duplicate the live
					// destination, so we point it at the destination instead.
					if loc.Queue != "" {
						err = s.removeFromQueue(loc)
					} else {
						err = s.Ledger.UpdateBot(loc.Bot, func(r *store.BotRecord) error {
							delete(r.Cases, loc.ID)
							return nil
						})
					}
					if err != nil && claimed {
						_ = s.updateTicket(loc, func(cc *store.Case) error {
							if cc.MovedTo == ticketMoving {
								cc.MovedTo = dest.String()
							}
							return nil
						})
						// The move landed at the destination; the leftover source
						// stub is cosmetic (and swept on restart), so do not fail.
						err = nil
					}
				} else if claimed {
					_ = s.updateTicket(loc, func(cc *store.Case) error {
						if cc.MovedTo == ticketMoving {
							cc.MovedTo = ""
						}
						return nil
					})
				}
			}
		}
	} else {
		// Active: re-dispatch to the new assignee, from ready.
		err = s.updateTicket(loc, func(cc *store.Case) error { return assignOnto(cc, store.TicketReady) })
		if err == nil {
			err = s.dispatchTicket(ctx, tid)
		}
	}
	if err != nil {
		return TicketView{}, err
	}
	ev := map[string]any{"event": "bot_ticket", "action": "assign", "ticket": tid, "by": a.String(), "from": c.Assignee, "to": assignee,
		"requester": who, "requester_class": class}
	if c.SpendLimit != nil {
		// The audit says a limit moved, and whom it is confirmed for now
		// ("" held: no operator confirmed it for the bot that will hold it).
		ev["spend_limit"], ev["spend_limit_moved"], ev["spend_for"] = spendText(c.SpendLimit), true, spendFor
	}
	s.auditCtx(ctx, ev)
	s.publishTicket(tid)
	return s.TicketShow(tid)
}

// sameHome reports whether a ticket at loc already lives where assignee
// keeps its tickets.
func sameHome(loc ticketLoc, assignee string) bool {
	kind, name, _ := strings.Cut(assignee, ":")
	switch kind {
	case "bot":
		return loc.Bot == name
	case "team":
		return loc.Queue == assignee
	}
	return loc.Queue == store.TriageQueue
}

// TicketComment adds a comment. An operator's comment on an active ticket a
// bot holds reaches that bot (an instruction, like `bot say --case`); a
// comment on a draft is a note — nothing starts on a draft. A bot comments
// on tickets it holds or wrote.
func (s *Service) TicketComment(ctx context.Context, a TicketActor, tid, text string, files []AttachmentIn) (TicketView, error) {
	c, loc, err := s.findTicket(tid)
	if err != nil {
		return TicketView{}, err
	}
	if err := s.mayTouch(a, c, loc); err != nil {
		return TicketView{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" && len(files) == 0 {
		return TicketView{}, errors.New("an empty comment")
	}
	if len(text) > MaxTicketComment {
		return TicketView{}, fmt.Errorf("comment: %d bytes, over the cap of %d", len(text), MaxTicketComment)
	}
	cfg := s.Cfg()
	var attIDs []string
	if len(files) > 0 {
		if loc.Bot == "" {
			return TicketView{}, errors.New("files go on a ticket a bot holds")
		}
		types, err := checkAttachments(cfg.Bots[loc.Bot].Attachments, files)
		if err != nil {
			return TicketView{}, err
		}
		mid, err := s.reserveMessage(loc.Bot)
		if err != nil {
			return TicketView{}, err
		}
		who, class := c.Requester, c.RequesterClass
		if !a.isBot() {
			who, class = chatRequester(cfg, loc.Bot, a.Operator)
		}
		atts, err := s.storeAttachments(loc.Bot, mid, loc.ID, files, types, attachOrigin{Source: "ticket", Requester: who, Class: class})
		if err != nil {
			return TicketView{}, err
		}
		attIDs = attachmentIDs(atts)
	}
	now := s.now()
	err = s.updateTicket(loc, func(cc *store.Case) error {
		cc.Comments = append(cc.Comments, store.TicketComment{At: now, By: a.String(), Text: text, Attachments: attIDs})
		if n := len(cc.Comments); n > ticketCommentCap {
			cc.Comments = cc.Comments[n-ticketCommentCap:]
		}
		cc.Updated = now
		return nil
	})
	if err != nil {
		return TicketView{}, err
	}
	if !a.isBot() && loc.Bot != "" && store.Active(c.Status) {
		s.enqueueMsg(loc.Bot, store.InboxMsg{CaseID: loc.ID, Attachments: attIDs,
			Text: fmt.Sprintf("[conductor: your operator %s commented on ticket %s (case %s)]\n%s\n(Answer with: conductor bot ticket comment %s \"…\")", orDash(a.Operator), c.Ticket, loc.ID, text, c.Ticket)})
	}
	s.auditCtx(ctx, map[string]any{"event": "bot_ticket", "action": "comment", "ticket": tid, "by": a.String(), "bytes": len(text)})
	s.publishTicket(tid)
	return s.TicketShow(tid)
}

// TicketShow is one ticket with where it is and the work it caused.
func (s *Service) TicketShow(tid string) (TicketView, error) {
	c, loc, err := s.findTicket(tid)
	if err != nil {
		return TicketView{}, err
	}
	v := TicketView{Case: c, Holder: loc.Bot, Queue: loc.Queue}
	if c.SpendLimit != nil {
		st := s.ticketSpent(tid, c.SpendLimit)
		v.Spent = &st
		if loc.Bot != "" {
			v.SpendHeld = SpendHeld(loc.Bot, &c)
		} else if !store.Closed(c.Status) {
			v.SpendHeld = "the spend limit is held until an operator confirms it for the bot that picks the ticket up"
		}
	}
	if loc.Bot != "" {
		rec := s.Ledger.Bot(loc.Bot)
		links := map[string]any{}
		var workers, runs []string
		for _, wid := range c.Workers {
			if w := rec.Workers[wid]; w != nil {
				workers = append(workers, wid+" ("+w.Status+")")
				runs = append(runs, w.HistoryID)
			}
		}
		if len(workers) > 0 {
			links["workers"], links["runs"] = workers, runs
		}
		if len(c.Proposals) > 0 {
			links["approvals"] = c.Proposals
		}
		var dels []string
		for _, d := range rec.Delegations {
			if d.FromCase == loc.ID {
				dels = append(dels, d.ID+" → "+d.To+"/"+d.ToCase+" ("+d.Status+")")
			}
		}
		sort.Strings(dels)
		if len(dels) > 0 {
			links["delegations"] = dels
		}
		if c.Delegation != nil {
			links["delegated_from"] = c.Delegation.From + "/" + c.Delegation.FromCase
		}
		if c.Conversation != "" {
			links["conversation"] = c.Conversation
		}
		if len(links) > 0 {
			v.Links = links
		}
	}
	return v, nil
}

// TicketFilter selects tickets.
type TicketFilter struct {
	Assignee, Holder, Priority, Label, Query string
	Statuses                                 []string // none: every status but done and declined
}

// Tickets lists the tickets a filter selects, most recently updated first.
func (s *Service) Tickets(f TicketFilter) ([]TicketView, error) {
	var out []TicketView
	add := func(c *store.Case, loc ticketLoc) {
		if c.MovedTo != "" || c.Ticket == "" {
			return
		}
		if len(f.Statuses) > 0 {
			ok := false
			for _, st := range f.Statuses {
				ok = ok || st == c.Status
			}
			if !ok {
				return
			}
		} else if store.Closed(c.Status) {
			return
		}
		switch {
		case f.Assignee == "none" && c.Assignee != "",
			f.Assignee != "" && f.Assignee != "none" && c.Assignee != f.Assignee,
			f.Holder != "" && loc.Bot != f.Holder,
			f.Priority != "" && c.Priority != f.Priority:
			return
		}
		if f.Label != "" {
			ok := false
			for _, l := range c.Labels {
				ok = ok || l == f.Label
			}
			if !ok {
				return
			}
		}
		if q := strings.ToLower(f.Query); q != "" && !strings.Contains(strings.ToLower(c.Title+"\n"+c.Description), q) {
			return
		}
		out = append(out, TicketView{Case: *c, Holder: loc.Bot, Queue: loc.Queue})
	}
	for _, b := range s.ticketBots() {
		for id, c := range s.Ledger.Bot(b).Cases {
			add(c, ticketLoc{Bot: b, ID: id})
		}
	}
	if q := s.queues(); q != nil {
		for _, name := range q.QueueNames() {
			for id, c := range q.Queue(name) {
				add(c, ticketLoc{Queue: name, ID: id})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Updated.Equal(out[j].Updated) {
			return out[i].Updated.After(out[j].Updated)
		}
		return out[i].Ticket > out[j].Ticket
	})
	for _, st := range f.Statuses {
		if !validStatus(st) {
			return nil, fmt.Errorf("status %q is not one of %s", st, strings.Join(store.TicketStatuses, ", "))
		}
	}
	return out, nil
}

func validStatus(st string) bool {
	for _, x := range store.TicketStatuses {
		if x == st {
			return true
		}
	}
	return false
}

// ---- live updates ----

type ticketHub struct {
	mu   sync.Mutex
	next int
	subs map[int]chan TicketEvent
}

// TicketEvent is a live update: a ticket changed.
type TicketEvent struct {
	Ticket string `json:"ticket"`
}

// SubscribeTickets streams ticket changes until cancel.
func (s *Service) SubscribeTickets() (<-chan TicketEvent, func()) {
	h := &s.ticketHub
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs == nil {
		h.subs = map[int]chan TicketEvent{}
	}
	h.next++
	id, ch := h.next, make(chan TicketEvent, 64)
	h.subs[id] = ch
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
	}
}

func (s *Service) publishTicket(tid string) {
	h := &s.ticketHub
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- TicketEvent{Ticket: tid}:
		default:
		}
	}
}
