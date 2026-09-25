package config

import (
	"strings"
	"testing"
)

const decideWF = engineWF + `      - id: verify
        model: light
`

func decideCfg(extra string) string {
	return `
models:
  light: { any: ["claude-sonnet-*"] }
  heavy: { any: ["claude-opus-*"] }
stores:
  log: { type: sqlite, path: /tmp/decisions.db }
` + decideWF + extra
}

const goodDecide = `        decide:
          state: "{{.pr.title}}"
          questions:
            refuted:
              type: noul
              instructions: The diff contradicts the finding.
            risk:
              type: choice
              criteria: { low: docs only, high: auth or money }
`

func TestDecideStepLoads(t *testing.T) {
	cfg, err := loadYAML(t, decideCfg(goodDecide+`          default: { refuted: { noul: 0 }, risk: { choice: high } }
          escalate:
            when: "refuted.noul >= 0.5 && refuted.noul < 0.8"
            to: heavy
            max: 2
          observe: log
`))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Workflows["w"].Steps[0]
	if s.Form() != "decide" {
		t.Fatalf("form = %q", s.Form())
	}
	if s.Decide.ProtocolOrDefault() != "system_one/v1" {
		t.Fatalf("an unset protocol is v1, got %q", s.Decide.ProtocolOrDefault())
	}
	if got := strings.Join(s.Decide.Questions.Names(), ","); got != "refuted,risk" {
		t.Fatalf("questions = %s", got)
	}
	if s.Decide.Escalate.Hops() != 2 || s.Decide.Escalate.To.Ref != "heavy" {
		t.Fatalf("escalate = %+v", s.Decide.Escalate)
	}
}

func TestDecideStepValidation(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"no state": {`        decide:
          questions: { a: { type: noul } }
`, "decide.state is required"},
		"no questions": {`        decide:
          state: x
`, "at least one question"},
		"unknown protocol": {`        decide:
          protocol: system_one/v9
          state: x
          questions: { a: { type: noul } }
`, "not supported"},
		"bad default label": {goodDecide + `          default: { refuted: { noul: 0 }, risk: { choice: medium } }
`, "decide.default"},
		"partial default": {goodDecide + `          default: { refuted: { noul: 0 } }
`, "must answer every question"},
		"escalate without when": {goodDecide + `          escalate: { max: 1 }
`, "needs `when:`"},
		"escalate max too high": {goodDecide + `          escalate: { when: "refuted.noul > 0.5", max: 9 }
`, "escalate.max"},
		"unknown observe store": {goodDecide + `          observe: nowhere
`, "unknown store"},
		"prompt on a decide step": {goodDecide + `        prompt: explore the repo
`, "prompt:"},
		"checkout on a decide step": {goodDecide + `        checkout: pr
`, "checkout:"},
		"output_schema on a decide step": {goodDecide + `        output_schema: { type: object }
`, "output_schema:"},
		"type agent plus decide": {goodDecide + `        type: agent
`, "mutually exclusive"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadYAML(t, decideCfg(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// The restricted-execution error names what was set and why it is refused,
// so the fix (make it an agent step) is obvious.
func TestDecideForbiddenFieldsAreNamed(t *testing.T) {
	_, err := loadYAML(t, decideCfg(goodDecide+`        mode: auto
        thinking: high
`))
	if err == nil {
		t.Fatal("agent-behavior fields on a decide step must be refused")
	}
	for _, want := range []string{"mode:", "thinking:", "type: agent"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q: %v", want, err)
		}
	}
}

const decidePack = `
pack:
  name: review
  version: "1.0.0"
  requires: { conductor: ">=0.1", connectors: [github], stores: [ledger] }
models:
  light: { any: ["claude-sonnet-*"] }
  heavy: { any: ["claude-opus-*"] }
workflows:
  verify:
    steps:
      - id: verify
        model: light
        decide:
          state: x
          questions: { refuted: { type: noul } }
          escalate: { when: "refuted.noul > 0.5", to: heavy }
      - id: tally
        model: light
        decide:
          state: y
          questions: { ok: { type: noul } }
          observe: ledger
`

func loadDecidePack(t *testing.T, instance string) *Config {
	t.Helper()
	dir := t.TempDir()
	writePackSource(t, dir, "src/review", decidePack)
	cfg, err := resolveAndLoad(t, writeDoc(t, dir, `
connectors:
  gh: { use: github, token: x }
stores:
  mine: { type: sqlite, path: /tmp/mine.db }
  theirs: { type: sqlite, path: /tmp/theirs.db }
packs:
  review:
    source: ./src/review
    stores: { ledger: theirs }
`+instance))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func decidePackStep(t *testing.T, cfg *Config, id string) Step {
	t.Helper()
	wf, ok := cfg.Workflows["review/verify"]
	if !ok {
		t.Fatal("the pack's workflow is namespaced review/verify")
	}
	for _, s := range wf.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no step %q", id)
	return Step{}
}

// A pack's own fleet named in escalate.to namespaces with it, exactly as
// model: does — otherwise it would resolve against the consumer's globals.
func TestPackDecideEscalateToNamespaces(t *testing.T) {
	cfg := loadDecidePack(t, "")
	s := decidePackStep(t, cfg, "verify")
	if s.Model.Ref != "review/light" {
		t.Fatalf("model: namespaces, got %q", s.Model.Ref)
	}
	if s.Decide.Escalate == nil || s.Decide.Escalate.To.Ref != "review/heavy" {
		t.Fatalf("escalate.to must namespace with the pack's fleets, got %+v", s.Decide.Escalate)
	}
	if got := decidePackStep(t, cfg, "tally").Decide.Observe; got != "theirs" {
		t.Fatalf("a pack's required store rebinds to the consumer's, got %q", got)
	}
}

func TestPackInstanceDecideSettingsLower(t *testing.T) {
	cfg := loadDecidePack(t, `    decide: { observe: mine, escalate: false }
`)
	v := decidePackStep(t, cfg, "verify")
	if v.Decide.Escalate != nil {
		t.Fatal("escalate: false on the instance turns the pack's escalation off")
	}
	if v.Decide.Observe != "mine" {
		t.Fatalf("the instance's observe fills a step that names none, got %q", v.Decide.Observe)
	}
	if got := decidePackStep(t, cfg, "tally").Decide.Observe; got != "theirs" {
		t.Fatalf("a step's own observe store is kept, got %q", got)
	}
}

func TestPackInstanceDecideLeavesEscalationByDefault(t *testing.T) {
	cfg := loadDecidePack(t, `    decide: { observe: mine }
`)
	if decidePackStep(t, cfg, "verify").Decide.Escalate == nil {
		t.Fatal("an instance that doesn't say escalate: false keeps the pack's escalation")
	}
}
