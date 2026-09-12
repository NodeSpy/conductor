package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDoc writes a WHOLE consumer config (writeConsumer prepends its own
// connectors: block, which these tests need to control).
func writeDoc(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Connector-owned scope (§5.2) and the mirrored-section overlay (§5.3).

// multiSourcePack ships triggers from two sources plus a named step and a
// named fleet — the surface both features address.
const multiSourcePack = `
pack:
  name: multi
  version: "1.0.0"
  requires:
    conductor: ">=0.1"
    sources:
      github: { desc: "PR review" }
      pagerduty: { desc: "incident triage" }
    # A sources: requirement says WHICH EVENTS the pack consumes; the
    # connector boundary is a separate statement about what it may REACH.
    # Optional here, because these tests exercise the unbound/dormant paths.
    connectors:
      github:    { version: "*", required: false }
      pagerduty: { version: "*", required: false }
models:
  reviewer: { any: ["claude-opus-*"] }
x-steps:
  security: &security
    type: agent
    name: security
    guidance: "pack tone"
    model: reviewer
  summarize: &summarize
    type: agent
    name: summarize
triggers:
  - name: github.pull_request
    on: github.pull_request
    steps:
      - { id: sec, <<: *security, prompt: "review" }
  - name: pagerduty.incident
    on: pagerduty.incident
    steps:
      - { id: tri, <<: *summarize, prompt: "triage" }
`

// A consumer with only a github connector gets the github trigger bound to
// it and the pagerduty trigger DORMANT — plus a notice. The rest of the pack
// runs; nothing fails the load.
func TestPackMissingConnectorGoesDormant(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", multiSourcePack)
	path := writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  multi:
    source: ./src/multi
    triggers:
      github.pull_request: { enabled: true, repos: [me/app] }
`)
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("a missing connector must not fail the load: %v", err)
	}
	var gh, pd *TriggerSpec
	for i := range cfg.Triggers {
		switch {
		case strings.HasSuffix(cfg.Triggers[i].Name, "github.pull_request"):
			gh = &cfg.Triggers[i]
		case strings.HasSuffix(cfg.Triggers[i].Name, "pagerduty.incident"):
			pd = &cfg.Triggers[i]
		}
	}
	if gh == nil || pd == nil {
		t.Fatalf("both triggers should be present: %+v", cfg.Triggers)
	}
	// The github trigger bound to the consumer's connector INSTANCE name.
	if gh.On != "gh.pull_request" {
		t.Errorf("github trigger should bind to the gh connector, got on: %q", gh.On)
	}
	if !gh.IsEnabled() {
		t.Error("the armed github trigger should be live")
	}
	// The pagerduty trigger is dormant and stays dormant.
	if pd.IsEnabled() {
		t.Error("a trigger with no connector must be dormant")
	}
	// …and surfaced.
	warns := strings.Join(cfg.PackWarnings(), "\n")
	if !strings.Contains(warns, "pagerduty") || !strings.Contains(warns, "DORMANT") {
		t.Errorf("dormancy must be surfaced: %s", warns)
	}
}

// A pack author who marks a source required gets a hard error instead of
// dormancy — for a pack that is meaningless without it.
func TestPackRequiredSourceIsAHardError(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", strings.Replace(multiSourcePack,
		`      pagerduty: { desc: "incident triage" }`,
		`      pagerduty: { desc: "incident triage", required: true }`, 1))
	path := writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  multi: { source: ./src/multi }
`)
	_, err := resolveAndLoad(t, path)
	if err == nil || !strings.Contains(err.Error(), "REQUIRED") {
		t.Fatalf("a required source with no connector must be a hard error, got %v", err)
	}
}

// More than one connector of a type is ambiguous — the pack's triggers could
// land on the wrong account, so it errors and names the fix.
func TestPackAmbiguousConnectorNeedsDisambiguation(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", multiSourcePack)
	base := `
connectors:
  work: { use: github, token: x }
  home: { use: github, token: y }
  pd:   { use: pagerduty, token: z }
packs:
  multi:
`
	_, err := resolveAndLoad(t, writeDoc(t, dir, base+"    source: ./src/multi\n"))
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("two github connectors must be ambiguous, got %v", err)
	}
	// The documented fix resolves it.
	dir2 := t.TempDir()
	writePackSource(t, dir2, "src/multi", multiSourcePack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir2, base+`    source: ./src/multi
    connectors: { github: work }
`))
	if err != nil {
		t.Fatalf("an explicit binding must resolve it: %v", err)
	}
	for _, tr := range cfg.Triggers {
		if strings.HasSuffix(tr.Name, "github.pull_request") && tr.On != "work.pull_request" {
			t.Errorf("should bind to the named connector, got %q", tr.On)
		}
	}
}

func TestPackConnectorBindingMustNameASourceThePackUses(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", multiSourcePack)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  multi:
    source: ./src/multi
    connectors: { gitlab: gh }
`))
	if err == nil || !strings.Contains(err.Error(), "does not use") {
		t.Fatalf("a binding for an unused source must be reported, got %v", err)
	}
}

// --- §5.3 the mirrored-section overlay -------------------------------------

func TestPackMirroredOverlay(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", multiSourcePack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
  pd: { use: pagerduty, token: y }
packs:
  multi:
    source: ./src/multi
    on:
      github.pull_request: { filters: { labels_not: [wip] } }
      pagerduty.incident:  { enabled: false }
    steps:
      github.pull_request/sec: { guidance: "focus on authz + SSRF" }
    models:
      reviewer: claude-opus-5
`))
	if err != nil {
		t.Fatalf("overlay load: %v", err)
	}
	// on: deep-merges onto the shipped trigger's filters…
	var gh, pd *TriggerSpec
	for i := range cfg.Triggers {
		switch {
		case strings.HasSuffix(cfg.Triggers[i].Name, "github.pull_request"):
			gh = &cfg.Triggers[i]
		case strings.HasSuffix(cfg.Triggers[i].Name, "pagerduty.incident"):
			pd = &cfg.Triggers[i]
		}
	}
	if gh == nil || gh.Filters["labels_not"] == nil {
		t.Fatalf("the overlay filter did not merge: %+v", gh)
	}
	// …and `enabled: false` turns one off.
	if pd == nil || pd.IsEnabled() {
		t.Errorf("enabled: false must disable the trigger: %+v", pd)
	}
	// steps: is ADDITIVE for guidance — the pack's tone survives underneath.
	step := packStep(t, cfg, "multi/github.pull_request/sec")
	parts := step.Guidance.Parts
	if len(parts) != 2 || parts[0] != "pack tone" || parts[1] != "focus on authz + SSRF" {
		t.Fatalf("guidance should stack pack-then-consumer, got %v", parts)
	}
	// models: replaces the fleet — rung 1 of the resolution ladder.
	fleet, ok := cfg.Models["multi/reviewer"]
	if !ok {
		t.Fatalf("namespaced fleet missing, have %v", mapKeys(cfg.Models))
	}
	if fleet.Ref != "claude-opus-5" {
		t.Fatalf("fleet override = %+v, want the consumer's model", fleet)
	}
}

// An overlay key that addresses nothing is an error: targeted override is
// the whole point, and a typo that silently does nothing is the worst case.
func TestPackOverlayTyposAreErrors(t *testing.T) {
	cases := map[string]string{
		"on":     "    on: { github.ghost: { enabled: false } }\n",
		"steps":  "    steps: { github.pull_request/ghost: { guidance: x } }\n",
		"models": "    models: { ghost: claude-opus-5 }\n",
	}
	for section, overlay := range cases {
		t.Run(section, func(t *testing.T) {
			dir := t.TempDir()
			writePackSource(t, dir, "src/multi", multiSourcePack)
			_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
  pd: { use: pagerduty, token: y }
packs:
  multi:
    source: ./src/multi
`+overlay))
			if err == nil || !strings.Contains(err.Error(), "no step with id/name") && !strings.Contains(err.Error(), "addresses no") {
				t.Fatalf("an unmatched %s overlay key must error, got %v", section, err)
			}
		})
	}
}

// A step disabled through the overlay is skipped rather than removed, so the
// pack's step list keeps its shape.
func TestPackOverlayCanDisableAStep(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/multi", multiSourcePack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
  pd: { use: pagerduty, token: y }
packs:
  multi:
    source: ./src/multi
    steps:
      pagerduty.incident/tri: { enabled: false }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := packStep(t, cfg, "multi/pagerduty.incident/tri").If; got != "false" {
		t.Fatalf("a disabled step should be skipped via if:, got %q", got)
	}
}
