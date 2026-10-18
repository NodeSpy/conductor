package browser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

const testCard = "4242424242424242"

func testCardDef() Card {
	return Card{Name: "visa", Merchants: []string{"https://shop.example.com"}, Identities: []string{"agent:t@o/r", "bot:shopper"},
		Limits: config.CardLimits{PerPurchase: 30, PerDay: 50, PerMonth: 100, Currency: "USD"}}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func ledger(t *testing.T) (*Purchases, *clock) {
	ck := &clock{t: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)}
	return &Purchases{Dir: t.TempDir(), Now: ck.now}, ck
}

var caller = Principal{Identity: "agent:t@o/r", Requester: RequesterOperator}

func propose(t *testing.T, l *Purchases, total float64) Purchase {
	t.Helper()
	p, err := l.Propose(Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: total, Currency: "USD", Identity: caller.Identity}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// An approval is single-use: the first consume spends it, a second is
// refused; it expires unused; it must sign the purchase's own terms.
func TestPurchaseApprovalSingleUseAndExpiry(t *testing.T) {
	l, ck := ledger(t)
	card := testCardDef()
	p := propose(t, l, 12)
	if _, err := l.Consume(p.ID, caller, card); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("a pending purchase was spent: %v", err)
	}
	if _, err := l.Approve(p.ID, "another digest", "op", time.Minute, false); err == nil {
		t.Fatal("an approval for other terms approved it")
	}
	if _, err := l.Approve(p.ID, p.Digest, "op", time.Minute, false); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Approve(p.ID, p.Digest, "op", time.Minute, false); err == nil {
		t.Fatal("approved twice")
	}
	if _, err := l.Consume(p.ID, Principal{Identity: "agent:other@o/r"}, card); err == nil || !strings.Contains(err.Error(), "another session") {
		t.Fatalf("another session spent it: %v", err)
	}
	if _, err := l.Consume(p.ID, caller, card); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Consume(p.ID, caller, card); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("a second use: %v", err)
	}
	// Expiry: approved, then its minute passes.
	q := propose(t, l, 5)
	l.Approve(q.ID, q.Digest, "op", time.Minute, false)
	ck.t = ck.t.Add(time.Minute)
	if _, err := l.Consume(q.ID, caller, card); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("an expired approval: %v", err)
	}
	if got, _, _ := l.Get(q.ID); got.Status != PurchaseExpired {
		t.Fatalf("status %s", got.Status)
	}
	// A pending proposal lapses too, and cannot then be approved.
	r := propose(t, l, 5)
	ck.t = ck.t.Add(2 * time.Hour)
	if _, err := l.Approve(r.ID, r.Digest, "op", time.Minute, false); err == nil {
		t.Fatal("an expired proposal was approved")
	}
	// The approval just before the boundary still works.
	s := propose(t, l, 5)
	l.Approve(s.ID, s.Digest, "op", time.Minute, false)
	ck.t = ck.t.Add(time.Minute - time.Nanosecond)
	if _, err := l.Consume(s.ID, caller, card); err != nil {
		t.Fatalf("just inside the minute: %v", err)
	}
}

// The digest covers every term an approval stands for.
func TestPurchaseDigestCoversTerms(t *testing.T) {
	base := Purchase{ID: "pu1", Card: "visa", Merchant: "https://shop.example.com", Total: 12, Currency: "USD", Identity: "a", Items: []PurchaseItem{{Name: "w"}}}
	d := PurchaseDigest(base)
	for name, mod := range map[string]func(*Purchase){
		"card": func(p *Purchase) { p.Card = "other" }, "merchant": func(p *Purchase) { p.Merchant = "https://evil.example" },
		"total": func(p *Purchase) { p.Total = 12.01 }, "currency": func(p *Purchase) { p.Currency = "EUR" },
		"recurring": func(p *Purchase) { p.Recurring = true }, "identity": func(p *Purchase) { p.Identity = "b" },
		"items": func(p *Purchase) { p.Items = nil }, "bot": func(p *Purchase) { p.Bot = "x" }, "id": func(p *Purchase) { p.ID = "pu2" },
	} {
		q := base
		mod(&q)
		if PurchaseDigest(q) == d {
			t.Errorf("the digest does not cover %s", name)
		}
	}
}

// Caps: per purchase, per day and per month, at exactly their edges, the
// day's boundary, and an identity's limits on top of the card's.
func TestCardLimitsAndPeriodBoundary(t *testing.T) {
	l, ck := ledger(t)
	card := testCardDef()
	spend := func(amt float64) error {
		p := propose(t, l, amt)
		if _, err := l.Approve(p.ID, p.Digest, "op", time.Hour, false); err != nil {
			t.Fatal(err)
		}
		_, err := l.Consume(p.ID, caller, card)
		return err
	}
	if err := l.CheckLimits(card, caller, 30.01, "USD"); err == nil {
		t.Fatal("over per_purchase")
	}
	if err := l.CheckLimits(card, caller, 30, "USD"); err != nil {
		t.Fatalf("exactly per_purchase: %v", err)
	}
	if err := l.CheckLimits(card, caller, 10, "EUR"); err == nil {
		t.Fatal("another currency")
	}
	if err := spend(30); err != nil {
		t.Fatal(err)
	}
	if err := spend(20); err != nil {
		t.Fatalf("exactly the day's 50: %v", err)
	}
	if err := spend(0.01); err == nil || !strings.Contains(err.Error(), "24h") {
		t.Fatalf("over the day: %v", err)
	}
	// A refused consumption spent its approval.
	if ps, _ := l.List(PurchaseRefused); len(ps) != 1 {
		t.Fatalf("the refused purchase is recorded: %+v", ps)
	}
	ck.t = ck.t.Add(24*time.Hour - time.Second)
	if err := l.CheckLimits(card, caller, 1, "USD"); err == nil {
		t.Fatal("a second before the day ends, the day is still full")
	}
	ck.t = ck.t.Add(time.Second)
	if err := spend(30); err != nil {
		t.Fatalf("the next day: %v", err)
	}
	if err := spend(20); err != nil {
		t.Fatalf("100 in the month exactly: %v", err)
	}
	ck.t = ck.t.Add(48 * time.Hour)
	if err := spend(1); err == nil || !strings.Contains(err.Error(), "720h") {
		t.Fatalf("over the month: %v", err)
	}
	// An identity's limit: only its own spend counts against it.
	l2, _ := ledger(t)
	card.IdentityLimits = map[string]config.CardLimits{"bot:shopper": {PerPurchase: 5, PerDay: 5, PerMonth: 10, Currency: "USD"}}
	bot := Principal{Identity: "agent:b@o/r", Groups: []string{"bot:shopper"}, Bot: "shopper"}
	if err := l2.CheckLimits(card, bot, 6, "USD"); err == nil || !strings.Contains(err.Error(), "bot:shopper") {
		t.Fatalf("the bot's per-purchase: %v", err)
	}
	if err := l2.CheckLimits(card, caller, 6, "USD"); err != nil {
		t.Fatalf("someone else's limit applied: %v", err)
	}
}

func TestMerchantAllowlist(t *testing.T) {
	c := testCardDef()
	for m, want := range map[string]bool{
		"https://shop.example.com": true, "https://shop.example.com:443": true, "https://SHOP.example.com": true,
		"http://shop.example.com": false, "https://shop.example.com:8443": false, "https://evil.shop.example.com": false,
		"https://shop.example.com.evil": false, "": false,
	} {
		if got := c.merchantAllowed(m); got != want {
			t.Errorf("%q: %v", m, got)
		}
	}
	c.Merchants = nil
	if c.merchantAllowed("https://shop.example.com") {
		t.Fatal("no merchant by default")
	}
}

func TestParseMoneyAndCheckTotal(t *testing.T) {
	for s, want := range map[string][]money{
		"Order total: $24.50":       {{24.5, "USD"}},
		"Total: 1,234.00 EUR":       {{1234, "EUR"}},
		"Subtotal £3 — total £4.99": {{3, "GBP"}, {4.99, "GBP"}},
		"Total items: 3":            nil,
		"USD 12":                    {{12, "USD"}},
		"Amount due €0.99, 2 items": {{0.99, "EUR"}},
	} {
		if got := parseMoney(s); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %+v", s, got)
		}
	}
	p := Purchase{Total: 24.5, Currency: "USD"}
	if _, err := checkTotal([]string{"Subtotal: $20.00", "Order total: $24.50"}, p); err != nil {
		t.Fatal(err)
	}
	if seen, err := checkTotal([]string{"Order total: $24.51"}, p); err == nil || seen != 24.51 {
		t.Fatalf("a cent over: %v %v", seen, err)
	}
	if _, err := checkTotal([]string{"Order total: €24.00"}, p); err == nil || !strings.Contains(err.Error(), "EUR") {
		t.Fatalf("another currency: %v", err)
	}
	if _, err := checkTotal([]string{"Total items: 2"}, p); err == nil || !strings.Contains(err.Error(), "cannot find") {
		t.Fatalf("no total: %v", err)
	}
	if _, err := checkTotal(nil, p); err == nil {
		t.Fatal("an empty page")
	}
	// The largest labelled amount counts: a cheaper line can't hide a total.
	if _, err := checkTotal([]string{"Shipping total: $1.00", "Grand total: $99.00"}, p); err == nil {
		t.Fatal("the grand total was ignored")
	}
}

func TestCardValues(t *testing.T) {
	if !luhn(testCard) || luhn("4242424242424241") || luhn("1234") {
		t.Fatal("luhn")
	}
	for v, ok := range map[string]bool{"12/34": true, "01/2031": true, "13/30": false, "1/30": false, "12-30": false} {
		if _, _, got := parseExpiry(v); got != ok {
			t.Errorf("expiry %q: %v", v, got)
		}
	}
	if err := checkCardValue("number", "4242 4242 4242 4241"); err == nil {
		t.Fatal("a bad check digit")
	}
	if err := checkCardValue("cvc", "12a"); err == nil {
		t.Fatal("a bad cvc")
	}
	if got := cardNumberForms(testCard); !reflect.DeepEqual(got, []string{testCard, "4242 4242 4242 4242", "4242-4242-4242-4242"}) {
		t.Fatalf("%v", got)
	}
}

func TestProfileFactsAndSites(t *testing.T) {
	for f, want := range map[string]string{"email|jo@Example.COM": "domain example.com", "address.country|us": "country US",
		"phone|+44 20 7946 0000": "country code +44", "birthday|1990-01-02": "18 or older", "name|Jo Q": "set"} {
		k, v, _ := strings.Cut(f, "|")
		if got := profileFact(k, v); got != want {
			t.Errorf("%s: %q", f, got)
		}
	}
	p := Profile{Sites: []ProfileSite{{Origin: "https://shop.example.com", Fields: []string{"email", "address"}}}}
	for k, want := range map[string]bool{"email|https://shop.example.com": true, "address.city|https://shop.example.com": true,
		"billing.city|https://shop.example.com": false, "phone|https://shop.example.com": false, "email|https://evil.example.com": false,
		"email|http://shop.example.com": false} {
		f, o, _ := strings.Cut(k, "|")
		if got := p.allowed(f, o); got != want {
			t.Errorf("%s: %v", k, got)
		}
	}
}

// Wallet values are write-only like logins': no listing, record or file
// carries one, and removing a card or profile removes its values.
func TestWalletValuesAreWriteOnly(t *testing.T) {
	s := &LoginStore{Dir: filepath.Join(t.TempDir(), "browser")}
	if err := s.PutCard(CardRecord{Name: "visa", Identities: []string{"x"}, Limits: config.CardLimits{PerPurchase: 1, PerDay: 2, PerMonth: 3}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutProfile(ProfileRecord{Name: "me", Identities: []string{"x"}, Sites: []ProfileSite{{Origin: "https://a.example", Fields: []string{"email"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWalletValue("card", "visa", "number", "4242 4242 4242 4241"); err == nil {
		t.Fatal("a number failing Luhn was stored")
	}
	for _, f := range [][3]string{{"card", "number", testCard}, {"card", "expiry", "12/34"}, {"card", "cvc", "987"}, {"profile", "email", "jo.private@example.test"}} {
		name := map[string]string{"card": "visa", "profile": "me"}[f[0]]
		if err := s.SetWalletValue(f[0], name, f[1], f[2]); err != nil {
			t.Fatal(err)
		}
	}
	ps, cs, fr, _ := s.Wallet()
	b, _ := json.Marshal([]any{ps, cs, fr, WithWallet(Settings{}, ps, cs, fr)})
	hay := []string{string(b)}
	filepath.Walk(s.Dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			x, _ := os.ReadFile(p)
			hay = append(hay, string(x))
		}
		return nil
	})
	for _, h := range hay {
		for _, v := range []string{testCard, "12/34", "987", "jo.private@example.test"} {
			if strings.Contains(h, v) {
				t.Fatalf("a wallet value is readable (%s): %.200s", v, h)
			}
		}
	}
	if v, err := s.walletValue("card", "visa", "number"); err != nil || v != testCard {
		t.Fatalf("the fill reads it: %v", err)
	}
	if ok, err := s.RemoveWallet("card", "visa"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := s.walletValue("card", "visa", "number"); err == nil {
		t.Fatal("a removed card's number survived")
	}
	// Freezing works for any card name, config's included.
	s.Freeze("config-card", true)
	_, _, fr, _ = s.Wallet()
	st := WithWallet(Settings{Cards: map[string]Card{"config-card": {Name: "config-card"}}}, nil, nil, fr)
	if !st.Cards["config-card"].Frozen {
		t.Fatal("a freeze did not reach a config card")
	}
}

func TestWalletRecordChecks(t *testing.T) {
	good := CardRecord{Name: "visa", Identities: []string{"x"}, Merchants: []string{"https://shop.example.com"}, Limits: config.CardLimits{PerPurchase: 1, PerDay: 2, PerMonth: 3}}
	for name, mod := range map[string]func(*CardRecord){
		"no limit":      func(c *CardRecord) { c.Limits = config.CardLimits{} },
		"day < buy":     func(c *CardRecord) { c.Limits.PerDay = 0.5 },
		"nobody":        func(c *CardRecord) { c.Identities = nil },
		"everyone":      func(c *CardRecord) { c.Identities = []string{"*"} },
		"merchant path": func(c *CardRecord) { c.Merchants = []string{"https://shop.example.com/cart"} },
		"wild merchant": func(c *CardRecord) { c.Merchants = []string{"https://*.example.com"} },
		"bad kind":      func(c *CardRecord) { c.Kind = "gold" },
		"id limit cur": func(c *CardRecord) {
			c.IdentityLimits = map[string]config.CardLimits{"bot:b": {PerPurchase: 1, PerDay: 1, PerMonth: 1, Currency: "EUR"}}
		},
	} {
		c := good
		mod(&c)
		if err := checkCardRecord(&c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := checkCardRecord(&good); err != nil {
		t.Fatal(err)
	}
	p := ProfileRecord{Name: "me", Identities: []string{"x"}, Sites: []ProfileSite{{Origin: "https://a.example", Fields: []string{"email"}}}}
	for name, mod := range map[string]func(*ProfileRecord){
		"no site":   func(p *ProfileRecord) { p.Sites = nil },
		"no fields": func(p *ProfileRecord) { p.Sites = []ProfileSite{{Origin: "https://a.example"}} },
		"bad field": func(p *ProfileRecord) {
			p.Sites = []ProfileSite{{Origin: "https://a.example", Fields: []string{"ssn"}}}
		},
		"bad visible": func(p *ProfileRecord) { p.Visible = map[string]string{"email": "always"} },
	} {
		q := p
		mod(&q)
		if err := checkProfileRecord(&q); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A bot's purchase goes to its gate: a proposal stays pending (approved
// through the bot's proposal, never the browser's path), an unattended
// verdict approves it at once — unless it is recurring — and a refusal
// records the purchase refused.
func TestProposePurchaseBotPath(t *testing.T) {
	card := testCardDef()
	st := Settings{Allow: []string{"shop.example.com"}, Cards: map[string]Card{"visa": card}, PurchaseTTL: time.Minute}
	var verdict struct {
		proposal   string
		unattended bool
		err        error
	}
	m := &Manager{Settings: func() Settings { return st }, Purchases: &Purchases{Dir: t.TempDir()},
		BotPurchase: func(ctx context.Context, c Principal, p Purchase) (string, bool, error) {
			return verdict.proposal, verdict.unattended, verdict.err
		}}
	bot := Principal{Identity: "agent:b@o/r", Bot: "shopper", Groups: []string{"bot:shopper"}, Requester: "bot"}
	args := func(recurring bool) map[string]any {
		return map[string]any{"card": "visa", "merchant": "https://shop.example.com", "total": "5", "recurring": recurring, "interval": "month"}
	}
	verdict.proposal = "p7"
	p, err := m.proposePurchase(context.Background(), bot, st, args(false), map[string]any{})
	if err != nil || p.Status != PurchasePending || p.BotProposal != "p7" {
		t.Fatalf("proposal: %+v %v", p, err)
	}
	if _, err := m.ApprovePurchase(p.ID, p.Digest, "daniel"); err == nil || !strings.Contains(err.Error(), "bot approve") {
		t.Fatalf("a bot's purchase approved on the browser's path: %v", err)
	}
	if _, err := m.ApproveBotPurchase(p.ID, "shopper", "p8"); err == nil {
		t.Fatal("another proposal approved it")
	}
	if q, err := m.ApproveBotPurchase(p.ID, "shopper", "p7"); err != nil || q.Status != PurchaseApproved {
		t.Fatalf("its proposal approves it: %+v %v", q, err)
	}
	verdict.proposal, verdict.unattended = "", true
	u, err := m.proposePurchase(context.Background(), bot, st, args(false), map[string]any{})
	if err != nil || u.Status != PurchaseApproved || !u.Unattended {
		t.Fatalf("unattended: %+v %v", u, err)
	}
	r, err := m.proposePurchase(context.Background(), bot, st, args(true), map[string]any{})
	if err != nil || r.Status != PurchasePending {
		t.Fatalf("a recurring purchase ran unattended: %+v %v", r, err)
	}
	verdict.unattended, verdict.err = false, errors.New("over the bot's spend cap")
	if _, err := m.proposePurchase(context.Background(), bot, st, args(false), map[string]any{}); err == nil {
		t.Fatal("the gate's refusal")
	}
	if ps, _ := m.Purchases.List(PurchaseRefused); len(ps) != 1 {
		t.Fatalf("the refused purchase is recorded: %+v", ps)
	}
	// An operator's approval needs the card as it stands now.
	h, _ := m.Purchases.Propose(Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 5, Currency: "USD", Identity: "agent:t@o/r"}, time.Hour)
	card.Frozen = true
	st.Cards["visa"] = card
	if _, err := m.ApprovePurchase(h.ID, h.Digest, "daniel"); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("approved on a frozen card: %v", err)
	}
}
