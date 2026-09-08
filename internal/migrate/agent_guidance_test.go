package migrate

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// parseMap decodes YAML into a generic map for structural assertions.
func parseMap(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal result: %v\n%s", err, b)
	}
	return m
}

func TestAgentGuidancePass_NoPolicy(t *testing.T) {
	in := []byte("agent_guidance: \"Terse and human.\"\nagents:\n  fixer: { provider: claude }\n")
	var notes []string
	out, changed, err := applyAgentGuidancePass(in, &notes)
	if err != nil || !changed {
		t.Fatalf("expected change, got changed=%v err=%v", changed, err)
	}
	m := parseMap(t, out)
	if _, ok := m["agent_guidance"]; ok {
		t.Errorf("agent_guidance should be removed:\n%s", out)
	}
	pol, ok := m["policy"].(map[string]any)
	if !ok || pol["guidance"] != "Terse and human." {
		t.Errorf("policy.guidance should carry the value, got: %v\n%s", m["policy"], out)
	}
}

func TestAgentGuidancePass_ExistingPolicyNoGuidance(t *testing.T) {
	in := []byte("policy:\n  concurrency: { max_agents: 3 }\nagent_guidance: \"Be brief.\"\n")
	var notes []string
	out, changed, err := applyAgentGuidancePass(in, &notes)
	if err != nil || !changed {
		t.Fatalf("expected change, got changed=%v err=%v", changed, err)
	}
	m := parseMap(t, out)
	pol := m["policy"].(map[string]any)
	if pol["guidance"] != "Be brief." {
		t.Errorf("guidance should be added under existing policy, got: %v", pol)
	}
	if pol["concurrency"] == nil {
		t.Errorf("existing policy keys must be preserved, got: %v", pol)
	}
	if _, ok := m["agent_guidance"]; ok {
		t.Errorf("agent_guidance should be removed")
	}
}

func TestAgentGuidancePass_PolicyGuidanceWins(t *testing.T) {
	in := []byte("policy:\n  guidance: \"Canonical.\"\nagent_guidance: \"Legacy.\"\n")
	var notes []string
	out, changed, err := applyAgentGuidancePass(in, &notes)
	if err != nil || !changed {
		t.Fatalf("expected change, got changed=%v err=%v", changed, err)
	}
	m := parseMap(t, out)
	if _, ok := m["agent_guidance"]; ok {
		t.Errorf("redundant agent_guidance should be dropped")
	}
	if m["policy"].(map[string]any)["guidance"] != "Canonical." {
		t.Errorf("existing policy.guidance must win, got: %v", m["policy"])
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "redundant") {
		t.Errorf("expected a 'redundant' note, got: %v", notes)
	}
}

func TestAgentGuidancePass_NoopAndIdempotent(t *testing.T) {
	// No agent_guidance → no change.
	in := []byte("agents:\n  fixer: { provider: claude }\n")
	if out, changed, err := applyAgentGuidancePass(in, new([]string)); err != nil || changed || out != nil {
		t.Fatalf("no agent_guidance should be a no-op, got changed=%v", changed)
	}
	// Running the migrated output again is a no-op (idempotent).
	first, _, _ := applyAgentGuidancePass([]byte("agent_guidance: x\n"), new([]string))
	if _, changed, _ := applyAgentGuidancePass(first, new([]string)); changed {
		t.Fatalf("second pass over migrated output should not change it:\n%s", first)
	}
}
