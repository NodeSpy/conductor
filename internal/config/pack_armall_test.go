package config

import (
	"strings"
	"testing"
)

const threeTriggerPack = `
pack:
  name: review
  version: "1.0.0"
  requires: { conductor: ">=0.1", connectors: [github] }
triggers:
  - name: watch
    on: github.pull_request
    steps: [{ id: s, uses: github.comment, options: { repo: "o/r", number: "1", body: a } }]
  - name: deploy
    on: github.push
    steps: [{ id: s, uses: github.comment, options: { repo: "o/r", number: "1", body: b } }]
  - name: nightly
    on: github.issues
    steps: [{ id: s, uses: github.comment, options: { repo: "o/r", number: "1", body: c } }]
`

func armedTriggers(t *testing.T, instance string) map[string]TriggerSpec {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/review", threeTriggerPack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  review:
    source: ./src/review
`+instance))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out := map[string]TriggerSpec{}
	for _, tr := range cfg.Triggers {
		if strings.HasPrefix(tr.Name, "review/") {
			out[strings.TrimPrefix(tr.Name, "review/")] = tr
		}
	}
	if len(out) != 3 {
		t.Fatalf("expected the pack's three triggers, got %d", len(out))
	}
	return out
}

func repoList(tr TriggerSpec) []string {
	raw, _ := tr.Filters["repos"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		s, _ := r.(string)
		out = append(out, s)
	}
	return out
}

// `triggers: {"*": …}` arms EVERY trigger the pack ships with one consent
// (docs/design/config-surface-refinements.md §5) — the operator supplies the
// repo list once rather than repeating it per trigger.
func TestArmAllArmsEveryPackTrigger(t *testing.T) {
	got := armedTriggers(t, `
    triggers:
      "*": { enabled: true, repos: [your-org/app] }
`)
	for name, tr := range got {
		if tr.Enabled == nil || !*tr.Enabled {
			t.Errorf("%q: `*` must arm every shipped trigger", name)
		}
		if r := repoList(tr); len(r) != 1 || r[0] != "your-org/app" {
			t.Errorf("%q: `*` must supply the repo consent, got %v", name, r)
		}
	}
}

// A named key refines that one trigger; everything it leaves unset keeps the
// wildcard's value.
func TestNamedArmRefinesTheWildcard(t *testing.T) {
	got := armedTriggers(t, `
    triggers:
      "*":    { enabled: true, repos: [your-org/app] }
      deploy: { repos: [your-org/infra] }
`)
	if r := repoList(got["deploy"]); len(r) != 1 || r[0] != "your-org/infra" {
		t.Errorf("a named repos: must REPLACE the wildcard's, not append — repos are "+
			"the consent and an operator must be able to narrow: %v", r)
	}
	if got["deploy"].Enabled == nil || !*got["deploy"].Enabled {
		t.Error("deploy keeps the wildcard's enabled: — a named entry refines, it does not reset")
	}
	for _, n := range []string{"watch", "nightly"} {
		if r := repoList(got[n]); len(r) != 1 || r[0] != "your-org/app" {
			t.Errorf("%q keeps the wildcard's repos: %v", n, r)
		}
	}
}

// A named entry may also DISARM one trigger out of a wildcard-armed set.
func TestNamedArmCanDisableOneTrigger(t *testing.T) {
	got := armedTriggers(t, `
    triggers:
      "*":     { enabled: true, repos: [your-org/app] }
      nightly: { enabled: false }
`)
	if got["nightly"].Enabled == nil || *got["nightly"].Enabled {
		t.Error("a named enabled:false must win over the wildcard's true")
	}
	if got["watch"].Enabled == nil || !*got["watch"].Enabled {
		t.Error("…without disarming the rest")
	}
}

// `"*"` names every trigger by construction, so it must not trip the
// "arm names no shipped trigger" check that catches a typo'd name.
func TestArmAllIsNotAnUnknownTriggerName(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/review", threeTriggerPack)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  review:
    source: ./src/review
    triggers:
      "*": { enabled: true, repos: [your-org/app] }
`))
	if err != nil {
		t.Fatalf("`*` must not be mistaken for a trigger name: %v", err)
	}
	// …while a real typo still errors.
	dir2 := t.TempDir()
	writePackSource(t, dir2, "src/review", threeTriggerPack)
	_, err = resolveAndLoad(t, writeDoc(t, dir2, `
connectors:
  gh: { use: github, token: x }
packs:
  review:
    source: ./src/review
    triggers:
      deplyo: { enabled: true, repos: [your-org/app] }
`))
	if err == nil || !strings.Contains(err.Error(), "deplyo") {
		t.Fatalf("a typo'd trigger name must still error: %v", err)
	}
}

// `"*"` supplies consent, not a bypass of it: an armed trigger with no repos
// still fails the repo-consent check, wildcard or not.
func TestArmAllStillNeedsRepoConsent(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/review", threeTriggerPack)
	_, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
packs:
  review:
    source: ./src/review
    triggers:
      "*": { enabled: true }
`))
	if err == nil || !strings.Contains(err.Error(), "names no repos") {
		t.Fatalf("arming every trigger with no repo list must still be refused — "+
			"the repo list IS the consent: %v", err)
	}
}
