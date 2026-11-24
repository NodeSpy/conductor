package config

import (
	"strings"
	"testing"
)

// A pack that only CALLS an optional connector (never listens to it): an
// unbound one parks what uses it, directly or through a called workflow,
// and the rest of the pack loads.
const optionalUsePack = `
pack:
  name: opt
  version: 1.0.0
  requires:
    connectors:
      github: "*"
      backup: { required: false }
workflows:
  save:
    steps:
      - { id: put, uses: backup.put, options: { set: x } }
  nightly:
    steps:
      - { id: run, call: save }
  sync:
    steps:
      - { id: snap, uses: github.pr_snapshot, options: { repo: x/y, pr: 1 } }
triggers:
  - name: backup
    "on": manual
    steps:
      - { id: run, call: nightly }
  - name: inline
    "on": manual
    steps:
      - { id: put, uses: backup.put, options: { set: x } }
  - name: sync
    "on": manual
    steps:
      - { id: run, call: sync }
`

func loadOptionalPack(t *testing.T, bindings, arms string) (*Config, []string, error) {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/opt", optionalUsePack)
	path := writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
  bk: { use: github, token: y }
packs:
  o:
    source: ./src/opt
    connectors: `+bindings+`
    triggers: `+arms+`
`)
	cfg, err := resolveAndLoad(t, path)
	var warns []string
	if cfg != nil {
		warns = cfg.PackWarnings()
	}
	return cfg, warns, err
}

func TestUnboundOptionalUsedConnectorParksWhatUsesIt(t *testing.T) {
	cfg, warns, err := loadOptionalPack(t, "{ github: gh }", "{ backup: { enabled: true }, sync: { enabled: true } }")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"o/save", "o/nightly"} {
		if _, ok := cfg.Workflows[gone]; ok {
			t.Errorf("workflow %s should be dormant", gone)
		}
	}
	if _, ok := cfg.Workflows["o/sync"]; !ok {
		t.Error("workflow o/sync should load")
	}
	names := map[string]bool{}
	for _, tr := range cfg.Triggers {
		names[tr.Name] = true
	}
	if names["o/backup"] || names["o/inline"] || !names["o/sync"] {
		t.Errorf("triggers: want only o/sync, got %v", names)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, `optional connector "backup" is not bound, so what uses it is DORMANT`) ||
		!strings.Contains(joined, "trigger backup") || !strings.Contains(joined, "workflow nightly") {
		t.Errorf("want one warning naming the dormant parts, got:\n%s", joined)
	}
}

func TestBoundOptionalUsedConnectorLoadsEverything(t *testing.T) {
	cfg, _, err := loadOptionalPack(t, "{ github: gh, backup: bk }", "{ backup: { enabled: true } }")
	if err != nil {
		t.Fatal(err)
	}
	for _, wf := range []string{"o/save", "o/nightly", "o/sync"} {
		if _, ok := cfg.Workflows[wf]; !ok {
			t.Errorf("workflow %s should load", wf)
		}
	}
	if got := cfg.Workflows["o/save"].Steps[0].Uses; got != "bk.put" {
		t.Errorf("save step bound to %q, want bk.put", got)
	}
}

func TestRequiredUsedConnectorStillFails(t *testing.T) {
	m := strings.Replace(optionalUsePack, "backup: { required: false }", `backup: "*"`, 1)
	dir := t.TempDir()
	writePackSource(t, dir, "src/opt", m)
	path := writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
  bk: { use: github, token: y }
packs:
  o:
    source: ./src/opt
    connectors: { github: gh }
`)
	if _, err := resolveAndLoad(t, path); err == nil || !strings.Contains(err.Error(), `requires connector "backup"`) {
		t.Fatalf("want the required-connector error, got %v", err)
	}
}
