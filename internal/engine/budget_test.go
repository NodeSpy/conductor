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

// The middle budget scope is the RUNTIME now (design §1), not an agent.
func budgetCfg(global, runtimeBudget *config.BudgetPolicy) *config.Config {
	c := baseCfg()
	if c.Runtimes == nil {
		c.Runtimes = config.RuntimeSet{}
	}
	c.Runtimes["fixer"] = config.RuntimeConfig{Use: "paseo", Budget: runtimeBudget}
	// The dispatch reaches that runtime through the step template its
	// legacy `agent: fixer` names.
	if c.Steps == nil {
		c.Steps = map[string]config.Step{}
	}
	st := c.Steps["fixer"]
	st.Runtime = "fixer"
	c.Steps["fixer"] = st
	if global != nil {
		c.Policy = &config.Policy{Budget: global}
	}
	return c
}

func TestCheckSpendBudgetScopes(t *testing.T) {
	global := &config.BudgetPolicy{MaxCostUSD: 1}
	runtimeBudget := &config.BudgetPolicy{MaxTokens: 100}
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, _ := newEng(t, budgetCfg(global, runtimeBudget), d, n, nil)

	// Under every cap → nil (cancel the reservation so later checks are clean).
	res, berr := e.checkSpendBudget("fixer", nil, "", cost.Usage{})
	if berr != nil {
		t.Fatalf("fresh meter must be under cap: %v", berr)
	}
	e.meter.Cancel(res)

	// Charge the runtime scope past its token cap.
	e.meter.Record([]string{"runtime:fixer"}, cost.Usage{TotalTokens: 100})
	_, berr = e.checkSpendBudget("fixer", nil, "", cost.Usage{})
	if berr == nil || berr.Scope != "runtime:fixer" || !strings.Contains(berr.Reason, "tokens") {
		t.Fatalf("runtime token cap: %+v", berr)
	}
	// A different runtime is untouched.
	res, berr = e.checkSpendBudget("other", nil, "", cost.Usage{})
	if berr != nil {
		t.Fatalf("other runtime: %v", berr)
	}
	e.meter.Cancel(res)

	// Charge global past its $ cap.
	e.meter.Record([]string{"global"}, cost.Usage{CostUSD: 1.5})
	if _, berr := e.checkSpendBudget("other", nil, "", cost.Usage{}); berr == nil || berr.Scope != "global" {
		t.Fatalf("global $ cap: %+v", berr)
	}

	// Workflow scope: its own ledger and cap.
	wf := &config.BudgetPolicy{MaxCostUSD: 0.10, Window: config.Duration(time.Hour)}
	e2, _ := newEng(t, budgetCfg(nil, nil), &fakeDispatcher{}, &fakeNotifier{}, nil)
	res, berr = e2.checkSpendBudget("fixer", wf, "gh.push/ci", cost.Usage{})
	if berr != nil {
		t.Fatalf("fresh workflow scope: %v", berr)
	}
	e2.meter.Cancel(res)
	e2.meter.Record([]string{"workflow:gh.push/ci"}, cost.Usage{CostUSD: 0.10})
	if _, berr := e2.checkSpendBudget("fixer", wf, "gh.push/ci", cost.Usage{}); berr == nil || berr.Scope != "workflow:gh.push/ci" {
		t.Fatalf("workflow cap: %+v", berr)
	}
}

func TestSpendBudgetWindowFrees(t *testing.T) {
	profile := &config.BudgetPolicy{MaxCostUSD: 1, Window: config.Duration(time.Hour)}
	e, _ := newEng(t, budgetCfg(nil, profile), &fakeDispatcher{}, &fakeNotifier{}, nil)
	now := time.Now()
	e.meter.SetNow(func() time.Time { return now })
	e.meter.Record([]string{"runtime:fixer"}, cost.Usage{CostUSD: 1})
	if _, berr := e.checkSpendBudget("fixer", nil, "", cost.Usage{}); berr == nil {
		t.Fatal("over cap")
	}
	// The window frees → dispatches flow again (the shed's retry semantics).
	now = now.Add(2 * time.Hour)
	if _, berr := e.checkSpendBudget("fixer", nil, "", cost.Usage{}); berr != nil {
		t.Fatalf("freed window: %v", berr)
	}
}

func TestLegacyDispatchShedsOnBudget(t *testing.T) {
	profile := &config.BudgetPolicy{MaxCostUSD: 1}
	d, n := &fakeDispatcher{}, &fakeNotifier{}
	e, st := newEng(t, budgetCfg(nil, profile), d, n, nil)
	e.meter.Record([]string{"runtime:fixer"}, cost.Usage{CostUSD: 2})

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
	if tok, _ := e.meter.SpentIn("runtime:fixer", time.Hour); tok != 15 {
		t.Fatalf("metered profile usage: %d", tok)
	}
	if tok, _ := e.meter.SpentIn("global", time.Hour); tok != 15 {
		t.Fatalf("metered global usage: %d", tok)
	}
}

// Regression (#36 review H7): the budget check RESERVES the admitted
// dispatch's estimated spend atomically — a second concurrent check sees the
// reservation and sheds BEFORE any charge lands, so a team's parallel
// workers can't all pass an under-cap read and overshoot a hard cap.
func TestBudgetReservationClosesCheckThenActRace(t *testing.T) {
	profile := &config.BudgetPolicy{MaxCostUSD: 1, Window: config.Duration(time.Hour)}
	e, _ := newEng(t, budgetCfg(nil, profile), &fakeDispatcher{}, &fakeNotifier{}, nil)

	est := cost.Usage{CostUSD: 0.6, TotalTokens: 100} // two of these exceed the $1 cap
	res1, berr := e.checkSpendBudget("fixer", nil, "", est)
	if berr != nil {
		t.Fatalf("first dispatch must be admitted: %v", berr)
	}
	// NO charge has landed yet — the reservation alone must block the second.
	if _, berr := e.checkSpendBudget("fixer", nil, "", est); berr == nil {
		t.Fatal("second concurrent dispatch must shed on the reservation")
	}
	// Settling with a smaller actual frees headroom for the next dispatch.
	e.meter.Settle(res1, chargeScopes("fixer", ""), cost.Usage{CostUSD: 0.2, TotalTokens: 40})
	res3, berr := e.checkSpendBudget("fixer", nil, "", est)
	if berr != nil {
		t.Fatalf("after settle, headroom must admit again: %v", berr)
	}
	// Cancelling releases without charging.
	e.meter.Cancel(res3)
	if tok, usd := e.meter.SpentIn("runtime:fixer", time.Hour); tok != 40 || usd != 0.2 {
		t.Fatalf("only the settled actual should remain: %d %v", tok, usd)
	}

	// The atomic step holds under real concurrency: 10 goroutines race for a
	// cap that admits exactly one 0.6 reservation.
	e2, _ := newEng(t, budgetCfg(nil, profile), &fakeDispatcher{}, &fakeNotifier{}, nil)
	admitted := make(chan *cost.Reservation, 10)
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			if res, berr := e2.checkSpendBudget("fixer", nil, "", est); berr == nil {
				admitted <- res
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	close(admitted)
	n := 0
	for range admitted {
		n++
	}
	if n != 1 {
		t.Fatalf("exactly one concurrent dispatch may pass a $1 cap with $0.6 estimates, got %d", n)
	}
}
