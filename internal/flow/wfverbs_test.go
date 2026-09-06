package flow

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/memory"
)

// tempSaved installs a fresh saved-workflow store (file-backed when path
// is non-empty).
func tempSaved(t *testing.T, path string) *SavedStore {
	t.Helper()
	sw, err := OpenSavedStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ConfigureSavedWorkflows(sw)
	t.Cleanup(func() { ConfigureSavedWorkflows(nil) })
	return sw
}

const wfBase = `
connectors:
  svc: { type: fake }
memory: { type: memory }
agents:
  planner: { model: x }
workflows:
  greet:
    description: "post a greeting"
    inputs: { who: { type: string, required: true } }
    steps: [ { id: post, uses: svc.post, options: { text: "hi {{.inputs.who}}" } } ]
policy:
  agent_authored:
    allow: [ svc.post, workflow, "workflow.*", kv.* ]
`

// TestWorkflowListCatalog: config + saved entries with descriptions, health,
// and rot flagging (rotting ones sort last and carry the flag).
func TestWorkflowListCatalog(t *testing.T) {
	sw := tempSaved(t, "")
	step := []config.Step{{Uses: "svc.post", Options: map[string]any{"text": "x"}}}
	_, _ = sw.Save("healthy", "does good things", step, memory.Source{Agent: "planner"})
	_, _ = sw.Save("rotten", "used to work", step, memory.Source{})
	_ = sw.Review("healthy")
	_ = sw.Review("rotten")
	sw.RecordOutcome("healthy", true)
	for i := 0; i < 3; i++ {
		sw.RecordOutcome("rotten", false)
	}

	cfg := loadConfig(t, wfBase)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - id: cat
    uses: workflow.list
  - id: report
    uses: svc.post
    options: { text: "{{.cat.count}} workflows" }
`))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("failed: %s", errStr)
	}
	if calls := fake.snapshot(); calls[len(calls)-1].Opts["text"] != "3 workflows" {
		t.Fatalf("catalog count: %+v", calls)
	}
	// Inspect the catalog structure directly.
	cat := rig.Runner.workflowCatalog()
	wfs := cat["workflows"].([]any)
	first := wfs[0].(map[string]any)
	if first["name"] != "greet" || first["source"] != "config" || first["description"] != "post a greeting" {
		t.Fatalf("config entry first: %+v", first)
	}
	last := wfs[2].(map[string]any)
	if last["name"] != "rotten" || last["flagged"] != true || !strings.Contains(last["description"].(string), "FLAGGED") {
		t.Fatalf("rotting entry deprioritized+flagged: %+v", last)
	}
	mid := wfs[1].(map[string]any)
	if mid["name"] != "healthy" || mid["success_rate"] != 1.0 || mid["reviewed"] != true {
		t.Fatalf("healthy saved entry: %+v", mid)
	}
}

// TestWorkflowRunByName: the Choose path — run a config workflow by name
// with inputs, rationale audited, outputs readable off the step.
func TestWorkflowRunByName(t *testing.T) {
	tempSaved(t, "")
	cfg := loadConfig(t, wfBase)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - id: pick
    uses: workflow.run
    options: { name: greet, with: { who: sam }, reason: "greeting fits the event" }
`))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("failed: %s", errStr)
	}
	if calls := fake.snapshot(); len(calls) != 1 || calls[0].Opts["text"] != "hi sam" {
		t.Fatalf("run-by-name: %+v", calls)
	}
	choices := rig.Store.auditsWithEvent("workflow_choice")
	if len(choices) != 1 || choices[0]["workflow"] != "greet" || choices[0]["reason"] != "greeting fits the event" {
		t.Fatalf("choice audit: %+v", choices)
	}
	// Bad shapes error clearly.
	rig2 := newTestRunner(t, cfg, reg)
	runTrigger(rig2, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps: [ { uses: workflow.run, options: { name: greet, steps: [] } } ]
`))
	if failed, errStr := rig2.workflowFailed(); !failed || !strings.Contains(errStr, "exactly one of") {
		t.Fatalf("name+steps: %v %q", failed, errStr)
	}
}

// TestWorkflowRunInlineStepsGuarded: workflow.run {steps} is agent-authored
// by definition — the plan guard applies.
func TestWorkflowRunInlineStepsGuarded(t *testing.T) {
	tempSaved(t, "")
	cfg := loadConfig(t, wfBase)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - id: inline
    uses: workflow.run
    options:
      steps:
        - { id: a, uses: svc.post, options: { text: inline-ran } }
`))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("allowed inline steps must run: %s", errStr)
	}
	if calls := fake.snapshot(); len(calls) != 1 || calls[0].Opts["text"] != "inline-ran" {
		t.Fatalf("inline run: %+v", calls)
	}
	// A non-allowed verb in inline steps is rejected pre-run.
	rig2 := newTestRunner(t, cfg, reg)
	runTrigger(rig2, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - uses: workflow.run
    options:
      steps: [ { uses: svc.ask, options: { prompt: p } } ]
`))
	if failed, errStr := rig2.workflowFailed(); !failed || !strings.Contains(errStr, "not in policy.agent_authored.allow") {
		t.Fatalf("inline guard: %v %q", failed, errStr)
	}
}

// TestWorkflowSavePromoteAndReuse: save persists a versioned, provenance-
// stamped, UNREVIEWED workflow; running it is refused until review; after
// review it runs by dynamic name and its health is tracked; a new save bumps
// the version and resets review.
func TestWorkflowSavePromoteAndReuse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workflows.json")
	sw := tempSaved(t, path)
	cfg := loadConfig(t, wfBase)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	// Save via a hook too (best-effort path) and a step.
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - id: promote
    uses: workflow.save
    options:
      name: cleanup
      description: "posts the cleanup note"
      steps:
        - { id: note, uses: svc.post, options: { text: "cleanup done" } }
`))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("save failed: %s", errStr)
	}
	w, ok := sw.Get("cleanup")
	if !ok || w.Version != 1 || w.Reviewed || w.Description != "posts the cleanup note" {
		t.Fatalf("saved: %+v", w)
	}
	if w.Source.Trigger != "ping" || w.Source.Repo != "o/r" {
		t.Fatalf("provenance: %+v", w.Source)
	}
	saves := rig.Store.auditsWithEvent("workflow_save")
	if len(saves) != 1 || saves[0]["workflow"] != "cleanup" {
		t.Fatalf("save audit: %+v", saves)
	}

	// The file persists across a reopen (restart).
	sw2, err := OpenSavedStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sw2.Get("cleanup"); !ok {
		t.Fatal("saved workflow must persist to disk")
	}

	// Unreviewed → a real run refuses with the review pointer.
	runSpec := mustSpec(t, `
on: svc.ping
steps: [ { id: go, workflow: cleanup } ]
`)
	rig2 := newTestRunner(t, cfg, reg)
	runTrigger(rig2, newTrigger("ping", nil), runSpec)
	if failed, errStr := rig2.workflowFailed(); !failed || !strings.Contains(errStr, "unreviewed") {
		t.Fatalf("unreviewed gate: %v %q", failed, errStr)
	}

	// Reviewed → runs; health recorded.
	if err := sw.Review("cleanup"); err != nil {
		t.Fatal(err)
	}
	rig3 := newTestRunner(t, cfg, reg)
	runTrigger(rig3, newTrigger("ping", nil), runSpec)
	if failed, errStr := rig3.workflowFailed(); failed {
		t.Fatalf("reviewed run failed: %s", errStr)
	}
	var ran bool
	for _, c := range fake.snapshot() {
		if c.Opts["text"] == "cleanup done" {
			ran = true
		}
	}
	if !ran {
		t.Fatal("promoted workflow must run after review")
	}
	if w, _ := sw.Get("cleanup"); w.Successes != 1 {
		t.Fatalf("health: %+v", w)
	}

	// A new version resets review + track record.
	_, err = sw.Save("cleanup", "v2", []config.Step{{Uses: "svc.post", Options: map[string]any{"text": "v2"}}}, memory.Source{})
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := sw.Get("cleanup"); w.Version != 2 || w.Reviewed || w.Runs() != 0 {
		t.Fatalf("re-save must reset trust: %+v", w)
	}

	// Guard rails on save itself.
	if _, err := sw.Save("", "d", w.Steps, memory.Source{}); err == nil {
		t.Fatal("empty name must error")
	}
	rig4 := newTestRunner(t, cfg, reg)
	runTrigger(rig4, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - uses: workflow.save
    options: { name: greet, steps: [ { uses: svc.post, options: { text: x } } ] }
`))
	if failed, errStr := rig4.workflowFailed(); !failed || !strings.Contains(errStr, "config workflow") {
		t.Fatalf("config-name shadowing: %v %q", failed, errStr)
	}
	rig5 := newTestRunner(t, cfg, reg)
	runTrigger(rig5, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - uses: workflow.save
    options: { name: bad, steps: [ { uses: svc.bogus, options: {} } ] }
`))
	if failed, errStr := rig5.workflowFailed(); !failed || !strings.Contains(errStr, `no verb "bogus"`) {
		t.Fatalf("save validates steps: %v %q", failed, errStr)
	}
}

// TestWorkflowSaveShadowDoesNotPersist: a dry-run save validates but writes
// nothing.
func TestWorkflowSaveShadowDoesNotPersist(t *testing.T) {
	sw := tempSaved(t, "")
	cfg := loadConfig(t, wfBase)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.DryRun = true
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - uses: workflow.save
    options: { name: ephemeral, steps: [ { uses: svc.post, options: { text: x } } ] }
`))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("shadow save failed: %s", errStr)
	}
	if _, ok := sw.Get("ephemeral"); ok {
		t.Fatal("a dry-run save must not persist")
	}
}

// TestChooseFallbackToAuthoring: the recognize→pick→run vs author decision is
// the agent's, but the mechanics compose: an agent plan can list the catalog
// via a kv-free verb step, run a fit by dynamic name, and fall back to inline
// steps when nothing fits — all in one plan.
func TestChooseFallbackToAuthoring(t *testing.T) {
	sw := tempSaved(t, "")
	step := []config.Step{{ID: "n", Uses: "svc.post", Options: map[string]any{"text": "from-saved"}}}
	_, _ = sw.Save("fit", "the fitting one", step, memory.Source{})
	_ = sw.Review("fit")

	cfg := loadConfig(t, wfBase)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	// The agent's plan: consult the catalog, run the pick, then author one
	// extra step of its own — the full choose-then-extend shape.
	plan := "```plan\n" +
		"- id: cat\n  uses: workflow.list\n" +
		"- id: run-pick\n  uses: workflow.run\n  options: { name: fit, reason: \"catalog said so\" }\n" +
		"- id: extra\n  uses: svc.post\n  options: { text: \"authored extra\" }\n" +
		"```"
	rig.Agents.dispatchFunc = planDispatch(plan)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("choose plan failed: %s", errStr)
	}
	var texts []string
	for _, c := range fake.snapshot() {
		texts = append(texts, fmt.Sprint(c.Opts["text"]))
	}
	if len(texts) != 2 || texts[0] != "from-saved" || texts[1] != "authored extra" {
		t.Fatalf("choose+author: %v", texts)
	}
	choices := rig.Store.auditsWithEvent("workflow_choice")
	if len(choices) != 1 || choices[0]["agent"] != "planner" || choices[0]["reason"] != "catalog said so" {
		t.Fatalf("choice rationale audit: %+v", choices)
	}
}

// REGRESSION (audit finding #1): promotion is not a laundering path. A
// policy-gated step is rejected at workflow.save; a workflow saved under a
// permissive policy is RE-GUARDED at every run under the then-current
// policy — review is a trust signal, never a policy bypass — and
// approve-listed content in a saved workflow goes through the approval gate
// at run time.
func TestSavedWorkflowLaunderingClosed(t *testing.T) {
	sw := tempSaved(t, "")

	// 1. Saving a plan with a non-allowed verb is rejected by the guard.
	cfg := loadConfig(t, wfBase)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - uses: workflow.save
    options: { name: sneak, steps: [ { uses: svc.ask, options: { prompt: p } } ] }
`))
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "not in policy.agent_authored.allow") {
		t.Fatalf("gated step must be rejected at save: %v %q", failed, errStr)
	}
	if _, ok := sw.Get("sneak"); ok {
		t.Fatal("a rejected save must persist nothing")
	}

	// 2. A hostless command step can't be saved either (sandbox rule).
	rig2 := newTestRunner(t, cfg, reg)
	runTrigger(rig2, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - uses: workflow.save
    options: { name: shell, steps: [ { type: command, command: [rm, -rf, /] } ] }
`))
	if failed, errStr := rig2.workflowFailed(); !failed || !strings.Contains(errStr, "not in policy.agent_authored.allow") {
		t.Fatalf("command step outside allow must reject at save: %v %q", failed, errStr)
	}

	// 3. THE LAUNDERING PATH: save legitimately under a permissive policy,
	// blind-review it, then TIGHTEN the policy — the run must be re-guarded
	// and refused under the current rules.
	if _, err := sw.Save("laundered", "was fine once",
		[]config.Step{{ID: "p", Uses: "svc.post", Options: map[string]any{"text": "x"}}},
		memory.Source{Agent: "planner"}); err != nil {
		t.Fatal(err)
	}
	if err := sw.Review("laundered"); err != nil {
		t.Fatal(err)
	}
	tight := loadConfig(t, `
connectors:
  svc: { type: fake }
memory: { type: memory }
agents:
  planner: { model: x }
workflows: {}
policy:
  agent_authored:
    allow: [ kv.* ]           # svc.post no longer allowed
`)
	regT := buildRegistry(t, tight)
	rigT := newTestRunner(t, tight, regT)
	runTrigger(rigT, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps: [ { id: go, workflow: laundered } ]
`))
	failed, errStr := rigT.workflowFailed()
	if !failed || !strings.Contains(errStr, `"svc.post" is not in policy.agent_authored.allow`) {
		t.Fatalf("reviewed-but-now-disallowed workflow must refuse: %v %q", failed, errStr)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatalf("nothing may run: %+v", fake.snapshot())
	}
	// And with NO agent_authored policy at all, a saved workflow refuses too.
	nopol := loadConfig(t, `
connectors:
  svc: { type: fake }
agents:
  planner: { model: x }
`)
	regN := buildRegistry(t, nopol)
	rigN := newTestRunner(t, nopol, regN)
	runTrigger(rigN, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps: [ { id: go, workflow: laundered } ]
`))
	if failed, errStr := rigN.workflowFailed(); !failed || !strings.Contains(errStr, "disabled") {
		t.Fatalf("saved workflow without a policy must refuse: %v %q", failed, errStr)
	}

	// 4. Approve-listed content in a saved workflow hits the approval gate at
	// run time (no approve_via here → dry-run + reject), even though it saved.
	approveCfg := loadConfig(t, `
connectors:
  svc: { type: fake }
memory: { type: memory }
agents:
  planner: { model: x }
policy:
  agent_authored:
    allow: [ svc.post ]
    approve: [ svc.ask ]
`)
	regA := buildRegistry(t, approveCfg)
	rigA := newTestRunner(t, approveCfg, regA)
	runTrigger(rigA, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - uses: workflow.save
    options: { name: risky, steps: [ { uses: svc.ask, options: { prompt: p } } ] }
`))
	if failed, errStr := rigA.workflowFailed(); failed {
		t.Fatalf("approve-listed steps may SAVE (gated at run): %s", errStr)
	}
	if err := sw.Review("risky"); err != nil {
		t.Fatal(err)
	}
	rigA2 := newTestRunner(t, approveCfg, regA)
	runTrigger(rigA2, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps: [ { id: go, workflow: risky } ]
`))
	if failed, errStr := rigA2.workflowFailed(); !failed || !strings.Contains(errStr, "approve_via is not set") {
		t.Fatalf("approve-listed saved workflow must gate at run: %v %q", failed, errStr)
	}

	// 5. Identity/host rewrites apply to the RUN copy without mutating the
	// registry: an identity policy forces as: on the saved steps at run.
	idCfg := loadConfig(t, `
connectors:
  svc: { type: fake }
memory: { type: memory }
agents:
  planner: { model: x }
policy:
  agent_authored:
    allow: [ svc.post ]
    identity: bot
`)
	regI := buildRegistry(t, idCfg)
	fakeI := newFakeState(t, "svc")
	rigI := newTestRunner(t, idCfg, regI)
	runTrigger(rigI, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps: [ { id: go, workflow: laundered } ]
`))
	if failed, errStr := rigI.workflowFailed(); failed {
		t.Fatalf("allowed saved workflow must run: %s", errStr)
	}
	calls := fakeI.snapshot()
	if len(calls) != 1 || calls[0].Opts["as"] != "bot" {
		t.Fatalf("run-time identity rewrite: %+v", calls)
	}
	if w, _ := sw.Get("laundered"); w.Steps[0].Options["as"] != nil {
		t.Fatal("the persisted registry copy must stay unmutated")
	}
}

// REGRESSION: a saved (agent-authored) workflow ran with the FULL
// secrets/vaults maps in its scope — a step like {{printf "%v" $}} dumped
// the entire template root, secret values included, while mentioning
// neither "secrets" nor "vaults", so the egress detector had nothing to
// catch. Saved workflows now run in the same empty-secrets scope as inline
// plans; config workflows keep their operator-authored access.
func TestSavedWorkflowScopeCarriesNoSecrets(t *testing.T) {
	sw := tempSaved(t, "")
	cfg := loadConfig(t, wfBase)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")

	// A promoted workflow that dumps its whole template root outward.
	_, err := sw.Save("dump", "posts the root", []config.Step{
		{ID: "leak", Uses: "svc.post", Options: map[string]any{"text": `{{printf "%v" $}}`}},
	}, memory.Source{Agent: "planner"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sw.Review("dump"); err != nil {
		t.Fatal(err)
	}
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps: [ { id: go, workflow: dump } ]
`))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("saved workflow run failed: %s", errStr)
	}
	for _, c := range fake.snapshot() {
		text, _ := c.Opts["text"].(string)
		if strings.Contains(text, "s3kr1t-value") {
			t.Fatalf("whole-root dump from a saved workflow exfiltrated a secret: %s", text)
		}
	}
	if len(fake.snapshot()) == 0 {
		t.Fatal("the dump step must still have run")
	}

	// A CONFIG workflow is operator-authored — its scope keeps the secrets.
	cfg2 := loadConfig(t, strings.Replace(wfBase, "workflows:\n",
		"workflows:\n  cfg-secret:\n    steps: [ { id: post, uses: svc.post, options: { text: \"tok={{.secrets.tok}}\" } } ]\n", 1))
	reg2 := buildRegistry(t, cfg2)
	fake2 := newFakeState(t, "svc")
	rig2 := newTestRunner(t, cfg2, reg2)
	runTrigger(rig2, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps: [ { id: go, workflow: cfg-secret } ]
`))
	if failed, errStr := rig2.workflowFailed(); failed {
		t.Fatalf("config workflow run failed: %s", errStr)
	}
	var sawSecret bool
	for _, c := range fake2.snapshot() {
		if c.Opts["text"] == "tok=s3kr1t-value" {
			sawSecret = true
		}
	}
	if !sawSecret {
		t.Fatal("a config workflow's scope must keep the operator's secrets")
	}
}
