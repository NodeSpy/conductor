package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// PACK WORKFLOWS ARE NAMESPACED TO THE PACK — the Terraform-module rule.
//
// A pack addresses only workflows it ships (or a DECLARED dependency's). It
// can never resolve the consumer's bare-named workflows, or a sibling pack's.
//
// This test exists because that rule was enforced for ONE spelling of a
// workflow call and not its twin:
//
//   - { workflow: review-flow }                              # namespaced
//   - { uses: workflow.run, options: {name: review-flow} }    # WAS NOT
//
// The second ran the CONSUMER's real review-flow — every connector inside it,
// none of them in the pack's requires.connectors. `workflow` is an OPEN
// namespace in the pack connector boundary (packNamespaces) *because* pack
// workflow names are namespaced; the verb form quietly falsified that premise.
//
// So the table below enumerates every field and verb where a pack-authored
// step can NAME a workflow, and asserts each one is namespace-rewritten at
// instantiation. A new workflow-naming shape that is not added here — or an
// existing one that stops being rewritten — fails.
//
// Each case is a pack step written with a BARE name that the consumer really
// does define. After instantiation the reference must read `<ns>/<name>`, not
// the bare name: bare would resolve against the consumer's flat global map.
func TestEveryPackWorkflowRefShapeIsNamespaced(t *testing.T) {
	for _, tc := range []struct {
		shape string // the syntax under test
		step  string // one pack step, YAML, naming the workflow "review-flow"
		// where the name lands after rewriting, as a path for readRef
		field string
	}{
		{shape: "step-call `workflow:`",
			step:  "{ id: s, workflow: review-flow }",
			field: "workflow"},
		{shape: "verb `uses: workflow.run` options.name — THE GAP",
			step:  "{ id: s, uses: workflow.run, options: { name: review-flow } }",
			field: "options.name"},
		{shape: "verb `uses: workflow.save` options.name",
			step:  "{ id: s, uses: workflow.save, options: { name: review-flow } }",
			field: "options.name"},
		{shape: "workflow.run inside a compensate block",
			step:  "{ id: s, uses: kv.get, options: { key: k }, compensate: { id: c, uses: workflow.run, options: { name: review-flow } } }",
			field: "compensate.options.name"},
		{shape: "workflow.run inside a parallel branch",
			step:  "{ id: s, parallel: [[{ id: b, uses: workflow.run, options: { name: review-flow } }]] }",
			field: "parallel.0.0.options.name"},
		{shape: "workflow.run in a step HOOK",
			step:  "{ id: s, uses: kv.get, options: { key: k }, hooks: [{ at: done, uses: workflow.run, options: { name: review-flow } }] }",
			field: "hooks.0.options.name"},
		{shape: "step-call inside compensate",
			step:  "{ id: s, uses: kv.get, options: { key: k }, compensate: { id: c, workflow: review-flow } }",
			field: "compensate.workflow"},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			cfg := instantiateWorkflowRefPack(t, tc.step)
			got := readRef(t, cfg, tc.field)
			if got == "review-flow" {
				t.Fatalf("%s: reference left BARE as %q — it resolves against the consumer's "+
					"global workflows, so a pack can run the operator's workflow (and every "+
					"connector in it) with no requires.connectors entry. It must be namespaced "+
					"to %q.", tc.shape, got, "kit/review-flow")
			}
			if got != "kit/review-flow" {
				t.Fatalf("%s: reference is %q, want %q", tc.shape, got, "kit/review-flow")
			}
		})
	}
}

// instantiateWorkflowRefPack installs a one-step pack into a config where the
// CONSUMER defines a real `review-flow`, and returns the pack's instantiated
// workflow as a generic map. The pack ships its own `review-flow` too, so the
// bare name is legitimately resolvable BOTH ways — which is the whole point:
// it must resolve to the pack's.
func instantiateWorkflowRefPack(t *testing.T, step string) map[string]any {
	t.Helper()
	man := `
pack:
  name: kit
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    connectors: [kv]
workflows:
  review-flow:
    steps:
      - { id: own, uses: kv.get, options: { key: mine } }
  entry:
    steps:
      - ` + step + "\n"
	cfg := loadPackedConfig(t, man, `
workflows:
  review-flow:
    steps:
      - { id: consumer, uses: kv.get, options: { key: THE-CONSUMERS-OWN } }
packs:
  kit:
    source: ./src/kit
`)
	wf, ok := cfg.Workflows["kit/entry"]
	if !ok {
		t.Fatalf("pack workflow not instantiated (have: %s)", sortedKeys(cfg.Workflows))
	}
	return stepAsMap(t, wf.Steps[0])
}

// loadPackedConfig writes the pack manifest + consumer config to a temp dir
// and loads them exactly as the daemon does after `conductor init`.
func loadPackedConfig(t *testing.T, manifest, consumer string) *Config {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/kit", manifest)
	path := filepath.Join(dir, "config.yaml")
	// A real connector: the loader refuses a config with none.
	if err := os.WriteFile(path, []byte("connectors: { gh: { use: github } }\n"+consumer), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	return cfg
}

// stepAsMap round-trips a Step through YAML so the table can address any
// reference by path, including ones nested in compensate/parallel/hooks —
// without this test needing to know each field's Go type.
func stepAsMap(t *testing.T, s Step) map[string]any {
	t.Helper()
	b, err := yaml.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// readRef walks a dotted path into a step map and returns the string there.
func readRef(t *testing.T, m map[string]any, path string) string {
	t.Helper()
	var cur any = m
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			cur = node[seg]
		case []any:
			i := int(seg[0] - '0')
			if i >= len(node) {
				t.Fatalf("path %q: index %d out of range", path, i)
			}
			cur = node[i]
		default:
			t.Fatalf("path %q: cannot descend %q into %T", path, seg, cur)
		}
	}
	s, _ := cur.(string)
	if s == "" {
		t.Fatalf("path %q resolved to no string (got %#v) — the shape moved; "+
			"update this table so the reference stays covered", path, cur)
	}
	return s
}

// THE CRITICAL, stated as the attack: a pack ships `uses: workflow.run,
// options: {name: review-flow}` and the consumer has a real `review-flow`
// that posts to github and pages pagerduty. The pack declares NEITHER
// connector. Before the fix this ran the consumer's workflow verbatim,
// which is the requires.connectors boundary defeated in one line of YAML.
func TestPackWorkflowRunCannotReachTheConsumersWorkflow(t *testing.T) {
	cfg := loadPackedConfig(t, `
pack:
  name: kit
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    connectors: [kv]
workflows:
  review-flow:
    steps:
      - { id: own, uses: kv.get, options: { key: mine } }
  entry:
    steps:
      - { id: s, uses: workflow.run, options: { name: review-flow } }
`, `
workflows:
  review-flow:
    steps:
      - { id: page, uses: gh.comment, options: { repo: "o/r", number: "1", body: pwned } }
packs:
  kit:
    source: ./src/kit
`)
	step := stepAsMap(t, cfg.Workflows["kit/entry"].Steps[0])
	name := readRef(t, step, "options.name")
	if name != "kit/review-flow" {
		t.Fatalf("pack workflow.run resolved to %q — it must be confined to the pack's own "+
			"namespace, not the consumer's global workflow map", name)
	}
	// And what that name resolves to is the PACK's workflow, not the one that
	// comments on the operator's repo.
	target, ok := cfg.Workflows[name]
	if !ok {
		t.Fatalf("%q does not resolve (workflows: %s)", name, sortedKeys(cfg.Workflows))
	}
	if got := target.Steps[0].Uses; got != "kv.get" {
		t.Fatalf("kit/review-flow's first step is %q — the pack reached the CONSUMER's "+
			"review-flow and the requires.connectors boundary bought nothing", got)
	}
	// The consumer's own review-flow is untouched and still theirs.
	if got := cfg.Workflows["review-flow"].Steps[0].Uses; got != "gh.comment" {
		t.Fatalf("the consumer's review-flow changed: %q", got)
	}
}

// Naming a workflow the pack does not ship fails at LOAD, the way an
// undeclared connector does — not silently at install, and not at 3am the
// first time the step fires.
func TestPackNamingANonOwnedWorkflowIsALoadError(t *testing.T) {
	for _, tc := range []struct{ shape, step string }{
		{"workflow.run", "{ id: s, uses: workflow.run, options: { name: not-mine } }"},
		{"step-call", "{ id: s, workflow: not-mine }"},
	} {
		t.Run(tc.shape, func(t *testing.T) {
			dir := t.TempDir()
			writePackSource(t, dir, "src/kit", `
pack:
  name: kit
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    connectors: [kv]
workflows:
  entry:
    steps:
      - `+tc.step+"\n")
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(`connectors: { gh: { use: github } }
workflows:
  not-mine:
    steps:
      - { id: consumer, uses: gh.comment, options: { repo: "o/r", number: "1", body: x } }
packs:
  kit:
    source: ./src/kit
`), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := resolveAndLoad(t, path)
			if err == nil {
				t.Fatal("naming a workflow the pack does not ship must be a load error")
			}
			if !strings.Contains(err.Error(), "not-mine") {
				t.Fatalf("the error must name the offending workflow: %v", err)
			}
		})
	}
}

// The LEGIT case: a pack calling its OWN workflow by name resolves and runs.
// A fix that closed the hole by breaking this would be no fix at all.
func TestPackCallingItsOwnWorkflowStillResolves(t *testing.T) {
	cfg := loadPackedConfig(t, `
pack:
  name: kit
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    connectors: [kv]
workflows:
  helper:
    steps:
      - { id: h, uses: kv.get, options: { key: mine } }
  entry:
    steps:
      - { id: viaVerb, uses: workflow.run, options: { name: helper } }
      - { id: viaCall, workflow: helper }
`, `
packs:
  kit:
    source: ./src/kit
`)
	entry, ok := cfg.Workflows["kit/entry"]
	if !ok {
		t.Fatalf("pack did not instantiate (workflows: %s)", sortedKeys(cfg.Workflows))
	}
	for i, want := range []string{"kit/helper", "kit/helper"} {
		got := entry.Steps[i].Workflow
		if i == 0 {
			got, _ = entry.Steps[i].Options["name"].(string)
		}
		if got != want {
			t.Fatalf("step %d resolved to %q, want %q", i, got, want)
		}
		if _, ok := cfg.Workflows[got]; !ok {
			t.Fatalf("%q does not resolve — the pack cannot call its own workflow", got)
		}
	}
}

// The OPERATOR's workflow.run is untouched: this namespacing applies to
// pack-instantiated steps only. A main-config workflow.run naming a bare
// workflow — or a saved one that is not in the config at all — must keep
// working exactly as before.
func TestOperatorWorkflowRunIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(`connectors: { gh: { use: github } }
workflows:
  review-flow:
    steps:
      - { id: c, uses: gh.comment, options: { repo: "o/r", number: "1", body: hi } }
  entry:
    steps:
      - { id: byVerb, uses: workflow.run, options: { name: review-flow } }
      - { id: bySaved, uses: workflow.run, options: { name: some-saved-workflow } }
      - { id: byCall, workflow: review-flow }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("an operator config must load unchanged: %v", err)
	}
	steps := cfg.Workflows["entry"].Steps
	if got, _ := steps[0].Options["name"].(string); got != "review-flow" {
		t.Fatalf("operator workflow.run name rewritten to %q — namespacing must apply to "+
			"pack-instantiated steps only", got)
	}
	// A name that is in NO config workflow is still accepted at load: the
	// operator may be naming a saved (agent-promoted) workflow, resolved at
	// runtime. Pack namespacing must not have made this a load error.
	if got, _ := steps[1].Options["name"].(string); got != "some-saved-workflow" {
		t.Fatalf("operator saved-workflow name rewritten to %q", got)
	}
	if steps[2].Workflow != "review-flow" {
		t.Fatalf("operator step-call rewritten to %q", steps[2].Workflow)
	}
}
