package config

import (
	"strings"
	"testing"
)

// M4: a trigger written as an instance ARRAY gets a content-addressed
// name (`review#a1b2c3d4`), so a consumer overlay keyed on the address the
// author actually wrote (`review`) matched nothing and was rejected as
// addressing no trigger.
func TestOverlayAddressesInstanceArrayTriggers(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/kit", `
pack:
  name: kit
  version: "1.0.0"
  requires:
    conductor: ">=0.1"
    # Declared, and OPTIONAL: these tests are about what happens when a
    # source is not bound (it goes dormant). The manifest still has to name
    # what the pack reaches — that is the boundary — but naming it does not
    # make binding mandatory.
    connectors:
      github:    { version: "*", required: false }
      pagerduty: { version: "*", required: false }
triggers:
  review:
    - { on: github.pull_request, filters: { repos: [a/one] }, steps: [{ id: s, type: agent, prompt: p }] }
    - { on: github.pull_request, filters: { repos: [a/two] }, steps: [{ id: s, type: agent, prompt: p }] }
`)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  kit:
    source: ./src/kit
    on:
      review: { filters: { labels_not: [wip] } }
`))
	if err != nil {
		t.Fatalf("an overlay on the authored address must apply: %v", err)
	}
	n := 0
	for _, tr := range cfg.Triggers {
		if !strings.HasPrefix(tr.Name, "kit/review") {
			continue
		}
		n++
		if tr.Filters["labels_not"] == nil {
			t.Errorf("the overlay should reach every instance of the address: %+v", tr)
		}
	}
	if n != 2 {
		t.Fatalf("both instances should exist, got %d", n)
	}
}

// A typo still errors — the point of the overlay is targeted change.
func TestOverlayTypoStillErrorsWithInstances(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/kit", `
pack:
  name: kit
  version: "1.0.0"
  requires:
    conductor: ">=0.1"
    # Declared, and OPTIONAL: these tests are about what happens when a
    # source is not bound (it goes dormant). The manifest still has to name
    # what the pack reaches — that is the boundary — but naming it does not
    # make binding mandatory.
    connectors:
      github:    { version: "*", required: false }
      pagerduty: { version: "*", required: false }
triggers:
  review:
    - { on: github.pull_request, filters: { repos: [a/one] }, steps: [{ id: s, type: agent, prompt: p }] }
`)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  kit:
    source: ./src/kit
    on:
      ghost: { filters: { labels_not: [wip] } }
`))
	if err == nil || !strings.Contains(err.Error(), "addresses no trigger") {
		t.Fatalf("an unmatched overlay key must still error, got %v", err)
	}
}
