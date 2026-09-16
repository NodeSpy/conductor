package migrate

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func stepsOf(t *testing.T, doc map[string]any, block, entry string) []any {
	t.Helper()
	b, ok := doc[block].(map[string]any)
	if !ok {
		t.Fatalf("no %s block in %#v", block, doc)
	}
	e, ok := b[entry].(map[string]any)
	if !ok {
		t.Fatalf("no %s.%s entry", block, entry)
	}
	steps, ok := e["steps"].([]any)
	if !ok {
		t.Fatalf("no %s.%s.steps", block, entry)
	}
	return steps
}

const stepUseSrc = `
connectors:
  gh: { use: github }
workflows:
  review-flow:
    steps:
      - { id: a, uses: gh.comment, options: { body: hi } }
triggers:
  t:
    on: gh.new_comment
    steps:
      - { id: call, use: review-flow, with: { pr: 1 } }
`

// A step-level `use:` always meant a workflow call, so it is rewritten to
// `call:` unconditionally — and the result loads.
func TestStepUseBecomesCall(t *testing.T) {
	doc, _ := transformDoc(t, stepUseSrc)
	step := stepsOf(t, doc, "triggers", "t")[0].(map[string]any)
	if _, stale := step["use"]; stale {
		t.Fatalf("step still carries use: %#v", step)
	}
	if step["call"] != "review-flow" {
		t.Fatalf("call = %v, want review-flow", step["call"])
	}
	if step["with"] == nil || step["id"] != "call" {
		t.Fatalf("the rest of the step must survive: %#v", step)
	}
	// A connector's own use: is a different key in a different block.
	conns := doc["connectors"].(map[string]any)
	if conns["gh"].(map[string]any)["use"] != "github" {
		t.Fatalf("a connector use: must not be touched: %#v", conns)
	}
}

// Re-running the migration on its own output changes nothing.
func TestStepUseMigrationIsIdempotent(t *testing.T) {
	first, err := Transform([]byte(stepUseSrc))
	if err != nil || !first.Changed {
		t.Fatalf("first pass: %v changed=%v", err, first.Changed)
	}
	second, err := Transform(first.Output)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if second.Changed {
		t.Fatalf("second pass rewrote again:\n%s", second.Output)
	}
}

// Every place a step can appear is reached: workflow steps, parallel
// branches, a compensation, and the one-step `checks:` entries.
func TestStepUseRewrittenEverywhere(t *testing.T) {
	doc, _ := transformDoc(t, `
connectors:
  gh: { use: github }
workflows:
  target: { steps: [ { id: a, uses: gh.comment, options: {} } ] }
  w:
    steps:
      - id: top
        use: target
      - id: par
        parallel:
          - [ { use: target } ]
          - [ { id: q, uses: gh.comment, options: {} } ]
      - id: undo
        uses: gh.comment
        options: {}
        compensate: { use: target }
checks:
  smoke: { use: target }
`)
	steps := stepsOf(t, doc, "workflows", "w")
	if steps[0].(map[string]any)["call"] != "target" {
		t.Errorf("top-level step: %#v", steps[0])
	}
	branch := steps[1].(map[string]any)["parallel"].([]any)[0].([]any)[0].(map[string]any)
	if branch["call"] != "target" {
		t.Errorf("parallel branch step: %#v", branch)
	}
	comp := steps[2].(map[string]any)["compensate"].(map[string]any)
	if comp["call"] != "target" {
		t.Errorf("compensation: %#v", comp)
	}
	check := doc["checks"].(map[string]any)["smoke"].(map[string]any)
	if check["call"] != "target" {
		t.Errorf("check: %#v", check)
	}
	if strings.Contains(string(mustYAML(t, doc)), "use: target") {
		t.Error("a step use: survived somewhere")
	}
}

// A config with no step-level use: is left exactly as it was — the pass must
// not be a reason to rewrite every file on the box at boot.
func TestNoStepUseNoRewrite(t *testing.T) {
	res, err := Transform([]byte(`
connectors:
  gh: { use: github }
workflows:
  target: { steps: [ { id: a, uses: gh.comment, options: {} } ] }
triggers:
  t:
    on: gh.new_comment
    steps:
      - { id: c, workflow: target }
      - { id: code, run: js, code: "return {}" }
`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed {
		t.Fatalf("nothing to migrate, but it rewrote:\n%s", res.Output)
	}
}

func mustYAML(t *testing.T, v any) []byte {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
