package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPackCLILifecycle exercises init -> pack update -> pack remove against a
// local pack source, asserting the observable output at each step.
func TestPackCLILifecycle(t *testing.T) {
	dir := t.TempDir()
	// A minimal behavior-only pack.
	packDir := filepath.Join(dir, "src", "kit")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `
pack: { name: kit, version: "1.0.0", requires: { conductor: ">=0.1" } }
workflows:
  flow: { steps: [ { id: s, run: js, code: "return {}" } ] }
`
	if err := os.WriteFile(filepath.Join(packDir, "conductor-pack.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := `
connectors: { gh: { use: github } }
packs:
  kit:
    source: ./src/kit
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", path}

	// init
	out, err := captureStdout(t, func() error { return cmdInit(args) })
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if !strings.Contains(out, "resolved 1 pack") || !strings.Contains(out, "kit") {
		t.Fatalf("init output missing resolution: %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "conductor.lock.yaml")); err != nil {
		t.Fatalf("lockfile not written: %v", err)
	}

	// update --packs (no change)
	out, err = captureStdout(t, func() error { return cmdPackUpdate(append([]string{"update"}, args...)) })
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !strings.Contains(out, "up to date") {
		t.Fatalf("expected 'up to date', got %q", out)
	}

	// remove
	out, err = captureStdout(t, func() error { return cmdPackRemove(append([]string{"remove", "kit"}, args...)) })
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out, "removed 1 lockfile") {
		t.Fatalf("expected removal message, got %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".conductor", "packs", "kit")); !os.IsNotExist(err) {
		t.Fatalf("vendored tree should be gone after remove")
	}
}

// TestPackCLIAddPrintsReview checks `pack add` renders a review without touching
// the config.
func TestPackCLIAddPrintsReview(t *testing.T) {
	dir := t.TempDir()
	packDir := filepath.Join(dir, "src", "kit")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `
pack:
  name: kit
  version: 2.0.0
  description: demo
  requires:
    conductor: ">=0.1"
    connectors: [github]
triggers:
  - name: on_thing
    on: github.review_requested
    steps: [ { id: s, run: js, code: "return {}" } ]
`
	if err := os.WriteFile(filepath.Join(packDir, "conductor-pack.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := "connectors: { gh: { type: github } }\n"
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error {
		return cmdPackAdd([]string{"add", packDir, "--config", path})
	})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	for _, want := range []string{"kit v2.0.0", "install review", "on_thing", "connectors: { github:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("add output missing %q; got:\n%s", want, out)
		}
	}
	// The config was not mutated.
	b, _ := os.ReadFile(path)
	if string(b) != cfg {
		t.Fatalf("pack add must not mutate the config")
	}
}
