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
		ctx["reverts_corroborated"] = true
	}
	// A github `_closed`: a signature-verified payload assigned this target,
	// which is what makes its outcome signals actionable at all (round-13).
	return core.Trigger{Source: "github", Instance: "i", Kind: core.KindClosed,
		TargetTrusted: true,
		Target:        core.Target{Repo: repo, PR: n, Number: n}, Context: ctx}
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
	st.RecordEngagement(store.TargetKey("o/r", 5), store.Engagement{Key: "fixer", Workflow: "eg.ping", SavedWorkflow: "", CostUSD: 1.5})

	// Merge signal → terminal outcome, engagements consumed.
	eng.process(context.Background(), closedTrigger("o/r", 5, true, nil))
	if got := outcomesFrom(st); len(got) != 1 || got[0] != "merged:fixer" {
		t.Fatalf("merge outcomes: %v", got)
	}
	if st.bumps["fixer"]["merged"] != 1 {
		t.Fatalf("stats: %+v", st.bumps)
	}
	if len(st.TakeEngagements(store.TargetKey("o/r", 5))) != 0 {
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
	st.RecordEngagement(store.TargetKey("o/r", 7), store.Engagement{Key: "fixer"})

	// A CI failure is non-terminal: outcome row, engagements kept.
	ci := core.Trigger{Source: "github", Instance: "i", Kind: "failing_checks",
		TargetTrusted: true,
		Target:        core.Target{Repo: "o/r", PR: 7, Number: 7}, Context: map[string]any{}}
	eng.observeOutcomeSignals(context.Background(), ci)
	if got := outcomesFrom(st); len(got) != 1 || got[0] != "ci_failed:fixer" {
		t.Fatalf("ci outcomes: %v", got)
	}
	if len(st.PeekEngagements(store.TargetKey("o/r", 7))) != 1 {
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
	off := eng.outcomeGuidance("fixer", config.Step{})
	if off != "" {
		t.Fatalf("opt-out must be empty: %q", off)
	}
	on := eng.outcomeGuidance("fixer", config.Step{OutcomeFeedback: true})
	if !strings.Contains(on, "TRACK RECORD") || !strings.Contains(on, "2 merged") ||
		!strings.Contains(on, "1 were later REVERTED") {
		t.Fatalf("guidance: %q", on)
	}
	// No history → no line, even opted in.
	if g := eng.outcomeGuidance("ghost", config.Step{OutcomeFeedback: true}); g != "" {
		t.Fatalf("no-history guidance: %q", g)
	}
}

// Regression: a fail-fast CI matrix fans one failed push out into dozens of
// failing_checks triggers (one job fails, its siblings cancel — cancelled is a
// failure conclusion). observeOutcomeSignals runs before any dedup gate, so
// ci_failed must dedup on the head itself: once per push, not once per check
// event. Real evidence: EdnitionCode/RosterStream#5376 took 23 identical
// ci_failed rows in ~70s for one head. A fresh push (new head) records anew.
func TestCIFailedOncePerHead(t *testing.T) {
	eng, st, _, _ := buildFlowEngine(t, gateCfg2())
	st.RecordEngagement(store.TargetKey("o/r", 5376), store.Engagement{Key: "fixer"})

	ciAt := func(head string) core.Trigger {
		return core.Trigger{Source: "github", Instance: "i", Kind: "failing_checks",
			TargetTrusted: true,
			Target:        core.Target{Repo: "o/r", PR: 5376, Number: 5376, HeadSHA: head},
			Context:       map[string]any{}}
	}

	// 23 failing_checks on the same head → exactly one ci_failed row / one bump.
	for i := 0; i < 23; i++ {
		eng.observeOutcomeSignals(context.Background(), ciAt("headA"))
	}
	if got := outcomesFrom(st); len(got) != 1 || got[0] != "ci_failed:fixer" {
		t.Fatalf("same head must record once, got: %v", got)
	}
	if st.bumps["fixer"]["ci_failed"] != 1 {
		t.Fatalf("same head must bump once: %+v", st.bumps["fixer"])
	}

	// A fresh push (new head) that fails is a distinct CI failure → a second row.
	eng.observeOutcomeSignals(context.Background(), ciAt("headB"))
	if got := outcomesFrom(st); len(got) != 2 {
		t.Fatalf("new head must record a second row, got: %v", got)
	}
	if st.bumps["fixer"]["ci_failed"] != 2 {
		t.Fatalf("new head must bump again: %+v", st.bumps["fixer"])
	}

	// ci_failed is non-terminal: engagements are never consumed.
	if len(st.PeekEngagements(store.TargetKey("o/r", 5376))) != 1 {
		t.Fatal("ci_failed must not consume engagements")
	}
}

// gateCfg2 is a minimal flow-engine config for outcome tests.
func gateCfg2() string {
	return `
connectors:
  eg: { use: enginegate }
triggers:
  - on: eg.ping
    steps:
      - { id: p, uses: eg.post, options: { text: "x" } }
`
}

// Regression (#36 review M9): an uncorroborated revert claim (title/body
// text anyone can edit) is recorded for the operator but acts on nothing —
// no reverted outcome, no per-agent bump, no engagement consumed, no
// workflow rot.
func TestUncorroboratedRevertClaimIsInert(t *testing.T) {
	eng, st, _, _ := buildFlowEngine(t, gateCfg2())
	st.RecordEngagement(store.TargetKey("o/r", 5), store.Engagement{Key: "fixer", Workflow: "eg.ping"})

	tr := closedTrigger("o/r", 90, true, []any{5})
	tr.Context["reverts_corroborated"] = false
	eng.process(context.Background(), tr)

	st.mu.Lock()
	var reverted, unconfirmed bool
	for _, a := range st.audits {
		if a["event"] != "outcome" {
			continue
		}
		switch a["outcome"] {
		case "reverted":
			reverted = true
		case "reverted_unconfirmed":
			if a["number"] == 5 {
				unconfirmed = true
			}
		}
	}
	bumped := st.bumps["fixer"]["reverted"]
	st.mu.Unlock()
	if reverted {
		t.Fatal("uncorroborated claim must not produce a reverted outcome")
	}
	if !unconfirmed {
		t.Fatal("the claim must still leave an audit trail (reverted_unconfirmed)")
	}
	if bumped != 0 {
		t.Fatalf("uncorroborated claim must not bump agent stats: %d", bumped)
	}
	if len(st.PeekEngagements(store.TargetKey("o/r", 5))) != 1 {
		t.Fatal("uncorroborated claim must not consume engagements")
	}
}
