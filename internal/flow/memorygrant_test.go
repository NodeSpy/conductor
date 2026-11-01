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

func TestMemoryGrantMatcher(t *testing.T) {
	r := &Runner{}
	if r.MemoryGrant(nil, trustedTrigger("acme/api")) != nil {
		t.Fatal("no grant, no restriction")
	}
	if r.MemoryGrant(map[string][]string{"channel": {"#x"}}, trustedTrigger("acme/api")) != nil {
		t.Fatal("a grant without a scope key restricts nothing")
	}
	g := r.MemoryGrant(map[string][]string{"scope": {"{{.repo}}", "standard:${repo}", "emphasis:{{.repo}}", "team/*"}}, trustedTrigger("acme/api"))
	if g == nil {
		t.Fatal("a scope key restricts")
	}
	for _, ok := range []string{"acme/api", "standard:acme/api", "emphasis:acme/api", "team/notes"} {
		if !g(ok) {
			t.Errorf("%q should be admitted", ok)
		}
	}
	for _, no := range []string{"acme/web", "standard:acme/web", "repo:acme/api", "", "team/a/b", "{{.repo}}", "standard:${repo}"} {
		if g(no) {
			t.Errorf("%q must be refused", no)
		}
	}
	// A rendered fact matches literally, never as a glob.
	star := r.MemoryGrant(map[string][]string{"scope": {"standard:{{.repo}}"}}, trustedTrigger("*"))
	if star("standard:acme/api") {
		t.Error("a fact that renders to a glob must not widen the entry")
	}
	// A target the sender chose has no repo fact: the templated entries
	// render empty and match nothing; static ones still apply.
	forged := r.MemoryGrant(map[string][]string{"scope": {"standard:{{.repo}}", "{{.repo}}", "fixed"}},
		core.Trigger{Target: core.Target{Repo: "acme/api"}})
	if forged("standard:acme/api") || forged("acme/api") || forged("standard:") || !forged("fixed") {
		t.Error("a forged target must not render its repo into the grant")
	}
	// An empty list is a restriction that admits nothing.
	none := r.MemoryGrant(map[string][]string{"scope": nil}, trustedTrigger("acme/api"))
	if none == nil || none("acme/api") {
		t.Error("scope: [] must deny everything")
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
	id := SkillIdentity{Agent: "calibrate", Repo: "acme/api", TargetTrusted: true,
		Verbs:  []string{"memory.remember", "memory.recall"},
		Scopes: map[string]map[string][]string{"memory.remember": {"scope": {"{{.repo}}", "standard:{{.repo}}", "emphasis:{{.repo}}"}}}}
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
	// A step without a scope list keeps the old behaviour.
	open := SkillIdentity{Agent: "x", Repo: "acme/api", TargetTrusted: true, Verbs: []string{"memory.remember"}}
	if _, err := r.RunSkillVerb(context.Background(), open, "memory.remember", map[string]any{"text": "n", "scope": "acme/web"}); err != nil {
		t.Errorf("an unrestricted grant under trust: full: %v", err)
	}
}

func TestEngineHarvestGrantFromStepSkill(t *testing.T) {
	r := &Runner{}
	sk := &config.SkillPolicy{Verbs: []string{"memory.*"},
		VerbScopes: map[string]map[string][]string{"memory.*": {"scope": {"standard:{{.repo}}"}}}}
	g := r.StepMemoryGrant(sk, "remember", trustedTrigger("acme/api"))
	if g == nil || !g("standard:acme/api") || g("acme/api") {
		t.Fatal("the step's memory.* scope list should bind the output contract")
	}
	if r.StepMemoryGrant(nil, "remember", trustedTrigger("acme/api")) != nil {
		t.Fatal("no skill policy, no restriction")
	}
	if r.StepMemoryGrant(&config.SkillPolicy{Verbs: []string{"gh.*"}}, "remember", trustedTrigger("acme/api")) != nil {
		t.Fatal("a grant that does not scope memory restricts nothing")
	}
	// The list is per verb: a recall list does not restrict remember, and a
	// remember list binds remember only.
	split := &config.SkillPolicy{Verbs: []string{"memory.recall", "memory.remember"},
		VerbScopes: map[string]map[string][]string{
			"memory.recall":   {"scope": {"acme/api"}},
			"memory.remember": {"scope": {"standard:{{.repo}}"}}}}
	if g := r.StepMemoryGrant(split, "remember", trustedTrigger("acme/api")); g == nil || g("acme/api") || !g("standard:acme/api") {
		t.Fatal("remember must be bound by its own list, not recall's")
	}
	if g := r.StepMemoryGrant(split, "recall", trustedTrigger("acme/api")); g == nil || !g("acme/api") || g("standard:acme/api") {
		t.Fatal("recall must be bound by its own list")
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
