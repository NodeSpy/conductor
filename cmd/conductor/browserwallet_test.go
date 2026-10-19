package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/bot"
	"github.com/NodeSpy/conductor/internal/browser"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/skill"
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
	cfg.Browser.Cards = map[string]config.BrowserCard{"visa": {Number: &config.BrowserSecretRef{Env: "N"}, Expiry: &config.BrowserSecretRef{Env: "E"},
		Merchants: []string{"https://shop.example.com"}, Identities: []string{"agent:shopper@home"},
		Limits: config.CardLimits{PerPurchase: 30, PerDay: 50, PerMonth: 100}}}
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

// A ticket's budget: set (a widening, clamped with a warning past a layer's
// 30-day cap), lowered and cleared (narrowing), read back with what it
// holds — through the op, the ledger's CaseBudgets, a purchase's view and
// the operator API.
func TestCaseBudgetOpAndAPI(t *testing.T) {
	call, m, cfg := walletRig(t)
	cfg.Bots = map[string]config.BotConfig{"shopper": {Team: "ops", Purchases: &config.PurchaseLimits{CardLimits: config.CardLimits{PerPurchase: 100, PerDay: 200, PerMonth: 300}}}}
	cfg.Teams = map[string]config.TeamConfig{"ops": {Budget: config.TeamBudget{Purchases: &config.PurchaseLimits{CardLimits: config.CardLimits{PerPurchase: 150, PerDay: 200, PerMonth: 250}}}}}
	cfg.Browser.Cards = map[string]config.BrowserCard{"visa": {Number: &config.BrowserSecretRef{Env: "N"}, Expiry: &config.BrowserSecretRef{Env: "E"},
		Merchants: []string{"https://shop.example.com"}, Identities: []string{"bot:shopper"},
		Limits: config.CardLimits{PerPurchase: 500, PerDay: 1000, PerMonth: 2000}}}
	st, err := store.Open(store.Options{StatePath: filepath.Join(t.TempDir(), "s.json")})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateBot("shopper", func(r *store.BotRecord) error {
		r.Cases = map[string]*store.Case{"c3": {ID: "c3", Status: "open", RequesterClass: "operator", Title: "buy a hard drive"}}
		return nil
	})
	browserLedger.Store(st)
	defer browserLedger.Store(nil)
	m.Budgets = ledgerBudgets{}
	data := func(r controlResponse) map[string]any {
		var v map[string]any
		_ = json.Unmarshal(r.Data, &v)
		return v
	}
	r := call("browser-case-budget", browserWalletReq{Bot: "shopper", Case: "c3", Amount: "500", Currency: "usd", DryRun: true})
	if v := data(r); v["change"] != changeWidening || !strings.Contains(fmt.Sprint(v["warning"]), "clamped to team ops's 30-day limit 250.00") {
		t.Fatalf("dry run: %+v %v", v, r.Error)
	}
	if st.Bot("shopper").Cases["c3"].SpendLimit != nil {
		t.Fatal("a dry run wrote the budget")
	}
	if v := data(call("browser-case-budget", browserWalletReq{Bot: "shopper", Case: "c3", Amount: "120", Currency: "USD"})); v["change"] != changeWidening || v["warning"] != nil {
		t.Fatalf("set 120: %+v", v)
	}
	if got := st.Bot("shopper").Cases["c3"].SpendLimit; got == nil || got.Amount != "120.00" || got.Currency != "USD" {
		t.Fatalf("stored: %+v", got)
	}
	b, ok := ledgerBudgets{}.CaseBudget("bot:shopper/c3")
	if !ok || b.Amount != 120 || b.Key != "bot:shopper/c3" {
		t.Fatalf("CaseBudget: %+v %v", b, ok)
	}
	if _, ok := (ledgerBudgets{}).CaseBudget("bot:shopper/c9"); ok {
		t.Fatal("a case without a budget has one")
	}
	// A purchase under it: its view carries every layer's remaining.
	p, err := m.Purchases.ProposeWithin(browser.Purchase{Card: "visa", Merchant: "https://shop.example.com", Total: 85, Currency: "USD",
		Identity: "agent:shopper@home", Bot: "shopper", CaseRef: "bot:shopper/c3", BudgetKey: "bot:shopper/c3"}, time.Hour,
		browser.SpendRules{Card: m.Effective().Cards["visa"], Who: browser.Principal{Identity: "agent:shopper@home", Bot: "shopper"}, Bot: "shopper", Budget: &b})
	if err != nil {
		t.Fatal(err)
	}
	view := string(call("browser-purchase", browserWalletReq{Purchase: p.ID}).Data)
	for _, w := range []string{`"remaining_by_layer"`, `"layer":"ticket bot:shopper/c3"`, `"layer":"bot shopper"`, `"layer":"team ops"`, `"layer":"card visa"`} {
		if !strings.Contains(view, w) {
			t.Fatalf("the approval's view lacks %s: %s", w, view)
		}
	}
	if v := data(call("browser-case-budget", browserWalletReq{Bot: "shopper", Case: "c3", Show: true})); v["committed"] != 85.0 || v["remaining"] != 35.0 {
		t.Fatalf("show: %+v", v)
	}
	// The operator API, read-only.
	mux := http.NewServeMux()
	browserAPIRoutes(mux)
	for path, want := range map[string]string{
		"/v1/bots/shopper/cases/c3/budget": `"remaining":35`,
		"/v1/purchases/" + p.ID:            `"remaining_by_layer"`,
		"/v1/purchases?status=pending":     `"id":"` + p.ID + `"`,
		"/v1/spend":                        `"cards"`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	// Lowered, then cleared: narrowing.
	if v := data(call("browser-case-budget", browserWalletReq{Bot: "shopper", Case: "c3", Amount: "90"})); v["change"] != changeNarrowing {
		t.Fatalf("lowered: %+v", v)
	}
	if v := data(call("browser-case-budget", browserWalletReq{Bot: "shopper", Case: "c3", Clear: true})); v["change"] != changeNarrowing {
		t.Fatalf("cleared: %+v", v)
	}
	for _, q := range []browserWalletReq{{Bot: "shopper", Case: "c9", Amount: "5"}, {Bot: "shopper", Case: "c3", Amount: "0"},
		{Bot: "shopper", Case: "c3", Amount: "5", Currency: "DOLLARS"}, {Bot: "shopper", Case: "c3", Amount: "5", BMerchant: []string{"https://*.example.com"}}} {
		if r := call("browser-case-budget", q); r.Error == "" {
			t.Errorf("%+v accepted", q)
		}
	}
}

func TestClassifyBudget(t *testing.T) {
	l := func(a string, card string, ms ...string) *store.SpendLimit {
		return &store.SpendLimit{Amount: a, Currency: "USD", Card: card, Merchants: ms}
	}
	for name, c := range map[string]struct {
		before, after *store.SpendLimit
		want          string
	}{
		"new":                {nil, l("10", ""), changeWidening},
		"raised":             {l("10", ""), l("11", ""), changeWidening},
		"lowered":            {l("10", ""), l("9", ""), changeNarrowing},
		"cleared":            {l("10", ""), nil, changeNarrowing},
		"same":               {l("10", ""), l("10.00", ""), changeNeutral},
		"card added":         {l("10", ""), l("10", "visa"), changeNarrowing},
		"card changed":       {l("10", "visa"), l("10", "amex"), changeWidening},
		"merchants dropped":  {l("10", "", "https://a.example"), l("10", ""), changeWidening},
		"merchant added":     {l("10", "", "https://a.example"), l("10", "", "https://a.example", "https://b.example"), changeWidening},
		"merchants narrowed": {l("10", ""), l("10", "", "https://a.example"), changeNarrowing},
		"currency":           {l("10", ""), &store.SpendLimit{Amount: "10", Currency: "EUR"}, changeWidening},
	} {
		if got := classifyBudget(c.before, c.after); got != c.want {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

// A bot's home session names no case: the browser holds it to the case the
// gate does (the least-trusted open one), whose ticket budget applies.
func TestBrowserCallerHomeCase(t *testing.T) {
	cfg := &config.Config{Bots: map[string]config.BotConfig{"shopper": {}}}
	browserCfg.Store(cfg)
	defer browserCfg.Store(nil)
	st, err := store.Open(store.Options{StatePath: filepath.Join(t.TempDir(), "s.json")})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.UpdateBot("shopper", func(r *store.BotRecord) error {
		r.Cases = map[string]*store.Case{"c1": {ID: "c1", Status: "open", RequesterClass: "operator"}, "c2": {ID: "c2", Status: "open", RequesterClass: "user"}}
		return nil
	})
	browserBotGate.Store(&bot.Gate{Cfg: func() *config.Config { return cfg }, Ledger: st, Sandbox: jail(true)})
	defer browserBotGate.Store(nil)
	id := skill.Identity{Agent: "bot:shopper", Bot: "shopper", BotRole: config.BotRoleHome,
		Sandbox: &skill.SandboxBinding{Identity: "agent:shopper@home", Workspace: t.TempDir()}}
	c, err := browserCaller("tok", id)
	if err != nil || c.CaseID != "c2" || c.Requester != "user" {
		t.Fatalf("home: %+v %v", c, err)
	}
	id.BotRole, id.CaseID = config.BotRoleWorker, "c1"
	if c, _ := browserCaller("tok", id); c.CaseID != "c1" {
		t.Fatalf("a worker keeps its own case: %+v", c)
	}
}
