package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree writes a map of relative path -> content under a temp dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const splitMonolith = `
connectors:
  box: { type: command }
  timer:
    type: cron
    schedules: { tick: { every: 1h } }
hosts:
  build-box: { host: build01.internal, user: ci }
agents:
  fixer: { provider: claude }
workflows:
  review-flow:
    inputs: { pr: { type: integer, required: true } }
    steps:
      - { id: note, uses: box.run, options: { command: "true" } }
triggers:
  - on: timer.tick
    steps:
      - { id: call, workflow: review-flow, with: { pr: 1 } }
  - on: timer.tick
    name: second
    steps:
      - { id: hi, uses: box.run, options: { command: "true" } }
`

// TestSectionImportsMatchMonolith (H): the same config split across
// conf.d/*.yaml + workflows/*.yaml + triggers/*.yaml loads to the same shape
// as the monolith, and validates.
func TestSectionImportsMatchMonolith(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml": `
connectors:
  imports: [conf.d/connectors/*.yaml]
  box: { type: command }
hosts:
  imports: [conf.d/hosts.yaml]
agents:
  imports: [conf.d/agents.yaml]
workflows:
  imports: [workflows/*.yaml]
triggers:
  - import: triggers/*.yaml
  - on: timer.tick
    name: second
    steps:
      - { id: hi, uses: box.run, options: { command: "true" } }
`,
		// Bare-entry form.
		"conf.d/connectors/cron.yaml": `
timer:
  type: cron
  schedules: { tick: { every: 1h } }
`,
		// Section-wrapped form.
		"conf.d/hosts.yaml": `
hosts:
  build-box: { host: build01.internal, user: ci }
`,
		"conf.d/agents.yaml": `
fixer: { provider: claude }
`,
		"workflows/review.yaml": `
workflows:
  review-flow:
    inputs: { pr: { type: integer, required: true } }
    steps:
      - { id: note, uses: box.run, options: { command: "true" } }
`,
		// Bare trigger list.
		"triggers/tick.yaml": `
- on: timer.tick
  steps:
    - { id: call, workflow: review-flow, with: { pr: 1 } }
`,
	})
	split, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("split config load: %v", err)
	}

	monoDir := writeTree(t, map[string]string{"config.yaml": splitMonolith})
	mono, err := Load(filepath.Join(monoDir, "config.yaml"))
	if err != nil {
		t.Fatalf("monolith load: %v", err)
	}

	if len(split.ConnectorsMap) != len(mono.ConnectorsMap) || split.ConnectorsMap["timer"].Type != "cron" {
		t.Errorf("connectors differ: split=%v", split.ConnectorsMap)
	}
	if split.Hosts["build-box"].Host != mono.Hosts["build-box"].Host {
		t.Errorf("hosts differ: %+v", split.Hosts)
	}
	if split.Agents["fixer"].Provider != "claude" {
		t.Errorf("agents differ: %+v", split.Agents)
	}
	if len(split.Workflows) != 1 || len(split.Workflows["review-flow"].Steps) != 1 {
		t.Errorf("workflows differ: %+v", split.Workflows)
	}
	if len(split.Triggers) != 2 {
		t.Fatalf("triggers: %d, want 2", len(split.Triggers))
	}
	// The imported trigger spliced FIRST (at its item position), calling the
	// workflow imported from another file by name.
	if split.Triggers[0].Steps[0].Workflow != "review-flow" || split.Triggers[1].Name != "second" {
		t.Errorf("trigger order/refs: %+v", split.Triggers)
	}
}

// TestSectionImportDuplicateNameErrors (H): a name defined in two files (or a
// file and inline) fails the load naming the key and both sources.
func TestSectionImportDuplicateNameErrors(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml": `
connectors:
  imports: [conf.d/*.yaml]
`,
		"conf.d/a.yaml": "box: { type: command }\n",
		"conf.d/b.yaml": "box: { type: command }\n",
	})
	_, err := Load(filepath.Join(dir, "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "connectors.box") ||
		!strings.Contains(err.Error(), "a.yaml") || !strings.Contains(err.Error(), "b.yaml") {
		t.Fatalf("want a duplicate error naming key + both files, got %v", err)
	}

	// Inline + imported collide too.
	dir = writeTree(t, map[string]string{
		"config.yaml": `
connectors:
  imports: [conf.d/a.yaml]
  box: { type: command }
`,
		"conf.d/a.yaml": "box: { type: command }\n",
	})
	_, err = Load(filepath.Join(dir, "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "connectors.box") {
		t.Fatalf("inline+import duplicate should error, got %v", err)
	}
}

// TestSectionImportEmptyGlobIsNoop: an unmatched GLOB no-ops (the default
// split layout ships empty conf.d/ folders) — but a missing LITERAL path is
// an error (a typo'd filename must not vanish silently).
func TestSectionImportEmptyGlobIsNoop(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml": `
connectors:
  imports: [conf.d/connectors/*.yaml]
  box: { type: command }
triggers:
  - import: conf.d/triggers/*.yaml
`,
	})
	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("empty globs should no-op: %v", err)
	}
	if len(cfg.ConnectorsMap) != 1 || len(cfg.Triggers) != 0 {
		t.Fatalf("unexpected merge result: %+v / %+v", cfg.ConnectorsMap, cfg.Triggers)
	}

	dir = writeTree(t, map[string]string{
		"config.yaml": "connectors:\n  imports: [conf.d/exact-file.yaml]\n",
	})
	_, err = Load(filepath.Join(dir, "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "matched no files") {
		t.Fatalf("a missing literal path must error, got %v", err)
	}
}

// TestUseImportFile (H): { workflow: name, import: file } loads that file's
// workflow without a section import.
func TestUseImportFile(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml": `
connectors:
  box: { type: command }
  timer:
    type: cron
    schedules: { tick: { every: 1h } }
triggers:
  - on: timer.tick
    steps:
      - { id: call, workflow: review-flow, import: ./workflows/review.yaml }
`,
		"workflows/review.yaml": `
workflows:
  review-flow:
    steps: [ { id: note, uses: box.run, options: { command: "true" } } ]
  other-flow:
    steps: [ { id: note, uses: box.run, options: { command: "true" } } ]
`,
	})
	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, ok := cfg.Workflows["review-flow"]; !ok {
		t.Fatalf("import: workflow not materialized: %v", cfg.Workflows)
	}
	if cfg.Triggers[0].Steps[0].Workflow != "review-flow" {
		t.Fatalf("use kept the name: %+v", cfg.Triggers[0].Steps[0])
	}

	// Naming a workflow the file doesn't define errors, listing what it has.
	dir2 := writeTree(t, map[string]string{
		"config.yaml": `
connectors:
  timer: { type: cron, schedules: { tick: { every: 1h } } }
triggers:
  - on: timer.tick
    steps: [ { id: call, workflow: nope, import: ./workflows/review.yaml } ]
`,
		"workflows/review.yaml": "review-flow:\n  steps: [ { id: n, type: command, command: [x] } ]\n",
	})
	_, err = Load(filepath.Join(dir2, "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), `no workflow "nope"`) || !strings.Contains(err.Error(), "review-flow") {
		t.Fatalf("want a no-such-workflow error listing names, got %v", err)
	}
}

// TestUseBareFilePath (H): { workflow: ./file.yaml } loads the file's single
// workflow; a multi-workflow file requires the workflow:+from: form.
func TestUseBareFilePath(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml": `
connectors:
  box: { type: command }
  timer:
    type: cron
    schedules: { tick: { every: 1h } }
triggers:
  - on: timer.tick
    steps:
      - { id: call, workflow: ./workflows/review.yaml }
`,
		// Bare name->definition form, single workflow.
		"workflows/review.yaml": `
review-flow:
  steps: [ { id: note, uses: box.run, options: { command: "true" } } ]
`,
	})
	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// The step's workflow: was rewritten to the loaded workflow's name.
	if cfg.Triggers[0].Steps[0].Workflow != "review-flow" {
		t.Fatalf("bare-path use not rewritten: %+v", cfg.Triggers[0].Steps[0])
	}
	if _, ok := cfg.Workflows["review-flow"]; !ok {
		t.Fatalf("workflow not materialized: %v", cfg.Workflows)
	}

	// Two workflows in the file → the bare form is ambiguous.
	dir2 := writeTree(t, map[string]string{
		"config.yaml": `
connectors:
  timer: { type: cron, schedules: { tick: { every: 1h } } }
triggers:
  - on: timer.tick
    steps: [ { id: call, workflow: ./workflows/two.yaml } ]
`,
		"workflows/two.yaml": `
a: { steps: [ { id: n, type: command, command: [x] } ] }
b: { steps: [ { id: n, type: command, command: [x] } ] }
`,
	})
	_, err = Load(filepath.Join(dir2, "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "exactly one workflow") {
		t.Fatalf("want an ambiguity error, got %v", err)
	}
}

// TestStepImportWithoutUseErrors: a step-level `import:` never stands alone.
func TestStepImportWithoutUseErrors(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml": `
connectors:
  timer: { type: cron, schedules: { tick: { every: 1h } } }
triggers:
  - on: timer.tick
    steps: [ { id: call, import: ./workflows/review.yaml } ]
`,
	})
	_, err := Load(filepath.Join(dir, "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "needs `workflow:") {
		t.Fatalf("want an import-without-use error, got %v", err)
	}
}

// TestEntryBodyImport (H): a named entry keeps its name in the main file and
// loads its body from its own file — in any map section.
func TestEntryBodyImport(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml": `
connectors:
  box: { type: command }
  timer: { import: ./conf.d/timer.yaml }
workflows:
  assess-and-post: { import: ./workflows/assess.yaml }
triggers:
  - on: timer.tick
    steps:
      - { id: call, workflow: assess-and-post }
`,
		// The body directly (canonical).
		"conf.d/timer.yaml": `
type: cron
schedules: { tick: { every: 1h } }
`,
		// The name-wrapped form also reads.
		"workflows/assess.yaml": `
assess-and-post:
  steps: [ { id: note, uses: box.run, options: { command: "true" } } ]
`,
	})
	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ConnectorsMap["timer"].Type != "cron" {
		t.Fatalf("entry-body connector import lost: %+v", cfg.ConnectorsMap["timer"])
	}
	wf, ok := cfg.Workflows["assess-and-post"]
	if !ok || len(wf.Steps) != 1 {
		t.Fatalf("entry-body workflow import lost: %+v", cfg.Workflows)
	}
	if len(cfg.Triggers) != 1 || cfg.Triggers[0].Steps[0].Workflow != "assess-and-post" {
		t.Fatalf("trigger should call the imported workflow: %+v", cfg.Triggers)
	}
}

// TestSectionImportNestedImportsRejected: an imported section file cannot
// itself import — one level, stated clearly.
func TestSectionImportNestedImportsRejected(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.yaml":   "connectors:\n  imports: [conf.d/a.yaml]\n",
		"conf.d/a.yaml": "imports: [b.yaml]\nbox: { type: command }\n",
		"conf.d/b.yaml": "timer: { type: cron, schedules: { t: { every: 1h } } }\n",
	})
	_, err := Load(filepath.Join(dir, "config.yaml"))
	if err == nil || !strings.Contains(err.Error(), "nested imports are not supported") {
		t.Fatalf("want a nested-imports rejection, got %v", err)
	}
}
