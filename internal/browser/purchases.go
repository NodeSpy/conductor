package browser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Purchases: every card fill is for one purchase the operator approved.
//
//  1. The agent proposes it (`conductor browser purchase`): the card, the
//     merchant (an exact origin on the card's list), the items, the total
//     and its currency, recurring or one-off, and why. Conductor checks the
//     card, the merchant and the limits, and records it pending — or, for a
//     bot, hands it to the bot's gate (its commit_money tier and spend cap),
//     which may allow it unattended under the bot's opt-in caps.
//  2. An operator approves it, signed (the same signed approval a bot's
//     proposal takes). The approval is single-use and expires.
//  3. `conductor browser pay` fills the card only for that purchase, only on
//     its merchant's page, after re-reading the total on the page: higher,
//     another currency or another merchant is refused, and a new approval is
//     proposed for what the page shows. The approval is consumed before
//     the fill, and the limits re-checked at that moment.
//  4. The order confirmation (merchant, total, order number) is recorded.

// Purchase statuses.
const (
	PurchasePending  = "pending"
	PurchaseApproved = "approved"
	PurchaseDenied   = "denied"
	PurchaseExpired  = "expired"
	PurchaseUsed     = "used"
	PurchaseRefused  = "refused"
	PurchaseDone     = "completed"
)

// PurchaseItem is one line of a proposed purchase.
type PurchaseItem struct {
	Name   string  `json:"name"`
	Qty    int     `json:"qty,omitempty"`
	Amount float64 `json:"amount,omitempty"`
}

// Receipt is an order confirmation, as conductor read it from the page.
type Receipt struct {
	OrderNumber string    `json:"order_number,omitempty"`
	Total       float64   `json:"total,omitempty"`
	Currency    string    `json:"currency,omitempty"`
	URL         string    `json:"url,omitempty"`
	At          time.Time `json:"at"`
	// Note flags a confirmation that does not match the approval.
	Note string `json:"note,omitempty"`
}

// Purchase is one proposed purchase.
type Purchase struct {
	ID        string         `json:"id"`
	Card      string         `json:"card"`
	Merchant  string         `json:"merchant"`
	Items     []PurchaseItem `json:"items,omitempty"`
	Total     float64        `json:"total"`
	Currency  string         `json:"currency"`
	Recurring bool           `json:"recurring,omitempty"`
	// Interval is a recurring charge's period (month, year, …).
	Interval string `json:"interval,omitempty"`
	Why      string `json:"why,omitempty"`
	// Who proposed it, and for what.
	Identity  string   `json:"identity"`
	Groups    []string `json:"groups,omitempty"`
	Requester string   `json:"requester,omitempty"`
	Dispatch  string   `json:"dispatch,omitempty"`
	Bot       string   `json:"bot,omitempty"`
	CaseID    string   `json:"case,omitempty"`
	// Status and its decision.
	Status     string    `json:"status"`
	Created    time.Time `json:"created"`
	Decided    time.Time `json:"decided,omitempty"`
	Approver   string    `json:"approver,omitempty"`
	Unattended bool      `json:"unattended,omitempty"`
	// Expires is when an approval lapses (unused); a pending proposal
	// lapses too.
	Expires time.Time `json:"expires"`
	// Digest pins what was proposed: an approval signs it.
	Digest string `json:"digest"`
	// BotProposal is the bot gate's proposal for a bot's purchase.
	BotProposal string `json:"bot_proposal,omitempty"`
	// Supersedes is the purchase a price change at checkout replaced.
	Supersedes string   `json:"supersedes,omitempty"`
	Reason     string   `json:"reason,omitempty"`
	Receipt    *Receipt `json:"receipt,omitempty"`
}

// PurchaseDigest pins a purchase's immutable terms.
func PurchaseDigest(p Purchase) string {
	b, _ := json.Marshal([]any{p.ID, p.Card, p.Merchant, p.Items, round2(p.Total), p.Currency, p.Recurring, p.Interval, p.Identity, p.Bot})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

// SpendEntry is one charge: when a card was filled for a purchase.
type SpendEntry struct {
	Card     string    `json:"card"`
	Identity string    `json:"identity"`
	Groups   []string  `json:"groups,omitempty"`
	Amount   float64   `json:"amount"`
	Currency string    `json:"currency"`
	Purchase string    `json:"purchase"`
	At       time.Time `json:"at"`
}

// purchasesFile is <state>/browser/purchases.json.
type purchasesFile struct {
	Next      int                  `json:"next"`
	Purchases map[string]*Purchase `json:"purchases"`
	Spend     []SpendEntry         `json:"spend"`
}

// Purchases is the ledger.
type Purchases struct {
	Dir string
	Now func() time.Time

	mu sync.Mutex
}

func (l *Purchases) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now().UTC()
}

func (l *Purchases) path() string { return filepath.Join(l.Dir, "purchases.json") }

func (l *Purchases) loadLocked() (*purchasesFile, error) {
	f := &purchasesFile{Purchases: map[string]*Purchase{}}
	b, err := os.ReadFile(l.path())
	if os.IsNotExist(err) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, f); err != nil {
		// An unreadable ledger is not an empty one: spend history unknown.
		return nil, fmt.Errorf("browser: the purchase ledger %s is unreadable (spend history unknown) — no purchase until it is restored: %w", l.path(), err)
	}
	if f.Purchases == nil {
		f.Purchases = map[string]*Purchase{}
	}
	return f, nil
}

func (l *Purchases) saveLocked(f *purchasesFile) error {
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := l.path() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, l.path())
}

// update runs fn on the ledger under its lock and saves it.
func (l *Purchases) update(fn func(f *purchasesFile) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.loadLocked()
	if err != nil {
		return err
	}
	if err := fn(f); err != nil {
		return err
	}
	return l.saveLocked(f)
}

// Get returns one purchase (expiring it first if its time passed).
func (l *Purchases) Get(id string) (Purchase, bool, error) {
	var out Purchase
	var ok bool
	err := l.update(func(f *purchasesFile) error {
		l.expireLocked(f)
		if p := f.Purchases[id]; p != nil {
			out, ok = *p, true
		}
		return nil
	})
	return out, ok, err
}

// List returns every purchase, newest first (status filters, "" = all).
func (l *Purchases) List(status string) ([]Purchase, error) {
	var out []Purchase
	err := l.update(func(f *purchasesFile) error {
		l.expireLocked(f)
		for _, p := range f.Purchases {
			if status == "" || p.Status == status {
				out = append(out, *p)
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, err
}

// expireLocked lapses pending proposals and unused approvals past their
// time.
func (l *Purchases) expireLocked(f *purchasesFile) {
	now := l.now()
	for _, p := range f.Purchases {
		if (p.Status == PurchasePending || p.Status == PurchaseApproved) && !now.Before(p.Expires) {
			p.Status = PurchaseExpired
		}
	}
}

// Spent sums a card's spend since a time, for the entries match accepts.
func spentBy(f *purchasesFile, card string, since time.Time, match func(SpendEntry) bool) float64 {
	var t float64
	for _, s := range f.Spend {
		// Strictly inside the window, as the bot ledger counts (a spend
		// exactly one period old has left it).
		if s.Card == card && s.At.After(since) && match(s) {
			t += s.Amount
		}
	}
	return t
}

// checkLimits is the card's limits (and each identity limit that applies)
// for amt more, with what the ledger has recorded: the one spend-cap rule
// (config.SpendCap.Check), per purchase, per day and per month.
func checkLimits(f *purchasesFile, card Card, c Principal, amt float64, currency string, now time.Time) error {
	if currency != card.Limits.CurrencyOr() {
		return fmt.Errorf("the purchase is in %s, card %s's limits are in %s", currency, card.Name, card.Limits.CurrencyOr())
	}
	for _, sc := range card.Limits.Caps() {
		if err := sc.Check(amt, now, func(since time.Time) float64 {
			return spentBy(f, card.Name, since, func(SpendEntry) bool { return true })
		}); err != nil {
			return fmt.Errorf("card %s: %v", card.Name, err)
		}
	}
	for _, k := range card.limitKeys(c) {
		key := k
		for _, sc := range card.IdentityLimits[k].Caps() {
			if err := sc.Check(amt, now, func(since time.Time) float64 {
				return spentBy(f, card.Name, since, func(s SpendEntry) bool {
					return (LoginSite{Identities: []string{key}}).ForPrincipal(Principal{Identity: s.Identity, Groups: s.Groups})
				})
			}); err != nil {
				return fmt.Errorf("card %s, for %s: %v", card.Name, key, err)
			}
		}
	}
	return nil
}

// CheckLimits checks amt against a card's limits as the ledger stands.
func (l *Purchases) CheckLimits(card Card, c Principal, amt float64, currency string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.loadLocked()
	if err != nil {
		return err
	}
	return checkLimits(f, card, c, amt, currency, l.now())
}

// Propose records a purchase, pending, expiring after ttl.
func (l *Purchases) Propose(p Purchase, ttl time.Duration) (Purchase, error) {
	err := l.update(func(f *purchasesFile) error {
		f.Next++
		p.ID = fmt.Sprintf("pu%d", f.Next)
		p.Status, p.Created = PurchasePending, l.now()
		p.Total = round2(p.Total)
		p.Expires = p.Created.Add(ttl)
		p.Digest = PurchaseDigest(p)
		cp := p
		f.Purchases[p.ID] = &cp
		return nil
	})
	return p, err
}

// Approve moves a pending purchase to approved — once (the re-check under
// the lock makes a double approval approve once) — usable until ttl from
// now. digest must be the one the approval signed.
func (l *Purchases) Approve(id, digest, approver string, ttl time.Duration, unattended bool) (Purchase, error) {
	var out Purchase
	err := l.update(func(f *purchasesFile) error {
		l.expireLocked(f)
		p := f.Purchases[id]
		switch {
		case p == nil:
			return fmt.Errorf("no purchase %s", id)
		case p.Status != PurchasePending:
			return fmt.Errorf("purchase %s is %s, not pending", id, p.Status)
		case digest != p.Digest:
			return fmt.Errorf("purchase %s: the approval is for other terms", id)
		}
		now := l.now()
		p.Status, p.Approver, p.Decided, p.Unattended = PurchaseApproved, approver, now, unattended
		p.Expires = now.Add(ttl)
		out = *p
		return nil
	})
	return out, err
}

// Deny refuses a pending purchase.
func (l *Purchases) Deny(id, operator, reason string) (Purchase, error) {
	var out Purchase
	err := l.update(func(f *purchasesFile) error {
		l.expireLocked(f)
		p := f.Purchases[id]
		if p == nil {
			return fmt.Errorf("no purchase %s", id)
		}
		if p.Status != PurchasePending && p.Status != PurchaseApproved {
			return fmt.Errorf("purchase %s is %s", id, p.Status)
		}
		p.Status, p.Approver, p.Decided, p.Reason = PurchaseDenied, operator, l.now(), reason
		out = *p
		return nil
	})
	return out, err
}

// Consume uses an approval: it must be approved, unexpired, the caller's,
// and the card's limits must still hold now (the period boundary is
// checked at the moment of spending). The approval is used up whatever
// happens next, and the spend recorded.
func (l *Purchases) Consume(id string, c Principal, card Card) (Purchase, error) {
	var out Purchase
	var refusal error
	err := l.update(func(f *purchasesFile) error {
		l.expireLocked(f)
		p := f.Purchases[id]
		switch {
		case p == nil:
			return refuse("no purchase %s", id)
		case p.Status == PurchaseUsed || p.Status == PurchaseDone:
			return refuse("purchase %s was already used — an approval is single-use; propose a new purchase", id)
		case p.Status == PurchaseExpired:
			return refuse("purchase %s's approval expired — propose it again", id)
		case p.Status != PurchaseApproved:
			return refuse("purchase %s is %s, not approved", id, p.Status)
		case p.Identity != c.Identity || p.Bot != c.Bot:
			return refuse("purchase %s is another session's", id)
		case p.Card != card.Name:
			return refuse("purchase %s is for card %s", id, p.Card)
		}
		if err := checkLimits(f, card, c, p.Total, p.Currency, l.now()); err != nil {
			// Recorded (and saved): the approval is spent on a refusal.
			p.Status, p.Reason = PurchaseRefused, err.Error()
			refusal = refuse("%v", err)
			return nil
		}
		p.Status = PurchaseUsed
		f.Spend = append(f.Spend, SpendEntry{Card: p.Card, Identity: p.Identity, Groups: c.Groups, Amount: p.Total, Currency: p.Currency, Purchase: p.ID, At: l.now()})
		out = *p
		return nil
	})
	if err == nil && refusal != nil {
		return Purchase{}, refusal
	}
	return out, err
}

// Complete records the order confirmation.
func (l *Purchases) Complete(id string, r Receipt) (Purchase, error) {
	var out Purchase
	err := l.update(func(f *purchasesFile) error {
		p := f.Purchases[id]
		if p == nil {
			return fmt.Errorf("no purchase %s", id)
		}
		p.Status, p.Receipt = PurchaseDone, &r
		out = *p
		return nil
	})
	return out, err
}

// Refuse marks a purchase refused, with why.
func (l *Purchases) Refuse(id, why string) {
	_ = l.update(func(f *purchasesFile) error {
		if p := f.Purchases[id]; p != nil && (p.Status == PurchasePending || p.Status == PurchaseApproved) {
			p.Status, p.Reason = PurchaseRefused, why
		}
		return nil
	})
}

// Unspend records that a used approval placed no order (the checkout was
// stopped before it was submitted): the purchase is refused, with why, its
// spend leaves the ledger, and the approval stays used.
func (l *Purchases) Unspend(id, why string) {
	_ = l.update(func(f *purchasesFile) error {
		p := f.Purchases[id]
		if p == nil || p.Status != PurchaseUsed {
			return nil
		}
		p.Status, p.Reason = PurchaseRefused, why
		kept := f.Spend[:0]
		for _, e := range f.Spend {
			if e.Purchase != id {
				kept = append(kept, e)
			}
		}
		f.Spend = kept
		return nil
	})
}

// SetBotProposal records the bot gate's proposal for a purchase.
func (l *Purchases) SetBotProposal(id, proposal string) {
	_ = l.update(func(f *purchasesFile) error {
		if p := f.Purchases[id]; p != nil {
			p.BotProposal = proposal
		}
		return nil
	})
}

// SpendSummary is spend per card and per identity in the last day and
// month (the dashboard's view).
type SpendSummary struct {
	Card     string  `json:"card"`
	Identity string  `json:"identity,omitempty"`
	Currency string  `json:"currency"`
	Day      float64 `json:"day"`
	Month    float64 `json:"month"`
}

// Summary sums spend per card, and per card and identity.
func (l *Purchases) Summary() ([]SpendSummary, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.loadLocked()
	if err != nil {
		return nil, err
	}
	now := l.now()
	idx := map[string]*SpendSummary{}
	add := func(card, id, cur string, s SpendEntry) {
		k := card + "\x00" + id
		x := idx[k]
		if x == nil {
			x = &SpendSummary{Card: card, Identity: id, Currency: cur}
			idx[k] = x
		}
		if !s.At.Before(now.Add(-24 * time.Hour)) {
			x.Day += s.Amount
		}
		if !s.At.Before(now.Add(-30 * 24 * time.Hour)) {
			x.Month += s.Amount
		}
	}
	for _, s := range f.Spend {
		add(s.Card, "", s.Currency, s)
		add(s.Card, s.Identity, s.Currency, s)
	}
	var out []SpendSummary
	for _, x := range idx {
		x.Day, x.Month = round2(x.Day), round2(x.Month)
		out = append(out, *x)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Card != out[j].Card {
			return out[i].Card < out[j].Card
		}
		return out[i].Identity < out[j].Identity
	})
	return out, nil
}
