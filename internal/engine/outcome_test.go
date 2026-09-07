package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/store"
)

// closedTrigger builds the github connector's `_closed` signal.
func closedTrigger(repo string, n int, merged bool, reverts []any) core.Trigger {
	ctx := map[string]any{"merged": merged}
	if reverts != nil {
		ctx["reverts"] = reverts
	}
	return core.Trigger{Source: "github", Instance: "i", Kind: core.KindClosed,
		Target: core.Target{Repo: repo, PR: n, Number: n}, Context: ctx}
}

func outcomesFrom(st *flowGateStore) []string {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []string
	for _, a := range st.audits {
		if a["event"] == "outcome" {
			out = append(out, a["outcome"].(string)+":"+a["agent"].(string))
		}
	}
	return out
}

func TestOutcomeLoopMergedAndReverted(t *testing.T) {
	eng, st, _, _ := buildFlowEngine(t, gateCfg2())
	// An agent acted on PR 5 (the engagement the flow service records).
	st.RecordEngagement("o/r", 5, store.Engagement{Agent: "fixer", Workflow: "eg.ping", SavedWorkflow: "", CostUSD: 1.5})

	// Merge signal → terminal outcome, engagements consumed.
	eng.process(context.Background(), closedTrigger("o/r", 5, true, nil))
	if got := outcomesFrom(st); len(got) != 1 || got[0] != "merged:fixer" {
		t.Fatalf("merge outcomes: %v", got)
	}
	if st.bumps["fixer"]["merged"] != 1 {
		t.Fatalf("stats: %+v", st.bumps)
	}
	if len(st.TakeEngagements("o/r", 5)) != 0 {
		t.Fatal("terminal outcome must consume engagements")
	}

	// A merged revert PR reverts #5: the merge consumed #5's engagements, so
	// a bare reverted row still lands for the report's join.
	eng.process(context.Background(), closedTrigger("o/r", 90, true, []any{5}))
	found := false
	st.mu.Lock()
	for _, a := range st.audits {
		if a["event"] == "outcome" && a["outcome"] == "reverted" && a["number"] == 5 {
			found = true
		}
	}
	st.mu.Unlock()
	if !found {
		t.Fatal("revert row missing")
	}
}

func TestOutcomeLoopClosedUnmergedAndCI(t *testing.T) {
	eng, st, _, _ := buildFlowEngine(t, gateCfg2())
	st.RecordEngagement("o/r", 7, store.Engagement{Agent: "fixer"})

	// A CI failure is non-terminal: outcome row, engagements kept.
	ci := core.Trigger{Source: "github", Instance: "i", Kind: "failing_checks",
		Target: core.Target{Repo: "o/r", PR: 7, Number: 7}, Context: map[string]any{}}
	eng.observeOutcomeSignals(context.Background(), ci)
	if got := outcomesFrom(st); len(got) != 1 || got[0] != "ci_failed:fixer" {
		t.Fatalf("ci outcomes: %v", got)
	}
	if len(st.PeekEngagements("o/r", 7)) != 1 {
		t.Fatal("ci_failed must not consume engagements")
	}

	// Closed without merging → "closed" (rejected posture), consumed.
	eng.process(context.Background(), closedTrigger("o/r", 7, false, nil))
	got := outcomesFrom(st)
	if got[len(got)-1] != "closed:fixer" {
		t.Fatalf("closed outcomes: %v", got)
	}
}

func TestDecisionOutcomeAndGuidance(t *testing.T) {
	eng, st, _, _ := buildFlowEngine(t, gateCfg2())
	tr := flowTrigger("d")
	eng.recordDecisionOutcome(tr, "reviewer", "approve")
	eng.recordDecisionOutcome(tr, "reviewer", "discard")
	eng.recordDecisionOutcome(tr, "reviewer", "revise") // not terminal
	if st.bumps["reviewer"]["approved"] != 1 || st.bumps["reviewer"]["rejected"] != 1 {
		t.Fatalf("decision stats: %+v", st.bumps["reviewer"])
	}

	// Guidance tuning is opt-in per profile and summarizes the counters.
	st.BumpOutcome("fixer", "merged")
	st.BumpOutcome("fixer", "merged")
	st.BumpOutcome("fixer", "reverted")
	off := eng.outcomeGuidance("fixer", config.AgentProfile{})
	if off != "" {
		t.Fatalf("opt-out must be empty: %q", off)
	}
	on := eng.outcomeGuidance("fixer", config.AgentProfile{OutcomeFeedback: true})
	if !strings.Contains(on, "TRACK RECORD") || !strings.Contains(on, "2 merged") ||
		!strings.Contains(on, "1 were later REVERTED") {
		t.Fatalf("guidance: %q", on)
	}
	// No history → no line, even opted in.
	if g := eng.outcomeGuidance("ghost", config.AgentProfile{OutcomeFeedback: true}); g != "" {
		t.Fatalf("no-history guidance: %q", g)
	}
}

// gateCfg2 is a minimal flow-engine config for outcome tests.
func gateCfg2() string {
	return `
connectors:
  eg: { type: enginegate }
triggers:
  - on: eg.ping
    steps:
      - { id: p, uses: eg.post, options: { text: "x" } }
`
}
