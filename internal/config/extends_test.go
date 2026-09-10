package config

import (
	"reflect"
	"strings"
	"testing"
)

func gspec(parts ...string) *GuidanceSpec { return &GuidanceSpec{Parts: parts} }

// A team role is a NAME, so the runtime has to join the synthesized role
// step to the `steps:` entry it addresses. That join uses the same merge
// policy as every `extends:` — this is where those rules are pinned for
// steps now that steps have no `extends:` of their own.
func TestResolveStepRefMergePolicy(t *testing.T) {
	c := &Config{Steps: map[string]Step{
		"base": {
			Model: ModelSpecOf("opus"), Workspace: "worktree",
			Labels:   map[string]string{"team": "autopilot", "tier": "base"},
			Guidance: gspec("house tone"),
		},
	}}
	role := Step{
		Model:    ModelSpecOf("sonnet"), // the caller's value wins
		Labels:   map[string]string{"tier": "fixer", "role": "ci"},
		Guidance: gspec("you fix CI"),
	}
	if err := c.ResolveStepRef("base", &role); err != nil {
		t.Fatalf("ResolveStepRef: %v", err)
	}
	if role.Model.Ref != "sonnet" {
		t.Errorf("scalar override: model = %q, want sonnet", role.Model.Ref)
	}
	if role.Workspace != "worktree" {
		t.Errorf("scalar inherit: workspace = %q, want worktree", role.Workspace)
	}
	// Labels deep-merge: caller's keys win, the entry's missing ones added.
	want := map[string]string{"team": "autopilot", "tier": "fixer", "role": "ci"}
	if !reflect.DeepEqual(role.Labels, want) {
		t.Errorf("labels deep-merge = %v, want %v", role.Labels, want)
	}
	// Guidance stacks: the named step's tone under the role's own.
	if got := role.Guidance.Parts; !reflect.DeepEqual(got, []string{"house tone", "you fix CI"}) {
		t.Errorf("guidance stack = %v, want [house tone, you fix CI]", got)
	}
	// The registry entry is untouched.
	if b := c.Steps["base"]; b.Model.Ref != "opus" || len(b.Guidance.Parts) != 1 {
		t.Errorf("registry entry mutated: %+v", b)
	}
}

// The role takes the entry's NAME, which is the whole point of naming it:
// every team using `base` as its planner shares one identity. A role that
// pins its own name keeps it.
func TestResolveStepRefIdentity(t *testing.T) {
	c := &Config{Steps: map[string]Step{"base": {Model: ModelSpecOf("opus")}}}
	var role Step
	if err := c.ResolveStepRef("base", &role); err != nil {
		t.Fatal(err)
	}
	if role.Name != "base" {
		t.Errorf("role name = %q, want base", role.Name)
	}
	pinned := Step{Name: "mine"}
	if err := c.ResolveStepRef("base", &pinned); err != nil {
		t.Fatal(err)
	}
	if pinned.Name != "mine" {
		t.Errorf("a pinned name must survive, got %q", pinned.Name)
	}
}

func TestResolveStepRefGuidanceReplace(t *testing.T) {
	c := &Config{Steps: map[string]Step{"base": {Guidance: gspec("house tone")}}}
	role := Step{Guidance: &GuidanceSpec{Parts: []string{"only mine"}, Replace: true}}
	if err := c.ResolveStepRef("base", &role); err != nil {
		t.Fatal(err)
	}
	// { replace } does not inherit the entry's parts.
	if p := role.Guidance.Parts; !reflect.DeepEqual(p, []string{"only mine"}) {
		t.Errorf("replace should not inherit, got %v", p)
	}
}

func TestResolveStepRefUnknown(t *testing.T) {
	c := &Config{Steps: map[string]Step{"base": {}}}
	var role Step
	err := c.ResolveStepRef("nope", &role)
	if err == nil || !strings.Contains(err.Error(), "names no entry") {
		t.Fatalf("want an unknown-step-ref error, got %v", err)
	}
}

// Steps have no `extends:` at all any more — reuse is a YAML anchor, and a
// leftover `extends:` on a step is a plain unknown-key load error.
func TestStepsHaveNoExtendsKey(t *testing.T) {
	var c Config
	err := strictUnmarshal([]byte("workflows:\n  w: { steps: [{ id: a, extends: base }] }\n"), &c)
	if err == nil || !strings.Contains(err.Error(), "extends") {
		t.Fatalf("a step-level extends: must be rejected, got %v", err)
	}
}

func TestResolveExtendsChainAndCycle(t *testing.T) {
	// A chain resolves root→leaf across a generic map section.
	c := &Config{Runtimes: map[string]RuntimeConfig{
		"a": {Use: "cli", Command: []string{"claude"}},
		"b": {Extends: "a", Host: "build-box"},
		"c": {Extends: "b", Bin: "/usr/bin/claude"},
	}}
	if err := c.resolveExtends(); err != nil {
		t.Fatalf("resolveExtends: %v", err)
	}
	if got := c.Runtimes["c"]; got.Use != "cli" || got.Host != "build-box" || got.Bin != "/usr/bin/claude" {
		t.Errorf("chain inherit: %+v", got)
	}
	// A cycle is a load error, not a hang.
	cyc := &Config{Runtimes: map[string]RuntimeConfig{"a": {Extends: "b"}, "b": {Extends: "a"}}}
	if err := cyc.resolveExtends(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected a cycle error, got %v", err)
	}
	// So is an unknown parent.
	unk := &Config{Runtimes: map[string]RuntimeConfig{"a": {Extends: "nope"}}}
	if err := unk.resolveExtends(); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("expected an unknown-target error, got %v", err)
	}
}

func TestResolveTriggerExtends(t *testing.T) {
	c := &Config{Triggers: []TriggerSpec{
		{
			Name:     "review-base",
			Abstract: true,
			Filters:  map[string]any{"gates": map[string]any{"not_draft": true}},
			Steps:    []Step{{ID: "r", Type: "agent", Agent: "reviewer", Prompt: "review"}},
		},
		{On: "gh.review_requested", Extends: "review-base", Filters: map[string]any{"repos": []any{"org/a"}}},
		{On: "gh.review_requested", Extends: "review-base", Filters: map[string]any{"repos": []any{"org/b"}}},
	}}
	if err := c.resolveTriggerExtends(); err != nil {
		t.Fatalf("resolveTriggerExtends: %v", err)
	}
	// The abstract base is stripped; only the two concrete children remain.
	if len(c.Triggers) != 2 {
		t.Fatalf("abstract base should be stripped, got %d triggers", len(c.Triggers))
	}
	for _, tr := range c.Triggers {
		if tr.On != "gh.review_requested" {
			t.Errorf("child On = %q", tr.On)
		}
		// Steps inherited from the base.
		if len(tr.Steps) != 1 || tr.Steps[0].Agent != "reviewer" {
			t.Errorf("child should inherit base steps, got %+v", tr.Steps)
		}
		// Filters deep-merge: base's gates + the child's own repos.
		if _, ok := tr.Filters["gates"]; !ok {
			t.Errorf("child should inherit base filter 'gates', got %v", tr.Filters)
		}
		if _, ok := tr.Filters["repos"]; !ok {
			t.Errorf("child should keep its own filter 'repos', got %v", tr.Filters)
		}
	}
}

func TestResolveTriggerExtendsInheritsOn(t *testing.T) {
	c := &Config{Triggers: []TriggerSpec{
		{Name: "base", Abstract: true, On: "gh.new_comment", Steps: []Step{{ID: "s", Type: "agent", Agent: "a", Prompt: "x"}}},
		{Name: "child", Extends: "base"}, // no on: — inherits the base's
	}}
	if err := c.resolveTriggerExtends(); err != nil {
		t.Fatalf("resolveTriggerExtends: %v", err)
	}
	if len(c.Triggers) != 1 || c.Triggers[0].On != "gh.new_comment" {
		t.Fatalf("child should inherit base On, got %+v", c.Triggers)
	}
}

func TestResolveTriggerExtendsErrors(t *testing.T) {
	// unknown target
	c := &Config{Triggers: []TriggerSpec{{On: "gh.x", Extends: "nope"}}}
	if err := c.resolveTriggerExtends(); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown target: got %v", err)
	}
	// cycle
	c = &Config{Triggers: []TriggerSpec{
		{Name: "a", On: "gh.x", Extends: "b"},
		{Name: "b", On: "gh.y", Extends: "a"},
	}}
	if err := c.resolveTriggerExtends(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle: got %v", err)
	}
	// abstract + manual is contradictory
	c = &Config{Triggers: []TriggerSpec{{Name: "m", On: "manual", Abstract: true, Steps: []Step{{ID: "s", Type: "command", Command: []string{"true"}}}}}}
	if err := c.resolveTriggerExtends(); err == nil || !strings.Contains(err.Error(), "abstract") {
		t.Fatalf("abstract manual: got %v", err)
	}
}

func TestResolveExtendsRuntime(t *testing.T) {
	c := &Config{Runtimes: map[string]RuntimeConfig{
		"remote": {Use: "cli", Host: "build-box", Command: []string{"gemini"}},
		"remote2": {
			Extends: "remote",
			Command: []string{"codex"}, // slice replaces
		},
	}}
	if err := c.resolveExtends(); err != nil {
		t.Fatalf("resolveExtends: %v", err)
	}
	r := c.Runtimes["remote2"]
	if r.Use != "cli" || r.Host != "build-box" {
		t.Errorf("runtime inherit: %+v", r)
	}
	if !reflect.DeepEqual(r.Command, []string{"codex"}) {
		t.Errorf("slice replace: command = %v, want [codex]", r.Command)
	}
}
