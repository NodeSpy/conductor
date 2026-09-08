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
