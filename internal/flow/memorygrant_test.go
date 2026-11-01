package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/memory"
)

func trustedTrigger(repo string) core.Trigger {
	return core.Trigger{Target: core.Target{Repo: repo}, TargetTrusted: true}
}

// trustedFacts are the scope facts of a trusted dispatch on repo, plus extra.
func trustedFacts(repo string, extra map[string]any) map[string]any {
	tr := trustedTrigger(repo)
	tr.Target.Owner, tr.Target.Name, _ = strings.Cut(repo, "/")
	return memory.ScopeFacts(extra, tr, "review", "lens", nil)
}

// grantFor renders a one-verb grant over facts and builds its matcher.
func grantFor(list []string, facts map[string]any) memory.GrantMatcher {
	vs := map[string]map[string][]string{"memory.remember": {"scope": list}}
	return MemoryGrantFrom(vs, memory.RenderGrantScopes(vs, facts), "memory.remember")
}

func TestMemoryGrantMatcher(t *testing.T) {
	if MemoryGrantFrom(nil, nil, "memory.remember") != nil {
		t.Fatal("no grant, no restriction")
	}
	if MemoryGrantFrom(map[string]map[string][]string{"memory.remember": {"channel": {"#x"}}}, nil, "memory.remember") != nil {
		t.Fatal("a grant without a scope key restricts nothing")
	}
	facts := trustedFacts("acme/api", map[string]any{"inputs": map[string]any{"team": "payments"}})
	g := grantFor([]string{"{{.repo}}", "standard:${repo}", "emphasis:{{.repo}}", "team:{{.inputs.team}}", "org:{{.owner}}", "w:${workflow}/${step}", "team/*"}, facts)
	if g == nil {
		t.Fatal("a scope key restricts")
	}
	for _, ok := range []string{"acme/api", "standard:acme/api", "emphasis:acme/api", "team:payments", "org:acme", "w:review/lens", "team/notes"} {
		if !g(ok) {
			t.Errorf("%q should be admitted", ok)
		}
	}
	for _, no := range []string{"acme/web", "standard:acme/web", "repo:acme/api", "", "team/a/b", "{{.repo}}", "standard:${repo}", "team:", "team:billing"} {
		if g(no) {
			t.Errorf("%q must be refused", no)
		}
	}
	// A rendered fact matches literally, never as a glob — whatever fact it is.
	glob := trustedFacts("acme/api", map[string]any{"head_ref": "*", "inputs": map[string]any{"team": "pay?ents"}, "labels": []string{"[a-z]*"}})
	star := grantFor([]string{"branch:{{.head_ref}}", "team:{{.inputs.team}}", "label:{{index .labels 0}}"}, glob)
	for _, no := range []string{"branch:main", "team:payments", "label:x"} {
		if star(no) {
			t.Errorf("a fact carrying glob characters widened the grant to %q", no)
		}
	}
	for _, ok := range []string{"branch:*", "team:pay?ents", "label:[a-z]*"} {
		if !star(ok) {
			t.Errorf("the literal %q should match itself", ok)
		}
	}
	// A target the sender chose has no repo fact: the templated entries
	// match nothing; static ones still apply.
	forged := memory.ScopeFacts(nil, core.Trigger{Target: core.Target{Repo: "acme/api", Owner: "acme"}}, "", "", nil)
	fg := grantFor([]string{"standard:{{.repo}}", "{{.repo}}", "org:{{.owner}}", "fixed"}, forged)
	if fg("standard:acme/api") || fg("acme/api") || fg("standard:") || fg("org:acme") || fg("org:") || !fg("fixed") {
		t.Error("a forged target must not render into the grant")
	}
	// An empty list is a restriction that admits nothing.
	if none := grantFor(nil, facts); none == nil || none("acme/api") {
		t.Error("scope: [] must deny everything")
	}
	// A session minted without facts keeps only the static entries.
	vs := map[string]map[string][]string{"memory.remember": {"scope": {"{{.repo}}", "fixed"}}}
	if nf := MemoryGrantFrom(vs, nil, "memory.remember"); nf == nil || nf("acme/api") || nf("{{.repo}}") || !nf("fixed") {
		t.Error("an unrendered templated entry must admit nothing")
	}
}

// The skill surface end to end: a step whose grant lists its repo's
// standard/emphasis scopes writes those and nothing else, even with trust:
// full and an open daemon policy.
func TestSkillGrantRestrictsMemoryWrites(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
memory:
  type: memory
policy:
  agent_authored:
    trust: full
`)
	mgr := installTestMemory(t, cfg)
	r := newTestRunner(t, cfg, buildRegistry(t, cfg)).Runner
	scopes := map[string]map[string][]string{"memory.remember": {"scope": {"{{.repo}}", "standard:{{.repo}}", "emphasis:{{.repo}}"}}}
	id := SkillIdentity{Agent: "calibrate", Repo: "acme/api", TargetTrusted: true,
		Verbs: []string{"memory.remember", "memory.recall"}, Scopes: scopes,
		MemoryScopes: memory.RenderGrantScopes(scopes, trustedFacts("acme/api", nil))}
	for _, ok := range []string{"acme/api", "standard:acme/api", "emphasis:acme/api"} {
		if _, err := r.RunSkillVerb(context.Background(), id, "memory.remember", map[string]any{"text": "n", "scope": ok}); err != nil {
			t.Errorf("granted scope %q refused: %v", ok, err)
		}
	}
	for _, no := range []string{"acme/web", "standard:acme/web", "repo:acme/api", ""} {
		_, err := r.RunSkillVerb(context.Background(), id, "memory.remember", map[string]any{"text": "n", "scope": no})
		if err == nil || !strings.Contains(err.Error(), "skill.verbs scope list") {
			t.Errorf("scope %q outside the step grant: err=%v", no, err)
		}
	}
	got, _ := mgr.List()
	if len(got) != 3 {
		t.Fatalf("exactly the granted writes should persist, got %d", len(got))
	}
	// The grant is per verb: recall (no scope list) keeps the daemon policy.
	if _, err := r.RunSkillVerb(context.Background(), id, "memory.recall", map[string]any{"scope": "acme/web"}); err != nil {
		t.Errorf("recall is not restricted by a remember grant: %v", err)
	}
	// forget under a grant: `*` does not reach the reserved bucket.
	shared, err := mgr.Remember("shared standard", nil, "", memory.Source{})
	if err != nil {
		t.Fatal(err)
	}
	wide := SkillIdentity{Agent: "calibrate", Repo: "acme/api", TargetTrusted: true,
		Verbs: []string{"memory.forget"}, Scopes: map[string]map[string][]string{"memory.forget": {"scope": {"*", "global"}}}}
	if _, err := r.RunSkillVerb(context.Background(), wide, "memory.forget", map[string]any{"id": shared.ID}); err == nil {
		t.Error("a forget grant must never delete from the reserved bucket")
	}
	if _, found, _ := mgr.ScopeOf(shared.ID); !found {
		t.Error("the shared entry was deleted")
	}
	// A step without a scope list keeps the old behaviour.
	open := SkillIdentity{Agent: "x", Repo: "acme/api", TargetTrusted: true, Verbs: []string{"memory.remember"}}
	if _, err := r.RunSkillVerb(context.Background(), open, "memory.remember", map[string]any{"text": "n", "scope": "acme/web"}); err != nil {
		t.Errorf("an unrestricted grant under trust: full: %v", err)
	}
}

func TestEngineHarvestGrantFromStepSkill(t *testing.T) {
	sk := &config.SkillPolicy{Verbs: []string{"memory.*"},
		VerbScopes: map[string]map[string][]string{"memory.*": {"scope": {"standard:{{.repo}}"}}}}
	g := StepMemoryGrant(sk, "remember", trustedFacts("acme/api", nil))
	if g == nil || !g("standard:acme/api") || g("acme/api") {
		t.Fatal("the step's memory.* scope list should bind the output contract")
	}
	if StepMemoryGrant(nil, "remember", trustedFacts("acme/api", nil)) != nil {
		t.Fatal("no skill policy, no restriction")
	}
	if StepMemoryGrant(&config.SkillPolicy{Verbs: []string{"gh.*"}}, "remember", trustedFacts("acme/api", nil)) != nil {
		t.Fatal("a grant that does not scope memory restricts nothing")
	}
	// The list is per verb: a recall list does not restrict remember, and a
	// remember list binds remember only.
	split := &config.SkillPolicy{Verbs: []string{"memory.recall", "memory.remember"},
		VerbScopes: map[string]map[string][]string{
			"memory.recall":   {"scope": {"acme/api"}},
			"memory.remember": {"scope": {"standard:{{.repo}}"}}}}
	if g := StepMemoryGrant(split, "remember", trustedFacts("acme/api", nil)); g == nil || g("acme/api") || !g("standard:acme/api") {
		t.Fatal("remember must be bound by its own list, not recall's")
	}
	if g := StepMemoryGrant(split, "recall", trustedFacts("acme/api", nil)); g == nil || !g("acme/api") || g("standard:acme/api") {
		t.Fatal("recall must be bound by its own list")
	}
	// Patterns matching one verb merge as a union (Memory.md documents it):
	// `memory.*: {scope: []}` does not override a specific verb's list, and
	// verbs matched only by it admit nothing.
	union := &config.SkillPolicy{Verbs: []string{"memory.*", "memory.remember"},
		VerbScopes: map[string]map[string][]string{
			"memory.*":        {"scope": nil},
			"memory.remember": {"scope": {"standard:{{.repo}}"}}}}
	if g := StepMemoryGrant(union, "remember", trustedFacts("acme/api", nil)); g == nil || !g("standard:acme/api") {
		t.Fatal("a second matching pattern widens: remember admits its own list")
	}
	if g := StepMemoryGrant(union, "forget", trustedFacts("acme/api", nil)); g == nil || g("standard:acme/api") || g("acme/api") {
		t.Fatal("a verb matched only by `memory.*: {scope: []}` admits nothing")
	}
	var _ memory.GrantMatcher = g
}

// skill.verbs may scope a memory verb by `scope:` — memory's own dimension —
// and still may not name an option no admitted verb treats as a resource.
func TestValidateMemoryVerbScopes(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\nmemory:\n  type: memory\n")
	reg := buildRegistry(t, cfg)
	ok := &config.SkillPolicy{Verbs: []string{"memory.remember"},
		VerbScopes: map[string]map[string][]string{"memory.remember": {"scope": {"standard:{{.repo}}"}}}}
	if err := validateVerbScopes("step", ok, reg); err != nil {
		t.Fatalf("memory.remember {scope} must validate: %v", err)
	}
	bad := &config.SkillPolicy{Verbs: []string{"memory.remember"},
		VerbScopes: map[string]map[string][]string{"memory.remember": {"channel": {"#x"}}}}
	if err := validateVerbScopes("step", bad, reg); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("an unknown key on a memory verb must still be refused, naming what it offers: %v", err)
	}
	svc := &config.SkillPolicy{Verbs: []string{"svc.post"},
		VerbScopes: map[string]map[string][]string{"svc.post": {"scope": {"x"}}}}
	if err := validateVerbScopes("step", svc, reg); err == nil {
		t.Fatal("scope: is memory's dimension, not every connector's")
	}
}

// A tracked secret value a step output or the event context echoed is
// redacted before any key renders from it: a scope key is stored and shown
// in prompts.
func TestMemoryFactsRedactTrackedSecrets(t *testing.T) {
	sec := testSecrets(nil)
	const tok = "tok-a1b2c3d4e5f6g7h8"
	sec.Track(tok)
	r := &Runner{Secrets: sec}
	f := r.MemoryFacts(map[string]any{"inputs": map[string]any{"team": tok}}, trustedTrigger("acme/api"), "", "")
	got, err := memory.RenderScopeKey("team:{{.inputs.team}}", f)
	if err == nil && strings.Contains(got, tok) {
		t.Fatalf("a tracked secret rendered into a scope key: %q", got)
	}
}
