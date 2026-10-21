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
// payments implement it): what has been spent on one bot's case, in a
// currency. A daemon without one reports nothing spent, and says it can't
// tell.
type SpendLedger interface {
	SpentOnCase(bot, caseID, currency string) (cents int64, err error)
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

// ticketSpent sums what has been spent on every copy of a ticket (it keeps
// its spend when it moves from one bot to another).
func (s *Service) ticketSpent(tid string, l *store.SpendLimit) SpendStatus {
	st := SpendStatus{Currency: l.Currency}
	if s.Spend == nil {
		st.Why = "no purchase ledger on this daemon"
		return st
	}
	var total int64
	for _, b := range s.ticketBots() {
		for id, c := range s.Ledger.Bot(b).Cases {
			if c.Ticket != tid {
				continue
			}
			n, err := s.Spend.SpentOnCase(b, id, l.Currency)
			if err != nil {
				st.Why = err.Error()
				return st
			}
			total += n
		}
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
