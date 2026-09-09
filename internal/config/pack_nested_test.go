package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A parent pack "kit" that depends on "base": kit's workflow calls base's
// workflow (a bare dependency-qualified ref), and kit forwards the github
// binding down to base.
const kitManifest = `
pack:
  name: kit
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    connectors: [github]
    packs:
      base: { source: ./unused }
workflows:
  kit-flow:
    steps:
      - id: prep
        uses: github.comment
        options: { repo: "{{.repo}}", number: "1", body: "kit" }
      - id: sub
        workflow: base/fetch
`

const baseManifest = `
pack:
  name: base-kit
  version: 2.0.0
  requires:
    conductor: ">=0.1"
    connectors: [github]
workflows:
  fetch:
    steps:
      - id: c
        uses: github.comment
        options: { repo: "{{.repo}}", number: "1", body: "base" }
`

func TestPackNestedDependency(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/kit", kitManifest)
	writePackSource(t, dir, "src/base", baseManifest)
	body := `
connectors: { gh: { type: github } }
packs:
  kit:
    source: ./src/kit
    connectors: { github: gh }
    packs:
      base:
        source: ./src/base
        connectors: { github: gh }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad nested: %v", err)
	}
	// Parent and child are namespaced; the child is double-namespaced.
	if _, ok := cfg.Workflows["kit/kit-flow"]; !ok {
		t.Fatalf("expected kit/kit-flow, have %v", workflowKeys(cfg))
	}
	if _, ok := cfg.Workflows["kit/base/fetch"]; !ok {
		t.Fatalf("expected double-namespaced kit/base/fetch, have %v", workflowKeys(cfg))
	}
	// The parent's dependency-qualified workflow ref resolves into the child ns.
	kf := cfg.Workflows["kit/kit-flow"]
	if got := kf.Steps[1].Workflow; got != "kit/base/fetch" {
		t.Fatalf("nested workflow ref should be kit/base/fetch, got %q", got)
	}
	// The forwarded connector binding rebinds verbs in BOTH parent and child.
	if kf.Steps[0].Uses != "gh.comment" {
		t.Fatalf("parent verb rebind failed: %q", kf.Steps[0].Uses)
	}
	if got := cfg.Workflows["kit/base/fetch"].Steps[0].Uses; got != "gh.comment" {
		t.Fatalf("child verb rebind (forwarded connector) failed: %q", got)
	}
	// The lockfile records both nodes.
	lock, _ := ReadLockfile(filepath.Dir(path))
	if lock == nil || len(lock.Packs) != 2 {
		t.Fatalf("lockfile should record 2 nodes, got %+v", lock)
	}
}

func TestPackDependencySourceDefaultsFromRequires(t *testing.T) {
	// The child instance omits `source:`, so it must default to the parent
	// manifest's requires.packs[alias].source.
	dir := t.TempDir()
	kit := `
pack:
  name: kit
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    packs:
      base: { source: ./src/base }
workflows:
  flow: { steps: [ { id: s, workflow: base/fetch } ] }
`
	writePackSource(t, dir, "src/kit", kit)
	writePackSource(t, dir, "src/base", baseManifestNoConn)
	body := `
connectors: { gh: { type: github } }
packs:
  kit:
    source: ./src/kit
    packs:
      base: {}          # no source -> default from requires.packs.base.source
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// requires.packs.base.source is "./src/base", resolved relative to the kit
	// vendor dir. Copy base there so the default source resolves.
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad (defaulted source): %v", err)
	}
	if _, ok := cfg.Workflows["kit/base/fetch"]; !ok {
		t.Fatalf("child from defaulted source should instantiate; have %v", workflowKeys(cfg))
	}
}

const baseManifestNoConn = `
pack:
  name: base-kit
  version: 2.0.0
  requires:
    conductor: ">=0.1"
workflows:
  fetch: { steps: [ { id: c, run: js, code: "return {}" } ] }
`

func TestPackCycleDetected(t *testing.T) {
	// kit depends on an alias "kit" (same name repeating in the chain) -> cycle.
	dir := t.TempDir()
	kit := `
pack:
  name: kit
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    packs:
      kit: { source: ./src/kit }
workflows:
  flow: { steps: [ { id: s, run: js, code: "return {}" } ] }
`
	writePackSource(t, dir, "src/kit", kit)
	body := `
connectors: { gh: { type: github } }
packs:
  kit:
    source: ./src/kit
    packs:
      kit: { source: ./src/kit }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolvePacks(path)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("a self-referential dependency should be a cycle error, got: %v", err)
	}
}

func TestPackUndeclaredDependencyRejected(t *testing.T) {
	// The instance instantiates a dep alias the manifest never declared.
	dir := t.TempDir()
	kit := `
pack:
  name: kit
  version: 1.0.0
  requires: { conductor: ">=0.1" }
workflows:
  flow: { steps: [ { id: s, run: js, code: "return {}" } ] }
`
	writePackSource(t, dir, "src/kit", kit)
	writePackSource(t, dir, "src/base", baseManifestNoConn)
	body := `
connectors: { gh: { type: github } }
packs:
  kit:
    source: ./src/kit
    packs:
      rogue: { source: ./src/base }
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveAndLoad(t, path)
	if err == nil || !strings.Contains(err.Error(), "not a declared dependency") {
		t.Fatalf("an undeclared dependency alias should be rejected, got: %v", err)
	}
}

// A declared requires.packs dependency is auto-pulled even when the consumer
// adds no override for it.
func TestPackDependencyAutoPulled(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/parent", `
pack:
  name: parent
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    packs:
      child: { source: ./src/child }
workflows:
  parent-flow: { steps: [ { id: s, workflow: child/flow } ] }
`)
	writePackSource(t, dir, "src/child", baseManifestNoConn2)
	body := `
connectors: { gh: { type: github } }
packs:
  p:
    source: ./src/parent
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	if _, ok := cfg.Workflows["p/child/flow"]; !ok {
		t.Fatalf("declared dependency should be auto-pulled + double-namespaced; have %v", workflowKeys(cfg))
	}
}

const baseManifestNoConn2 = `
pack: { name: child, version: "1.0.0", requires: { conductor: ">=0.1" } }
workflows:
  flow: { steps: [ { id: c, run: js, code: "return {}" } ] }
`

// A cycle reached under a DIFFERENT alias (same pack identity) is detected.
func TestPackIdentityCycleDetected(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/alpha", `
pack:
  name: alpha
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    packs:
      b: { source: ./src/beta }
workflows:
  flow: { steps: [ { id: s, run: js, code: "return {}" } ] }
`)
	writePackSource(t, dir, "src/beta", `
pack:
  name: beta
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    packs:
      c: { source: ./src/alpha }
workflows:
  flow: { steps: [ { id: s, run: js, code: "return {}" } ] }
`)
	body := `
connectors: { gh: { type: github } }
packs:
  x:
    source: ./src/alpha
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolvePacks(path)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("a cycle via a different alias (same pack identity) should be detected, got: %v", err)
	}
}
