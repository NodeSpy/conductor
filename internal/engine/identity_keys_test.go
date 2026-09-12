package engine

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/store"
)

// Outcome tracking is keyed by an opaque TRACK-RECORD KEY — the step
// identity by default, or an explicit outcome_key
// (docs/design/agents-removal.md §4). The point of a stable key is that
// feedback matches a step to its OWN past outcomes across runs and restarts.

func TestOutcomeKeyForDefaultsToIdentity(t *testing.T) {
	if got := OutcomeKeyFor("github.pull_request/security", config.Step{}); got != "github.pull_request/security" {
		t.Fatalf("default key = %q, want the identity", got)
	}
	if got := OutcomeKeyFor("a", config.Step{OutcomeKey: "reviewers"}); got != "reviewers" {
		t.Fatalf("explicit outcome_key must win, got %q", got)
	}
	// Several steps can deliberately pool one record.
	a := OutcomeKeyFor("x", config.Step{OutcomeKey: "reviewers"})
	b := OutcomeKeyFor("y", config.Step{OutcomeKey: "reviewers"})
	if a != b {
		t.Fatal("an explicit key is how steps pool a record")
	}
}

// The feedback line is looked up by that key, so a step sees its own history
// — and a step that did not opt in sees nothing.
func TestOutcomeGuidanceMatchesTheStepsOwnRecord(t *testing.T) {
	cfg := baseCfg()
	e, _ := newEng(t, cfg, &fakeDispatcher{}, &fakeNotifier{}, nil)
	e.store.BumpOutcome("reviewer", "merged")
	e.store.BumpOutcome("reviewer", "merged")
	e.store.BumpOutcome("reviewer", "reverted")
	e.store.BumpOutcome("other", "closed")

	line := e.outcomeGuidance("reviewer", config.Step{OutcomeFeedback: true})
	if !strings.Contains(line, "TRACK RECORD") || !strings.Contains(line, "2 merged") {
		t.Fatalf("feedback should carry this key's record: %q", line)
	}
	if !strings.Contains(line, "REVERTED") {
		t.Fatalf("a revert should be surfaced: %q", line)
	}
	// Another key's record must not leak in.
	if strings.Contains(line, "1 were closed") {
		t.Fatalf("another key's outcomes leaked: %q", line)
	}
	// No opt-in → nothing, no token cost.
	if got := e.outcomeGuidance("reviewer", config.Step{}); got != "" {
		t.Fatalf("without outcome_feedback the line must be empty, got %q", got)
	}
	// An explicit key redirects the lookup.
	e.store.BumpOutcome("pooled", "merged")
	if got := e.outcomeGuidance("reviewer", config.Step{OutcomeFeedback: true, OutcomeKey: "pooled"}); !strings.Contains(got, "1 merged") {
		t.Fatalf("explicit key not honored: %q", got)
	}
}

// An engagement carries the track-record key and the runtime it ran on, so
// the report can attribute both quality and spend.
func TestEngagementCarriesKeyAndRuntime(t *testing.T) {
	cfg := baseCfg()
	e, _ := newEng(t, cfg, &fakeDispatcher{}, &fakeNotifier{}, nil)
	e.store.RecordEngagement(store.TargetKey("o/r", 7), store.Engagement{
		Key: "github.pull_request/security", Runtime: "paseo", Kind: "pull_request",
	})
	got := e.store.PeekEngagements(store.TargetKey("o/r", 7))
	if len(got) != 1 || got[0].Key != "github.pull_request/security" || got[0].Runtime != "paseo" {
		t.Fatalf("engagement = %+v", got)
	}
	// An engagement with no key is not recorded (there would be nothing to
	// attribute it to).
	e.store.RecordEngagement(store.TargetKey("o/r", 8), store.Engagement{Runtime: "paseo"})
	if n := len(e.store.PeekEngagements(store.TargetKey("o/r", 8))); n != 0 {
		t.Fatalf("a keyless engagement must not record, got %d", n)
	}
}

// --- budget on the runtime (§1) -------------------------------------------

func TestBudgetScopeIsTheRuntime(t *testing.T) {
	cfg := baseCfg()
	cfg.Runtimes = config.RuntimeSet{
		"paseo": {Use: "paseo", Default: true},
		"gpu":   {Use: "paseo", Budget: &config.BudgetPolicy{MaxTokens: 10}},
	}
	e, _ := newEng(t, cfg, &fakeDispatcher{}, &fakeNotifier{}, nil)

	// A step pinned to the capped runtime is governed by it.
	scopes := e.budgetScopes("gpu", nil, "")
	if len(scopes) != 1 || scopes[0].key != "runtime:gpu" {
		t.Fatalf("scopes = %+v, want runtime:gpu", scopes)
	}
	// An uncapped runtime contributes no scope.
	if got := e.budgetScopes("paseo", nil, ""); len(got) != 0 {
		t.Fatalf("an uncapped runtime governs nothing, got %+v", got)
	}
	// runtimeOf: the step's pin, else the default runtime.
	if got := e.runtimeOf(config.Step{Runtime: "gpu"}); got != "gpu" {
		t.Fatalf("pinned runtime = %q", got)
	}
	if got := e.runtimeOf(config.Step{}); got != "paseo" {
		t.Fatalf("unpinned should take the default runtime, got %q", got)
	}
}

func TestChargeScopesUseTheRuntime(t *testing.T) {
	got := chargeScopes("gpu", "gh.pr/review")
	want := []string{"global", "runtime:gpu", "workflow:gh.pr/review"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("charge scopes = %v, want %v", got, want)
	}
	if got := chargeScopes("", ""); len(got) != 1 || got[0] != "global" {
		t.Fatalf("no runtime/workflow leaves only global, got %v", got)
	}
}
