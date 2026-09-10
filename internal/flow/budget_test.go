package flow

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

const budgetCfg = `
connectors:
  svc: { use: fake }
x-t:
  fixer: &fixer { type: agent, name: fixer, model: claude-sonnet }
workflows:
  roles:
    steps:
      - { id: fixer, type: agent, name: fixer, prompt: p, model: claude-sonnet }
`

var budgetSpecYAML = `
on: svc.ping
name: nightly
steps:
  - id: fix
    type: agent
    <<: *fixer
    prompt: "fix it"
`

// wireBudget attaches recording CheckBudget/RecordUsage fakes to a rig.
func wireBudget(rig *testRig, checkErr error) (*[]string, *[]cost.Usage, *[]string) {
	var checks, scopes []string
	var usages []cost.Usage
	rig.Runner.Agents.CheckBudget = func(runtimeName string, wf *config.BudgetPolicy, wfScope string, est cost.Usage) (*cost.Reservation, error) {
		checks = append(checks, runtimeName+"|"+wfScope)
		return nil, checkErr
	}
	rig.Runner.Agents.RecordUsage = func(t core.Trigger, identity, runtimeName, stepID, runID, wfScope, savedWF string, res *cost.Reservation, u cost.Usage) {
		usages = append(usages, u)
		scopes = append(scopes, identity+"|"+stepID+"|"+wfScope)
	}
	return &checks, &usages, &scopes
}

func TestAgentStepChecksAndRecordsBudget(t *testing.T) {
	cfg := loadConfig(t, budgetCfg)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	checks, usages, scopes := wireBudget(rig, nil)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1",
			Output: `{"usage":{"input_tokens":100,"output_tokens":50},"total_cost_usd":0.25}`}, nil
	}

	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, budgetSpecYAML))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	// No trigger/connector budget → no workflow scope in the check.
	if len(*checks) != 1 || (*checks)[0] != "paseo|" {
		t.Fatalf("budget checks: %v", *checks)
	}
	if len(*usages) != 1 || (*usages)[0].TotalTokens != 150 || (*usages)[0].Approximate {
		t.Fatalf("recorded usage: %+v", *usages)
	}
	if (*scopes)[0] != "fixer|fix|" {
		t.Fatalf("usage scope: %v", *scopes)
	}
}

func TestWorkflowScopeBudgetFromTriggerPolicy(t *testing.T) {
	cfg := loadConfig(t, budgetCfg)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	checks, _, scopes := wireBudget(rig, nil)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}

	spec := mustSpec(t, budgetSpecYAML+`
policy:
  budget: { max_cost_usd: 1.5 }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	// The budget CHECK is keyed by the runtime (design §1); the usage SCOPE
	// is keyed by the step identity.
	want := "paseo|svc.ping/nightly"
	if len(*checks) != 1 || (*checks)[0] != want {
		t.Fatalf("workflow-scope check: %v (want %s)", *checks, want)
	}
	if (*scopes)[0] != "fixer|fix|svc.ping/nightly" {
		t.Fatalf("usage scope: %v", *scopes)
	}
}

func TestGlobalOnlyBudgetIsNotAWorkflowScope(t *testing.T) {
	// A global policy.budget must not be re-charged as a workflow scope.
	cfg := loadConfig(t, budgetCfg+`
policy:
  budget: { max_cost_usd: 10 }
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	checks, _, _ := wireBudget(rig, nil)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, budgetSpecYAML))
	if len(*checks) != 1 || (*checks)[0] != "paseo|" {
		t.Fatalf("global-only budget leaked a workflow scope: %v", *checks)
	}
}

func TestOverBudgetShedsStep(t *testing.T) {
	cfg := loadConfig(t, budgetCfg)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	wireBudget(rig, fmt.Errorf("spend budget: runtime:paseo over cap ($2.00 of $2.00 in 24h0m0s) — shedding until the window frees"))
	dispatched := false
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		dispatched = true
		return dispatch.RunRef{}, nil
	}

	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, budgetSpecYAML))
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "spend budget") {
		t.Fatalf("over-budget step must fail the workflow with the budget error: %v %q", failed, errStr)
	}
	if dispatched {
		t.Fatal("over-budget dispatch must never launch")
	}
}

// TestBackgroundDispatchKeepsReservationOpen is the F1 regression (#36 §146):
// a background/hand-off dispatch must NOT settle its reservation with the
// launch-confirmation output (which cost.FromRun scores at ~0). Wired to a real
// meter mirroring the engine, the reservation stays OPEN — its estimated spend
// keeps counting against the caps — and the estimate is reported approximate.
func TestBackgroundDispatchKeepsReservationOpen(t *testing.T) {
	cfg := loadConfig(t, budgetCfg)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)

	m := cost.NewMeter()
	scopesFor := func(runtimeName, wf string) []string {
		s := []string{"global", "runtime:" + runtimeName}
		if wf != "" {
			s = append(s, "workflow:"+wf)
		}
		return s
	}
	var settled []cost.Usage
	var reservedUSD float64
	rig.Runner.Agents.CheckBudget = func(runtimeName string, wf *config.BudgetPolicy, wfScope string, est cost.Usage) (*cost.Reservation, error) {
		reservedUSD = est.CostUSD
		return m.Reserve(scopesFor(runtimeName, wfScope), est), nil
	}
	rig.Runner.Agents.CancelBudget = func(res *cost.Reservation) { m.Cancel(res) }
	rig.Runner.Agents.RecordUsage = func(_ core.Trigger, _, runtimeName, _, _, wfScope, _ string, res *cost.Reservation, u cost.Usage) {
		settled = append(settled, u)
		m.Settle(res, scopesFor(runtimeName, wfScope), u)
	}
	// paseo returns a launch confirmation (the agent id), not a transcript —
	// cost.FromRun would score this at ~0.
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "bg1", Output: `{"agent_id":"bg1","status":"launched"}`}, nil
	}

	spec := mustSpec(t, `
on: svc.ping
name: nightly
steps:
  - id: handoff
    type: agent
    <<: *fixer
    prompt: "take it from here"
    background: true
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	// A background dispatch never settles — settling with the launch JSON would
	// charge a bogus ~0 and erase the reserved estimate.
	if len(settled) != 0 {
		t.Fatalf("background dispatch must not settle its reservation, settled=%+v", settled)
	}
	if reservedUSD <= 0 {
		t.Fatal("test setup: prompt estimate should be > 0")
	}
	// The open reservation still holds the full estimate against the cap.
	if _, usd := m.SpentIn("runtime:paseo", 24*time.Hour); usd != reservedUSD {
		t.Fatalf("open reservation must hold the estimate against the cap: spent=%v want=%v", usd, reservedUSD)
	}
	// And the estimate is surfaced as an approximate agent_usage row.
	usage := rig.Store.auditsWithEvent("agent_usage")
	if len(usage) != 1 || usage[0]["approximate"] != true || usage[0]["background"] != true {
		t.Fatalf("background agent_usage audit: %+v", usage)
	}
}

func TestRunRecordCarriesCost(t *testing.T) {
	cfg := loadConfig(t, budgetCfg)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	wireBudget(rig, nil)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1",
			Output: `{"usage":{"input_tokens":200,"output_tokens":100},"total_cost_usd":0.5}`}, nil
	}

	run := store.WorkflowRun{ID: "r-1", Outputs: map[string]map[string]any{}}
	runTriggerWithRun(rig, run, newTrigger("ping", nil), mustSpec(t, budgetSpecYAML))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	// The checkpointed run record carries the spend tally.
	last, ok := rig.Store.lastPut("r-1")
	if !ok || last.Tokens != 300 || last.CostUSD != 0.5 {
		t.Fatalf("run record cost: ok=%v tokens=%d usd=%v", ok, last.Tokens, last.CostUSD)
	}
	// And the run-completion audit has the totals (cost per run).
	costs := rig.Store.auditsWithEvent("workflow_cost")
	if len(costs) != 1 || costs[0]["tokens"] != 300 || costs[0]["cost_usd"] != 0.5 || costs[0]["run"] != "r-1" {
		t.Fatalf("workflow_cost audit: %+v", costs)
	}
}

// A flood through the flow path must SHED, not queue. Only the legacy engine
// path had the agents/hour guard, so work arriving through the callable
// service faced no rate limit at all: a narrow token could flood the shared
// dispatch queue and starve every other consumer, while an identical flood
// through a trigger was shed. Both paths now count against one window.
func TestAFloodShedsInsteadOfStarvingTheQueue(t *testing.T) {
	const cap = 3
	dispatched, admitted := 0, 0
	cfg := loadConfig(t, budgetCfg)
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	rig.Agents.dispatchFunc = func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
		dispatched++
		return dispatch.RunRef{AgentID: "a", Output: "{}"}, nil
	}
	rig.Runner.Agents.CheckRate = func() error {
		if admitted >= cap {
			return fmt.Errorf("agents_per_hour cap of %d reached in the last hour — shedding this dispatch", cap)
		}
		admitted++
		return nil
	}

	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: work, type: agent, prompt: "go" }
`)
	for i := 0; i < cap*3; i++ {
		runTrigger(rig, newTrigger("ping", map[string]any{"n": i}), spec)
	}

	if dispatched > cap {
		t.Fatalf("%d dispatches got through a cap of %d — an unrated flow path lets one "+
			"consumer starve the shared dispatch queue", dispatched, cap)
	}
	if dispatched == 0 {
		t.Fatal("nothing dispatched at all — the guard sheds everything, which would " +
			"make the assertion above pass for the wrong reason")
	}
}
