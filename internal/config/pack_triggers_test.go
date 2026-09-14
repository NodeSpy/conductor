package config

import (
	"os"
	"path/filepath"
	"testing"
)

// A pack trigger authored with a list-form on: must be expanded by
// NormalizeTriggers (which now runs after instantiation) into one trigger per
// source, each with its connector rebound.
func TestPackTriggerListFormOnExpands(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", `
pack:
  name: p
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    connectors: [github]
workflows:
  flow: { steps: [ { id: s, run: js, code: "return {}" } ] }
triggers:
  - name: multi
    on: [github.review_requested, github.pull_request]
    steps: [ { id: s, workflow: flow } ]
`)
	body := `
connectors: { gh: { use: github } }
packs:
  p:
    source: ./src/p
    connectors: { github: gh }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	got := map[string]bool{}
	for _, tr := range cfg.Triggers {
		if tr.Name == "p/multi" {
			got[tr.On] = true
		}
	}
	if !got["gh.review_requested"] || !got["gh.pull_request"] {
		t.Fatalf("list-form on: should expand to two rebound triggers, got %v", got)
	}
}

// A pack-local workflow extends: is namespaced and resolved by resolveExtends,
// which now runs after instantiation.
func TestPackWorkflowExtendsResolves(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", `
pack: { name: p, version: "1.0.0", requires: { conductor: ">=0.1" } }
workflows:
  base:
    description: base flow
    steps: [ { id: s, run: js, code: "return {}" } ]
  child:
    extends: base
`)
	body := `
connectors: { gh: { use: github } }
packs:
  p:
    source: ./src/p
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	child, ok := cfg.Workflows["p/child"]
	if !ok {
		t.Fatalf("expected p/child, have %v", workflowKeys(cfg))
	}
	// child inherited base's steps via the namespaced extends target.
	if len(child.Steps) != 1 {
		t.Fatalf("p/child should inherit base's steps, got %d steps", len(child.Steps))
	}
}
