package engine

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/store"
)

// The outcome-learning loop (#36 §18). Every agent dispatch on a PR/issue
// records an ENGAGEMENT (who acted, under which workflow, at what cost).
// Terminal signals close the loop:
//
//   - the github connector's `_closed` signal — merged vs closed-unmerged,
//     plus the revert back-references a merged revert PR carries — consumes
//     the target's engagements and writes one `outcome` audit row per
//     engagement (merged / closed / reverted);
//   - a configured failing_checks trigger marks a non-terminal `ci_failed`
//     outcome without consuming the engagements;
//   - review hand-off decisions record approved / rejected;
//   - gate results (§16) are already audited per round (`event: gate`).
//
// The rows feed three places: `conductor report`'s agent-quality view
// (accept rate, revert rate, cost per merged change), shared memory (§9 — a
// scoped note per terminal outcome), and saved-workflow delivery health
// (§11 — merged-and-not-reverted beats "the run finished"). Per-agent
// counters persist for the optional guidance tuning (`outcome_feedback`).

// observeOutcomeSignals inspects one incoming trigger for outcome facts.
// Called early in process(), before any gate can drop the trigger.
func (e *Engine) observeOutcomeSignals(ctx context.Context, t core.Trigger) {
	switch t.Kind {
	case core.KindClosed:
		e.observeClosed(ctx, t)
	case "failing_checks":
		// Non-terminal: the PR lives on; the engagements stay for the
		// terminal signal.
		for _, g := range e.store.PeekEngagements(t.Target.Repo, t.Target.Number) {
			e.recordOutcome(ctx, t.Target.Repo, t.Target.Number, "ci_failed", g)
		}
	}
}

// observeClosed handles the terminal `_closed` signal: the PR's own outcome,
// and any same-repo PRs a merged revert PR reverts.
func (e *Engine) observeClosed(ctx context.Context, t core.Trigger) {
	merged, _ := t.Context["merged"].(bool)
	outcome := "closed"
	if merged {
		outcome = "merged"
	}
	for _, g := range e.store.TakeEngagements(t.Target.Repo, t.Target.Number) {
		e.recordOutcome(ctx, t.Target.Repo, t.Target.Number, outcome, g)
	}
	// A merged revert PR closes the loop on the PRs it reverts — their
	// engagements were already consumed at merge time, so the revert outcome
	// is attributed through the per-agent/service records the merge left
	// behind: re-take anything still tracked, and re-attribute via memory of
	// the merge rows is the report's job. Here we record the revert against
	// any engagements still held AND emit a bare row when none are (the
	// merge consumed them) so the report still counts the revert.
	// A revert claim is only actionable when the integration corroborated it
	// against the revert PR's own commit messages — the title/body a claim
	// rides on are attacker-editable (#36 review M9). Absent or false →
	// record the claim, act on nothing.
	corroborated, _ := t.Context["reverts_corroborated"].(bool)
	if reverts, ok := t.Context["reverts"].([]int); ok {
		for _, n := range reverts {
			e.recordRevert(ctx, t.Target.Repo, n, corroborated)
		}
	} else if revertsAny, ok := t.Context["reverts"].([]any); ok {
		for _, v := range revertsAny {
			if n, ok := v.(int); ok {
				e.recordRevert(ctx, t.Target.Repo, n, corroborated)
			} else if f, ok := v.(float64); ok {
				e.recordRevert(ctx, t.Target.Repo, int(f), corroborated)
			}
		}
	}
}

// recordRevert attributes a revert of repo#n. The merge that landed the
// change usually consumed its engagements, so attribution comes from the
// audit's merge rows; the store may still hold engagements when the revert
// raced the merge signal — consume those too.
func (e *Engine) recordRevert(ctx context.Context, repo string, n int, corroborated bool) {
	if !corroborated {
		// Title/body-only claim: keep the trail (the operator can look), but
		// it consumes no engagements, bumps no per-agent counters, and rots
		// no workflow — none of the report/guidance paths count this row.
		e.store.Audit(map[string]any{"event": "outcome", "repo": repo, "number": n,
			"outcome": "reverted_unconfirmed"})
		e.log("outcome: %s#%d revert claimed but not corroborated by its commits — ignored", repo, n)
		return
	}
	gs := e.store.TakeEngagements(repo, n)
	if len(gs) == 0 {
		// Attribution happens at report time by joining this row to the PR's
		// earlier merged rows (same repo#n).
		e.store.Audit(map[string]any{"event": "outcome", "repo": repo, "number": n,
			"outcome": "reverted"})
		e.log("outcome: %s#%d reverted", repo, n)
		return
	}
	for _, g := range gs {
		e.recordOutcome(ctx, repo, n, "reverted", g)
	}
}

// recordOutcome writes one engagement's outcome row and feeds the loop.
func (e *Engine) recordOutcome(ctx context.Context, repo string, number int, outcome string, g store.Engagement) {
	e.store.Audit(map[string]any{"event": "outcome", "repo": repo, "number": number,
		"outcome": outcome, "agent": g.Agent, "workflow": g.Workflow,
		"saved_workflow": g.SavedWorkflow, "kind": g.Kind, "run": g.Run,
		"cost_usd": g.CostUSD, "tokens": g.Tokens})
	e.store.BumpOutcome(g.Agent, outcome)
	e.log("outcome: %s#%d %s (agent %s)", repo, number, outcome, g.Agent)

	// Saved-workflow delivery health (§11): merged is a delivery; reverted
	// takes one back. Non-terminal signals don't move it.
	if g.SavedWorkflow != "" {
		switch outcome {
		case "merged":
			flow.SavedWorkflows().RecordDelivery(g.SavedWorkflow, true)
		case "reverted":
			flow.SavedWorkflows().RecordDelivery(g.SavedWorkflow, false)
		}
	}

	// Shared memory (§9): a scoped note per terminal outcome, so recall and
	// memory-injected prompts can see what worked. Best-effort; respects the
	// memory manager's own guards.
	if m := memory.Active(); m != nil && (outcome == "merged" || outcome == "reverted" || outcome == "closed") {
		text := fmt.Sprintf("outcome: %s#%d %s (agent %s, workflow %s)", repo, number, outcome, g.Agent, orNone(g.Workflow))
		_, _ = m.Remember(text, []string{"outcome", outcome}, "repo:"+repo,
			memory.Source{Trigger: "outcome", Repo: repo, Agent: g.Agent, Run: g.Run})
	}
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// recordDecisionOutcome captures a review hand-off's terminal call (§17):
// approve → approved, discard → rejected. Revisions aren't terminal.
func (e *Engine) recordDecisionOutcome(t core.Trigger, agent, action string) {
	outcome := ""
	switch action {
	case "approve":
		outcome = "approved"
	case "discard":
		outcome = "rejected"
	default:
		return
	}
	e.store.Audit(map[string]any{"event": "outcome", "repo": t.Target.Repo,
		"number": t.Target.Number, "outcome": outcome, "agent": agent, "kind": t.Kind})
	e.store.BumpOutcome(agent, outcome)
}

// outcomeGuidance renders the optional per-profile tuning line (#36 §18):
// a profile with `outcome_feedback: true` gets a one-line track-record
// summary appended to its guidance, nudging the agent with its own history.
func (e *Engine) outcomeGuidance(agentName string, profile config.AgentProfile) string {
	if agentName == "" || !profile.OutcomeFeedback {
		return ""
	}
	st := e.store.AgentOutcomeStats(agentName)
	merged, reverted := st["merged"], st["reverted"]
	closed, rejected := st["closed"], st["rejected"]
	total := merged + closed + rejected
	if total == 0 && reverted == 0 {
		return ""
	}
	line := fmt.Sprintf("\n\nTRACK RECORD: of your last %d delivered changes, %d merged, %d were closed unmerged, %d rejected in review",
		total, merged, closed, rejected)
	if reverted > 0 {
		line += fmt.Sprintf(", and %d were later REVERTED — bias toward smaller, well-tested changes", reverted)
	}
	return line + "."
}
