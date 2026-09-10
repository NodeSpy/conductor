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
		Steps: map[string]config.Step{
			"a": {Isolation: &config.IsolationConfig{Mode: "user", User: "sbx",
				Network: &config.IsolationNetwork{Egress: []string{"api.example.com:443"}}}},
			"b": {Isolation: &config.IsolationConfig{Mode: "user", User: "sbx"}},
		},
		Hosts: map[string]config.HostConfig{
			"box": {Host: "h", Isolation: &config.IsolationConfig{Mode: "user", User: "other"}},
		},
	}
	warns := strings.Join(IsolationWarnings(cfg), "\n")
	if !strings.Contains(warns, "step:a") || !strings.Contains(warns, "ADVISORY-ONLY") {
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
	quiet := &config.Config{Steps: map[string]config.Step{
		"c": {Isolation: &config.IsolationConfig{Mode: "namespace",
			Network: &config.IsolationNetwork{Deny: true, Egress: []string{"a:443"}}}},
	}}
	if ws := IsolationWarnings(quiet); len(ws) != 0 {
		t.Fatalf("enforced posture must not warn: %v", ws)
	}
}
