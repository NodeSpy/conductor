package config

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Personal details and payment cards conductor fills for an agent, beside
// its logins (docs/wiki/Browser.md, "Personal details" and "Payment
// cards"). The agent never receives a value: it asks conductor to fill a
// field by its meaning, on a site the operator opted in, and conductor
// types the value into the confined browser.

// BrowserProfile is one named set of personal details (a personal one, a
// work one, …).
type BrowserProfile struct {
	// Fields are where each value comes from (vault reference, ${env.X},
	// a named secret); a typed value goes in write-only
	// (`conductor browser profile set`), never in the config.
	Fields map[string]*BrowserSecretRef `yaml:"fields,omitempty"`
	// Identities may use it: identities (exact, or a prefix ending in
	// "*"), bot:<name>, team:<name>. Nobody by default.
	Identities []string `yaml:"identities"`
	// Sites are where it may be filled, and which fields on each.
	Sites []BrowserProfileSite `yaml:"sites"`
	// Visible are the fields the agent may learn something about: exists
	// (that it is set), fact (a non-sensitive fact: an email's domain, an
	// address's country, a phone's country code) or value. Default:
	// hidden.
	Visible map[string]string `yaml:"visible,omitempty"`
}

// BrowserProfileSite opts one exact origin in for some of a profile's
// fields.
type BrowserProfileSite struct {
	Origin string   `yaml:"origin"`
	Fields []string `yaml:"fields"`
}

// BrowserCard is one payment card. Prefer a virtual or single-use card
// with its own provider limits: conductor's checks are the first layer, a
// limited card the second.
type BrowserCard struct {
	// Number / Expiry (MM/YY) / CVC / Name: where each value comes from.
	Number *BrowserSecretRef `yaml:"number"`
	Expiry *BrowserSecretRef `yaml:"expiry"`
	CVC    *BrowserSecretRef `yaml:"cvc,omitempty"`
	Name   *BrowserSecretRef `yaml:"name,omitempty"`
	// Kind documents the card: virtual, single-use or physical (a physical
	// card is a validation warning).
	Kind string `yaml:"kind,omitempty"`
	// BillingProfile fills the billing address from that profile's
	// billing.* fields.
	BillingProfile string `yaml:"billing_profile,omitempty"`
	// Merchants are the exact origins it may be filled on. None by
	// default: nothing can be bought until the operator adds one.
	Merchants []string `yaml:"merchants"`
	// Identities may propose purchases with it (as for a profile).
	Identities []string `yaml:"identities"`
	// Limits bound every purchase with the card; IdentityLimits each
	// identity's (bot:<name>, team:<name> or an identity) on top.
	Limits         CardLimits            `yaml:"limits"`
	IdentityLimits map[string]CardLimits `yaml:"identity_limits,omitempty"`
	// Frozen stops every fill at once (also `conductor browser card freeze`).
	Frozen bool `yaml:"frozen,omitempty"`
}

// CardLimits are a card's spend limits: per purchase, per day, per month
// (each a spend cap, the same rule a bot's commit_money uses).
type CardLimits struct {
	PerPurchase float64 `yaml:"per_purchase"`
	PerDay      float64 `yaml:"per_day"`
	PerMonth    float64 `yaml:"per_month"`
	Currency    string  `yaml:"currency,omitempty"` // default USD
}

// Caps are the limits as spend caps: a day and a 30-day month, each with
// the per-purchase maximum.
func (l CardLimits) Caps() []SpendCap {
	return []SpendCap{
		{PerAction: l.PerPurchase, PerPeriod: l.PerDay, Period: Duration(24 * time.Hour), Currency: l.Currency},
		{PerAction: l.PerPurchase, PerPeriod: l.PerMonth, Period: Duration(30 * 24 * time.Hour), Currency: l.Currency},
	}
}

// CurrencyOr is the limits' currency.
func (l CardLimits) CurrencyOr() string { return SpendCap{Currency: l.Currency}.CurrencyOr() }

// PurchaseLimits are a bot's or a team's purchase caps (per purchase, per
// day, per 30 days) and, optionally, tighter ones per card. They stack
// with the card's own limits and a ticket's spend_limit: every layer
// applies, and the lowest wins. A team's are ceilings for its bots.
type PurchaseLimits struct {
	CardLimits `yaml:",inline"`
	Cards      map[string]CardLimits `yaml:"cards,omitempty"`
}

// ForCard is the limits that apply with one card: the overall ones, and
// the card's own (tighter) ones when set — as two layers.
func (p *PurchaseLimits) ForCard(card string) []CardLimits {
	if p == nil {
		return nil
	}
	out := []CardLimits{p.CardLimits}
	if c, ok := p.Cards[card]; ok {
		out = append(out, c)
	}
	return out
}

func (p *PurchaseLimits) check(where string) error {
	if p == nil {
		return nil
	}
	if err := p.CardLimits.check(where); err != nil {
		return err
	}
	for card, l := range p.Cards {
		if err := l.check(where + ".cards." + card); err != nil {
			return err
		}
		if l.CurrencyOr() != p.CurrencyOr() {
			return fmt.Errorf("%s.cards.%s: currency %s differs from %s", where, card, l.CurrencyOr(), p.CurrencyOr())
		}
	}
	return nil
}

// narrows refuses a bot's limits past its team's (each cap, overall and
// per card): a bot can only narrow.
func (p *PurchaseLimits) narrows(team *PurchaseLimits, where, teamName string) error {
	if p == nil || team == nil {
		return nil
	}
	if p.CurrencyOr() != team.CurrencyOr() {
		return fmt.Errorf("%s currency %s differs from team %s's %s", where, p.CurrencyOr(), teamName, team.CurrencyOr())
	}
	fit := func(what string, b, t CardLimits) error {
		switch {
		case b.PerPurchase > t.PerPurchase:
			return fmt.Errorf("%s.per_purchase %g exceeds team %s's %g", what, b.PerPurchase, teamName, t.PerPurchase)
		case b.PerDay > t.PerDay:
			return fmt.Errorf("%s.per_day %g exceeds team %s's %g", what, b.PerDay, teamName, t.PerDay)
		case b.PerMonth > t.PerMonth:
			return fmt.Errorf("%s.per_month %g exceeds team %s's %g", what, b.PerMonth, teamName, t.PerMonth)
		}
		return nil
	}
	if err := fit(where, p.CardLimits, team.CardLimits); err != nil {
		return err
	}
	// A bot's per-card caps fit the team's overall ones and, when the team
	// has its own for that card, those.
	for _, card := range walletKeys(p.Cards) {
		l := p.Cards[card]
		if err := fit(where+".cards."+card, l, team.CardLimits); err != nil {
			return err
		}
		if tl, ok := team.Cards[card]; ok {
			if err := fit(where+".cards."+card, l, tl); err != nil {
				return err
			}
		}
	}
	return nil
}

// BrowserPayments are how purchases are approved.
type BrowserPayments struct {
	// Approvers are the operators (operators:, each with an ssh_key) who
	// may approve a purchase. Default: every configured operator.
	Approvers []string `yaml:"approvers,omitempty"`
	// ApprovalTTL is how long an approval may be used (default 1h); it is
	// single-use either way.
	ApprovalTTL Duration `yaml:"approval_ttl,omitempty"`
}

// DefaultPurchaseApprovalTTL is how long an approved purchase may be filled.
const DefaultPurchaseApprovalTTL = time.Hour

// ApprovalTTLOr is the effective approval lifetime.
func (p *BrowserPayments) ApprovalTTLOr() time.Duration {
	if p != nil && p.ApprovalTTL > 0 {
		return p.ApprovalTTL.D()
	}
	return DefaultPurchaseApprovalTTL
}

// ProfileFieldRE is a profile field's name: a known one, a postal or
// billing address part, or custom.<name>.
var ProfileFieldRE = regexp.MustCompile(`^(name|given_name|family_name|email|phone|company|birthday|(address|billing)\.(line1|line2|city|region|postal_code|country)|custom\.[a-z0-9_]{1,40})$`)

// Visibility levels.
var profileVisibility = map[string]bool{"hidden": true, "exists": true, "fact": true, "value": true}

// cardKinds are the documented kinds.
var cardKinds = map[string]bool{"": true, "virtual": true, "single-use": true, "physical": true}

// exactOrigin refuses anything but scheme://host[:port].
func exactOrigin(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || strings.Contains(u.Host, "*") ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q is not an exact origin (scheme://host[:port])", s)
	}
	return nil
}

func checkBindings(where string, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("%s names no identities — nothing is shared by default; bind identities, bot:<name> or team:<name>", where)
	}
	for _, id := range ids {
		if id == "" || id == "*" || strings.TrimSuffix(id, "*") == "" {
			return fmt.Errorf("%s: identity %q — name each identity (or a prefix ending in *)", where, id)
		}
	}
	return nil
}

func (l CardLimits) check(where string) error {
	switch {
	case l.PerPurchase <= 0 || l.PerDay <= 0 || l.PerMonth <= 0:
		return fmt.Errorf("%s: per_purchase, per_day and per_month are all required (a card with no limit is refused)", where)
	case l.PerPurchase > l.PerDay || l.PerDay > l.PerMonth:
		return fmt.Errorf("%s: per_purchase ≤ per_day ≤ per_month", where)
	case l.Currency != "" && !regexp.MustCompile(`^[A-Z]{3}$`).MatchString(l.Currency):
		return fmt.Errorf("%s: currency %q is not an ISO code (USD, EUR, …)", where, l.Currency)
	}
	return nil
}

func (c *Config) validateWallet() error {
	b := c.Browser
	if b == nil {
		return nil
	}
	for _, name := range walletKeys(b.Profiles) {
		p := b.Profiles[name]
		where := "config: browser.profiles." + name
		if !walletNameRE.MatchString(name) {
			return fmt.Errorf("%s: a name is letters, digits, - and _", where)
		}
		if err := checkBindings(where, p.Identities); err != nil {
			return err
		}
		for f, r := range p.Fields {
			if !ProfileFieldRE.MatchString(f) {
				return fmt.Errorf("%s: no field %q (name, given_name, family_name, email, phone, company, birthday, address.*, billing.*, custom.<name>)", where, f)
			}
			// Personal details are not typed into the config: a vault, the
			// environment, or the store's write-only values.
			if err := c.checkRef(where, f, r, true, false); err != nil {
				return err
			}
		}
		if len(p.Sites) == 0 {
			return fmt.Errorf("%s names no sites — a profile is filled only on the sites you opt in", where)
		}
		for i, s := range p.Sites {
			if err := exactOrigin(s.Origin); err != nil {
				return fmt.Errorf("%s.sites[%d]: %v", where, i, err)
			}
			if len(s.Fields) == 0 {
				return fmt.Errorf("%s.sites[%d] (%s) allows no field", where, i, s.Origin)
			}
			for _, f := range s.Fields {
				if !ProfileFieldRE.MatchString(f) && f != "address" && f != "billing" {
					return fmt.Errorf("%s.sites[%d]: no field %q", where, i, f)
				}
			}
			if !browserHostAllowed(b.Allow, originHost(s.Origin), originPort(s.Origin)) {
				return fmt.Errorf("%s.sites[%d]: %s is not reachable through browser.allow", where, i, s.Origin)
			}
		}
		for f, v := range p.Visible {
			if !ProfileFieldRE.MatchString(f) || !profileVisibility[v] {
				return fmt.Errorf("%s.visible: %s: %q — a field, and hidden, exists, fact or value", where, f, v)
			}
		}
	}
	for _, name := range walletKeys(b.Cards) {
		cd := b.Cards[name]
		where := "config: browser.cards." + name
		if !walletNameRE.MatchString(name) {
			return fmt.Errorf("%s: a name is letters, digits, - and _", where)
		}
		if err := checkBindings(where, cd.Identities); err != nil {
			return err
		}
		if cd.Number == nil || cd.Expiry == nil {
			return fmt.Errorf("%s: number and expiry are required (vault references)", where)
		}
		for f, r := range map[string]*BrowserSecretRef{"number": cd.Number, "expiry": cd.Expiry, "cvc": cd.CVC, "name": cd.Name} {
			if err := c.checkRef(where, f, r, true, false); err != nil {
				return err
			}
		}
		if !cardKinds[cd.Kind] {
			return fmt.Errorf("%s: kind %q is virtual, single-use or physical", where, cd.Kind)
		}
		if cd.BillingProfile != "" {
			if _, ok := b.Profiles[cd.BillingProfile]; !ok {
				return fmt.Errorf("%s: billing_profile %q is not in browser.profiles", where, cd.BillingProfile)
			}
		}
		for i, m := range cd.Merchants {
			if err := exactOrigin(m); err != nil {
				return fmt.Errorf("%s.merchants[%d]: %v", where, i, err)
			}
			if !browserHostAllowed(b.Allow, originHost(m), originPort(m)) {
				return fmt.Errorf("%s.merchants[%d]: %s is not reachable through browser.allow", where, i, m)
			}
		}
		if err := cd.Limits.check(where + ".limits"); err != nil {
			return err
		}
		for id, l := range cd.IdentityLimits {
			if err := l.check(where + ".identity_limits." + id); err != nil {
				return err
			}
			if l.CurrencyOr() != cd.Limits.CurrencyOr() {
				return fmt.Errorf("%s.identity_limits.%s: currency %s differs from the card's %s", where, id, l.CurrencyOr(), cd.Limits.CurrencyOr())
			}
		}
	}
	if p := b.Payments; p != nil {
		for _, op := range p.Approvers {
			if o, ok := c.Operators[op]; !ok || strings.TrimSpace(o.SSHKey) == "" {
				return fmt.Errorf("config: browser.payments.approvers: %q is not an operator with an ssh_key (operators:)", op)
			}
		}
	}
	return nil
}

// PaymentApprovers is who may approve a purchase: browser.payments.approvers,
// else every operator with a key. Empty: no purchase can be approved.
func (c *Config) PaymentApprovers() []string {
	if c.Browser != nil && c.Browser.Payments != nil && len(c.Browser.Payments.Approvers) > 0 {
		return c.Browser.Payments.Approvers
	}
	var out []string
	for name, o := range c.Operators {
		if strings.TrimSpace(o.SSHKey) != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// WalletNotes are validate's warnings about profiles and cards.
func (c *Config) WalletNotes() []string {
	if c.Browser == nil {
		return nil
	}
	var out []string
	for _, name := range walletKeys(c.Browser.Cards) {
		cd := c.Browser.Cards[name]
		if cd.Kind == "physical" || cd.Kind == "" {
			out = append(out, fmt.Sprintf("browser.cards.%s: prefer a virtual or single-use card with its own provider limits (kind: virtual) — conductor's checks are the first layer, a limited card the second", name))
		}
		if len(cd.Merchants) == 0 {
			out = append(out, fmt.Sprintf("browser.cards.%s: no merchant is allowed yet — nothing can be bought with it", name))
		}
	}
	if len(c.Browser.Cards) > 0 && len(c.PaymentApprovers()) == 0 {
		out = append(out, "browser.cards: no operator with an ssh_key (operators:) can approve a purchase — every purchase will wait")
	}
	return out
}

var walletNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

func walletKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func originHost(o string) string {
	u, _ := url.Parse(o)
	return u.Hostname()
}

func originPort(o string) string {
	u, _ := url.Parse(o)
	if p := u.Port(); p != "" {
		return p
	}
	return map[string]string{"https": "443", "http": "80"}[u.Scheme]
}
