package config

import (
	"strings"
	"testing"
)

// H6: a trigger's `name:` is its identity — the prefix its steps' memory,
// sessions, and track records hang off, and the handle `extends:` and a
// step reference use. Two triggers sharing one collapsed silently into a
// single identity; only manual triggers were ever checked.
func TestNonManualTriggerNamesMustBeUnique(t *testing.T) {
	var c Config
	if err := strictUnmarshal([]byte(`
connectors:
  gh: { use: github }
triggers:
  - { name: review, on: gh.pull_request, steps: [{ id: a, type: agent, prompt: p }] }
  - { name: review, on: gh.issues,       steps: [{ id: b, type: agent, prompt: p }] }
`), &c); err != nil {
		t.Fatal(err)
	}
	err := c.NormalizeTriggers()
	if err == nil || !strings.Contains(err.Error(), "review") {
		t.Fatalf("two triggers sharing a name: must be a load error, got %v", err)
	}
	if !strings.Contains(err.Error(), "unique") {
		t.Fatalf("the error should say what is wrong: %v", err)
	}
}

// …but a LIST-form trigger legitimately expands into one variant per
// source, all carrying the author's single name.
func TestListFormExpansionIsNotANameCollision(t *testing.T) {
	var c Config
	if err := strictUnmarshal([]byte(`
connectors:
  gh: { use: github }
  pd: { use: pagerduty }
triggers:
  - name: fanin
    on: [gh.pull_request, pd.incident]
    steps: [{ id: a, type: agent, prompt: p }]
`), &c); err != nil {
		t.Fatal(err)
	}
	if err := c.NormalizeTriggers(); err != nil {
		t.Fatalf("a fan-in trigger's variants share its name by design: %v", err)
	}
	if len(c.Triggers) != 2 {
		t.Fatalf("want 2 expanded variants, got %d", len(c.Triggers))
	}
}
