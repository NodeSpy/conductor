package config

import (
	"strings"
	"testing"
)

// gateBase is isoBase plus a checks: map and an agent profile.
func gateBase(t *testing.T) *Config {
	c := isoBase(t)
	c.Agents["fixer"] = AgentProfile{Model: "m"}
	c.Checks = map[string]Step{
		"test":   {Type: "command", Command: []string{"make", "test"}},
		"lint":   {Run: "sh", Code: "golangci-lint run"},
		"critic": {Type: "agent", Agent: "fixer", Prompt: "review", OutputSchema: map[string]any{"type": "object"}},
		"status": {Uses: "gh.comment"},
	}
	return c
}

func TestChecksValidation(t *testing.T) {
	c := gateBase(t)
	if err := c.Validate(); err != nil {
		t.Fatalf("valid checks: %v", err)
	}

	cases := []struct {
		name    string
		chk     Step
		wantErr string
	}{
		{"workflow form", Step{Workflow: "wf"}, "pass/fail reading"},
		{"background critic", Step{Type: "agent", Agent: "fixer", Prompt: "x", Background: true}, "background agent"},
		{"nested gate", Step{Type: "command", Command: []string{"x"}, Gate: &GateSpec{Run: []string{"test"}}}, "its own gate"},
		{"fan out", Step{Type: "command", Command: []string{"x"}, ForEach: "{{.list}}"}, "fan out"},
	}
	for _, tc := range cases {
		c := gateBase(t)
		c.Checks["bad"] = tc.chk
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.wantErr)
		}
	}
	c = gateBase(t)
	c.Checks[""] = Step{Type: "command", Command: []string{"x"}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "empty check name") {
		t.Fatalf("empty name: %v", err)
	}
}

func TestGateValidation(t *testing.T) {
	agentStep := func(g *GateSpec) Step {
		return Step{ID: "fix", Type: "agent", Agent: "fixer", Prompt: "p", Gate: g}
	}
	neg := -1

	cases := []struct {
		name    string
		step    Step
		wantErr string
	}{
		{"ok", agentStep(&GateSpec{Run: []string{"test", "critic"}}), ""},
		{"ok require pass", agentStep(&GateSpec{Run: []string{"test"}, Require: "pass"}), ""},
		{"no run", agentStep(&GateSpec{}), "gate needs `run:"},
		{"unknown check", agentStep(&GateSpec{Run: []string{"ghost"}}), "unknown check"},
		{"bad require", agentStep(&GateSpec{Run: []string{"test"}, Require: "any"}), "require must be"},
		{"neg revisions", agentStep(&GateSpec{Run: []string{"test"}, MaxRevisions: &neg}), "max_revisions"},
		{"gate on verb step", Step{ID: "v", Uses: "gh.comment", Gate: &GateSpec{Run: []string{"test"}}}, "agent steps only"},
		{"gate on background", Step{ID: "b", Type: "agent", Agent: "fixer", Prompt: "p", Background: true,
			Gate: &GateSpec{Run: []string{"test"}}}, "background agent"},
	}
	for _, tc := range cases {
		c := gateBase(t)
		c.Triggers = []TriggerSpec{{On: "gh.release", Steps: []Step{tc.step}}}
		err := c.Validate()
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected: %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: got %v, want %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestGateOnTriggerAndWorkflowValidated(t *testing.T) {
	c := gateBase(t)
	c.Triggers = []TriggerSpec{{On: "gh.release", Gate: &GateSpec{Run: []string{"ghost"}},
		Steps: []Step{{Uses: "gh.comment"}}}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "unknown check") {
		t.Fatalf("trigger gate: %v", err)
	}
	c = gateBase(t)
	c.Workflows = map[string]WorkflowDef{"wf": {Gate: &GateSpec{Run: nil},
		Steps: []Step{{Uses: "gh.comment"}}}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "gate needs") {
		t.Fatalf("workflow gate: %v", err)
	}
}

func TestGateMaxRevisionsDefault(t *testing.T) {
	var g *GateSpec
	if g.MaxRevisionsOrDefault() != DefaultGateMaxRevisions {
		t.Fatal("nil gate default")
	}
	zero := 0
	if (&GateSpec{MaxRevisions: &zero}).MaxRevisionsOrDefault() != 0 {
		t.Fatal("explicit zero must stick (escalate on first fail)")
	}
}
