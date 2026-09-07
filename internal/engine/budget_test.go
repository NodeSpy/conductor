package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

func budgetCfg(global, profile *config.BudgetPolicy) *config.Config {
	c := baseCfg()
	c.Agents["fixer"] = config.AgentProfile{Provider: "claude", Model: "claude-sonnet", Budget: profile}
	if global != nil {
		c.Policy = &config.Policy{Budget: global}
	}
	return c
}

func TestCheckSpendBudgetScopes(t *testing.T) {
	global := &config.BudgetPolicy{MaxCostUSD: 1}
	profile := &config.BudgetPolicy{MaxTokens: 100}
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, budgetCfg(global, profile), d, n, nil)

	// Under every cap → nil.
	if berr := e.checkSpendBudget("fixer", nil, ""); berr != nil {
		t.Fatalf("fresh meter must be under cap: %v", berr)
	}

	// Charge the profile scope past its token cap.
	e.meter.Record([]string{"profile:fixer"}, cost.Usage{TotalTokens: 100})
	berr := e.checkSpendBudget("fixer", nil, "")
	if berr == nil || berr.Scope != "profile:fixer" || !strings.Contains(berr.Reason, "tokens") {
		t.Fatalf("profile token cap: %+v", berr)
	}
	// A different profile is untouched.
	if berr := e.checkSpendBudget("other", nil, ""); berr != nil {
		t.Fatalf("other profile: %v", berr)
	}

	// Charge global past its $ cap.
	e.meter.Record([]string{"global"}, cost.Usage{CostUSD: 1.5})
	if berr := e.checkSpendBudget("other", nil, ""); berr == nil || berr.Scope != "global" {
		t.Fatalf("global $ cap: %+v", berr)
	}

	// Workflow scope: its own ledger and cap.
	wf := &config.BudgetPolicy{MaxCostUSD: 0.10, Window: config.Duration(time.Hour)}
	e2, _ := newEng(t, budgetCfg(nil, nil), &fakeDispatcher{}, &fakeNotifier{}, nil)
	if berr := e2.checkSpendBudget("fixer", wf, "gh.push/ci"); berr != nil {
		t.Fatalf("fresh workflow scope: %v", berr)
	}
	e2.meter.Record([]string{"workflow:gh.push/ci"}, cost.Usage{CostUSD: 0.10})
	if berr := e2.checkSpendBudget("fixer", wf, "gh.push/ci"); berr == nil || berr.Scope != "workflow:gh.push/ci" {
		t.Fatalf("workflow cap: %+v", berr)
	}
}

func TestSpendBudgetWindowFrees(t *testing.T) {
	profile := &config.BudgetPolicy{MaxCostUSD: 1, Window: config.Duration(time.Hour)}
	e, _ := newEng(t, budgetCfg(nil, profile), &fakeDispatcher{}, &fakeNotifier{}, nil)
	now := time.Now()
	e.meter.SetNow(func() time.Time { return now })
	e.meter.Record([]string{"profile:fixer"}, cost.Usage{CostUSD: 1})
	if berr := e.checkSpendBudget("fixer", nil, ""); berr == nil {
		t.Fatal("over cap")
	}
	// The window frees → dispatches flow again (the shed's retry semantics).
	now = now.Add(2 * time.Hour)
	if berr := e.checkSpendBudget("fixer", nil, ""); berr != nil {
		t.Fatalf("freed window: %v", berr)
	}
}

func TestLegacyDispatchShedsOnBudget(t *testing.T) {
	profile := &config.BudgetPolicy{MaxCostUSD: 1}
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, budgetCfg(nil, profile), d, n, nil)
	e.meter.Record([]string{"profile:fixer"}, cost.Usage{CostUSD: 2})

	tr := agentTrigger("merge_conflict", "o/r", 1, "h", "sig", config.Action{Type: "agent", Agent: "fixer"})
	e.process(context.Background(), tr)

	if len(d.reqs) != 0 {
		t.Fatalf("over-budget dispatch must shed, got %d dispatches", len(d.reqs))
	}
	if !n.has("escalate") {
		t.Fatal("shed must notify")
	}
	// The attempt is recorded so backoff/sweep retries once the window frees.
	if got := st.Attempts(tr.Key(), tr.Kind, "h"); got != 1 {
		t.Fatalf("shed must record the attempt: %d", got)
	}
}

func TestLegacyDispatchRecordsUsage(t *testing.T) {
	d := &fakeDispatcher{ref: dispatch.RunRef{AgentID: "a1",
		Output: `{"usage":{"input_tokens":10,"output_tokens":5}}`}}
	n := &fakeNotifier{}
	e, _ := newEng(t, budgetCfg(nil, nil), d, n, nil)

	tr := agentTrigger("merge_conflict", "o/r", 1, "h", "sig", config.Action{Type: "agent", Agent: "fixer"})
	e.process(context.Background(), tr)
	if len(d.reqs) != 1 {
		t.Fatalf("dispatched: %d", len(d.reqs))
	}
	if tok, _ := e.meter.SpentIn("profile:fixer", time.Hour); tok != 15 {
		t.Fatalf("metered profile usage: %d", tok)
	}
	if tok, _ := e.meter.SpentIn("global", time.Hour); tok != 15 {
		t.Fatalf("metered global usage: %d", tok)
	}
}
