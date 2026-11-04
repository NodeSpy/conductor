package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// Regression (#36 iso-review C2/C4): validate must say out loud that
// mode:user egress is advisory-only and that a shared sandbox user does not
// isolate concurrent dispatches from each other — never present either as
// enforced.
func TestIsolationWarningsHonestAboutUserMode(t *testing.T) {
	cfg := &config.Config{
		Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
			{ID: "a", Isolation: &config.IsolationConfig{Mode: "user", User: "sbx",
				Network: &config.IsolationNetwork{Egress: []string{"api.example.com:443"}}}},
			{ID: "b", Isolation: &config.IsolationConfig{Mode: "user", User: "sbx"}},
		}}},
		Hosts: map[string]config.HostConfig{
			"box": {Host: "h", Isolation: &config.IsolationConfig{Mode: "user", User: "other"}},
		},
	}
	warns := strings.Join(IsolationWarnings(cfg), "\n")
	if !strings.Contains(warns, "workflow:w a") || !strings.Contains(warns, "ADVISORY-ONLY") {
		t.Fatalf("user-mode egress must be flagged advisory: %s", warns)
	}
	if !strings.Contains(warns, "not concurrent agents from each other") &&
		!strings.Contains(warns, "isolates the agent from the DAEMON") {
		t.Fatalf("shared-uid honesty warning missing: %s", warns)
	}
	if !strings.Contains(warns, `sandbox user "sbx" is SHARED by 2`) {
		t.Fatalf("shared account across scopes must be named: %s", warns)
	}
	if strings.Contains(warns, `"other" is SHARED`) {
		t.Fatalf("a single-scope user is not shared: %s", warns)
	}

	// namespace / container scopes produce no user-mode noise.
	quiet := &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
		{ID: "c", Isolation: &config.IsolationConfig{Mode: "namespace",
			Network: &config.IsolationNetwork{Deny: true, Egress: []string{"a:443"}}}},
	}}}}
	if ws := IsolationWarnings(quiet); len(ws) != 0 {
		t.Fatalf("enforced posture must not warn: %v", ws)
	}
}

// A code step that opted out of the confined-by-default namespace sandbox
// (trust: full, or an explicit isolation: { mode: none }) gets a boot-time
// warning naming it — the operator sees exactly which steps run bare instead
// of discovering it from an incident.
func TestIsolationWarningsNameCodeStepOptOuts(t *testing.T) {
	cfg := &config.Config{Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
		{ID: "trusted", Use: "cli", Command: []string{"bash"}, Trust: "full"},
		{ID: "opted-out", Use: "cli", Command: []string{"bash"}, Isolation: &config.IsolationConfig{Mode: "none"}},
		{ID: "confined", Use: "cli", Command: []string{"bash"},
			Isolation: &config.IsolationConfig{Mode: "namespace", Network: &config.IsolationNetwork{Deny: true}}},
	}}}}
	warns := strings.Join(IsolationWarnings(cfg), "\n")
	if !strings.Contains(warns, "trusted") || !strings.Contains(warns, "UNCONFINED") {
		t.Fatalf("trust: full opt-out must be named: %s", warns)
	}
	if !strings.Contains(warns, "opted-out") {
		t.Fatalf("isolation: {mode: none} opt-out must be named: %s", warns)
	}
	if strings.Contains(warns, "\"w confined\"") || strings.Count(warns, "UNCONFINED") != 2 {
		t.Fatalf("a confined step must not be named as an opt-out: %s", warns)
	}
}

// `code: { isolation_default: none }` turns the whole default off, so
// per-step opt-out warnings are noise (every step runs bare by the
// operator's own global choice) and stay quiet.
func TestIsolationWarningsQuietWhenDefaultOff(t *testing.T) {
	cfg := &config.Config{
		Code: config.CodeConfig{IsolationDefault: "none"},
		Workflows: map[string]config.WorkflowDef{"w": {Steps: []config.Step{
			{ID: "a", Use: "cli", Command: []string{"bash"}, Trust: "full"},
		}}},
	}
	if ws := IsolationWarnings(cfg); len(ws) != 0 {
		t.Fatalf("code.isolation_default: none must silence opt-out warnings: %v", ws)
	}
}
