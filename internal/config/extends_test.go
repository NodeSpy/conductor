package config

import (
	"reflect"
	"strings"
	"testing"
)

func gspec(parts ...string) *GuidanceSpec { return &GuidanceSpec{Parts: parts} }

func TestResolveExtendsAgents(t *testing.T) {
	c := &Config{Agents: map[string]AgentProfile{
		"base": {
			Provider: "claude", Model: "opus", Workspace: "worktree",
			Labels:   map[string]string{"team": "autopilot", "tier": "base"},
			Guidance: gspec("house tone"),
		},
		"fixer": {
			Extends:  "base",
			Model:    "sonnet", // overrides base
			Labels:   map[string]string{"tier": "fixer", "role": "ci"},
			Guidance: gspec("you fix CI"),
		},
	}}
	if err := c.resolveExtends(); err != nil {
		t.Fatalf("resolveExtends: %v", err)
	}
	f := c.Agents["fixer"]
	if f.Provider != "claude" {
		t.Errorf("scalar inherit: provider = %q, want claude", f.Provider)
	}
	if f.Model != "sonnet" {
		t.Errorf("scalar override: model = %q, want sonnet", f.Model)
	}
	if f.Workspace != "worktree" {
		t.Errorf("scalar inherit: workspace = %q, want worktree", f.Workspace)
	}
	// Labels deep-merge: child keys win, parent's missing keys added.
	want := map[string]string{"team": "autopilot", "tier": "fixer", "role": "ci"}
	if !reflect.DeepEqual(f.Labels, want) {
		t.Errorf("labels deep-merge = %v, want %v", f.Labels, want)
	}
	// Guidance stacks: parent under child.
	if got := f.Guidance.Parts; !reflect.DeepEqual(got, []string{"house tone", "you fix CI"}) {
		t.Errorf("guidance stack = %v, want [house tone, you fix CI]", got)
	}
	// The base is untouched.
	if b := c.Agents["base"]; b.Model != "opus" || len(b.Guidance.Parts) != 1 {
		t.Errorf("base mutated: %+v", b)
	}
}

func TestResolveExtendsChain(t *testing.T) {
	c := &Config{Agents: map[string]AgentProfile{
		"a": {Provider: "claude", Guidance: gspec("A")},
		"b": {Extends: "a", Model: "opus", Guidance: gspec("B")},
		"c": {Extends: "b", Thinking: "hard", Guidance: gspec("C")},
	}}
	if err := c.resolveExtends(); err != nil {
		t.Fatalf("resolveExtends: %v", err)
	}
	got := c.Agents["c"]
	if got.Provider != "claude" || got.Model != "opus" || got.Thinking != "hard" {
		t.Errorf("chain inherit: %+v", got)
	}
	if p := got.Guidance.Parts; !reflect.DeepEqual(p, []string{"A", "B", "C"}) {
		t.Errorf("chain guidance = %v, want [A B C]", p)
	}
}

func TestResolveExtendsGuidanceReplace(t *testing.T) {
	c := &Config{Agents: map[string]AgentProfile{
		"base":  {Guidance: gspec("house tone")},
		"stark": {Extends: "base", Guidance: &GuidanceSpec{Parts: []string{"only mine"}, Replace: true}},
	}}
	if err := c.resolveExtends(); err != nil {
		t.Fatalf("resolveExtends: %v", err)
	}
	// { replace } does not inherit the parent's parts.
	if p := c.Agents["stark"].Guidance.Parts; !reflect.DeepEqual(p, []string{"only mine"}) {
		t.Errorf("replace should not inherit parent guidance, got %v", p)
	}
}

func TestResolveExtendsCycle(t *testing.T) {
	c := &Config{Agents: map[string]AgentProfile{
		"a": {Extends: "b"},
		"b": {Extends: "a"},
	}}
	if err := c.resolveExtends(); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected a cycle error, got %v", err)
	}
}

func TestResolveExtendsUnknownTarget(t *testing.T) {
	c := &Config{Agents: map[string]AgentProfile{
		"fixer": {Extends: "nope"},
	}}
	if err := c.resolveExtends(); err == nil || !strings.Contains(err.Error(), "unknown") {
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
		"remote": {Type: "cli", Host: "build-box", Command: []string{"gemini"}},
		"remote2": {
			Extends: "remote",
			Command: []string{"codex"}, // slice replaces
		},
	}}
	if err := c.resolveExtends(); err != nil {
		t.Fatalf("resolveExtends: %v", err)
	}
	r := c.Runtimes["remote2"]
	if r.Type != "cli" || r.Host != "build-box" {
		t.Errorf("runtime inherit: %+v", r)
	}
	if !reflect.DeepEqual(r.Command, []string{"codex"}) {
		t.Errorf("slice replace: command = %v, want [codex]", r.Command)
	}
}
