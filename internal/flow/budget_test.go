package flow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/store"
)

const budgetCfg = `
connectors:
  svc: { type: fake }
agents:
  fixer: { model: claude-sonnet }
`

var budgetSpecYAML = `
on: svc.ping
name: nightly
steps:
  - id: fix
    type: agent
    agent: fixer
    prompt: "fix it"
`

// wireBudget attaches recording CheckBudget/RecordUsage fakes to a rig.
func wireBudget(rig *testRig, checkErr error) (*[]string, *[]cost.Usage, *[]string) {
	var checks, scopes []string
	var usages []cost.Usage
	rig.Runner.Agents.CheckBudget = func(agentName string, wf *config.BudgetPolicy, wfScope string) error {
		checks = append(checks, agentName+"|"+wfScope)
		return checkErr
	}
	rig.Runner.Agents.RecordUsage = func(t core.Trigger, agentName, stepID, runID, wfScope string, u cost.Usage) {
		usages = append(usages, u)
		scopes = append(scopes, agentName+"|"+stepID+"|"+wfScope)
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
	if len(*checks) != 1 || (*checks)[0] != "fixer|" {
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
	want := "fixer|svc.ping/nightly"
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
	if len(*checks) != 1 || (*checks)[0] != "fixer|" {
		t.Fatalf("global-only budget leaked a workflow scope: %v", *checks)
	}
}

func TestOverBudgetShedsStep(t *testing.T) {
	cfg := loadConfig(t, budgetCfg)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	wireBudget(rig, fmt.Errorf("spend budget: profile:fixer over cap ($2.00 of $2.00 in 24h0m0s) — shedding until the window frees"))
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
