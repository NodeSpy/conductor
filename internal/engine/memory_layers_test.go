package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/store"
)

// Outcome notes are filed under outcome:<repo>, out of the repo key every
// opted-in step recalls by default — one note per merged PR was burying a
// busy repo's learned entries under the recall limit.
func TestOutcomeNotesStayOutOfTheRepoScope(t *testing.T) {
	m := setupEngineMemory(t)
	e, _ := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	for _, outcome := range []string{"merged", "reverted", "closed"} {
		e.recordOutcome(context.Background(), "acme/api", 7, outcome, store.Engagement{Key: "fixer", Workflow: "fix"})
	}
	e.recordOutcome(context.Background(), "acme/api", 8, "ci_failed", store.Engagement{Key: "fixer"})
	if got, _ := m.Recall(memory.Query{Scopes: []string{"acme/api"}}); len(got) != 0 {
		t.Fatalf("outcome notes landed in the repo scope: %v", got)
	}
	got, _ := m.Recall(memory.Query{Scopes: []string{"outcome:acme/api"}, Tags: []string{"outcome"}})
	if len(got) != 3 {
		t.Fatalf("want the three terminal outcomes under outcome:acme/api, got %d", len(got))
	}
	if OutcomeScope("acme/api") != "outcome:acme/api" {
		t.Fatal("OutcomeScope")
	}
	// And so a default recall of the repo shows learned entries, not
	// outcome noise.
	if _, err := m.Remember("learned: tests need the fake clock", nil, "acme/api", memory.Source{}); err != nil {
		t.Fatal(err)
	}
	step := config.Step{Memory: &config.MemorySelector{Enabled: true, Limit: 1}}
	trig := core.Trigger{Source: "github", Kind: "pr", TargetTrusted: true, Target: core.Target{Repo: "acme/api", Number: 1}}
	if p := e.memoryPrompt("lens", step, trig, "review", nil); !strings.Contains(p, "fake clock") || strings.Contains(p, "outcome:") {
		t.Fatalf("default recall should be the learned entry:\n%s", p)
	}
}

func TestMemoryPromptExtraAndTemplatedScopes(t *testing.T) {
	m := setupEngineMemory(t)
	for _, n := range []struct{ text, scope string }{
		{"shared taste", ""},
		{"repo fact", "acme/api"},
		{"api standard", "standard:acme/api"},
		{"api emphasis", "emphasis:acme/api"},
		{"web standard", "standard:acme/web"},
	} {
		if _, err := m.Remember(n.text, nil, n.scope, memory.Source{}); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{}
	trusted := core.Trigger{Source: "github", Kind: "pr", TargetTrusted: true, Target: core.Target{Repo: "acme/api", Number: 1}}
	noRepo := core.Trigger{Source: "cron", Kind: "tick"}

	extra := config.Step{Memory: &config.MemorySelector{Enabled: true, ExtraScopes: []string{"standard:${repo}", "emphasis:${repo}"}}}
	p := e.memoryPrompt("lens", extra, trusted, "review", nil)
	for _, want := range []string{"shared taste", "repo fact", "api standard", "api emphasis"} {
		if !strings.Contains(p, want) {
			t.Errorf("extra_scopes should add to the defaults; missing %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "web standard") {
		t.Errorf("another repo's standard leaked:\n%s", p)
	}
	// No repo: the templated extras drop rather than recalling a literal or
	// a bare `standard:` key; the defaults still apply.
	p = e.memoryPrompt("lens", extra, noRepo, "review", nil)
	if !strings.Contains(p, "shared taste") || strings.Contains(p, "standard") || strings.Contains(p, "emphasis") {
		t.Errorf("no-repo run:\n%s", p)
	}
	// Explicit scopes that all drop recall nothing — not the defaults.
	only := config.Step{Memory: &config.MemorySelector{Enabled: true, Scopes: []string{"standard:${repo}"}}}
	if p := e.memoryPrompt("lens", only, noRepo, "review", nil); p != "" {
		t.Errorf("explicit scopes that cannot resolve must not fall back to the defaults:\n%s", p)
	}
	if p := e.memoryPrompt("lens", only, trusted, "review", nil); !strings.Contains(p, "api standard") || strings.Contains(p, "repo fact") {
		t.Errorf("templated explicit scope:\n%s", p)
	}

	// Any fact names a key, not just the repo: a team the workflow was
	// called with, an owner, a label from the event.
	for _, n := range []struct{ text, scope string }{
		{"payments team", "team:payments"},
		{"billing team", "team:billing"},
		{"acme org", "org:acme"},
		{"backend label", "label:backend"},
	} {
		if _, err := m.Remember(n.text, nil, n.scope, memory.Source{}); err != nil {
			t.Fatal(err)
		}
	}
	trusted.Target.Owner = "acme"
	trusted.Context = map[string]any{"labels": []string{"backend"}}
	facts := e.memoryFacts(map[string]any{"inputs": map[string]any{"team": "payments"}}, trusted, "review", "lens")
	keyed := config.Step{Memory: &config.MemorySelector{Enabled: true,
		Scopes: []string{"team:{{.inputs.team}}", "org:{{.owner}}", "label:{{index .labels 0}}", "x:{{.inputs.missing}}"}}}
	p = e.memoryPrompt("lens", keyed, trusted, "review", facts)
	for _, want := range []string{"payments team", "acme org", "backend label"} {
		if !strings.Contains(p, want) {
			t.Errorf("a key over %q should recall it:\n%s", want, p)
		}
	}
	if strings.Contains(p, "billing team") || strings.Contains(p, "repo fact") {
		t.Errorf("only the rendered keys apply:\n%s", p)
	}
}

// The ```remember output contract is held to the step's skill.verbs scope
// list for memory.remember, the same as the verb.
func TestHarvestHonoursTheStepsMemoryGrant(t *testing.T) {
	m := setupEngineMemory(t)
	e, st := newEng(t, baseCfg(), &fakeDispatcher{}, &fakeNotifier{}, nil)
	e.flow = flow.New(flow.Runner{Cfg: baseCfg(), Store: st})
	trig := core.Trigger{Source: "github", Kind: "pr", TargetTrusted: true, Target: core.Target{Repo: "acme/api", Number: 3}}
	step := config.Step{
		Memory: &config.MemorySelector{Enabled: true},
		Skill: &config.SkillPolicy{Verbs: []string{"memory.remember"},
			VerbScopes: map[string]map[string][]string{"memory.remember": {"scope": {"standard:{{.repo}}"}}}},
	}
	e.harvestMemory(trig, "calibrate", "r1", "```remember\n- text: other repo\n  scope: standard:acme/web\n```\n", step, nil)
	if got, _ := m.List(); len(got) != 0 {
		t.Fatalf("a note outside the step's grant was harvested: %v", got)
	}
	e.harvestMemory(trig, "calibrate", "r1", "```remember\n- text: kept\n  scope: standard:acme/api\n```\n", step, nil)
	if got, _ := m.List(); len(got) != 1 || got[0].Scope != "standard:acme/api" {
		t.Fatalf("the granted note should be harvested: %v", got)
	}
	// Without a grant, the output contract works as before.
	step.Skill = nil
	e.harvestMemory(trig, "calibrate", "r1", "```remember\n- text: plain\n```\n", step, nil)
	if got, _ := m.List(); len(got) != 2 {
		t.Fatalf("ungranted harvest: %v", got)
	}
}
