package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

const (
	wCard  = "4242424242424242"
	wEmail = "jo.private@example.test"
)

func walletRig(t *testing.T) (call func(cmd string, q browserWalletReq) controlResponse, m *browser.Manager, cfg *config.Config) {
	t.Helper()
	dir := t.TempDir()
	cfg = &config.Config{
		Operators: map[string]config.OperatorConfig{"daniel": {SSHKey: "ssh-ed25519 AAAAdaniel"}, "guest": {SSHKey: "ssh-ed25519 AAAAguest"}},
		Browser: &config.BrowserConfig{Allow: []string{"shop.example.com"},
			Payments: &config.BrowserPayments{Approvers: []string{"daniel"}}},
	}
	cfg.Store.StateFile = filepath.Join(dir, "state.json")
	browserCfg.Store(cfg)
	m = &browser.Manager{StateDir: filepath.Join(dir, "browser"), Store: &browser.LoginStore{Dir: filepath.Join(dir, "browser")},
		Purchases: &browser.Purchases{Dir: filepath.Join(dir, "browser")},
		Settings:  func() browser.Settings { return browserSettings(cfg) }}
	browserMgr.Store(m)
	t.Cleanup(func() { browserMgr.Store(nil); browserCfg.Store(nil) })
	old := verifyApproval
	verifyApproval = func(ctx context.Context, op, key string, payload []byte, sig string) error {
		if sig != "good:"+op+":"+string(payload) {
			return errors.New("bad signature")
		}
		return nil
	}
	t.Cleanup(func() { verifyApproval = old })
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return func(cmd string, q browserWalletReq) controlResponse {
		return browserControl(a, controlRequest{Cmd: cmd, Wallet: &q})
	}, m, cfg
}

func walletChange(r controlResponse) string {
	if r.Error != "" {
		return "error: " + r.Error
	}
	var v map[string]any
	_ = json.Unmarshal(r.Data, &v)
	c, _ := v["change"].(string)
	return c
}

func TestBrowserWalletOpsNoValueCrosses(t *testing.T) {
	call, m, _ := walletRig(t)
	var all []controlResponse
	do := func(cmd string, q browserWalletReq) controlResponse {
		r := call(cmd, q)
		all = append(all, r)
		return r
	}
	lim := &config.CardLimits{PerPurchase: 30, PerDay: 50, PerMonth: 100}
	if c := walletChange(do("browser-card-add", browserWalletReq{Name: "visa", Identities: []string{"bot:shopper"}, Merchants: []string{"https://shop.example.com"},
		Limits: lim, Kind: "virtual", Values: map[string]string{"number": wCard, "expiry": "12/34", "cvc": "987"}})); c != changeWidening {
		t.Fatalf("card add: %s", c)
	}
	if c := walletChange(do("browser-profile-add", browserWalletReq{Name: "work", Identities: []string{"bot:shopper"},
		Sites:  []browser.ProfileSite{{Origin: "https://shop.example.com", Fields: []string{"email", "address"}}},
		Values: map[string]string{"email": wEmail}, Refs: map[string]string{"address.city": "${env.CITY}"}, Visible: map[string]string{"email": "fact"}})); c != changeWidening {
		t.Fatalf("profile add: %s", c)
	}
	for _, tc := range []struct {
		cmd  string
		q    browserWalletReq
		want string
	}{
		{"browser-card-bind", browserWalletReq{Name: "visa", AddMerch: []string{"https://shop.example.com"}}, changeNeutral},
		{"browser-card-bind", browserWalletReq{Name: "visa", Bind: []string{"team:ops"}, DryRun: true}, changeWidening},
		{"browser-card-bind", browserWalletReq{Name: "visa", Limits: &config.CardLimits{PerPurchase: 31, PerDay: 50, PerMonth: 100}, DryRun: true}, changeWidening},
		{"browser-card-bind", browserWalletReq{Name: "visa", Limits: &config.CardLimits{PerPurchase: 20, PerDay: 50, PerMonth: 100}}, changeNarrowing},
		{"browser-card-bind", browserWalletReq{Name: "visa", IDLimits: map[string]config.CardLimits{"bot:shopper": {PerPurchase: 5, PerDay: 5, PerMonth: 10}}}, changeNarrowing},
		{"browser-card-bind", browserWalletReq{Name: "visa", IDLimits: map[string]config.CardLimits{"bot:shopper": {PerPurchase: 6, PerDay: 6, PerMonth: 10}}}, changeWidening},
		{"browser-card-bind", browserWalletReq{Name: "visa", AddMerch: []string{"https://evil.example.org"}}, "error: browser: https://evil.example.org is not reachable"},
		{"browser-profile-bind", browserWalletReq{Name: "work", AddSites: []browser.ProfileSite{{Origin: "https://shop.example.com", Fields: []string{"phone"}}}, DryRun: true}, changeWidening},
		{"browser-profile-bind", browserWalletReq{Name: "work", Visible: map[string]string{"email": "value"}, DryRun: true}, changeWidening},
		{"browser-profile-bind", browserWalletReq{Name: "work", Unbind: []string{"bot:shopper"}, Bind: []string{"bot:shopper2"}, DryRun: true}, changeWidening},
		{"browser-card-freeze", browserWalletReq{Name: "visa"}, changeNarrowing},
		{"browser-card-unfreeze", browserWalletReq{Name: "visa", DryRun: true}, changeWidening},
		{"browser-card-set", browserWalletReq{Name: "visa", Values: map[string]string{"number": "4242424242424241"}}, "error: browser: the card number does not pass"},
		{"browser-card-set", browserWalletReq{Name: "visa", Refs: map[string]string{"cvc": "${settings.cvc}"}}, "error: browser: cvc: a ${settings"},
		{"browser-card-set", browserWalletReq{Name: "visa", Refs: map[string]string{"cvc": `{{ vault "nope" "k" }}`}}, `error: browser: cvc: no vault named "nope"`},
	} {
		if got := walletChange(do(tc.cmd, tc.q)); !strings.HasPrefix(got, tc.want) {
			t.Errorf("%s %+v: %s, want %s", tc.cmd, tc.q, got, tc.want)
		}
	}
	w := do("browser-wallet", browserWalletReq{})
	if !strings.Contains(string(w.Data), `"frozen":true`) || !strings.Contains(string(w.Data), `(stored, write-only)`) || !strings.Contains(string(w.Data), `${env.CITY}`) {
		t.Fatalf("wallet: %s", w.Data)
	}
	if st := m.Effective(); !st.Cards["visa"].Frozen || st.Cards["visa"].Limits.PerPurchase != 20 {
		t.Fatalf("effective: %+v", st.Cards["visa"])
	}
	for i, r := range all {
		b, _ := json.Marshal(r)
		for _, v := range []string{wCard, "12/34", "987", wEmail} {
			if strings.Contains(string(b), v) {
				t.Fatalf("response %d carries a value %q: %s", i, v, b)
			}
		}
	}
}

// A purchase decision is an approver's signature over the purchase's own
// digest: anyone else, a bad signature, or a second approval is refused;
// a bot's purchase is approved through its bot's proposal, not here.
func TestBrowserPurchaseDecide(t *testing.T) {
	call, m, _ := walletRig(t)
	call("browser-card-add", browserWalletReq{Name: "visa", Identities: []string{"agent:a@o/r"}, Merchants: []string{"https://shop.example.com"},
		Limits: &config.CardLimits{PerPurchase: 30, PerDay: 50, PerMonth: 100}, Values: map[string]string{"number": wCard, "expiry": "12/34"}})
	p, err := m.Purchases.Propose(browser.Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 12, Currency: "USD", Identity: "agent:a@o/r"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	sig := func(op, decision string) string {
		return "good:" + op + ":" + string(bot.ApprovalPayload(PurchaseApprovalBot, p.ID, p.Digest, decision, "", ""))
	}
	decide := func(op, decision, s string) controlResponse {
		return call("browser-purchase-decide", browserWalletReq{Purchase: p.ID, Decision: decision, Operator: op, Signature: s})
	}
	if r := decide("guest", "approve", sig("guest", "approve")); !strings.Contains(r.Error, "may not decide") {
		t.Fatalf("a non-approver: %+v", r)
	}
	if r := decide("daniel", "approve", sig("daniel", "deny")); !strings.Contains(r.Error, "does not verify") {
		t.Fatalf("a signature for another decision: %+v", r)
	}
	if r := decide("daniel", "approve", sig("daniel", "approve")); r.Error != "" || !strings.Contains(string(r.Data), `"status":"approved"`) {
		t.Fatalf("approve: %+v", r)
	}
	if r := decide("daniel", "approve", sig("daniel", "approve")); !strings.Contains(r.Error, "not pending") {
		t.Fatalf("a second approval: %+v", r)
	}
	bp, _ := m.Purchases.Propose(browser.Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 5, Currency: "USD", Identity: "agent:b@o/r", Bot: "shopper", BotProposal: "p3"}, time.Hour)
	r := call("browser-purchase-decide", browserWalletReq{Purchase: bp.ID, Decision: "approve", Operator: "daniel",
		Signature: "good:daniel:" + string(bot.ApprovalPayload(PurchaseApprovalBot, bp.ID, bp.Digest, "approve", "", ""))})
	if !strings.Contains(r.Error, "conductor bot approve shopper") {
		t.Fatalf("a bot's purchase approved here: %+v", r)
	}
	list := call("browser-purchases", browserWalletReq{Status: browser.PurchasePending})
	if !strings.Contains(string(list.Data), bp.ID) || strings.Contains(string(list.Data), `"id":"`+p.ID+`"`) {
		t.Fatalf("pending: %s", list.Data)
	}
}

type jail bool

func (j jail) Active() bool                        { return bool(j) }
func (j jail) PaseoProvider(string) (string, bool) { return "", false }

// A bot's purchase goes through its gate: a proposal (commit_money under
// spend_cap); approved and executed, it approves the browser's purchase;
// unattended only where opted in.
func TestBotPurchaseThroughTheGate(t *testing.T) {
	_, m, cfg := walletRig(t)
	cfg.Bots = map[string]config.BotConfig{"shopper": {Team: "", Operators: []string{"daniel"}, Autonomy: config.BotAutonomy{
		Shadow:   func() *bool { b := false; return &b }(),
		Tiers:    map[string][]string{config.TierCommitMoney: {BrowserPurchaseVerb}},
		Amounts:  map[string]string{BrowserPurchaseVerb: "total"},
		SpendCap: &config.SpendCap{PerAction: 50, PerPeriod: 100},
	}}}
	st, err := store.Open(store.Options{StatePath: filepath.Join(t.TempDir(), "s.json")})
	if err != nil {
		t.Fatal(err)
	}
	gate := &bot.Gate{Cfg: func() *config.Config { return cfg }, Ledger: st, Sandbox: jail(true)}
	browserBotGate.Store(gate)
	defer browserBotGate.Store(nil)
	c := browser.Principal{Identity: "agent:shopper@home", Bot: "shopper", BotRole: config.BotRoleHome, Dispatch: "d1", Requester: "bot"}
	p, _ := m.Purchases.Propose(browser.Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 12, Currency: "USD", Identity: c.Identity, Bot: "shopper"}, time.Hour)
	proposal, unattended, err := botPurchase(context.Background(), c, p)
	if err != nil || unattended || proposal == "" {
		t.Fatalf("a proposal: %q %v %v", proposal, unattended, err)
	}
	m.Purchases.SetBotProposal(p.ID, proposal)
	// The executor's approved run of the proposal approves the purchase.
	exec := &bot.Executor{Cfg: func() *config.Config { return cfg }, Ledger: st, Gate: gate,
		RunVerb: func(ctx context.Context, id flow.SkillIdentity, uses string, options map[string]any) (map[string]any, error) {
			return runBotPurchaseApproved(ctx, gate, id, options)
		},
		Verify: func(ctx context.Context, op, key string, payload []byte, sig string) error { return nil }}
	got, err := exec.Decide(context.Background(), bot.Decision{Bot: "shopper", Proposal: proposal, Decision: "approve", Operator: "daniel", Signature: "x"})
	if err != nil || got.Status != "executed" {
		t.Fatalf("decide: %+v %v", got, err)
	}
	if bp, _, _ := m.Purchases.Get(p.ID); bp.Status != browser.PurchaseApproved || bp.Approver != "bot proposal "+proposal {
		t.Fatalf("the browser's purchase: %+v", bp)
	}
	// In another currency than the bot's cap: refused.
	q, _ := m.Purchases.Propose(browser.Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 12, Currency: "EUR", Identity: c.Identity, Bot: "shopper"}, time.Hour)
	if _, _, err := botPurchase(context.Background(), c, q); err == nil || !strings.Contains(err.Error(), "spend cap in USD") {
		t.Fatalf("currency: %v", err)
	}
	// Unattended, opted in: allowed at once, under the small cap.
	b := cfg.Bots["shopper"]
	b.Autonomy.Unattended = map[string]config.Unattended{BrowserPurchaseVerb: {SpendCap: config.SpendCap{PerAction: 10, PerPeriod: 20}, Merchants: []string{"https://shop.example.com"}}}
	cfg.Bots["shopper"] = b
	u, _ := m.Purchases.Propose(browser.Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 9, Currency: "USD", Identity: c.Identity, Bot: "shopper"}, time.Hour)
	if _, unattended, err := botPurchase(context.Background(), c, u); err != nil || !unattended {
		t.Fatalf("unattended: %v %v", unattended, err)
	}
}
