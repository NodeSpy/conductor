package config

import (
	"strings"
	"testing"
)

// C2: a pack trigger whose `on:` is a LIST goes through the same
// bind/dormancy/ambiguity machinery as the scalar form. It used to skip it
// entirely — `Connector()` reads `On`, which is empty for the list form —
// so the generic `github` source was never rebound and the load died on
// `unknown connector "github"` instead of degrading.
const listFormPack = `
pack:
  name: multi
  version: "1.0.0"
  requires: { conductor: ">=0.1" }
workflows:
  flow:
    steps: [{ id: s, type: agent, prompt: p }]
triggers:
  - name: fanin
    on:
      - github.pull_request
      - pagerduty.incident
    steps:
      - { id: run, workflow: flow }
`

func TestListFormPackTriggerRebindsItsSources(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", listFormPack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
  pd: { use: pagerduty, token: y }
packs:
  multi: { source: ./src/multi }
`))
	if err != nil {
		t.Fatalf("a list-form pack trigger must load: %v", err)
	}
	var got []string
	for _, tr := range cfg.Triggers {
		if strings.HasPrefix(tr.Name, "multi/") {
			got = append(got, tr.On)
		}
	}
	if len(got) != 2 {
		t.Fatalf("both sources should expand, got %v", got)
	}
	for _, on := range got {
		if strings.HasPrefix(on, "github.") || strings.HasPrefix(on, "pagerduty.") {
			t.Fatalf("source not rebound to the consumer's connector: %q", on)
		}
	}
}

func TestListFormPackTriggerGoesDormantWithoutAConnector(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", listFormPack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  multi: { source: ./src/multi }
`))
	if err != nil {
		t.Fatalf("a missing source must degrade, not fail the load: %v", err)
	}
	warns := strings.Join(cfg.PackWarnings(), "\n")
	if !strings.Contains(warns, "pagerduty") || !strings.Contains(warns, "DORMANT") {
		t.Fatalf("the unservable source should be surfaced: %s", warns)
	}
	for _, tr := range cfg.Triggers {
		if strings.HasPrefix(tr.Name, "multi/") && strings.Contains(tr.On, "pagerduty") && tr.IsEnabled() {
			t.Fatalf("the pagerduty variant must be dormant: %+v", tr)
		}
	}
}

func TestListFormPackTriggerAmbiguousSourceErrors(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", listFormPack)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh:  { use: github, token: x }
  gh2: { use: github, token: y }
  pd:  { use: pagerduty, token: z }
packs:
  multi: { source: ./src/multi }
`))
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("two connectors of one type must be an ambiguity error, got %v", err)
	}
}
