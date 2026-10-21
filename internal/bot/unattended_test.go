package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

const shop = "https://shop.example.com"

func shopperBot() config.BotConfig {
	m := testBot()
	m.Autonomy.Tiers[config.TierCommitMoney] = append(m.Autonomy.Tiers[config.TierCommitMoney], "browser.purchase")
	m.Autonomy.Amounts["browser.purchase"] = "total"
	m.Autonomy.Unattended = map[string]config.Unattended{"browser.purchase": {
		SpendCap:  config.SpendCap{PerAction: 10, PerPeriod: 25, Period: config.Duration(24 * time.Hour)},
		Merchants: []string{shop},
	}}
	return m
}

func buy(total float64, merchant string, recurring bool) map[string]any {
	return map[string]any{"total": total, "merchant": merchant, "recurring": recurring, "card": "visa"}
}

// Unattended is opt-in and narrow: allowed only with every condition met;
// any one missing makes it a proposal, never a refusal of the purchase.
func TestUnattendedPurchaseOnlyWhenEveryConditionHolds(t *testing.T) {
	f := newGateFixture(t, shopperBot(), true)
	allow := func(id flow.SkillIdentity, opts map[string]any) bool {
		t.Helper()
		v, err := f.gate.CheckVerb(context.Background(), id, "browser.purchase", opts)
		if err != nil {
			t.Fatal(err)
		}
		return v.Allow
	}
	if !allow(homeID(), buy(9.99, shop, false)) {
		t.Fatal("the bot's own small purchase at a named merchant")
	}
	for name, c := range map[string]struct {
		opts  map[string]any
		setup func()
		undo  func()
	}{
		"recurring":     {opts: buy(5, shop, true)},
		"another shop":  {opts: buy(5, "https://evil.example.com", false)},
		"a lookalike":   {opts: buy(5, shop+".evil", false)},
		"no merchant":   {opts: map[string]any{"total": 5.0}},
		"over the cap":  {opts: buy(10.01, shop, false)},
		"shadow mode":   {opts: buy(5, shop, false), setup: func() { m := f.cfg.Bots["sre"]; m.Autonomy.Shadow = boolp(true); f.cfg.Bots["sre"] = m }, undo: func() { m := f.cfg.Bots["sre"]; m.Autonomy.Shadow = boolp(false); f.cfg.Bots["sre"] = m }},
		"not sandboxed": {opts: buy(5, shop, false), setup: func() { f.gate.Sandbox = fixedJail(false) }, undo: func() { f.gate.Sandbox = fixedJail(true) }},
	} {
		if c.setup != nil {
			c.setup()
		}
		if allow(homeID(), c.opts) {
			t.Errorf("%s ran unattended", name)
		}
		if c.undo != nil {
			c.undo()
		}
	}
	// A third party's open case: the home session is that requester.
	cs := f.openCase(t, ReqUser)
	if allow(homeID(), buy(5, shop, false)) {
		t.Fatal("unattended for a third party's case")
	}
	if allow(f.worker(t, cs), buy(5, shop, false)) {
		t.Fatal("a worker on a third party's case")
	}
	_ = f.st.UpdateBot("sre", func(r *store.BotRecord) error { r.Cases[cs].Status = "closed"; return nil })
	if !allow(f.worker(t, f.openCase(t, ReqOperator)), buy(5, shop, false)) {
		t.Fatal("the operator's case")
	}
	if allow(f.worker(t, f.openCase(t, ReqEvent)), buy(5, shop, false)) {
		t.Fatal("an event's case ran unattended")
	}
}

// The unattended period cap counts only unattended spend, at its boundary;
// the spend cap counts everything; both are re-checked in the ledger
// transaction that records the spend.
func TestUnattendedCapsAndPeriod(t *testing.T) {
	f := newGateFixture(t, shopperBot(), true)
	run := func(amt float64) bool {
		v, err := f.gate.CheckVerb(context.Background(), homeID(), "browser.purchase", buy(amt, shop, false))
		if err != nil {
			t.Fatal(err)
		}
		return v.Allow
	}
	if !run(10) || !run(10) || !run(5) {
		t.Fatal("25 within the day")
	}
	if run(0.01) {
		t.Fatal("over the unattended day")
	}
	rec := f.st.Bot("sre")
	if len(rec.Spend) != 3 || rec.Spend[0].Proposal != UnattendedTag {
		t.Fatalf("spend recorded: %+v", rec.Spend)
	}
	f.now = f.now.Add(24*time.Hour - time.Second)
	if run(1) {
		t.Fatal("a second before the day ends")
	}
	f.now = f.now.Add(time.Second)
	if !run(10) {
		t.Fatal("the next day")
	}
	// Approved spend counts against spend_cap (150 per 30 days), not the
	// unattended cap: with 140 spent, a 10 unattended buy still fits the
	// day but not the month.
	_ = f.st.UpdateBot("sre", func(r *store.BotRecord) error {
		r.Spend = append(r.Spend, store.SpendEntry{At: f.now, Amount: 130, Proposal: "p9"})
		return nil
	})
	if _, err := f.gate.CheckVerb(context.Background(), homeID(), "browser.purchase", buy(10, shop, false)); err == nil || !strings.Contains(err.Error(), "over the cap 150") {
		t.Fatalf("the spend cap: %v", err)
	}
}

// Never the default: a commit_money verb without an unattended entry is a
// proposal however small.
func TestUnattendedNeverDefault(t *testing.T) {
	m := shopperBot()
	m.Autonomy.Unattended = nil
	f := newGateFixture(t, m, true)
	v, err := f.gate.CheckVerb(context.Background(), homeID(), "browser.purchase", buy(0.5, shop, false))
	if err != nil || v.Allow {
		t.Fatalf("ran without an opt-in: %+v %v", v, err)
	}
	if rec := f.st.Bot("sre"); len(rec.Proposals) != 1 || len(rec.Spend) != 0 {
		t.Fatalf("a proposal, no spend: %+v", rec)
	}
}

// The unattended branch re-checks spend_cap inside the ledger write that
// records the spend: two purchases that each passed the earlier check
// cannot jointly overshoot it.
func TestUnattendedRechecksSpendCapInTheLedgerWrite(t *testing.T) {
	m := shopperBot()
	m.Autonomy.SpendCap = &config.SpendCap{PerAction: 10, PerPeriod: 15}
	f := newGateFixture(t, m, true)
	cl := f.gate.caller(homeID())
	ok, err := f.gate.unattended(homeID(), m, "browser.purchase", buy(10, shop, false), 10, cl)
	if err != nil || !ok {
		t.Fatalf("the first: %v %v", ok, err)
	}
	// The second already passed checkSpend (as a racing call would have).
	if ok, err := f.gate.unattended(homeID(), m, "browser.purchase", buy(10, shop, false), 10, cl); ok || err == nil || !strings.Contains(err.Error(), "over the cap 15") {
		t.Fatalf("the second overshot spend_cap: %v %v", ok, err)
	}
}
