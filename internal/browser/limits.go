package browser

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

// Spend limits stack, and the lowest wins. Every layer applies to every
// purchase and none can widen another:
//
//  1. the card's limits — per purchase, per day, per 30 days;
//  2. the card's identity_limits for the caller (an identity, bot or team);
//  3. the bot's purchases: caps (and per card), and its team's
//     budget.purchases (ceilings the bot's can only narrow);
//  4. the ticket's (case's) spend_limit: everything proposed under it over
//     its whole life — pending, approved, being paid, paid — never more
//     than the amount; a refused, denied or expired purchase frees its
//     share; unspent budget is never transferable. Clamped to the lowest
//     30-day cap of the layers above.
//
// Each check point — propose, approve, the fill — evaluates every layer,
// records which one bound the decision, and a refusal names that layer and
// what is left in it.

// CaseBudget is a ticket's spend budget, as read for a purchase.
type CaseBudget struct {
	// Key is what the budget's spend is counted under: by default the
	// case's own reference ("bot:<name>/<case>"); a ticket store may give
	// its ticket's stable id so a moved ticket keeps one count.
	Key       string
	Amount    float64
	Currency  string
	Card      string
	Merchants []string
	// Held, when set, says why the case's budget allows nothing now (the
	// limit was confirmed for another bot, the ticket moved on or closed):
	// the purchase is refused with it, not left to the other layers.
	Held string
}

// CaseBudgets reads a case's spend_limit by the case's reference
// ("bot:<name>/<case>"). ok=false: the case has none, and the purchase
// falls back to the other layers.
type CaseBudgets interface {
	CaseBudget(caseRef string) (CaseBudget, bool)
}

// CaseRef is a bot case's reference.
func CaseRef(bot, caseID string) string {
	if bot == "" || caseID == "" {
		return ""
	}
	return "bot:" + bot + "/" + caseID
}

// ParseSpendLimit reads a spend_limit's amount ("50.00").
func ParseSpendLimit(amount string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(amount), 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, fmt.Errorf("spend_limit.amount %q is not a positive amount", amount)
	}
	return round2(f), nil
}

// LayerState is one layer's standing for a purchase: its limit, what the
// window already holds (this purchase not counted), and what is left.
type LayerState struct {
	Layer     string  `json:"layer"`
	Window    string  `json:"window"` // purchase | day | 30 days | ticket
	Limit     float64 `json:"limit"`
	Used      float64 `json:"used"`
	Remaining float64 `json:"remaining"`
	Currency  string  `json:"currency"`
	Note      string  `json:"note,omitempty"`
	// Binding marks the layer that decided: the one that refused, else
	// the one this purchase leaves the least room in.
	Binding bool `json:"binding,omitempty"`
}

// SpendRules are the layers for one purchase.
type SpendRules struct {
	Card       Card
	Who        Principal
	Bot        string
	BotLimits  *config.PurchaseLimits
	Team       string
	TeamLimits *config.PurchaseLimits
	Budget     *CaseBudget
}

// rulesFor builds a purchase's layers from the settings now.
func (m *Manager) rulesFor(st Settings, card Card, p Purchase) SpendRules {
	r := SpendRules{Card: card, Who: Principal{Identity: p.Identity, Groups: p.Groups, Bot: p.Bot}, Bot: p.Bot}
	if p.Bot != "" {
		r.BotLimits = st.BotPurchases[p.Bot]
		r.Team = st.BotTeam[p.Bot]
		if r.Team != "" {
			r.TeamLimits = st.TeamPurchases[r.Team]
		}
	}
	if p.CaseRef != "" && m.Budgets != nil {
		if b, ok := m.Budgets.CaseBudget(p.CaseRef); ok {
			r.Budget = &b
		}
	}
	return r
}

// committing are the statuses a ticket's budget counts: everything
// proposed under it that may still be (or was) paid.
var committing = map[string]bool{PurchasePending: true, PurchaseApproved: true, PurchaseUsed: true, PurchaseDone: true}

// evaluate is every layer for p (amt in cur) as the ledger stands, the
// purchase exclude not counted; the error names the refusing layer.
func evaluate(f *purchasesFile, r SpendRules, p Purchase, exclude string, now time.Time) ([]LayerState, error) {
	amt, cur := round2(p.Total), p.Currency
	var states []LayerState
	var refusal error
	refuseAt := -1
	windows := func(name string, l config.CardLimits, match func(SpendEntry) bool) {
		if l.CurrencyOr() != cur {
			states = append(states, LayerState{Layer: name, Window: "currency", Currency: l.CurrencyOr()})
			if refusal == nil {
				refuseAt = len(states) - 1
				refusal = refuse("the purchase is in %s, %s's limits are in %s", cur, name, l.CurrencyOr())
			}
			return
		}
		sum := func(d time.Duration) float64 {
			var t float64
			for _, s := range f.Spend {
				// Strictly inside the window, as the bot ledger counts.
				if s.At.After(now.Add(-d)) && s.Purchase != exclude && match(s) {
					t += s.Amount
				}
			}
			return round2(t)
		}
		for _, w := range []struct {
			window string
			limit  float64
			used   float64
		}{{"purchase", l.PerPurchase, 0}, {"day", l.PerDay, sum(24 * time.Hour)}, {"30 days", l.PerMonth, sum(30 * 24 * time.Hour)}} {
			st := LayerState{Layer: name, Window: w.window, Limit: w.limit, Used: w.used, Remaining: round2(w.limit - w.used), Currency: cur}
			states = append(states, st)
			if refusal == nil && amt > st.Remaining+0.0001 {
				refuseAt = len(states) - 1
				refusal = refuse("%s", overMessage(st))
			}
		}
	}
	card := r.Card
	windows("card "+card.Name, card.Limits, func(s SpendEntry) bool { return s.Card == card.Name })
	for _, k := range card.limitKeys(r.Who) {
		key := k
		windows("card "+card.Name+" for "+key, card.IdentityLimits[k], func(s SpendEntry) bool {
			return s.Card == card.Name && (LoginSite{Identities: []string{key}}).ForPrincipal(Principal{Identity: s.Identity, Groups: s.Groups, Bot: s.Bot})
		})
	}
	if r.Bot != "" {
		bot := r.Bot
		if r.BotLimits != nil {
			windows("bot "+bot, r.BotLimits.CardLimits, func(s SpendEntry) bool { return s.Bot == bot })
			if l, ok := r.BotLimits.Cards[card.Name]; ok {
				windows("bot "+bot+" on card "+card.Name, l, func(s SpendEntry) bool { return s.Bot == bot && s.Card == card.Name })
			}
		}
		if r.Team != "" && r.TeamLimits != nil {
			team := r.Team
			windows("team "+team, r.TeamLimits.CardLimits, func(s SpendEntry) bool { return s.Team == team })
			if l, ok := r.TeamLimits.Cards[card.Name]; ok {
				windows("team "+team+" on card "+card.Name, l, func(s SpendEntry) bool { return s.Team == team && s.Card == card.Name })
			}
		}
	}
	if b := r.Budget; b != nil {
		st := LayerState{Layer: "ticket " + b.Key, Window: "ticket", Currency: b.Currency}
		limit, note := clampBudget(b.Amount, states)
		st.Limit, st.Note = limit, note
		for _, q := range f.Purchases {
			if q.BudgetKey == b.Key && q.ID != exclude && committing[q.Status] {
				st.Used += q.Total
			}
		}
		st.Used = round2(st.Used)
		st.Remaining = round2(st.Limit - st.Used)
		states = append(states, st)
		if refusal == nil {
			switch {
			case b.Held != "":
				refusal = refuse("%s", b.Held)
			case b.Currency != cur:
				refusal = refuse("the purchase is in %s, this ticket's budget is in %s", cur, b.Currency)
			case b.Card != "" && b.Card != card.Name:
				refusal = refuse("this ticket's budget is for card %s, not %s", b.Card, card.Name)
			case len(b.Merchants) > 0 && !merchantIn(b.Merchants, p.Merchant):
				refusal = refuse("this ticket's budget does not cover %s (its merchants: %s)", p.Merchant, strings.Join(b.Merchants, ", "))
			case amt > st.Remaining+0.0001:
				refusal = refuse("%s", overMessage(st))
			}
			if refusal != nil {
				refuseAt = len(states) - 1
			}
		}
	}
	// The binding layer: the refusing one, else the one this purchase
	// leaves the least room in.
	bind := refuseAt
	if bind < 0 {
		least := math.Inf(1)
		for i, s := range states {
			if s.Window == "currency" {
				continue
			}
			if left := s.Remaining - amt; left < least {
				least, bind = left, i
			}
		}
	}
	if bind >= 0 {
		states[bind].Binding = true
	}
	return states, refusal
}

// overMessage names a layer and what is left in it.
func overMessage(s LayerState) string {
	who := s.Layer
	if strings.HasPrefix(who, "ticket ") {
		who = "this ticket"
	}
	switch s.Window {
	case "purchase":
		return fmt.Sprintf("over %s's %.2f %s per-purchase limit", who, s.Limit, s.Currency)
	case "day":
		return fmt.Sprintf("over %s's %.2f %s daily limit: %.2f %s left", who, s.Limit, s.Currency, math.Max(s.Remaining, 0), s.Currency)
	case "30 days":
		return fmt.Sprintf("over %s's %.2f %s 30-day limit: %.2f %s left", who, s.Limit, s.Currency, math.Max(s.Remaining, 0), s.Currency)
	}
	return fmt.Sprintf("over %s's %.2f %s limit: %.2f %s left", who, s.Limit, s.Currency, math.Max(s.Remaining, 0), s.Currency)
}

// clampBudget is a ticket budget clamped to the lowest 30-day cap of the
// layers above it (a ticket cannot exceed them), and why.
func clampBudget(amount float64, above []LayerState) (float64, string) {
	limit, by := amount, ""
	for _, s := range above {
		if s.Window == "30 days" && s.Limit < limit {
			limit, by = s.Limit, s.Layer
		}
	}
	if by == "" {
		return amount, ""
	}
	return limit, fmt.Sprintf("clamped from %.2f to %s's 30-day limit %.2f", amount, by, limit)
}

func merchantIn(list []string, m string) bool {
	mo, err := parseOrigin(m)
	if err != nil {
		return false
	}
	for _, x := range list {
		o, err := parseOrigin(strings.TrimSuffix(x, "/"))
		if err == nil && o.scheme == mo.scheme && o.host == mo.host && o.port == mo.port {
			return true
		}
	}
	return false
}

// Evaluate is every layer for p now (exclude: the purchase itself, when it
// is already in the ledger).
func (l *Purchases) Evaluate(r SpendRules, p Purchase, exclude string) ([]LayerState, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.loadLocked()
	if err != nil {
		return nil, err
	}
	l.expireLocked(f)
	return evaluate(f, r, p, exclude, l.now())
}

// bindingOf is the binding layer's summary ("ticket bot:x/c3: 35.00 USD
// left").
func bindingOf(states []LayerState) string {
	for _, s := range states {
		if s.Binding {
			return fmt.Sprintf("%s (%s): %.2f %s left of %.2f", s.Layer, s.Window, s.Remaining, s.Currency, s.Limit)
		}
	}
	return ""
}

// RemainingByLayer is the dashboard's view: each layer's tightest window.
func RemainingByLayer(states []LayerState) []LayerState {
	best := map[string]LayerState{}
	for _, s := range states {
		if s.Window == "currency" {
			continue
		}
		if b, ok := best[s.Layer]; !ok || s.Remaining < b.Remaining || s.Binding {
			if ok && b.Binding && !s.Binding {
				continue
			}
			best[s.Layer] = s
		}
	}
	out := make([]LayerState, 0, len(best))
	for _, s := range best {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Layer < out[j].Layer })
	return out
}

// PurchaseLayers is every layer for a purchase as things stand now (the
// purchase itself not counted): the approval's view of what is left.
func (m *Manager) PurchaseLayers(p Purchase) ([]LayerState, error) {
	if m.Purchases == nil {
		return nil, fmt.Errorf("browser: no purchase ledger")
	}
	st := m.settings()
	cd, ok := st.Cards[p.Card]
	if !ok {
		return nil, fmt.Errorf("browser: no card %s", p.Card)
	}
	return m.Purchases.Evaluate(m.rulesFor(st, cd, p), p, p.ID)
}

// SpentOnCase sums, in cents, what one bot's case has committed in a currency
// — the pending/approved/being-paid/paid purchases proposed under it (the same
// committing set the budget counts). It implements bot.SpendLedger so a
// ticket's spend view reads the real ledger: ticketSpent asks it per copy
// (bot + the copy's case id) and sums across copies, matching the lifetime cap
// enforcement (which counts the same committing statuses under the ticket's
// stable id). A copy keeps its own case id on the purchase, so this is the
// per-copy slice of that total.
func (l *Purchases) SpentOnCase(bot, caseID, currency string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.loadLocked()
	if err != nil {
		return 0, err
	}
	l.expireLocked(f)
	cur := strings.ToUpper(strings.TrimSpace(currency))
	var cents int64
	for _, p := range f.Purchases {
		if p.Bot != bot || p.CaseID != caseID || !committing[p.Status] {
			continue
		}
		if strings.ToUpper(p.Currency) != cur {
			continue
		}
		cents += int64(math.Round(round2(p.Total) * 100))
	}
	return cents, nil
}

// CommittedCents sums, in cents, what a ticket's budget holds now — the
// committing purchases under a budget key, in a currency. It is the durable
// lifetime total the cap is enforced against (the key is the ticket's stable
// id), independent of whether the bot's case copies still live in the ledger:
// a reassigned ticket's old copy is closed and eventually pruned from the bot
// ledger, but its purchases keep their budget key here, so a ticket's spend
// view reads this and never under-reports.
func (l *Purchases) CommittedCents(key, currency string) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.loadLocked()
	if err != nil {
		return 0, err
	}
	l.expireLocked(f)
	cur := strings.ToUpper(strings.TrimSpace(currency))
	var cents int64
	for _, p := range f.Purchases {
		if p.BudgetKey != key || !committing[p.Status] {
			continue
		}
		if strings.ToUpper(p.Currency) != cur {
			continue
		}
		cents += int64(math.Round(round2(p.Total) * 100))
	}
	return cents, nil
}

// Committed is what a ticket's budget holds now (pending, approved, being
// paid, paid), and those purchases.
func (l *Purchases) Committed(key string) (float64, []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := l.loadLocked()
	if err != nil {
		return 0, nil
	}
	l.expireLocked(f)
	var t float64
	var ids []string
	for _, p := range f.Purchases {
		if p.BudgetKey == key && committing[p.Status] {
			t += p.Total
			ids = append(ids, p.ID)
		}
	}
	sort.Strings(ids)
	return round2(t), ids
}
