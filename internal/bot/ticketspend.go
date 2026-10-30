package bot

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/NodeSpy/conductor/internal/store"
)

// A TICKET'S SPEND LIMIT (docs/wiki/Tickets.md#spend-limit): "buy a hard
// drive, at most $120". It lives on the ticket (store.Case.SpendLimit), so
// the browser's payments read the same field: they key a purchase by the bot
// and its case id, and ask TicketSpend for the cap and what has gone.
//
// Only an operator sets, raises or clears a limit — raising (a higher
// amount, another currency or card, more merchants, or none at all) is a
// widening, recorded as one. A bot may only LOWER the limit on a ticket it
// holds or wrote: a smaller amount, fewer merchants, a card pinned.

// SpendLedger is the purchase ledger as tickets see it (the browser's
// payments implement it): the durable committed total for a ticket, by its
// stable id, in a currency — the same total the lifetime cap is enforced
// against. Keying by the ticket (not its per-bot case copies) means the view
// does not under-report once a reassigned ticket's old copy is pruned from the
// bot ledger. A daemon without one reports nothing spent, and says it can't
// tell.
type SpendLedger interface {
	SpentOnTicket(ticketID, currency string) (cents int64, err error)
	// CloseTicket reclaims a closed ticket's purchase records (its committing
	// spend counts toward no open cap once terminal), keyed by the ticket's
	// stable id. A no-op where no record matches.
	CloseTicket(ticketID string) error
}

// MaxSpendMerchants bounds a limit's merchant list.
const MaxSpendMerchants = 20

var (
	amountRE   = regexp.MustCompile(`^[0-9]{1,9}(\.[0-9]{1,2})?$`)
	currencyRE = regexp.MustCompile(`^[A-Z]{3}$`)
	cardRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// parseCents reads a decimal amount ("120", "50.5", "50.00") as cents.
func parseCents(a string) (int64, error) {
	a = strings.TrimSpace(a)
	if !amountRE.MatchString(a) {
		return 0, fmt.Errorf("amount %q: a positive number with at most two decimals (e.g. 120 or 49.99)", a)
	}
	whole, frac, _ := strings.Cut(a, ".")
	w, _ := strconv.ParseInt(whole, 10, 64)
	for len(frac) < 2 {
		frac += "0"
	}
	f, _ := strconv.ParseInt(frac, 10, 64)
	return w*100 + f, nil
}

func formatCents(c int64) string { return fmt.Sprintf("%d.%02d", c/100, c%100) }

// NormalizeSpendLimit checks a limit and writes it in canonical form: the
// amount with two decimals, the currency upper-case (default USD), each
// merchant an https origin.
func NormalizeSpendLimit(l *store.SpendLimit) (*store.SpendLimit, error) {
	if l == nil {
		return nil, nil
	}
	c, err := parseCents(l.Amount)
	if err != nil {
		return nil, err
	}
	if c <= 0 {
		return nil, errors.New("amount: more than zero (clear the limit to remove it)")
	}
	out := &store.SpendLimit{Amount: formatCents(c), Currency: strings.ToUpper(strings.TrimSpace(l.Currency)), Card: strings.TrimSpace(l.Card)}
	if out.Currency == "" {
		out.Currency = "USD"
	}
	if !currencyRE.MatchString(out.Currency) {
		return nil, fmt.Errorf("currency %q: a three-letter code (USD, EUR, …)", l.Currency)
	}
	if out.Card != "" && !cardRE.MatchString(out.Card) {
		return nil, fmt.Errorf("card %q: a card's name (lowercase letters, digits, . _ -)", out.Card)
	}
	seen := map[string]bool{}
	for _, m := range l.Merchants {
		o, err := merchantOrigin(m)
		if err != nil {
			return nil, err
		}
		if !seen[o] {
			seen[o] = true
			out.Merchants = append(out.Merchants, o)
		}
	}
	if len(out.Merchants) > MaxSpendMerchants {
		return nil, fmt.Errorf("%d merchants, at most %d", len(out.Merchants), MaxSpendMerchants)
	}
	return out, nil
}

// merchantOrigin is a merchant as an origin: https://host[:port], nothing
// more.
func merchantOrigin(m string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(m))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("merchant %q: an https origin (https://shop.example.com)", m)
	}
	return "https://" + strings.ToLower(u.Host), nil
}

// spendLimitWidens reports whether moving from old to new widens what may be
// spent, and how: a limit set where there was none counts (only an operator
// sets one), as do a higher amount, another currency, another or no card,
// a merchant added or the merchant restriction dropped, or the limit cleared.
func spendLimitWidens(old, new *store.SpendLimit) (bool, string) {
	switch {
	case old == nil && new == nil:
		return false, ""
	case old == nil:
		return true, "a spend limit set"
	case new == nil:
		return true, "the spend limit cleared"
	}
	oc, _ := parseCents(old.Amount)
	nc, _ := parseCents(new.Amount)
	switch {
	case new.Currency != old.Currency:
		return true, "the spend limit's currency changed"
	case nc > oc:
		return true, "the spend limit raised " + old.Amount + " → " + new.Amount + " " + new.Currency
	case old.Card != "" && new.Card != old.Card:
		return true, "the spend limit's card changed"
	}
	if len(old.Merchants) > 0 {
		if len(new.Merchants) == 0 {
			return true, "the spend limit's merchant restriction dropped"
		}
		allowed := map[string]bool{}
		for _, m := range old.Merchants {
			allowed[m] = true
		}
		for _, m := range new.Merchants {
			if !allowed[m] {
				return true, "merchant " + m + " added to the spend limit"
			}
		}
	}
	return false, ""
}

// checkSpendChange applies who may change a limit: an operator anything (a
// widening is returned to be recorded); a bot only a narrowing.
func checkSpendChange(a TicketActor, old, new *store.SpendLimit) (widening string, err error) {
	w, why := spendLimitWidens(old, new)
	if !w {
		return "", nil
	}
	if a.isBot() {
		return "", fmt.Errorf("only an operator sets or raises a ticket's spend limit (%s): a bot may only lower it", why)
	}
	return why, nil
}

// SpendStatus is a ticket's limit beside what has been spent on it.
type SpendStatus struct {
	Known     bool   `json:"known"`
	Spent     string `json:"spent,omitempty"`
	Remaining string `json:"remaining,omitempty"`
	Currency  string `json:"currency"`
	Why       string `json:"why,omitempty"`
}

// ticketSpent reads the durable committed total for a ticket by its stable id
// (the same key the lifetime cap is enforced against). It does not sum the
// ticket's per-bot case copies: a reassigned ticket's old copy is closed and
// eventually pruned from the bot ledger, but its purchases keep the ticket's
// budget key, so this never under-reports.
func (s *Service) ticketSpent(tid string, l *store.SpendLimit) SpendStatus {
	st := SpendStatus{Currency: l.Currency}
	if s.Spend == nil {
		st.Why = "no purchase ledger on this daemon"
		return st
	}
	total, err := s.Spend.SpentOnTicket(tid, l.Currency)
	if err != nil {
		st.Why = err.Error()
		return st
	}
	limit, _ := parseCents(l.Amount)
	rem := limit - total
	if rem < 0 {
		rem = 0
	}
	st.Known, st.Spent, st.Remaining = true, formatCents(total), formatCents(rem)
	return st
}

// TicketSpend is what the browser's payments enforce: the spend limit of
// the ticket a bot's case is, and what has been spent on it so far (every
// copy of the ticket). ok false: the case is no ticket, or it has no limit.
func (s *Service) TicketSpend(bot, caseID string) (limit store.SpendLimit, spent SpendStatus, ok bool) {
	c := s.Ledger.Bot(bot).Cases[caseID]
	if c == nil || c.Ticket == "" {
		return store.SpendLimit{}, SpendStatus{}, false
	}
	live, _, err := s.findTicket(c.Ticket)
	if err != nil || live.SpendLimit == nil {
		return store.SpendLimit{}, SpendStatus{}, false
	}
	return *live.SpendLimit, s.ticketSpent(c.Ticket, live.SpendLimit), true
}

// SpendHeld says why bot may not spend under case c's spend limit, or ""
// when it may. A limit is confirmed by an operator for one bot (SpendFor);
// it is spent by that bot, on the live copy of an open ticket. A copy the
// ticket left behind when it moved, a closed ticket, and a limit that
// reached another bot without an operator confirming it (a handoff, a
// team's routing or a lead's triage, a bot's reassignment) allow nothing:
// the purchase is refused, never left to the looser layers.
func SpendHeld(bot string, c *store.Case) string {
	what := "case " + c.ID
	if c.Ticket != "" {
		what = "ticket " + c.Ticket
	}
	switch {
	case c.MovedTo != "":
		return what + " moved to " + c.MovedTo + ": its spend limit went with it"
	case store.Closed(c.Status):
		return what + " is " + c.Status + ": a closed ticket's spend limit allows nothing (an operator who reopens it confirms the limit again)"
	case c.SpendFor != bot:
		was := "no bot"
		if c.SpendFor != "" {
			was = "bot:" + c.SpendFor
		}
		return "the spend limit on " + what + " is held: an operator confirmed it for " + was + ", not bot:" + bot +
			"; it is spent once an operator confirms it for bot:" + bot + " (sets the limit again, or assigns the ticket to it)"
	}
	return ""
}

// spendHolder is the bot an assignee keeps a limit for: "bot:<name>"'s
// name, and "" for a team or the triage queue (the bot that picks the ticket
// up there is confirmed afterwards).
func spendHolder(assignee string) string {
	if kind, name, _ := strings.Cut(assignee, ":"); kind == "bot" {
		return name
	}
	return ""
}

// spendForText names the bot a limit is confirmed for, for a history line.
func spendForText(bot string) string {
	if bot == "" {
		return "held until an operator confirms it for the bot that picks the ticket up"
	}
	return "confirmed for bot:" + bot
}

// spendText is a limit in a line: "120.00 USD on card visa at https://shop".
func spendText(l *store.SpendLimit) string {
	if l == nil {
		return "none"
	}
	t := l.Amount + " " + l.Currency
	if l.Card != "" {
		t += " on card " + l.Card
	}
	if len(l.Merchants) > 0 {
		t += " at " + strings.Join(l.Merchants, ", ")
	}
	return t
}

func sameSpendLimit(a, b *store.SpendLimit) bool { return spendText(a) == spendText(b) }
