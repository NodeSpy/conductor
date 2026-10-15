package config

import (
	"strings"
	"testing"
)

const optoutBase = `
connectors:
  svc: { use: web }
runtimes:
  pz: { use: paseo, default: true }
  acpr: { use: acp, agent: claude-code, transport: acp }
`

// On a paseo step or runtime only `isolation: {mode: none}` (paseo's plain
// provider) or `{mode: container, …}` (the sandboxed entry, in a container)
// applies. Anything else is refused, not a silent no-op.
func TestPaseoIsolationOnlyOptOut(t *testing.T) {
	ok := []string{
		optoutBase + "triggers:\n  - on: svc.x\n    steps: [ { id: a, type: agent, prompt: p, background: true, isolation: { mode: none } } ]\n",
		optoutBase + "triggers:\n  - on: svc.x\n    steps: [ { id: a, type: agent, prompt: p, runtime: pz, isolation: { mode: none } } ]\n",
		strings.Replace(optoutBase, "pz: { use: paseo, default: true }", "pz: { use: paseo, default: true, isolation: { mode: none } }", 1),
		"connectors:\n  svc: { use: web }\ntriggers:\n  - on: svc.x\n    steps: [ { id: a, type: agent, prompt: p, isolation: { mode: none } } ]\n", // the built-in paseo runtime
		optoutBase + "triggers:\n  - on: svc.x\n    steps: [ { id: a, type: agent, prompt: p, isolation: { mode: container, persistent: true, image: 'img:1', limits: { pids: 100 } } } ]\n",
		strings.Replace(optoutBase, "pz: { use: paseo, default: true }", "pz: { use: paseo, default: true, isolation: { mode: container } }", 1),
	}
	for i, doc := range ok {
		if _, err := loadDoc(t, doc); err != nil {
			t.Errorf("ok[%d]: %v", i, err)
		}
	}
	bad := map[string]string{
		"namespace on a paseo step": optoutBase + "triggers:\n  - on: svc.x\n    steps: [ { id: a, type: agent, prompt: p, isolation: { mode: namespace } } ]\n",
		"user on a paseo runtime":   strings.Replace(optoutBase, "pz: { use: paseo, default: true }", "pz: { use: paseo, default: true, isolation: { mode: user, user: x } }", 1),
		"none with extras":          optoutBase + "triggers:\n  - on: svc.x\n    steps: [ { id: a, type: agent, prompt: p, isolation: { mode: none, privileged: true } } ]\n",
		"built-in, namespace":       "connectors:\n  svc: { use: web }\ntriggers:\n  - on: svc.x\n    steps: [ { id: a, type: agent, prompt: p, isolation: { mode: namespace } } ]\n",
		"container with fs":         optoutBase + "triggers:\n  - on: svc.x\n    steps: [ { id: a, type: agent, prompt: p, isolation: { mode: container, fs: [/x] } } ]\n",
		"container with network":    optoutBase + "triggers:\n  - on: svc.x\n    steps: [ { id: a, type: agent, prompt: p, isolation: { mode: container, network: { egress: [a.com] } } } ]\n",
	}
	for name, doc := range bad {
		_, err := loadDoc(t, doc)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		want := "only `isolation: {mode: none}` or `{mode: container, …}` applies"
		if strings.HasPrefix(name, "container with") {
			want = "are not applied to a paseo agent"
		}
		if name != "none with extras" && !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// A trigger's / workflow's isolation: is validated as on each of its agent
// steps that carries none.
func TestScopeIsolationValidated(t *testing.T) {
	for i, doc := range []string{
		optoutBase + "triggers:\n  - on: svc.x\n    isolation: { mode: none }\n    steps: [ { id: a, type: agent, prompt: p, background: true } ]\n",
		optoutBase + "workflows:\n  w:\n    isolation: { mode: none }\n    steps: [ { id: a, type: agent, prompt: p } ]\n",
		// A step's own isolation is its own business: the default is not
		// checked against it.
		optoutBase + "triggers:\n  - on: svc.x\n    isolation: { mode: namespace }\n    steps: [ { id: a, type: agent, prompt: p, runtime: acpr }, { id: b, type: agent, prompt: p, isolation: { mode: none } } ]\n",
		// Non-agent steps are not governed by it.
		optoutBase + "triggers:\n  - on: svc.x\n    isolation: { mode: namespace }\n    steps: [ { id: a, uses: svc.post, options: { text: x } }, { id: b, type: agent, prompt: p, runtime: acpr } ]\n",
	} {
		if _, err := loadDoc(t, doc); err != nil {
			t.Errorf("ok[%d]: %v", i, err)
		}
	}
	for name, doc := range map[string]string{
		"trigger namespace over a paseo step":  optoutBase + "triggers:\n  - on: svc.x\n    isolation: { mode: namespace }\n    steps: [ { id: a, type: agent, prompt: p } ]\n",
		"workflow namespace over a paseo step": optoutBase + "workflows:\n  w:\n    isolation: { mode: namespace }\n    steps: [ { id: a, type: agent, prompt: p } ]\n",
		"an invalid mode":                      optoutBase + "workflows:\n  w:\n    isolation: { mode: bogus }\n    steps: [ { id: a, uses: svc.post } ]\n",
	} {
		if _, err := loadDoc(t, doc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestIsolationLoosens(t *testing.T) {
	for _, c := range []struct {
		iso   *IsolationConfig
		agent bool
		loose bool
	}{
		{nil, true, false},
		{&IsolationConfig{Mode: "none"}, true, true},
		{&IsolationConfig{Mode: "none"}, false, true},
		{&IsolationConfig{Mode: "user", User: "x"}, true, true},
		{&IsolationConfig{Mode: "user", User: "x"}, false, false},
		{&IsolationConfig{Mode: "container"}, true, true},
		{&IsolationConfig{Mode: "namespace", Privileged: true}, true, true},
		{&IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Egress: []string{"x"}}}, true, true},
		{&IsolationConfig{Mode: "namespace", FS: []string{"/x"}}, true, true},
		{&IsolationConfig{Mode: "namespace"}, true, false},
		{&IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{Deny: true}}, true, false},
		{&IsolationConfig{Mode: "namespace", Network: &IsolationNetwork{}}, true, false},
		{&IsolationConfig{Mode: "namespace", Limits: &IsolationLimits{Pids: 10}}, true, false},
	} {
		if got := isolationLoosens(c.iso, c.agent) != ""; got != c.loose {
			t.Errorf("%+v agent=%v: loosens=%v, want %v", c.iso, c.agent, got, c.loose)
		}
	}
	if (*IsolationConfig)(nil).OptsOut() || (&IsolationConfig{Mode: "namespace"}).OptsOut() || !(&IsolationConfig{Mode: "none"}).OptsOut() {
		t.Fatal("OptsOut")
	}
}

const optoutPack = `
pack: { name: rv, version: "1.0.0", requires: { conductor: ">=0.1", connectors: { svc: "*" } } }
workflows:
  review:
    steps:
      - { id: look, type: agent, prompt: p, background: true }
triggers:
  - name: watch
    on: svc.x
    steps: [ { id: s, workflow: review } ]
`

// A pack cannot opt its own agents out of the sandbox — or otherwise loosen
// it — on a step, a workflow or a trigger; load and lint both refuse it,
// naming the operator's override.
func TestPackCannotLoosenItsSandbox(t *testing.T) {
	cases := map[string]struct{ from, to, want string }{
		"step mode none":      {"background: true }", "background: true, isolation: { mode: none } }", "steps: { review/look: { isolation: { mode: none } } }"},
		"step container":      {"background: true }", "background: true, isolation: { mode: container, container: { image: x } } }", "review/look"},
		"step egress":         {"background: true }", "background: true, isolation: { mode: namespace, network: { egress: [evil.example] } } }", "network.egress"},
		"workflow mode none":  {"  review:\n    steps:", "  review:\n    isolation: { mode: none }\n    steps:", "triggers: { <trigger>: { isolation: { mode: none } } }"},
		"trigger mode none":   {"    on: svc.x\n", "    on: svc.x\n    isolation: { mode: none }\n", "triggers: { watch: { isolation: { mode: none } } }"},
		"code step mode none": {"    steps: [ { id: s, workflow: review } ]", "    steps: [ { id: s, workflow: review }, { id: c, run: sh, code: 'true', isolation: { mode: none } } ]", "watch/c"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(optoutPack, c.from, c.to, 1)
			if body == optoutPack {
				t.Fatal("fixture edit did not apply")
			}
			dir := t.TempDir()
			writePackSource(t, dir, "src/rv", body)
			_, err := resolveAndLoad(t, writeDoc(t, dir, "connectors:\n  svc: { use: web }\npacks:\n  rv:\n    source: ./src/rv\n    connectors: { svc: svc }\n"))
			if err == nil || !strings.Contains(err.Error(), "a pack can narrow its sandbox, never loosen it") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("load: %v", err)
			}
			man, err := LoadPackManifestForLint(dir + "/src/rv")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, p := range LintPackManifest(man) {
				found = found || strings.Contains(p, "never loosen it")
			}
			if !found {
				t.Fatalf("pack lint misses it: %v", LintPackManifest(man))
			}
		})
	}
	// Narrowing is fine.
	dir := t.TempDir()
	writePackSource(t, dir, "src/rv", strings.Replace(optoutPack, "background: true }", "background: true, isolation: { mode: namespace, network: { deny: true } } }", 1))
	if _, err := resolveAndLoad(t, writeDoc(t, dir, "connectors:\n  svc: { use: web }\npacks:\n  rv:\n    source: ./src/rv\n    connectors: { svc: svc }\n")); err != nil && strings.Contains(err.Error(), "never loosen") {
		t.Fatalf("a narrowing isolation is a pack's to set: %v", err)
	}
}

// The operator opts a pack's hand-off out from their own packs: block — on
// the step (steps: overlay) or on the trigger that runs the workflow (its
// arm) — the same mechanisms that set its runtime or gate.
func TestOperatorOptsAPackHandoffOut(t *testing.T) {
	load := func(inst string) *Config {
		t.Helper()
		dir := t.TempDir()
		writePackSource(t, dir, "src/rv", optoutPack)
		cfg, err := resolveAndLoad(t, writeDoc(t, dir, "connectors:\n  svc: { use: web }\npacks:\n  rv:\n    source: ./src/rv\n    connectors: { svc: svc }\n"+inst))
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		return cfg
	}
	cfg := load("    steps:\n      review/look: { isolation: { mode: none } }\n")
	if wf, ok := cfg.Workflows["rv/review"]; !ok || !wf.Steps[0].Isolation.OptsOut() {
		t.Fatalf("steps: overlay: %+v", cfg.Workflows["rv/review"])
	}
	cfg = load("    triggers:\n      watch: { enabled: true, isolation: { mode: none } }\n")
	var tr *TriggerSpec
	for i := range cfg.Triggers {
		if cfg.Triggers[i].Name == "rv/watch" {
			tr = &cfg.Triggers[i]
		}
	}
	if tr == nil || !tr.Isolation.OptsOut() {
		t.Fatalf("trigger arm: %+v", tr)
	}
	cfg = load("    triggers:\n      \"*\": { enabled: true }\n      watch: { isolation: { mode: none } }\n")
	for i := range cfg.Triggers {
		if cfg.Triggers[i].Name == "rv/watch" && !cfg.Triggers[i].Isolation.OptsOut() {
			t.Fatalf("a named arm's isolation over the wildcard: %+v", cfg.Triggers[i].Isolation)
		}
	}
	// Without the operator, the pack's hand-off carries none (sandboxed).
	cfg = load("    triggers:\n      watch: { enabled: true }\n")
	if cfg.Workflows["rv/review"].Steps[0].Isolation != nil {
		t.Fatal("no opt-out unless the operator writes one")
	}
}
