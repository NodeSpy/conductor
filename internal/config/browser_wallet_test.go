package config

import (
	"strings"
	"testing"
)

func walletConfig() *Config {
	ref := func(k string) *BrowserSecretRef { return &BrowserSecretRef{Vault: "op", Key: k} }
	return &Config{Vaults: map[string]VaultRef{"op": {}},
		Operators: map[string]OperatorConfig{"daniel": {SSHKey: "ssh-ed25519 AAAA"}},
		Browser: &BrowserConfig{Allow: []string{"shop.example.com"},
			Profiles: map[string]BrowserProfile{"work": {Identities: []string{"bot:shopper"},
				Fields:  map[string]*BrowserSecretRef{"email": ref("Me/email"), "address.city": {Env: "CITY"}},
				Sites:   []BrowserProfileSite{{Origin: "https://shop.example.com", Fields: []string{"email", "address"}}},
				Visible: map[string]string{"email": "fact"}}},
			Cards: map[string]BrowserCard{"visa": {Number: ref("Card/number"), Expiry: ref("Card/expiry"), CVC: ref("Card/cvc"), Kind: "virtual",
				Merchants: []string{"https://shop.example.com"}, Identities: []string{"bot:shopper"},
				Limits: CardLimits{PerPurchase: 50, PerDay: 100, PerMonth: 300}}},
		}}
}

func TestValidateWallet(t *testing.T) {
	if err := walletConfig().validateBrowser(); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		mod  func(b *BrowserConfig)
		want string
	}{
		"profile typed": {func(b *BrowserConfig) {
			b.Profiles["work"].Fields["email"] = &BrowserSecretRef{Literal: "me@example.com"}
		}, "typed secret"},
		"profile nobody":    {func(b *BrowserConfig) { p := b.Profiles["work"]; p.Identities = nil; b.Profiles["work"] = p }, "names no identities"},
		"profile no site":   {func(b *BrowserConfig) { p := b.Profiles["work"]; p.Sites = nil; b.Profiles["work"] = p }, "names no sites"},
		"profile bad field": {func(b *BrowserConfig) { b.Profiles["work"].Fields["ssn"] = &BrowserSecretRef{Env: "X"} }, `no field "ssn"`},
		"profile site path": {func(b *BrowserConfig) {
			p := b.Profiles["work"]
			p.Sites[0].Origin = "https://shop.example.com/x"
			b.Profiles["work"] = p
		}, "exact origin"},
		"profile unreach": {func(b *BrowserConfig) {
			p := b.Profiles["work"]
			p.Sites[0].Origin = "https://other.example.com"
			b.Profiles["work"] = p
		}, "not reachable"},
		"profile visibility": {func(b *BrowserConfig) {
			p := b.Profiles["work"]
			p.Visible = map[string]string{"email": "always"}
			b.Profiles["work"] = p
		}, "hidden, exists, fact or value"},
		"card typed number": {func(b *BrowserConfig) {
			c := b.Cards["visa"]
			c.Number = &BrowserSecretRef{Literal: "4242424242424242"}
			b.Cards["visa"] = c
		}, "typed secret"},
		"card no expiry":    {func(b *BrowserConfig) { c := b.Cards["visa"]; c.Expiry = nil; b.Cards["visa"] = c }, "number and expiry"},
		"card no limits":    {func(b *BrowserConfig) { c := b.Cards["visa"]; c.Limits = CardLimits{}; b.Cards["visa"] = c }, "all required"},
		"card limits order": {func(b *BrowserConfig) { c := b.Cards["visa"]; c.Limits.PerDay = 10; b.Cards["visa"] = c }, "per_purchase ≤ per_day"},
		"card wild merchant": {func(b *BrowserConfig) {
			c := b.Cards["visa"]
			c.Merchants = []string{"https://*.example.com"}
			b.Cards["visa"] = c
		}, "exact origin"},
		"card everyone":   {func(b *BrowserConfig) { c := b.Cards["visa"]; c.Identities = []string{"*"}; b.Cards["visa"] = c }, "name each identity"},
		"card kind":       {func(b *BrowserConfig) { c := b.Cards["visa"]; c.Kind = "gold"; b.Cards["visa"] = c }, "virtual, single-use or physical"},
		"approver no key": {func(b *BrowserConfig) { b.Payments = &BrowserPayments{Approvers: []string{"nobody"}} }, "ssh_key"},
	} {
		cfg := walletConfig()
		c.mod(cfg.Browser)
		if err := cfg.validateBrowser(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
}

func TestWalletNotesAndApprovers(t *testing.T) {
	c := walletConfig()
	if got := c.PaymentApprovers(); len(got) != 1 || got[0] != "daniel" {
		t.Fatalf("approvers: %v", got)
	}
	cd := c.Browser.Cards["visa"]
	cd.Kind, cd.Merchants = "physical", nil
	c.Browser.Cards["visa"] = cd
	c.Operators = nil
	notes := strings.Join(c.WalletNotes(), "\n")
	for _, w := range []string{"prefer a virtual", "no merchant is allowed", "can approve a purchase"} {
		if !strings.Contains(notes, w) {
			t.Errorf("notes lack %q: %s", w, notes)
		}
	}
}

// A setting is plain config: never a profile's or card's source.
func TestWalletSecretSources(t *testing.T) {
	for body, want := range map[string]string{
		"browser:\n  profiles:\n    me:\n      fields: { email: \"${settings.mail}\" }\n": "plain config",
		"browser:\n  cards:\n    v: { number: \"${settings.card}\" }\n":                   "plain config",
		"browser:\n  cards:\n    v:\n      cvc: ${CVC}\n":                                 "${env.CVC}",
		"browser:\n  cards:\n    v: { number: \"${env.CARD}\" }\n":                        "",
	} {
		err := checkSecretSources("c.yaml", []byte(body))
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: %v", body, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: want %q, got %v", body, want, err)
		}
	}
}
