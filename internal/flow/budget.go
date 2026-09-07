package flow

import (
	"context"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/memory"
)

// The flow side of spend budgets (#36 §14). The workflow-scope budget is the
// trigger's merged policy.budget (trigger → connector → global precedence,
// like every policy key); the runner resolves it once per run and carries it
// on the context so every agent step — including plan sub-agents spliced in
// later — checks and charges the same scope.

// runBudgetKey carries the run's workflow-scope budget through the step tree.
type runBudgetKey struct{}

// runBudget is the per-run resolution: the budget (nil = no workflow cap)
// and the meter scope key it charges.
type runBudget struct {
	b     *config.BudgetPolicy
	scope string
}

// withRunBudget resolves the trigger's workflow-scope budget onto the ctx.
// The scope key is the trigger's `on:` plus its variant name — one ledger per
// configured workflow, shared by every run it fires.
func (r *Runner) withRunBudget(ctx context.Context, t core.Trigger, spec config.TriggerSpec) context.Context {
	var connPol, global *config.Policy
	if r.Cfg != nil {
		if ref, ok := r.Cfg.ConnectorsMap[spec.Connector()]; ok {
			connPol = ref.Policy
		}
		global = r.Cfg.Policy
	}
	// The global budget is its own scope (the engine always checks it); only
	// a budget set at a more specific scope — the connector's or the
	// trigger's — is a *workflow* cap. Without this, a global-only budget
	// would be charged twice (once as "global", once as "workflow:…").
	specific := (connPol != nil && connPol.Budget != nil) ||
		(spec.Policy != nil && spec.Policy.Budget != nil)
	if !specific {
		return ctx
	}
	pol := config.MergePolicy(global, connPol, spec.Policy)
	scope := spec.On
	if spec.Name != "" {
		scope += "/" + spec.Name
	}
	return context.WithValue(ctx, runBudgetKey{}, runBudget{b: pol.Budget, scope: scope})
}

// costAccKey carries the run's cost accumulator: the run record's own
// tokens/$ tally (#36 §14 "store on the run record"), persisted with each
// checkpoint and stamped into the run-completion audit.
type costAccKey struct{}

// costAcc tallies one run's spend across its agent steps (plan sub-agents
// included — they run under the same ctx).
type costAcc struct {
	mu          sync.Mutex
	tokens      int
	usd         float64
	approximate bool // any contributing figure was estimated
}

func (a *costAcc) add(u cost.Usage) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.tokens += u.TotalTokens
	a.usd += u.CostUSD
	a.approximate = a.approximate || u.Approximate
	a.mu.Unlock()
}

// totals snapshots the tally.
func (a *costAcc) totals() (tokens int, usd float64, approximate bool) {
	if a == nil {
		return 0, 0, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.tokens, a.usd, a.approximate
}

// withCostAcc attaches a fresh accumulator to the run's context.
func withCostAcc(ctx context.Context) (context.Context, *costAcc) {
	acc := &costAcc{}
	return context.WithValue(ctx, costAccKey{}, acc), acc
}

// costAccFrom reads the run's accumulator ("" when not in a run).
func costAccFrom(ctx context.Context) *costAcc {
	acc, _ := ctx.Value(costAccKey{}).(*costAcc)
	return acc
}

// budgetFrom reads the run's workflow-scope budget off the context.
func budgetFrom(ctx context.Context) (b *config.BudgetPolicy, scope string) {
	rb, ok := ctx.Value(runBudgetKey{}).(runBudget)
	if !ok {
		return nil, ""
	}
	return rb.b, rb.scope
}

// checkBudget vets one agent dispatch against every governing cap.
func (r *Runner) checkBudget(ctx context.Context, agentName string) error {
	if r.Agents.CheckBudget == nil {
		return nil
	}
	wf, scope := budgetFrom(ctx)
	return r.Agents.CheckBudget(agentName, wf, scope)
}

// recordUsage charges one agent run's usage: the run's own tally and history
// record always accumulate; the engine's meter/audit service runs when wired.
func (r *Runner) recordUsage(ctx context.Context, t core.Trigger, agentName, stepID string, u cost.Usage) {
	costAccFrom(ctx).add(u)
	historySetCost(ctx, stepID, u)
	if r.Agents.RecordUsage == nil {
		return
	}
	_, scope := budgetFrom(ctx)
	r.Agents.RecordUsage(t, agentName, stepID, memory.SourceFrom(ctx).Run, scope, u)
}
