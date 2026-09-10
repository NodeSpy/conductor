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

// §4: validateSourceDeclarations built its "used" set from t.Connector(),
// empty for a list-form `on:` — so a pack whose only github use is
// list-form rejected the consumer's own correct disambiguation.
func TestListFormSourceCountsAsUsedForDisambiguation(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", listFormPack)
	if _, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh:  { use: github, token: x }
  gh2: { use: github, token: y }
  pd:  { use: pagerduty, token: z }
packs:
  multi:
    source: ./src/multi
    connectors: { github: gh, pagerduty: pd }
`)); err != nil {
		t.Fatalf("a list-form source must count as used: %v", err)
	}
}

// …and a genuine typo is still caught.
func TestUnusedSourceDisambiguationStillErrors(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", listFormPack)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh:    { use: github, token: x }
  pd:    { use: pagerduty, token: z }
  slack: { use: slack, token: s }
packs:
  multi:
    source: ./src/multi
    connectors: { slack: slack }
`))
	if err == nil || !strings.Contains(err.Error(), "does not use") {
		t.Fatalf("a disambiguation for an unused source must still error, got %v", err)
	}
}

// §5: the list loop rewrote every source as `bound + "." + event`, but
// bound=="" means "needs no rebinding" (manual, conductor, already an
// instance name). `manual.rerun` became `.rerun`.
func TestListFormLeavesManualSourcesIntact(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/mixed", `
pack:
  name: mixed
  version: "1.0.0"
  requires: { conductor: ">=0.1" }
workflows:
  flow:
    steps: [{ id: s, type: agent, prompt: p }]
triggers:
  - name: both
    on:
      - manual.rerun
      - github.pull_request
    steps:
      - { id: run, workflow: flow }
`)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  mixed: { source: ./src/mixed }
`))
	if err != nil {
		t.Fatalf("a mixed list form must load: %v", err)
	}
	var ons []string
	for _, tr := range cfg.Triggers {
		if strings.HasPrefix(tr.Name, "mixed/") {
			ons = append(ons, tr.On)
		}
	}
	found := false
	for _, on := range ons {
		if strings.HasPrefix(on, ".") {
			t.Fatalf("a source needing no rebinding was corrupted: %q (all: %v)", on, ons)
		}
		if on == "manual.rerun" {
			found = true
		}
	}
	if !found {
		t.Fatalf("manual.rerun should survive untouched, got %v", ons)
	}
}
