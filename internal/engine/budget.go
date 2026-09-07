package engine

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/notify"
)

// The spend-budget layer (#36 §14): hard $/token caps over rolling windows,
// at three scopes — global (policy.budget), profile (agents.<name>.budget),
// and workflow (a trigger-level policy.budget, resolved by the flow runner).
// Every scope with a cap must be under it for a dispatch to proceed; an
// over-cap dispatch SHEDS exactly like the agents-per-hour budget: the
// attempt is recorded (so backoff/sweep re-derives once the window frees),
// nothing launches, and a notification goes out. The meter is in-memory like
// the agent-count window — the durable record is the audit's agent_usage
// rows.

// ErrBudget marks a dispatch shed by a spend cap.
type ErrBudget struct {
	Scope  string // "global" | "profile:<name>" | "workflow:<key>"
	Reason string
}

func (e *ErrBudget) Error() string {
	return fmt.Sprintf("spend budget: %s over cap (%s) — shedding until the window frees", e.Scope, e.Reason)
}

// budgetScope pairs a meter scope key with the cap governing it.
type budgetScope struct {
	key string
	b   *config.BudgetPolicy
}

// budgetScopes resolves the (scope key, cap) pairs governing one dispatch.
func (e *Engine) budgetScopes(agentName string, wf *config.BudgetPolicy, wfScope string) []budgetScope {
	var out []budgetScope
	if e.cfg.Policy != nil && e.cfg.Policy.Budget != nil {
		out = append(out, budgetScope{"global", e.cfg.Policy.Budget})
	}
	if p, ok := e.cfg.Agents[agentName]; ok && p.Budget != nil {
		out = append(out, budgetScope{"profile:" + agentName, p.Budget})
	}
	if wf != nil && wfScope != "" {
		out = append(out, budgetScope{"workflow:" + wfScope, wf})
	}
	return out
}

// checkSpendBudget reports the first over-cap scope for this dispatch (nil =
// under every cap). The meter is charged per scope key, so each scope's
// window is its own ledger.
func (e *Engine) checkSpendBudget(agentName string, wf *config.BudgetPolicy, wfScope string) *ErrBudget {
	if e.meter == nil {
		return nil
	}
	for _, s := range e.budgetScopes(agentName, wf, wfScope) {
		tokens, usd := e.meter.SpentIn(s.key, s.b.WindowOrDefault())
		if max := s.b.MaxCostUSD; max > 0 && usd >= max {
			return &ErrBudget{Scope: s.key, Reason: fmt.Sprintf("$%.2f of $%.2f in %s", usd, max, s.b.WindowOrDefault())}
		}
		if max := int(s.b.MaxTokens); max > 0 && tokens >= max {
			return &ErrBudget{Scope: s.key, Reason: fmt.Sprintf("%d of %d tokens in %s", tokens, max, s.b.WindowOrDefault())}
		}
	}
	return nil
}

// shedForBudget records the shed (attempt + audit + notify) so the dispatch
// isn't silently forgotten and the operator hears about the cap once hit.
func (e *Engine) shedForBudget(ctx context.Context, t core.Trigger, berr *ErrBudget, shadow bool) {
	e.log("%s %v", tag(t), berr)
	if !shadow {
		_ = e.store.RecordAttempt(t.Key(), t.Kind, t.Target.HeadSHA)
	}
	e.store.Audit(map[string]any{"event": "budget_shed", "repo": t.Target.Repo,
		"number": t.Target.Number, "kind": t.Kind, "scope": berr.Scope, "reason": berr.Reason})
	e.notif.Emit(ctx, notify.EventEscalate, t, berr.Error())
}

// recordUsage charges one agent run's usage to its budget scopes and writes
// the agent_usage audit row — the durable per-run cost record `conductor
// report` aggregates (per run / workflow / repo / day).
func (e *Engine) recordUsage(t core.Trigger, agentName, stepID, runID, wfScope string, u cost.Usage) {
	scopes := []string{"global"}
	if agentName != "" {
		scopes = append(scopes, "profile:"+agentName)
	}
	if wfScope != "" {
		scopes = append(scopes, "workflow:"+wfScope)
	}
	e.meter.Record(scopes, u)
	e.store.Audit(map[string]any{"event": "agent_usage", "repo": t.Target.Repo,
		"number": t.Target.Number, "kind": t.Kind, "agent": agentName, "step": stepID,
		"run": runID, "workflow": wfScope, "model": u.Model,
		"input_tokens": u.InputTokens, "output_tokens": u.OutputTokens,
		"tokens": u.TotalTokens, "cost_usd": u.CostUSD, "approximate": u.Approximate})
}
