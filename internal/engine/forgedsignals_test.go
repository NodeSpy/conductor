package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/store"
)

// ROUND-13 #1. The engagement store keyed on a raw "repo#number" and
// observeOutcomeSignals read merged/reverts/reverts_corroborated straight off
// the trigger's Context — neither asked who assigned the target.
//
// A source plugin (or any untrusted source) could therefore emit
// kind:_closed for somebody else's repo#number and: consume that dispatch's
// engagements, record a merged/closed outcome against another agent's track
// record, and assert `reverts_corroborated` — the one bit that turns an
// attacker-editable revert claim into an actionable one (#36 review M9). The
// corroboration control is worthless if its carrier is forgeable.
func TestForgedSignalsCannotTouchATrustedEngagement(t *testing.T) {
	eng, st, _, _ := buildFlowEngine(t, gateCfg2())
	// A real dispatch acted on a real PR.
	real := core.Trigger{Source: "github", Instance: "i", Kind: "_closed",
		TargetTrusted: true, Target: core.Target{Repo: "o/r", PR: 5, Number: 5}}
	st.RecordEngagement(real.Key(), store.Engagement{Key: "fixer", Workflow: "eg.ping", CostUSD: 1.5})

	// The forgery: same repo and number, but the target came off a plugin's
	// wire event, so TargetTrusted is false.
	forged := core.Trigger{Source: "acme", Instance: "plug", Kind: "_closed",
		Target: core.Target{Repo: "o/r", PR: 5, Number: 5},
		Context: map[string]any{
			"merged": true, "reverts": []int{9}, "reverts_corroborated": true,
		},
	}
	eng.process(context.Background(), forged)

	if got := outcomesFrom(st); len(got) != 0 {
		t.Fatalf("a forged _closed recorded outcomes against another dispatch's record: %v", got)
	}
	if len(st.bumps) != 0 {
		t.Fatalf("a forged _closed moved a track record: %+v", st.bumps)
	}
	// The victim's engagement is untouched: not consumed, still there for the
	// real signal.
	if gs := st.PeekEngagements(real.Key()); len(gs) != 1 {
		t.Fatalf("the forged trigger consumed the real dispatch's engagements: %+v", gs)
	}
	// And no corroborated revert was forged.
	for _, a := range st.audits {
		if a["event"] == "outcome" && a["outcome"] == "reverted" {
			t.Fatalf("a forged trigger asserted a CORROBORATED revert: %+v", a)
		}
	}

	// The real signal still works — the fix is a gate, not a wall.
	eng.process(context.Background(), core.Trigger{Source: "github", Instance: "i",
		Kind: "_closed", TargetTrusted: true,
		Target:  core.Target{Repo: "o/r", PR: 5, Number: 5},
		Context: map[string]any{"merged": true}})
	if got := outcomesFrom(st); len(got) != 1 || got[0] != "merged:fixer" {
		t.Fatalf("the trusted signal must still record: %v", got)
	}
}

// …and even keyed alone, a forged trigger's own engagements live in their own
// namespace, so it cannot collide with the trusted record.
func TestForgedAndTrustedEngagementKeysDiffer(t *testing.T) {
	real := core.Trigger{Source: "github", TargetTrusted: true,
		Target: core.Target{Repo: "o/r", Number: 5}}
	forged := core.Trigger{Source: "acme", Instance: "plug",
		Target: core.Target{Repo: "o/r", Number: 5}}
	if real.Key() == forged.Key() {
		t.Fatalf("the engagement keys collide: %q", real.Key())
	}
}

// ROUND-13, found by the enforcement sweep: a remedy spends the instance's
// credentials on a write to the named repo. A target the sender chose would
// have conductor re-run CI in a repo of the attacker's choosing — so a
// declared remediation runs only for a target the platform assigned.
func TestRemediationRefusesAnUnassignedTarget(t *testing.T) {
	d := &fakeDispatcher{}
	e, _ := newEng(t, baseCfg(), d, &fakeNotifier{}, nil)
	ran := false
	e.invokeVerb = func(context.Context, string, string, map[string]any) (map[string]any, error) {
		ran = true
		return map[string]any{"status": "completed"}, nil
	}
	tr := agentTrigger("failing_checks", "victim/repo", 1, "h", "s", config.Action{Type: "agent", Agent: "w/fixer", FlakyRerun: config.FlakyRerun{Enabled: true}})
	tr.TargetTrusted = false
	tr.Context["run_id"] = int64(42)
	e.process(context.Background(), tr)
	if ran {
		t.Fatal("a forged target must not spend the instance's credentials on a remedy")
	}
}

// Finding #2: prompt recall. The default memory selector expands scope refs
// from the dispatch's repo, so a forged target pulled ANOTHER repo's memories
// into this agent's prompt — a cross-tenant read, and a prompt-injection
// vector, since recalled text lands in the model's context verbatim.
func TestPromptRecallUsesTheTrustedRepo(t *testing.T) {
	m := setupEngineMemory(t)
	if _, err := m.Remember("the victim's note", nil, "victim/repo",
		memory.Source{Step: "s", Repo: "victim/repo"}); err != nil {
		t.Fatal(err)
	}
	e := &Engine{}
	step := config.Step{Memory: &config.MemorySelector{Enabled: true, Scopes: []string{"${repo}"}}}
	for _, tc := range []struct {
		name    string
		trusted bool
		wantIn  bool
	}{
		{"a platform-assigned target recalls its own repo", true, true},
		{"a target the sender chose recalls nothing of that repo", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trig := core.Trigger{Source: "webhook", Instance: "hooks", Kind: "delivery",
				TargetTrusted: tc.trusted, Target: core.Target{Repo: "victim/repo", Number: 1}}
			got := e.memoryPrompt("step", step, trig, "wf")
			if strings.Contains(got, "the victim's note") != tc.wantIn {
				t.Fatalf("%s: recall=%q", tc.name, got)
			}
		})
	}
}
