package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/skill"
	"github.com/NodeSpy/conductor/internal/store"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// Personal details and payment cards (internal/browser/wallet.go): the
// operator's ops on the control socket — the dashboard's "Personal
// details", "Payment cards", purchase approvals (Needs you) and spend —
// each also a `conductor browser profile|card|purchases|purchase|spend …`
// command. No answer carries a value.
//
//	browser-profile-add|set|unset|bind|rm   a profile: fields (typed write-only, or any reference), who, sites and their fields, visibility
//	browser-card-add|set|unset|bind|rm      a card: values, who, merchants, limits
//	browser-card-freeze|unfreeze            the kill switch (config cards too)
//	browser-wallet                          profiles and cards, sources only; spend per card and identity
//	browser-purchases                       purchases (status filter): pending ones are Needs you
//	browser-purchase                        one purchase, with its digest (what an approval signs)
//	browser-purchase-decide                 approve or deny, SSH-signed by an approver (browser.payments.approvers)
//
// Widening (a new profile or card, a binding, a site or field, a merchant, a
// higher limit, an unfreeze) answers change: "widening" — the dashboard's
// passkey gate — and dry_run changes nothing.

var browserWalletOps = map[string]bool{
	"browser-profile-add": true, "browser-profile-set": true, "browser-profile-unset": true, "browser-profile-bind": true, "browser-profile-rm": true,
	"browser-card-add": true, "browser-card-set": true, "browser-card-unset": true, "browser-card-bind": true, "browser-card-rm": true,
	"browser-card-freeze": true, "browser-card-unfreeze": true,
	"browser-wallet": true, "browser-purchases": true, "browser-purchase": true, "browser-purchase-decide": true,
	"browser-case-budget": true,
}

// browserWalletReq is a wallet command's body.
type browserWalletReq struct {
	Name       string                       `json:"name,omitempty"`
	Field      string                       `json:"field,omitempty"`
	Values     map[string]string            `json:"values,omitempty"`
	Refs       map[string]string            `json:"refs,omitempty"`
	Identities []string                     `json:"identities,omitempty"`
	Sites      []browser.ProfileSite        `json:"sites,omitempty"`
	Visible    map[string]string            `json:"visible,omitempty"`
	Kind       string                       `json:"kind,omitempty"`
	Merchants  []string                     `json:"merchants,omitempty"`
	Limits     *config.CardLimits           `json:"limits,omitempty"`
	IDLimits   map[string]config.CardLimits `json:"identity_limits,omitempty"`
	Bind       []string                     `json:"bind,omitempty"`
	Unbind     []string                     `json:"unbind,omitempty"`
	AddSites   []browser.ProfileSite        `json:"add_sites,omitempty"`
	RmSites    []string                     `json:"remove_sites,omitempty"`
	AddMerch   []string                     `json:"add_merchants,omitempty"`
	RmMerch    []string                     `json:"remove_merchants,omitempty"`
	Purchase   string                       `json:"purchase,omitempty"`
	Status     string                       `json:"status,omitempty"`
	Decision   string                       `json:"decision,omitempty"`
	Operator   string                       `json:"operator,omitempty"`
	Signature  string                       `json:"signature,omitempty"`
	Reason     string                       `json:"reason,omitempty"`
	DryRun     bool                         `json:"dry_run,omitempty"`
	Via        string                       `json:"via,omitempty"`
	// browser-case-budget: a bot's case and its spend_limit (Clear drops
	// it; Show only reads).
	Bot       string   `json:"bot,omitempty"`
	Case      string   `json:"case,omitempty"`
	Amount    string   `json:"amount,omitempty"`
	Currency  string   `json:"currency,omitempty"`
	Card      string   `json:"card,omitempty"`
	BMerchant []string `json:"budget_merchants,omitempty"`
	Clear     bool     `json:"clear,omitempty"`
	Show      bool     `json:"show,omitempty"`
}

// walletSettings converts the config's profiles and cards.
func walletSettings(cfg *config.Config, st *browser.Settings) {
	b := cfg.Browser
	if b == nil {
		return
	}
	ref := func(r *config.BrowserSecretRef) *browser.SecretRef {
		if r == nil {
			return nil
		}
		return &browser.SecretRef{Vault: r.Vault, Key: r.Key, Named: r.Named, Env: r.Env, Literal: r.Literal}
	}
	st.PurchaseTTL = b.Payments.ApprovalTTLOr()
	for name, bc := range cfg.Bots {
		if st.BotTeam == nil {
			st.BotTeam, st.BotPurchases = map[string]string{}, map[string]*config.PurchaseLimits{}
		}
		st.BotTeam[name] = string(bc.Team)
		if bc.Purchases != nil {
			st.BotPurchases[name] = bc.Purchases
		}
	}
	for name, tc := range cfg.Teams {
		if tc.Budget.Purchases != nil {
			if st.TeamPurchases == nil {
				st.TeamPurchases = map[string]*config.PurchaseLimits{}
			}
			st.TeamPurchases[name] = tc.Budget.Purchases
		}
	}
	if len(b.Profiles) > 0 {
		st.Profiles = map[string]browser.Profile{}
	}
	for name, p := range b.Profiles {
		bp := browser.Profile{Name: name, Identities: p.Identities, Visible: p.Visible, Method: browser.MethodConfig, Fields: map[string]*browser.SecretRef{}}
		for f, r := range p.Fields {
			bp.Fields[f] = ref(r)
		}
		for _, s := range p.Sites {
			bp.Sites = append(bp.Sites, browser.ProfileSite{Origin: s.Origin, Fields: s.Fields})
		}
		st.Profiles[name] = bp
	}
	if len(b.Cards) > 0 {
		st.Cards = map[string]browser.Card{}
	}
	for name, c := range b.Cards {
		st.Cards[name] = browser.Card{Name: name, Number: ref(c.Number), Expiry: ref(c.Expiry), CVC: ref(c.CVC), HolderName: ref(c.Name),
			Kind: c.Kind, Merchants: c.Merchants, Identities: c.Identities, Limits: c.Limits, IdentityLimits: c.IdentityLimits,
			Frozen: c.Frozen, Method: browser.MethodConfig}
	}
}

// pausedFor reports "pause all automation" for this config (the engine's
// pause file): no payment while it holds.
func pausedFor(cfg *config.Config) func() bool {
	p := pausePath(cfg)
	return func() bool {
		_, err := os.Stat(p)
		return err == nil
	}
}

// PurchaseApprovalBot namespaces a purchase's signed approval: the same
// signature a bot's proposal takes, over browser-purchase/<id>/<digest>.
const PurchaseApprovalBot = "browser-purchase"

func widensList(before, after []string) bool {
	for _, x := range after {
		if !contains(before, x) {
			return true
		}
	}
	return false
}

func limitsWiden(a, b config.CardLimits) bool {
	return b.PerPurchase > a.PerPurchase || b.PerDay > a.PerDay || b.PerMonth > a.PerMonth || a.CurrencyOr() != b.CurrencyOr()
}

// browserWalletControl answers the wallet commands.
func browserWalletControl(cfg *config.Config, m *browser.Manager, req controlRequest) controlResponse {
	q := req.Wallet
	if q == nil {
		q = &browserWalletReq{}
	}
	data := func(v any) controlResponse {
		b, _ := json.Marshal(v)
		return controlResponse{OK: true, Data: b}
	}
	fail := func(err error) controlResponse { return controlResponse{Error: err.Error()} }
	audit := func(e map[string]any) {
		if m.Audit != nil {
			e["event"], e["by"] = "browser", "operator"
			if q.Via != "" {
				e["via"] = q.Via
			}
			m.Audit(e)
		}
	}
	if m.Store == nil {
		return fail(fmt.Errorf("browser: no wallet store on this daemon"))
	}
	inConfig := func(kind, name string) bool {
		if cfg.Browser == nil {
			return false
		}
		if kind == "profile" {
			_, ok := cfg.Browser.Profiles[name]
			return ok
		}
		_, ok := cfg.Browser.Cards[name]
		return ok
	}
	st := m.Effective()
	kind := "profile"
	if strings.HasPrefix(req.Cmd, "browser-card") {
		kind = "card"
	}
	setFields := func() error {
		for f, v := range q.Values {
			if err := m.Store.SetWalletValue(kind, q.Name, f, v); err != nil {
				return err
			}
		}
		for f, r := range q.Refs {
			if src, err := secrets.ParseSource(r); err == nil && src.Vault != "" && vaultTypeOf(src.Vault) == "" {
				return fmt.Errorf("browser: %s: no vault named %q (vaults:)", f, src.Vault)
			}
			if err := m.Store.SetWalletRef(kind, q.Name, f, r); err != nil {
				return err
			}
		}
		return nil
	}
	ps, cs, frozen, err := m.Store.Wallet()
	if err != nil {
		return fail(err)
	}
	findProfile := func() (browser.ProfileRecord, bool) {
		for _, p := range ps {
			if p.Name == q.Name {
				return p, true
			}
		}
		return browser.ProfileRecord{}, false
	}
	findCard := func() (browser.CardRecord, bool) {
		for _, c := range cs {
			if c.Name == q.Name {
				return c, true
			}
		}
		return browser.CardRecord{}, false
	}
	reachable := func(origins []string) error { return browser.OriginsReachable(st, origins) }

	switch req.Cmd {
	case "browser-wallet":
		out := map[string]any{"profiles": walletProfiles(st), "cards": walletCards(st, frozen)}
		if m.Purchases != nil {
			sum, err := m.Purchases.Summary()
			if err != nil {
				return fail(err)
			}
			out["spend"] = sum
		}
		return data(out)

	case "browser-profile-add", "browser-card-add":
		if inConfig(kind, q.Name) {
			return fail(fmt.Errorf("browser: %s %s is defined in the config — pick another name", kind, q.Name))
		}
		if _, ok := findProfile(); ok && kind == "profile" {
			return fail(fmt.Errorf("browser: a profile named %s exists", q.Name))
		}
		if _, ok := findCard(); ok && kind == "card" {
			return fail(fmt.Errorf("browser: a card named %s exists", q.Name))
		}
		if q.DryRun {
			return data(map[string]any{"change": changeWidening, "dry_run": true})
		}
		var err error
		if kind == "profile" {
			var origins []string
			for _, s := range q.Sites {
				origins = append(origins, s.Origin)
			}
			if err = reachable(origins); err == nil {
				err = m.Store.PutProfile(browser.ProfileRecord{Name: q.Name, Identities: q.Identities, Sites: q.Sites, Visible: q.Visible, Via: q.Via})
			}
		} else {
			if q.Limits == nil {
				return fail(fmt.Errorf("browser: a card needs limits (per_purchase, per_day, per_month)"))
			}
			if err = reachable(q.Merchants); err == nil {
				err = m.Store.PutCard(browser.CardRecord{Name: q.Name, Identities: q.Identities, Merchants: q.Merchants, Kind: q.Kind,
					Limits: *q.Limits, IdentityLimits: q.IDLimits, Via: q.Via})
			}
		}
		if err == nil {
			if err = setFields(); err != nil {
				_, _ = m.Store.RemoveWallet(kind, q.Name)
			}
		}
		if err != nil {
			return fail(err)
		}
		audit(map[string]any{"action": kind + "-add", kind: q.Name, "identities": q.Identities, "change": changeWidening, "outcome": "ok"})
		return data(map[string]any{kind: q.Name, "change": changeWidening})

	case "browser-profile-set", "browser-card-set", "browser-profile-unset", "browser-card-unset":
		if inConfig(kind, q.Name) {
			return fail(fmt.Errorf("browser: %s %s is defined in the config — change it there", kind, q.Name))
		}
		if q.DryRun {
			return data(map[string]any{"change": changeNeutral, "dry_run": true})
		}
		var err error
		if strings.HasSuffix(req.Cmd, "-unset") {
			err = m.Store.ClearWalletField(kind, q.Name, q.Field)
		} else {
			err = setFields()
		}
		if err != nil {
			return fail(err)
		}
		audit(map[string]any{"action": strings.TrimPrefix(req.Cmd, "browser-"), kind: q.Name, "field": q.Field, "change": changeNeutral, "outcome": "ok"})
		return data(map[string]any{kind: q.Name, "change": changeNeutral})

	case "browser-profile-bind":
		if inConfig(kind, q.Name) {
			return fail(fmt.Errorf("browser: profile %s is defined in the config — change it there", q.Name))
		}
		before, ok := findProfile()
		if !ok {
			return fail(fmt.Errorf("browser: no stored profile %q", q.Name))
		}
		after := before
		after.Identities = edit(before.Identities, q.Bind, q.Unbind)
		var sites []browser.ProfileSite
		for _, s := range before.Sites {
			if !contains(q.RmSites, s.Origin) {
				sites = append(sites, s)
			}
		}
		var newOrigins []string
		widen := widensList(before.Identities, after.Identities)
		for _, add := range q.AddSites {
			newOrigins = append(newOrigins, add.Origin)
			merged := false
			for i := range sites {
				if sites[i].Origin == add.Origin {
					widen = widen || widensList(sites[i].Fields, add.Fields)
					sites[i].Fields = edit(sites[i].Fields, add.Fields, nil)
					merged = true
				}
			}
			if !merged {
				sites = append(sites, add)
				widen = true
			}
		}
		after.Sites = sites
		for f, v := range q.Visible {
			if after.Visible == nil {
				after.Visible = map[string]string{}
			}
			if rank := map[string]int{"hidden": 0, "exists": 1, "fact": 2, "value": 3}; rank[v] > rank[before.Visible[f]] {
				widen = true
			}
			after.Visible[f] = v
		}
		change := changeNeutral
		if widen {
			change = changeWidening
		} else if len(q.Unbind) > 0 || len(q.RmSites) > 0 {
			change = changeNarrowing
		}
		if q.DryRun {
			return data(map[string]any{"change": change, "dry_run": true})
		}
		if err := reachable(newOrigins); err != nil {
			return fail(err)
		}
		if err := m.Store.PutProfile(after); err != nil {
			return fail(err)
		}
		audit(map[string]any{"action": "profile-bind", "profile": q.Name, "identities": after.Identities, "sites": after.Sites, "change": change, "outcome": "ok"})
		return data(map[string]any{"profile": q.Name, "change": change})

	case "browser-card-bind":
		if inConfig(kind, q.Name) {
			return fail(fmt.Errorf("browser: card %s is defined in the config — change it there", q.Name))
		}
		before, ok := findCard()
		if !ok {
			return fail(fmt.Errorf("browser: no stored card %q", q.Name))
		}
		after := before
		after.Identities = edit(before.Identities, q.Bind, q.Unbind)
		after.Merchants = edit(before.Merchants, q.AddMerch, q.RmMerch)
		widen := widensList(before.Identities, after.Identities) || widensList(before.Merchants, after.Merchants)
		if q.Limits != nil {
			widen = widen || limitsWiden(before.Limits, *q.Limits)
			after.Limits = *q.Limits
		}
		for k, l := range q.IDLimits {
			if old, ok := before.IdentityLimits[k]; !ok || limitsWiden(old, l) {
				// A new identity limit narrows; a looser one widens.
				widen = widen || ok
			}
			if after.IdentityLimits == nil {
				after.IdentityLimits = map[string]config.CardLimits{}
			}
			after.IdentityLimits[k] = l
		}
		change := changeNeutral
		if widen {
			change = changeWidening
		} else if len(q.Unbind) > 0 || len(q.RmMerch) > 0 || q.Limits != nil || len(q.IDLimits) > 0 {
			change = changeNarrowing
		}
		if q.DryRun {
			return data(map[string]any{"change": change, "dry_run": true})
		}
		if err := reachable(q.AddMerch); err != nil {
			return fail(err)
		}
		if err := m.Store.PutCard(after); err != nil {
			return fail(err)
		}
		audit(map[string]any{"action": "card-bind", "card": q.Name, "identities": after.Identities, "merchants": after.Merchants, "limits": after.Limits, "change": change, "outcome": "ok"})
		return data(map[string]any{"card": q.Name, "change": change})

	case "browser-card-freeze", "browser-card-unfreeze":
		freeze := req.Cmd == "browser-card-freeze"
		if _, ok := st.Cards[q.Name]; !ok {
			return fail(fmt.Errorf("browser: no card %q", q.Name))
		}
		change := map[bool]string{true: changeNarrowing, false: changeWidening}[freeze]
		if q.DryRun {
			return data(map[string]any{"change": change, "dry_run": true})
		}
		if !freeze && cfg.Browser != nil && cfg.Browser.Cards[q.Name].Frozen {
			return fail(fmt.Errorf("browser: card %s is frozen in the config (frozen: true) — unfreeze it there", q.Name))
		}
		if err := m.Store.Freeze(q.Name, freeze); err != nil {
			return fail(err)
		}
		audit(map[string]any{"action": strings.TrimPrefix(req.Cmd, "browser-"), "card": q.Name, "change": change, "outcome": "ok"})
		return data(map[string]any{"card": q.Name, "frozen": freeze, "change": change})

	case "browser-profile-rm", "browser-card-rm":
		if inConfig(kind, q.Name) {
			return fail(fmt.Errorf("browser: %s %s is defined in the config — remove it there", kind, q.Name))
		}
		if q.DryRun {
			return data(map[string]any{"change": changeNarrowing, "dry_run": true})
		}
		ok, err := m.Store.RemoveWallet(kind, q.Name)
		if err != nil {
			return fail(err)
		}
		if !ok {
			return fail(fmt.Errorf("browser: no stored %s %q", kind, q.Name))
		}
		audit(map[string]any{"action": kind + "-remove", kind: q.Name, "change": changeNarrowing, "outcome": "ok"})
		return data(map[string]any{"removed": q.Name, "change": changeNarrowing})

	case "browser-purchases":
		if m.Purchases == nil {
			return data(map[string]any{"purchases": []any{}})
		}
		list, err := m.Purchases.List(q.Status)
		if err != nil {
			return fail(err)
		}
		return data(map[string]any{"purchases": list, "approvers": cfg.PaymentApprovers()})

	case "browser-purchase":
		if m.Purchases == nil {
			return fail(fmt.Errorf("browser: no purchase ledger"))
		}
		p, ok, err := m.Purchases.Get(q.Purchase)
		if err != nil || !ok {
			return fail(fmt.Errorf("browser: no purchase %s", q.Purchase))
		}
		// The approval's view: every layer's remaining budget as it stands
		// now (this purchase not counted), and at the last check.
		now, nowErr := m.PurchaseLayers(p)
		out := map[string]any{"purchase": p, "approvers": cfg.PaymentApprovers(), "remaining_by_layer": browser.RemainingByLayer(now)}
		if nowErr != nil {
			out["would_refuse"] = strings.TrimPrefix(nowErr.Error(), "browser: refused: ")
		}
		return data(out)

	case "browser-case-budget":
		return caseBudgetOp(cfg, m, q, data, fail, audit)

	case "browser-purchase-decide":
		if m.Purchases == nil {
			return fail(fmt.Errorf("browser: no purchase ledger"))
		}
		p, ok, err := m.Purchases.Get(q.Purchase)
		if err != nil || !ok {
			return fail(fmt.Errorf("browser: no purchase %s", q.Purchase))
		}
		if q.Decision != "approve" && q.Decision != "deny" {
			return fail(fmt.Errorf("browser: decision is approve or deny"))
		}
		if !contains(cfg.PaymentApprovers(), q.Operator) {
			audit(map[string]any{"action": "purchase-" + q.Decision, "purchase": p.ID, "operator": q.Operator, "outcome": "refused", "reason": "not an approver"})
			return fail(fmt.Errorf("browser: %q may not decide purchases (browser.payments.approvers: %s)", q.Operator, strings.Join(cfg.PaymentApprovers(), ", ")))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		payload := bot.ApprovalPayload(PurchaseApprovalBot, p.ID, p.Digest, q.Decision, "", q.Reason)
		if err := verifyApproval(ctx, q.Operator, cfg.Operators[q.Operator].SSHKey, payload, q.Signature); err != nil {
			audit(map[string]any{"action": "purchase-" + q.Decision, "purchase": p.ID, "operator": q.Operator, "outcome": "refused", "reason": "signature"})
			return fail(fmt.Errorf("browser: the approval's signature does not verify: %v", err))
		}
		var out browser.Purchase
		if q.Decision == "deny" {
			out, err = m.Purchases.Deny(p.ID, q.Operator, q.Reason)
			audit(map[string]any{"action": "purchase-deny", "purchase": p.ID, "operator": q.Operator, "outcome": map[bool]string{true: "ok", false: "error"}[err == nil]})
		} else {
			out, err = m.ApprovePurchase(p.ID, p.Digest, q.Operator)
		}
		if err != nil {
			return fail(err)
		}
		return data(map[string]any{"purchase": out})
	}
	return controlResponse{Error: "browser: unknown command " + req.Cmd}
}

// verifyApproval is bot.VerifyApproval (tests replace it).
var verifyApproval = bot.VerifyApproval

// vaultTypeOf is the vaults registry's type of a vault ("" when unknown).
var vaultTypeOf = vaults.Type

func walletProfiles(st browser.Settings) []map[string]any {
	var out []map[string]any
	for _, name := range sortedKeysOf(st.Profiles) {
		p := st.Profiles[name]
		fields := map[string]fieldView{}
		for f, r := range p.Fields {
			if r != nil {
				fields[f] = fieldView{Kind: r.Kind(), Source: r.Source()}
			}
		}
		out = append(out, map[string]any{"name": name, "method": p.Method, "identities": p.Identities, "sites": p.Sites,
			"visible": p.Visible, "fields": fields, "editable": p.Method != browser.MethodConfig})
	}
	return out
}

func walletCards(st browser.Settings, frozen map[string]time.Time) []map[string]any {
	var out []map[string]any
	for _, name := range sortedKeysOf(st.Cards) {
		c := st.Cards[name]
		fields := map[string]fieldView{}
		for f, r := range map[string]*browser.SecretRef{"number": c.Number, "expiry": c.Expiry, "cvc": c.CVC, "name": c.HolderName} {
			if r != nil {
				fields[f] = fieldView{Kind: r.Kind(), Source: r.Source()}
			}
		}
		v := map[string]any{"name": name, "method": c.Method, "kind": c.Kind, "identities": c.Identities, "merchants": c.Merchants,
			"limits": c.Limits, "identity_limits": c.IdentityLimits, "frozen": c.Frozen, "fields": fields, "editable": c.Method != browser.MethodConfig}
		if t, ok := frozen[name]; ok {
			v["frozen_at"] = t
		}
		out = append(out, v)
	}
	return out
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const browserWalletUsage = `usage: conductor browser profile|card|purchases|purchase|spend …

Personal details (never shown to an agent; filled by meaning):
  profile add NAME --bind WHO --site ORIGIN=field,field [--visible field=fact]
      [--FIELD-ref REF]… [--FIELD-stdin]…         FIELD: name, email, phone, company, birthday,
                                                  address.line1 … billing.country, custom.<x>
  profile set NAME FIELD (--stdin | --ref REF) | profile unset NAME FIELD
  profile bind NAME [--bind WHO] [--unbind WHO] [--site ORIGIN=fields] [--remove-site ORIGIN] [--visible f=level] [--dry-run]
  profile rm NAME

Payment cards (filled only for an approved purchase, on its merchant):
  card add NAME --bind WHO --merchant ORIGIN --per-purchase N --per-day N --per-month N [--currency USD] [--kind virtual]
      --number-stdin --expiry-stdin [--cvc-stdin] [--name-stdin]   (or --number-ref REF …)
  card bind NAME [--bind WHO] [--unbind WHO] [--merchant ORIGIN] [--remove-merchant ORIGIN]
      [--per-purchase N --per-day N --per-month N] [--dry-run]
  card freeze NAME | card unfreeze NAME     the kill switch (pause: conductor pause stops every payment too)
  card rm NAME

Purchases:
  purchases [--status pending]              Needs you: what waits for an approval
  purchase show ID
  purchase approve ID [--as OPERATOR] [--key FILE] [--reason TEXT]   signed with your operator key
  purchase deny ID [--as OPERATOR] [--key FILE] [--reason TEXT]
  spend                                     profiles, cards, and spend per card, identity, bot and team

Spend limits stack and the lowest wins: the card's, the card's per identity,
the bot's (bots.<b>.purchases), its team's (teams.<t>.budget.purchases), and a
ticket's spend_limit:
  budget BOT CASE                           a ticket's budget: committed, remaining, its purchases
  budget BOT CASE --amount 120.00 [--currency USD] [--card C] [--merchant ORIGIN]… [--dry-run]
  budget BOT CASE --clear
`

// browserWalletCmd is the operator's wallet commands.
func browserWalletCmd(cmd string, rest []string) error {
	if len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help") {
		fmt.Fprint(os.Stderr, browserWalletUsage)
		return nil
	}
	pos, multi, bools := credFlags(rest)
	sock := map[string]any{}
	for _, k := range []string{"config", "socket"} {
		if v := one(multi, k); v != "" {
			sock[k] = v
		}
	}
	call := func(op string, q browserWalletReq) (json.RawMessage, error) {
		q.Via = "cli"
		resp, err := browserCtl(sock, controlRequest{Cmd: op, Wallet: &q})
		if err != nil {
			return nil, err
		}
		return resp.Data, nil
	}
	show := func(raw json.RawMessage, err error) error {
		if err != nil {
			return err
		}
		var v any
		_ = json.Unmarshal(raw, &v)
		return printJSON(v)
	}
	stdin := bufio.NewReader(os.Stdin)
	fields := func(kind string, q *browserWalletReq) error {
		q.Values, q.Refs = map[string]string{}, map[string]string{}
		names := []string{"number", "expiry", "cvc", "name"}
		if kind == "profile" {
			names = []string{"name", "given_name", "family_name", "email", "phone", "company", "birthday"}
			for _, part := range []string{"line1", "line2", "city", "region", "postal_code", "country"} {
				names = append(names, "address."+part, "billing."+part)
			}
			for k := range multi {
				if strings.HasPrefix(k, "custom.") && strings.HasSuffix(k, "-ref") {
					names = append(names, strings.TrimSuffix(k, "-ref"))
				}
			}
		}
		for _, f := range names {
			if r := one(multi, f+"-ref"); r != "" {
				q.Refs[f] = r
			}
			if bools[f+"-stdin"] {
				v, err := readSecret(stdin, f)
				if err != nil {
					return err
				}
				q.Values[f] = v
			}
		}
		return nil
	}
	sites := func(k string) []browser.ProfileSite {
		var out []browser.ProfileSite
		for _, s := range multi[k] {
			o, fs, _ := strings.Cut(s, "=")
			var list []string
			for _, f := range strings.Split(fs, ",") {
				if f = strings.TrimSpace(f); f != "" {
					list = append(list, f)
				}
			}
			out = append(out, browser.ProfileSite{Origin: strings.TrimSuffix(o, "/"), Fields: list})
		}
		return out
	}
	visible := func() map[string]string {
		out := map[string]string{}
		for _, v := range list(multi, "visible") {
			f, lvl, _ := strings.Cut(v, "=")
			out[f] = lvl
		}
		return out
	}
	limits := func() *config.CardLimits {
		pp, pd, pm := one(multi, "per-purchase"), one(multi, "per-day"), one(multi, "per-month")
		if pp == "" && pd == "" && pm == "" {
			return nil
		}
		l := &config.CardLimits{Currency: one(multi, "currency")}
		l.PerPurchase, _ = strconv.ParseFloat(pp, 64)
		l.PerDay, _ = strconv.ParseFloat(pd, 64)
		l.PerMonth, _ = strconv.ParseFloat(pm, 64)
		return l
	}
	name := func() (string, error) {
		if len(pos) < 2 {
			return "", fmt.Errorf("usage: conductor browser %s %s NAME …", cmd, firstOr(pos))
		}
		return pos[1], nil
	}
	switch cmd {
	case "budget":
		// conductor browser budget BOT CASE [--amount N [--currency C] [--card X] [--merchant O]… | --clear] [--dry-run]
		if len(pos) != 2 {
			return fmt.Errorf("usage: conductor browser budget BOT CASE [--amount 120.00 [--currency USD] [--card C] [--merchant ORIGIN]… | --clear] [--dry-run]")
		}
		q := browserWalletReq{Bot: pos[0], Case: pos[1], Amount: one(multi, "amount"), Currency: one(multi, "currency"), Card: one(multi, "card"),
			BMerchant: list(multi, "merchant"), Clear: bools["clear"], DryRun: bools["dry-run"]}
		q.Show = q.Amount == "" && !q.Clear
		return show(call("browser-case-budget", q))
	case "spend":
		return show(call("browser-wallet", browserWalletReq{}))
	case "purchases":
		return show(call("browser-purchases", browserWalletReq{Status: one(multi, "status")}))
	case "purchase":
		if len(pos) < 2 {
			return fmt.Errorf("usage: conductor browser purchase show|approve|deny ID")
		}
		switch pos[0] {
		case "show":
			return show(call("browser-purchase", browserWalletReq{Purchase: pos[1]}))
		case "approve", "deny":
			return purchaseDecide(sock, pos[0], pos[1], multi)
		}
		return fmt.Errorf("usage: conductor browser purchase show|approve|deny ID")
	case "profile", "card":
		if len(pos) == 0 {
			fmt.Fprint(os.Stderr, browserWalletUsage)
			return nil
		}
		op := "browser-" + cmd + "-" + pos[0]
		if !browserWalletOps[op] {
			return fmt.Errorf("unknown: conductor browser %s %s (conductor browser %s --help)", cmd, pos[0], cmd)
		}
		n, err := name()
		if err != nil {
			return err
		}
		q := browserWalletReq{Name: n, DryRun: bools["dry-run"], Identities: list(multi, "bind"), Kind: one(multi, "kind")}
		switch pos[0] {
		case "add":
			if err := fields(cmd, &q); err != nil {
				return err
			}
			q.Sites, q.Visible, q.Merchants, q.Limits = sites("site"), visible(), list(multi, "merchant"), limits()
		case "set", "unset":
			if len(pos) < 3 {
				return fmt.Errorf("usage: conductor browser %s %s NAME FIELD …", cmd, pos[0])
			}
			q.Field = pos[2]
			if pos[0] == "set" {
				switch {
				case one(multi, "ref") != "":
					q.Refs = map[string]string{q.Field: one(multi, "ref")}
				case bools["stdin"]:
					v, err := readSecret(stdin, q.Field)
					if err != nil {
						return err
					}
					q.Values = map[string]string{q.Field: v}
				default:
					return fmt.Errorf("set %s: --stdin (a typed value, stored write-only) or --ref REF", q.Field)
				}
			}
		case "bind":
			q.Identities = nil
			q.Bind, q.Unbind = list(multi, "bind"), list(multi, "unbind")
			q.AddSites, q.RmSites, q.Visible = sites("site"), list(multi, "remove-site"), visible()
			q.AddMerch, q.RmMerch, q.Limits = list(multi, "merchant"), list(multi, "remove-merchant"), limits()
		}
		return show(call(op, q))
	}
	return fmt.Errorf("unknown command")
}

func firstOr(pos []string) string {
	if len(pos) > 0 {
		return pos[0]
	}
	return "add"
}

// purchaseDecide signs and submits a purchase decision, as `bot approve`
// does a proposal: the operator's key, through the ssh-agent by default.
func purchaseDecide(sock map[string]any, decision, id string, multi map[string][]string) error {
	resp, err := browserCtl(sock, controlRequest{Cmd: "browser-purchase", Wallet: &browserWalletReq{Purchase: id}})
	if err != nil {
		return err
	}
	var v struct {
		Purchase  browser.Purchase     `json:"purchase"`
		Approvers []string             `json:"approvers"`
		Remaining []browser.LayerState `json:"remaining_by_layer"`
		Would     string               `json:"would_refuse"`
	}
	if err := json.Unmarshal(resp.Data, &v); err != nil {
		return err
	}
	p := v.Purchase
	op := one(multi, "as")
	if op == "" {
		if len(v.Approvers) != 1 {
			return fmt.Errorf("say which operator you are with --as (approvers: %s)", strings.Join(v.Approvers, ", "))
		}
		op = v.Approvers[0]
	}
	cfgPath, _ := sock["config"].(string)
	cfg, _, err := loadConfig(configArgs(cfgPath))
	if err != nil {
		return err
	}
	opCfg, ok := cfg.Operators[op]
	if !ok {
		return fmt.Errorf("operator %q is not configured", op)
	}
	rec := ""
	if p.Recurring {
		rec = fmt.Sprintf(" — RECURRING every %s", p.Interval)
	}
	fmt.Printf("%s purchase %s: %.2f %s at %s on card %s%s\n  for %s (requester %s%s); why: %s\n",
		map[string]string{"approve": "Approving", "deny": "Denying"}[decision], p.ID, p.Total, p.Currency, p.Merchant, p.Card, rec,
		p.Identity, p.Requester, func() string {
			if p.Bot != "" {
				return ", bot " + p.Bot + " case " + p.CaseID
			}
			return ""
		}(), p.Why)
	if p.CaseRef != "" {
		fmt.Printf("  under ticket %s\n", p.CaseRef)
	}
	for _, l := range v.Remaining {
		mark := " "
		if l.Binding {
			mark = "*"
		}
		fmt.Printf("  %s %-40s %-8s %.2f of %.2f %s left%s\n", mark, l.Layer, l.Window, l.Remaining, l.Limit, l.Currency, func() string {
			if l.Note != "" {
				return " (" + l.Note + ")"
			}
			return ""
		}())
	}
	if v.Would != "" && decision == "approve" {
		fmt.Printf("  as things stand, the approval is refused: %s\n", v.Would)
	}
	key := one(multi, "key")
	if key == "" {
		f, err := os.CreateTemp("", "conductor-operator-*.pub")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if _, err := f.WriteString(strings.TrimSpace(opCfg.SSHKey) + "\n"); err != nil {
			return err
		}
		f.Close()
		key = f.Name()
	}
	reason := one(multi, "reason")
	sig, err := bot.SignApproval(context.Background(), key, bot.ApprovalPayload(PurchaseApprovalBot, p.ID, p.Digest, decision, "", reason))
	if err != nil {
		return fmt.Errorf("%w\n(no ssh-agent holding operators.%s.ssh_key? pass --key <private key file>)", err, op)
	}
	out, err := browserCtl(sock, controlRequest{Cmd: "browser-purchase-decide", Wallet: &browserWalletReq{Purchase: p.ID, Decision: decision,
		Operator: op, Signature: sig, Reason: reason, Via: "cli"}})
	if err != nil {
		return err
	}
	var x any
	_ = json.Unmarshal(out.Data, &x)
	return printJSON(x)
}

// configArgs is a --config flag for loadConfig ("" = the default).
func configArgs(path string) []string {
	if path == "" {
		return nil
	}
	return []string{"--config", path}
}

// browserBotGate is the daemon's bot gate (buildBots), for a bot session's
// purchases and its provenance in the browser.
var browserBotGate atomic.Pointer[bot.Gate]

// botFlowIdentity is a browser caller as the bot gate sees it.
func botFlowIdentity(c browser.Principal) flow.SkillIdentity {
	return flow.SkillIdentity{Agent: c.Agent, Repo: c.Repo, Dispatch: c.Dispatch, Bot: c.Bot, BotRole: c.BotRole, CaseID: c.CaseID}
}

// botPurchase hands a bot's purchase to its gate: browser.purchase, its
// tier (commit_money, with the total as its amount, under spend_cap), and
// unattended only where the operator opted it in.
func botPurchase(ctx context.Context, c browser.Principal, p browser.Purchase) (string, bool, error) {
	g := browserBotGate.Load()
	cfg := browserCfg.Load()
	if g == nil || cfg == nil {
		return "", false, fmt.Errorf("no bot gate on this daemon")
	}
	m, ok := cfg.Bots[c.Bot]
	if !ok {
		return "", false, fmt.Errorf("bot %q is not configured", c.Bot)
	}
	if sc := m.Autonomy.SpendCap; sc != nil && sc.CurrencyOr() != p.Currency {
		return "", false, fmt.Errorf("the purchase is in %s, bot %s's spend cap in %s", p.Currency, c.Bot, sc.CurrencyOr())
	}
	var items []string
	for _, it := range p.Items {
		items = append(items, fmt.Sprintf("%dx %s", max(it.Qty, 1), it.Name))
	}
	opts := map[string]any{"purchase": p.ID, "card": p.Card, "merchant": p.Merchant, "total": p.Total, "currency": p.Currency,
		"recurring": p.Recurring, "interval": p.Interval, "items": strings.Join(items, ", "), "why": p.Why,
		"ticket": p.CaseRef, "binding_limit": p.Binding}
	v, err := g.CheckVerb(ctx, botFlowIdentity(c), BrowserPurchaseVerb, opts)
	if err != nil {
		return "", false, err
	}
	if v.Allow {
		return "", true, nil
	}
	proposal, _ := v.Output["proposal"].(string)
	return proposal, false, nil
}

// BrowserPurchaseVerb is a purchase as a bot's gate classifies it.
const BrowserPurchaseVerb = "browser.purchase"

// runBotPurchaseApproved executes an approved browser.purchase proposal:
// the gate re-checks it (and its spend cap, at execution), then the
// browser's purchase is approved — the card is filled later, once, by pay.
func runBotPurchaseApproved(ctx context.Context, g *bot.Gate, id flow.SkillIdentity, opts map[string]any) (map[string]any, error) {
	v, err := g.CheckVerb(ctx, id, BrowserPurchaseVerb, opts)
	if err != nil {
		return nil, err
	}
	if !v.Allow {
		return nil, fmt.Errorf("browser.purchase: the gate did not admit the approved purchase")
	}
	b, proposal, ok := bot.ApprovedProposal(ctx)
	m := browserMgr.Load()
	if !ok || m == nil {
		return nil, fmt.Errorf("browser.purchase: no approved proposal, or no browser on this daemon")
	}
	pid, _ := opts["purchase"].(string)
	p, err := m.ApproveBotPurchase(pid, b, proposal)
	if err != nil {
		return nil, err
	}
	return map[string]any{"purchase": p.ID, "status": p.Status, "expires": p.Expires.Format(time.RFC3339)}, nil
}

// botRequester is a bot session's requester for the browser: the gate's
// class now ("operator" for an operator's case), else least trust.
func botRequester(id skill.Identity) string {
	g := browserBotGate.Load()
	if g == nil {
		return ""
	}
	class, _ := g.RequesterClass(flow.SkillIdentity{Agent: id.Agent, Repo: id.Repo, Dispatch: id.Dispatch, Bot: id.Bot, BotRole: id.BotRole, CaseID: id.CaseID})
	return class
}

// botGroups is the bot and team a bot session acts for.
func botGroups(id skill.Identity) []string {
	if id.Bot == "" {
		return nil
	}
	out := []string{"bot:" + id.Bot}
	if cfg := browserCfg.Load(); cfg != nil {
		if t := string(cfg.Bots[id.Bot].Team); t != "" {
			out = append(out, "team:"+t)
		}
	}
	return out
}

// browserLedger is the daemon's store (the case ledger tickets' budgets
// are read from).
var browserLedger atomic.Pointer[store.Store]

// ledgerBudgets is the default CaseBudgets: a case's spend_limit in the bot
// ledger, counted under the case's own reference. (A ticket store may
// answer with its ticket's stable id as the key instead.)
type ledgerBudgets struct{}

func (ledgerBudgets) CaseBudget(caseRef string) (browser.CaseBudget, bool) {
	st := browserLedger.Load()
	bot, caseID, ok := parseCaseRef(caseRef)
	if st == nil || !ok {
		return browser.CaseBudget{}, false
	}
	c := st.Bot(bot).Cases[caseID]
	if c == nil || c.SpendLimit == nil {
		return browser.CaseBudget{}, false
	}
	amt, err := browser.ParseSpendLimit(c.SpendLimit.Amount)
	if err != nil {
		// An unreadable budget allows nothing (never falls back to none).
		amt = 0
	}
	return browser.CaseBudget{Key: caseRef, Amount: amt, Currency: strings.ToUpper(c.SpendLimit.Currency),
		Card: c.SpendLimit.Card, Merchants: c.SpendLimit.Merchants}, true
}

// parseCaseRef reads "bot:<name>/<case>".
func parseCaseRef(ref string) (bot, caseID string, ok bool) {
	rest, ok := strings.CutPrefix(ref, "bot:")
	if !ok {
		return "", "", false
	}
	bot, caseID, ok = strings.Cut(rest, "/")
	return bot, caseID, ok && bot != "" && caseID != ""
}

// caseBudgetOp sets, clears or shows a bot case's (ticket's) spend_limit.
// A new or larger budget widens what may be spent; a smaller one, or none,
// narrows. A budget above a layer's 30-day cap is clamped there (warned).
func caseBudgetOp(cfg *config.Config, m *browser.Manager, q *browserWalletReq, data func(any) controlResponse, fail func(error) controlResponse, audit func(map[string]any)) controlResponse {
	st := browserLedger.Load()
	if st == nil {
		return fail(fmt.Errorf("browser: no case ledger on this daemon"))
	}
	c := st.Bot(q.Bot).Cases[q.Case]
	if c == nil {
		return fail(fmt.Errorf("browser: bot %s has no case %s", q.Bot, q.Case))
	}
	ref := browser.CaseRef(q.Bot, q.Case)
	standing := func() map[string]any {
		out := map[string]any{"bot": q.Bot, "case": q.Case, "ticket": ref, "spend_limit": c.SpendLimit}
		if m.Purchases != nil {
			used, list := m.Purchases.Committed(ref)
			out["committed"], out["purchases"] = used, list
			if c.SpendLimit != nil {
				if amt, err := browser.ParseSpendLimit(c.SpendLimit.Amount); err == nil {
					out["remaining"] = math.Max(0, math.Round((amt-used)*100)/100)
				}
			}
		}
		if w := budgetClampWarning(cfg, q.Bot, c.SpendLimit); w != "" {
			out["warning"] = w
		}
		return out
	}
	if q.Show {
		return data(standing())
	}
	var next *store.SpendLimit
	if !q.Clear {
		amt, err := browser.ParseSpendLimit(q.Amount)
		if err != nil {
			return fail(fmt.Errorf("browser: %v", err))
		}
		cur := strings.ToUpper(q.Currency)
		if cur == "" {
			cur = "USD"
		}
		if len(cur) != 3 {
			return fail(fmt.Errorf("browser: currency is an ISO code (USD, EUR, …)"))
		}
		for _, mo := range q.BMerchant {
			if browser.CheckOrigin(mo) != nil {
				return fail(fmt.Errorf("browser: merchant %q is not an exact origin", mo))
			}
		}
		next = &store.SpendLimit{Amount: strconv.FormatFloat(amt, 'f', 2, 64), Currency: cur, Card: q.Card, Merchants: q.BMerchant}
	}
	change := classifyBudget(c.SpendLimit, next)
	if q.DryRun {
		return data(map[string]any{"change": change, "dry_run": true, "warning": budgetClampWarning(cfg, q.Bot, next)})
	}
	if err := st.UpdateBot(q.Bot, func(r *store.BotRecord) error {
		cs := r.Cases[q.Case]
		if cs == nil {
			return fmt.Errorf("case %s vanished", q.Case)
		}
		cs.SpendLimit = next
		return nil
	}); err != nil {
		return fail(err)
	}
	c.SpendLimit = next
	audit(map[string]any{"action": "case-budget", "bot": q.Bot, "case": q.Case, "spend_limit": next, "change": change, "outcome": "ok"})
	out := standing()
	out["change"] = change
	return data(out)
}

// classifyBudget: a new or larger budget, or one freed from a card or a
// merchant list, widens; a smaller one or none narrows.
func classifyBudget(before, after *store.SpendLimit) string {
	amt := func(l *store.SpendLimit) float64 {
		if l == nil {
			return 0
		}
		f, _ := browser.ParseSpendLimit(l.Amount)
		return f
	}
	switch {
	case after == nil && before == nil:
		return changeNeutral
	case after == nil:
		return changeNarrowing
	case before == nil, amt(after) > amt(before), after.Currency != before.Currency,
		before.Card != "" && after.Card != before.Card,
		len(before.Merchants) > 0 && (len(after.Merchants) == 0 || widensList(before.Merchants, after.Merchants)):
		return changeWidening
	case amt(after) < amt(before), before.Card == "" && after.Card != "",
		len(before.Merchants) == 0 && len(after.Merchants) > 0, len(after.Merchants) < len(before.Merchants):
		return changeNarrowing
	}
	return changeNeutral
}

// budgetClampWarning says when a ticket's budget exceeds a 30-day cap of
// the layers above it (the bot's, its team's, the card's): it is clamped
// there at every check.
func budgetClampWarning(cfg *config.Config, bot string, l *store.SpendLimit) string {
	if l == nil {
		return ""
	}
	amt, err := browser.ParseSpendLimit(l.Amount)
	if err != nil {
		return ""
	}
	lowest, by := amt, ""
	consider := func(name string, cl config.CardLimits) {
		if cl.CurrencyOr() == strings.ToUpper(l.Currency) && cl.PerMonth > 0 && cl.PerMonth < lowest {
			lowest, by = cl.PerMonth, name
		}
	}
	b := cfg.Bots[bot]
	if pl := b.Purchases; pl != nil {
		consider("bot "+bot, pl.CardLimits)
		if cl, ok := pl.Cards[l.Card]; ok {
			consider("bot "+bot+" on card "+l.Card, cl)
		}
	}
	if t := string(b.Team); t != "" {
		if pl := cfg.Teams[t].Budget.Purchases; pl != nil {
			consider("team "+t, pl.CardLimits)
			if cl, ok := pl.Cards[l.Card]; ok {
				consider("team "+t+" on card "+l.Card, cl)
			}
		}
	}
	if l.Card != "" && cfg.Browser != nil {
		if cd, ok := cfg.Browser.Cards[l.Card]; ok {
			consider("card "+l.Card, cd.Limits)
		}
	}
	if by == "" {
		return ""
	}
	return fmt.Sprintf("the ticket's budget %.2f %s is clamped to %s's 30-day limit %.2f — a ticket can never exceed the layers above it", amt, l.Currency, by, lowest)
}

// browserAPIRoutes are the wallet's read-only routes on the operator API
// (the dashboard's Needs-you purchases, spend, a ticket's budget). A
// decision stays a signed control-socket op; nothing here returns a value.
func browserAPIRoutes(mux *http.ServeMux) {
	serve := func(w http.ResponseWriter, cmd string, q browserWalletReq) {
		cfg, m := browserCfg.Load(), browserMgr.Load()
		w.Header().Set("Content-Type", "application/json")
		if cfg == nil || m == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "the browser is not configured on this daemon"})
			return
		}
		r := browserWalletControl(cfg, m, controlRequest{Cmd: cmd, Wallet: &q})
		if r.Error != "" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": r.Error})
			return
		}
		_, _ = w.Write(r.Data)
	}
	mux.HandleFunc("GET /v1/purchases", func(w http.ResponseWriter, r *http.Request) {
		serve(w, "browser-purchases", browserWalletReq{Status: r.URL.Query().Get("status")})
	})
	mux.HandleFunc("GET /v1/purchases/{id}", func(w http.ResponseWriter, r *http.Request) {
		serve(w, "browser-purchase", browserWalletReq{Purchase: r.PathValue("id")})
	})
	mux.HandleFunc("GET /v1/spend", func(w http.ResponseWriter, r *http.Request) {
		serve(w, "browser-wallet", browserWalletReq{})
	})
	mux.HandleFunc("GET /v1/bots/{bot}/cases/{case}/budget", func(w http.ResponseWriter, r *http.Request) {
		serve(w, "browser-case-budget", browserWalletReq{Bot: r.PathValue("bot"), Case: r.PathValue("case"), Show: true})
	})
}
