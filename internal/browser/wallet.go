package browser

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// The wallet: personal details (profiles) and payment cards conductor fills
// for an agent, beside its logins. The agent never receives a value — it
// asks conductor to fill a field by its meaning, on a site the operator
// opted the profile in for (and only the fields allowed there), and
// conductor types the value into the page from the isolated world, as it
// fills a password. A card is filled only for an approved purchase
// (purchases.go), only on that purchase's merchant, after conductor re-reads
// the total on the page.

// Profile is one named set of personal details.
type Profile struct {
	Name   string
	Fields map[string]*SecretRef
	// Identities may use it: identities (exact, prefix*), bot:, team:.
	Identities []string
	Sites      []ProfileSite
	// Visible: field → exists | fact | value (default hidden).
	Visible map[string]string
	// Method is config, or stored (added at runtime).
	Method string
}

// ProfileSite allows some fields on one exact origin.
type ProfileSite struct {
	Origin string   `json:"origin"`
	Fields []string `json:"fields"`
}

// For reports whether the profile is bound to the caller.
func (p Profile) For(c Principal) bool { return LoginSite{Identities: p.Identities}.ForPrincipal(c) }

// allowed reports whether field may be filled on the page's origin.
func (p Profile) allowed(field, securityOrigin string) bool {
	so, err := parseOrigin(securityOrigin)
	if err != nil {
		return false
	}
	for _, s := range p.Sites {
		o, err := parseOrigin(s.Origin)
		if err != nil || o.scheme != so.scheme || o.host != so.host || o.port != so.port {
			continue
		}
		for _, f := range s.Fields {
			// "address" / "billing" allow every part of that address.
			if f == field || strings.HasPrefix(field, f+".") {
				return true
			}
		}
	}
	return false
}

// Card is one payment card.
type Card struct {
	Name                            string
	Number, Expiry, CVC, HolderName *SecretRef
	Kind                            string
	// Merchants are the exact origins it may be filled on (none: nothing).
	Merchants      []string
	Identities     []string
	Limits         config.CardLimits
	IdentityLimits map[string]config.CardLimits
	Frozen         bool
	Method         string
}

// For reports whether the card is bound to the caller.
func (c Card) For(p Principal) bool { return LoginSite{Identities: c.Identities}.ForPrincipal(p) }

// merchantAllowed reports whether an exact origin is on the card's list.
func (c Card) merchantAllowed(m string) bool {
	mo, err := parseOrigin(m)
	if err != nil {
		return false
	}
	for _, x := range c.Merchants {
		o, err := parseOrigin(x)
		if err == nil && o.scheme == mo.scheme && o.host == mo.host && o.port == mo.port {
			return true
		}
	}
	return false
}

// limitKeys are the identity_limits entries that apply to a caller: its
// identity (exact or a prefix ending in *) and its bots and teams.
func (c Card) limitKeys(p Principal) []string {
	var out []string
	for k := range c.IdentityLimits {
		if (LoginSite{Identities: []string{k}}).ForPrincipal(p) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Profile field names (config.ProfileFieldRE) and what a form's autocomplete
// tokens mean (the HTML standard's names; billing/shipping sections).
var profileAutocomplete = map[string]string{
	"name": "name", "given-name": "given_name", "family-name": "family_name", "email": "email", "tel": "phone",
	"organization": "company", "bday": "birthday",
	"address-line1": "address.line1", "street-address": "address.line1", "address-line2": "address.line2",
	"address-level2": "address.city", "address-level1": "address.region", "postal-code": "address.postal_code",
	"country": "address.country", "country-name": "address.country",
}

// ---------------------------------------------------------------------------
// The runtime wallet: profiles and cards the operator adds (the dashboard,
// `conductor browser profile|card …`), their values write-only in the login
// store's sealed vault, and freezes (config cards included).
// ---------------------------------------------------------------------------

// ProfileRecord is a stored profile (sources, never values).
type ProfileRecord struct {
	Name       string                 `json:"name"`
	Fields     map[string]FieldSource `json:"fields,omitempty"`
	Identities []string               `json:"identities"`
	Sites      []ProfileSite          `json:"sites"`
	Visible    map[string]string      `json:"visible,omitempty"`
	Added      time.Time              `json:"added"`
	Via        string                 `json:"via,omitempty"`
	Updated    time.Time              `json:"updated"`
}

// CardRecord is a stored card (sources, never values).
type CardRecord struct {
	Name           string                       `json:"name"`
	Fields         map[string]FieldSource       `json:"fields,omitempty"`
	Kind           string                       `json:"kind,omitempty"`
	Merchants      []string                     `json:"merchants"`
	Identities     []string                     `json:"identities"`
	Limits         config.CardLimits            `json:"limits"`
	IdentityLimits map[string]config.CardLimits `json:"identity_limits,omitempty"`
	Added          time.Time                    `json:"added"`
	Via            string                       `json:"via,omitempty"`
	Updated        time.Time                    `json:"updated"`
}

// walletFile is <state>/browser/wallet.json.
type walletFile struct {
	Profiles map[string]*ProfileRecord `json:"profiles,omitempty"`
	Cards    map[string]*CardRecord    `json:"cards,omitempty"`
	// Frozen are cards no fill may use (a config card too), since when.
	Frozen map[string]time.Time `json:"frozen,omitempty"`
}

var cardFields = map[string]bool{"number": true, "expiry": true, "cvc": true, "name": true}

func (s *LoginStore) walletPath() string { return filepath.Join(s.Dir, "wallet.json") }

func (s *LoginStore) loadWalletLocked() (*walletFile, error) {
	w := &walletFile{Profiles: map[string]*ProfileRecord{}, Cards: map[string]*CardRecord{}, Frozen: map[string]time.Time{}}
	b, err := os.ReadFile(s.walletPath())
	if os.IsNotExist(err) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, w); err != nil {
		return nil, fmt.Errorf("browser: %s: %w", s.walletPath(), err)
	}
	if w.Profiles == nil {
		w.Profiles = map[string]*ProfileRecord{}
	}
	if w.Cards == nil {
		w.Cards = map[string]*CardRecord{}
	}
	if w.Frozen == nil {
		w.Frozen = map[string]time.Time{}
	}
	return w, nil
}

func (s *LoginStore) saveWalletLocked(w *walletFile) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.walletPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.walletPath())
}

// Wallet returns the stored profiles, cards and freezes.
func (s *LoginStore) Wallet() ([]ProfileRecord, []CardRecord, map[string]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.loadWalletLocked()
	if err != nil {
		return nil, nil, nil, err
	}
	var ps []ProfileRecord
	for _, p := range w.Profiles {
		ps = append(ps, *p)
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Name < ps[j].Name })
	var cs []CardRecord
	for _, c := range w.Cards {
		cs = append(cs, *c)
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	fr := map[string]time.Time{}
	for k, v := range w.Frozen {
		fr[k] = v
	}
	return ps, cs, fr, nil
}

var walletNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

// checkProfileRecord validates a stored profile.
func checkProfileRecord(p *ProfileRecord) error {
	if !walletNameRE.MatchString(p.Name) {
		return fmt.Errorf("browser: a profile's name is letters, digits, - and _")
	}
	if len(p.Identities) == 0 {
		return fmt.Errorf("browser: profile %s is bound to nobody — bind identities, bot:<name> or team:<name>", p.Name)
	}
	for _, b := range p.Identities {
		if err := CheckBinding(b); err != nil {
			return err
		}
	}
	if len(p.Sites) == 0 {
		return fmt.Errorf("browser: profile %s names no site — a profile is filled only on the sites you opt in, with the fields you allow there", p.Name)
	}
	for _, st := range p.Sites {
		if err := exactOrigin(st.Origin); err != nil {
			return fmt.Errorf("browser: profile %s: %v", p.Name, err)
		}
		if len(st.Fields) == 0 {
			return fmt.Errorf("browser: profile %s: %s allows no field", p.Name, st.Origin)
		}
		for _, f := range st.Fields {
			if !config.ProfileFieldRE.MatchString(f) && f != "address" && f != "billing" {
				return fmt.Errorf("browser: profile %s: no field %q", p.Name, f)
			}
		}
	}
	for f := range p.Fields {
		if !config.ProfileFieldRE.MatchString(f) {
			return fmt.Errorf("browser: profile %s: no field %q", p.Name, f)
		}
	}
	for f, v := range p.Visible {
		if !config.ProfileFieldRE.MatchString(f) || (v != "hidden" && v != "exists" && v != "fact" && v != "value") {
			return fmt.Errorf("browser: profile %s: visible.%s %q — hidden, exists, fact or value", p.Name, f, v)
		}
	}
	return nil
}

// checkCardRecord validates a stored card.
func checkCardRecord(c *CardRecord) error {
	if !walletNameRE.MatchString(c.Name) {
		return fmt.Errorf("browser: a card's name is letters, digits, - and _")
	}
	if len(c.Identities) == 0 {
		return fmt.Errorf("browser: card %s is bound to nobody — bind identities, bot:<name> or team:<name>", c.Name)
	}
	for _, b := range c.Identities {
		if err := CheckBinding(b); err != nil {
			return err
		}
	}
	for _, m := range c.Merchants {
		if err := exactOrigin(m); err != nil {
			return fmt.Errorf("browser: card %s: merchant %v", c.Name, err)
		}
	}
	for f := range c.Fields {
		if !cardFields[f] {
			return fmt.Errorf("browser: card %s: no field %q (number, expiry, cvc, name)", c.Name, f)
		}
	}
	if c.Kind != "" && c.Kind != "virtual" && c.Kind != "single-use" && c.Kind != "physical" {
		return fmt.Errorf("browser: card %s: kind %q is virtual, single-use or physical", c.Name, c.Kind)
	}
	l := c.Limits
	if l.PerPurchase <= 0 || l.PerDay <= 0 || l.PerMonth <= 0 || l.PerPurchase > l.PerDay || l.PerDay > l.PerMonth {
		return fmt.Errorf("browser: card %s: limits need per_purchase ≤ per_day ≤ per_month, all > 0 (a card with no limit is refused)", c.Name)
	}
	for k, il := range c.IdentityLimits {
		if err := CheckBinding(k); err != nil {
			return err
		}
		if il.PerPurchase <= 0 || il.PerDay <= 0 || il.PerMonth <= 0 || il.CurrencyOr() != l.CurrencyOr() {
			return fmt.Errorf("browser: card %s: identity_limits.%s needs all three limits, in %s", c.Name, k, l.CurrencyOr())
		}
	}
	return nil
}

// exactOrigin refuses anything but scheme://host[:port].
func exactOrigin(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || strings.Contains(u.Host, "*") ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("%q is not an exact origin (scheme://host[:port])", s)
	}
	return nil
}

// PutProfile adds or replaces a stored profile (its values set separately).
func (s *LoginStore) PutProfile(p ProfileRecord) error {
	if err := checkProfileRecord(&p); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.loadWalletLocked()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if old, ok := w.Profiles[p.Name]; ok {
		p.Added, p.Via = old.Added, old.Via
		if p.Fields == nil {
			p.Fields = old.Fields
		}
	} else {
		p.Added = now
	}
	p.Updated = now
	w.Profiles[p.Name] = &p
	return s.saveWalletLocked(w)
}

// PutCard adds or replaces a stored card (its values set separately).
func (s *LoginStore) PutCard(c CardRecord) error {
	if err := checkCardRecord(&c); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.loadWalletLocked()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if old, ok := w.Cards[c.Name]; ok {
		c.Added, c.Via = old.Added, old.Via
		if c.Fields == nil {
			c.Fields = old.Fields
		}
	} else {
		c.Added = now
	}
	c.Updated = now
	w.Cards[c.Name] = &c
	return s.saveWalletLocked(w)
}

// walletKey is where a stored wallet value lives in the vault.
func walletKey(kind, name, field string) string { return kind + ":" + name + "/" + field }

// SetWalletValue stores a typed value write-only (kind profile or card).
func (s *LoginStore) SetWalletValue(kind, name, field, value string) error {
	if value == "" {
		return fmt.Errorf("browser: an empty %s — remove the field instead", field)
	}
	if kind == "card" {
		if err := checkCardValue(field, value); err != nil {
			return err
		}
	}
	return s.setWalletField(kind, name, field, FieldSource{Kind: "stored"}, value)
}

// SetWalletRef points one field at a reference (any source but a value).
func (s *LoginStore) SetWalletRef(kind, name, field, ref string) error {
	src, err := secrets.ParseSource(ref)
	if err != nil {
		return fmt.Errorf("browser: %s: %w", field, err)
	}
	if !src.IsRef() {
		return fmt.Errorf("browser: %s: not a reference — a typed value is stored write-only (set the value instead)", field)
	}
	return s.setWalletField(kind, name, field, FieldSource{Kind: string(src.Kind), Ref: src.String()}, "")
}

func (s *LoginStore) setWalletField(kind, name, field string, fs FieldSource, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.loadWalletLocked()
	if err != nil {
		return err
	}
	var fields *map[string]FieldSource
	switch kind {
	case "profile":
		p, ok := w.Profiles[name]
		if !ok {
			return fmt.Errorf("browser: no stored profile %q", name)
		}
		if !config.ProfileFieldRE.MatchString(field) {
			return fmt.Errorf("browser: no profile field %q", field)
		}
		fields = &p.Fields
	case "card":
		c, ok := w.Cards[name]
		if !ok {
			return fmt.Errorf("browser: no stored card %q", name)
		}
		if !cardFields[field] {
			return fmt.Errorf("browser: no card field %q (number, expiry, cvc, name)", field)
		}
		fields = &c.Fields
	default:
		return fmt.Errorf("browser: no wallet kind %q", kind)
	}
	v, err := s.openValuesLocked()
	if err != nil {
		return err
	}
	key := walletKey(kind, name, field)
	if fs.Kind == "stored" {
		if err := v.Set(key, value); err != nil {
			return err
		}
	} else {
		v.Delete(key)
	}
	if err := v.Save(); err != nil {
		return err
	}
	if *fields == nil {
		*fields = map[string]FieldSource{}
	}
	fs.Set = time.Now().UTC()
	(*fields)[field] = fs
	return s.saveWalletLocked(w)
}

// ClearWalletField removes one field (and its stored value).
func (s *LoginStore) ClearWalletField(kind, name, field string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.loadWalletLocked()
	if err != nil {
		return err
	}
	switch kind {
	case "profile":
		if p, ok := w.Profiles[name]; ok {
			delete(p.Fields, field)
		}
	case "card":
		if c, ok := w.Cards[name]; ok {
			delete(c.Fields, field)
		}
	}
	v, err := s.openValuesLocked()
	if err != nil {
		return err
	}
	if v.Delete(walletKey(kind, name, field)) {
		if err := v.Save(); err != nil {
			return err
		}
	}
	return s.saveWalletLocked(w)
}

// RemoveWallet deletes a stored profile or card and every value it stored.
func (s *LoginStore) RemoveWallet(kind, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.loadWalletLocked()
	if err != nil {
		return false, err
	}
	var fields []string
	switch kind {
	case "profile":
		p, ok := w.Profiles[name]
		if !ok {
			return false, nil
		}
		for f := range p.Fields {
			fields = append(fields, f)
		}
		delete(w.Profiles, name)
	case "card":
		if _, ok := w.Cards[name]; !ok {
			return false, nil
		}
		for f := range cardFields {
			fields = append(fields, f)
		}
		delete(w.Cards, name)
	default:
		return false, fmt.Errorf("browser: no wallet kind %q", kind)
	}
	if err := s.saveWalletLocked(w); err != nil {
		return false, err
	}
	v, err := s.openValuesLocked()
	if err != nil {
		return true, err
	}
	changed := false
	for _, f := range fields {
		if v.Delete(walletKey(kind, name, f)) {
			changed = true
		}
	}
	if changed {
		return true, v.Save()
	}
	return true, nil
}

// Freeze stops (or, false, restarts) every fill of a card, config or
// stored, at once.
func (s *LoginStore) Freeze(card string, frozen bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, err := s.loadWalletLocked()
	if err != nil {
		return err
	}
	if frozen {
		w.Frozen[card] = time.Now().UTC()
	} else {
		delete(w.Frozen, card)
	}
	return s.saveWalletLocked(w)
}

// walletValue reads a stored wallet value for a fill (unexported, like the
// login values: nothing reads one back but a fill).
func (s *LoginStore) walletValue(kind, name, field string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.openValuesLocked()
	if err != nil {
		return "", err
	}
	val, err := v.Get(walletKey(kind, name, field))
	if err != nil {
		return "", fmt.Errorf("browser: %s %s has no stored %s", kind, name, field)
	}
	return val, nil
}

// checkCardValue refuses a card value that cannot be one (a typo would only
// be found at checkout): the number passes Luhn, the expiry is MM/YY.
func checkCardValue(field, v string) error {
	switch field {
	case "number":
		if !luhn(digitsOf(v)) {
			return fmt.Errorf("browser: the card number does not pass its check digit")
		}
	case "expiry":
		if _, _, ok := parseExpiry(v); !ok {
			return fmt.Errorf("browser: the expiry is MM/YY (or MM/YYYY)")
		}
	case "cvc":
		if d := digitsOf(v); len(d) < 3 || len(d) > 4 || len(d) != len(strings.TrimSpace(v)) {
			return fmt.Errorf("browser: the CVC is 3 or 4 digits")
		}
	}
	return nil
}

func digitsOf(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func luhn(d string) bool {
	if len(d) < 12 || len(d) > 19 {
		return false
	}
	sum, dbl := 0, false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if dbl {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		dbl = !dbl
	}
	return sum%10 == 0
}

// parseExpiry reads MM/YY or MM/YYYY.
func parseExpiry(v string) (mm, yyyy string, ok bool) {
	m := regexp.MustCompile(`^\s*(0[1-9]|1[0-2])\s*/\s*(\d{2}|\d{4})\s*$`).FindStringSubmatch(v)
	if m == nil {
		return "", "", false
	}
	y := m[2]
	if len(y) == 2 {
		y = "20" + y
	}
	return m[1], y, true
}

// WithWallet adds the stored profiles and cards (and the freezes) to st. A
// name the config already has stays the config's; a freeze applies to
// either.
func WithWallet(st Settings, ps []ProfileRecord, cs []CardRecord, frozen map[string]time.Time) Settings {
	prof := map[string]Profile{}
	for k, v := range st.Profiles {
		prof[k] = v
	}
	for _, r := range ps {
		if _, ok := prof[r.Name]; ok {
			continue
		}
		p := Profile{Name: r.Name, Identities: r.Identities, Sites: r.Sites, Visible: r.Visible, Method: "stored", Fields: map[string]*SecretRef{}}
		for f, fs := range r.Fields {
			if ref := fieldRef("profile", r.Name, f, fs); ref != nil {
				p.Fields[f] = ref
			}
		}
		prof[r.Name] = p
	}
	cards := map[string]Card{}
	for k, v := range st.Cards {
		cards[k] = v
	}
	for _, r := range cs {
		if _, ok := cards[r.Name]; ok {
			continue
		}
		c := Card{Name: r.Name, Kind: r.Kind, Merchants: r.Merchants, Identities: r.Identities, Limits: r.Limits, IdentityLimits: r.IdentityLimits, Method: "stored"}
		for f, fs := range r.Fields {
			ref := fieldRef("card", r.Name, f, fs)
			switch f {
			case "number":
				c.Number = ref
			case "expiry":
				c.Expiry = ref
			case "cvc":
				c.CVC = ref
			case "name":
				c.HolderName = ref
			}
		}
		cards[r.Name] = c
	}
	for name := range frozen {
		if c, ok := cards[name]; ok {
			c.Frozen = true
			cards[name] = c
		}
	}
	st.Profiles, st.Cards = prof, cards
	return st
}

// fieldRef is a stored field's source as a SecretRef.
func fieldRef(kind, name, field string, fs FieldSource) *SecretRef {
	if fs.Kind == "stored" {
		return &SecretRef{Stored: walletKey(kind, name, field)}
	}
	src, err := secrets.ParseSource(fs.Ref)
	if err != nil || !src.IsRef() {
		return nil
	}
	return RefFromSource(src)
}
