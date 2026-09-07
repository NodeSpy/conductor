package flow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"encoding/json"
	"github.com/NodeSpy/conductor/internal/config"
	"os"
	"path/filepath"

	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/store"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// planCfg builds a config with an agent_authored policy block.
func planCfg(t *testing.T, policyYAML string) *config.Config {
	t.Helper()
	return loadConfig(t, `
connectors:
  svc: { type: fake }
memory: { type: memory }
agents:
  planner: { model: x }
  helper:  { model: y }
`+policyYAML)
}

const allowPolicy = `
policy:
  agent_authored:
    allow: [ kv.*, memory.*, svc.post, workflow, agent ]
`

// planSpec is a trigger whose single agent step emits whatever the fake
// dispatcher returns.
var planSpec = `
on: svc.ping
steps:
  - id: author
    type: agent
    agent: planner
    prompt: "plan it"
`

// dispatchPlan wires the fake dispatcher to return output and fires the
// trigger.
func dispatchPlan(t *testing.T, cfg *config.Config, output string) (*testRig, *fakeState) {
	t.Helper()
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		if req.Action.Agent == "planner" {
			return dispatch.RunRef{AgentID: "a1", Output: output}, nil
		}
		return dispatch.RunRef{AgentID: "sub", Output: `{"done":true}`}, nil
	}
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "m"}), mustSpec(t, planSpec))
	return rig, fake
}

func TestParsePlanShapes(t *testing.T) {
	// Fenced YAML.
	steps, found, err := ParsePlan("done\n```plan\n- id: a\n  uses: svc.post\n  options: { text: hi }\n```")
	if err != nil || !found || len(steps) != 1 || steps[0].Uses != "svc.post" {
		t.Fatalf("fence: %v %v %+v", found, err, steps)
	}
	// JSON plan key under a wrapper.
	steps, found, err = ParsePlan(`{"output":{"plan":[{"run":"js","code":"return 1;"}]}}`)
	if err != nil || !found || len(steps) != 1 || steps[0].Run != "js" {
		t.Fatalf("json: %v %v %+v", found, err, steps)
	}
	// No plan.
	if _, found, err := ParsePlan("all done"); found || err != nil {
		t.Fatalf("absent: %v %v", found, err)
	}
	if _, found, err := ParsePlan(`{"verdict":"ok"}`); found || err != nil {
		t.Fatalf("json absent: %v %v", found, err)
	}
	// Malformed shapes are errors, not silent drops.
	if _, _, err := ParsePlan("```plan\n- id: [broken\n```"); err == nil {
		t.Fatal("bad yaml must error")
	}
	if _, _, err := ParsePlan("```plan\n- uses: x\n"); err == nil {
		t.Fatal("unterminated fence must error")
	}
	if _, _, err := ParsePlan(`{"plan":"not a list"}`); err == nil {
		t.Fatal("non-list plan must error")
	}
	// Prose mentioning ```planning is not a fence.
	if _, found, _ := ParsePlan("i keep ```planning things``` badly"); found {
		t.Fatal("prose must not parse as a plan")
	}
}

// TestPlanExecutesThroughRunner: an allowlisted plan runs deterministically
// through the flow runner, its outputs land under the agent step, the run is
// audited with its gate + determinism class.
func TestPlanExecutesThroughRunner(t *testing.T) {
	mem := tempMemory(t)
	cfg := planCfg(t, allowPolicy)
	out := "on it\n```plan\n" +
		"- id: note\n  uses: memory.remember\n  options: { text: \"planned for {{.repo}}\" }\n" +
		"- id: post\n  uses: svc.post\n  options: { text: \"did {{.plan_author}} thing\" }\n" +
		"```"
	rig, fake := dispatchPlan(t, cfg, out)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if calls := fake.snapshot(); len(calls) != 1 || calls[0].Opts["text"] != "did planner thing" {
		t.Fatalf("plan verb calls: %+v", calls)
	}
	if all, _ := mem.List(); len(all) != 1 || all[0].Text != "planned for o/r" {
		t.Fatalf("plan memory write: %+v", all)
	}
	plans := rig.Store.auditsWithEvent("plan")
	if len(plans) != 1 || plans[0]["outcome"] != "admitted" || plans[0]["gate"] != "allow" || plans[0]["deterministic"] != true {
		t.Fatalf("plan audit: %+v", plans)
	}
	stepsAudited := rig.Store.auditsWithEvent("plan_step")
	if len(stepsAudited) != 2 || stepsAudited[0]["outcome"] != "ok" {
		t.Fatalf("plan_step audits: %+v", stepsAudited)
	}
}

// TestPlanDefaultReject: with no agent_authored policy anywhere, a plan is
// rejected before anything runs — safe by default.
func TestPlanDefaultReject(t *testing.T) {
	cfg := planCfg(t, "")
	out := "```plan\n- uses: svc.post\n  options: { text: hi }\n```"
	rig, fake := dispatchPlan(t, cfg, out)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "disabled") {
		t.Fatalf("default must reject: %v %q", failed, errStr)
	}
	if calls := fake.snapshot(); len(calls) != 0 {
		t.Fatalf("nothing may run on rejection: %+v", calls)
	}
	plans := rig.Store.auditsWithEvent("plan")
	if len(plans) != 1 || plans[0]["outcome"] != "rejected" {
		t.Fatalf("rejection audit: %+v", plans)
	}
}

// TestPlanAllowlistRejection: a verb outside the allowlist rejects the WHOLE
// plan pre-run, naming the class.
func TestPlanAllowlistRejection(t *testing.T) {
	cfg := planCfg(t, allowPolicy)
	out := "```plan\n- uses: svc.post\n  options: { text: ok }\n- uses: svc.ask\n  options: { prompt: p }\n```"
	rig, fake := dispatchPlan(t, cfg, out)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, `"svc.ask" is not in policy.agent_authored.allow`) {
		t.Fatalf("allowlist rejection: %v %q", failed, errStr)
	}
	if calls := fake.snapshot(); len(calls) != 0 {
		t.Fatalf("no step may run when any is rejected: %+v", calls)
	}
}

// TestPlanSchemaValidation: emitted steps validate against the connector
// schemas before the guard even looks.
func TestPlanSchemaValidation(t *testing.T) {
	cfg := planCfg(t, allowPolicy)
	cases := []struct{ name, plan, wantErr string }{
		{"unknown verb", "- uses: svc.bogus\n  options: {}", `no verb "bogus"`},
		{"unknown connector", "- uses: ghost.post\n  options: {}", `unknown connector "ghost"`},
		{"bad option", "- uses: svc.post\n  options: { text: t, nope: 1 }", `"nope"`},
		{"missing required option", "- uses: svc.post\n  options: {}", `"text"`},
		{"unknown agent", "- type: agent\n  agent: ghost\n  prompt: p", `unknown agent "ghost"`},
		{"formless step", "- id: what", "no recognizable step form"},
		{"unknown workflow", "- workflow: ghost", `unknown workflow "ghost"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rig, _ := dispatchPlan(t, cfg, "```plan\n"+c.plan+"\n```")
			failed, errStr := rig.workflowFailed()
			if !failed || !strings.Contains(errStr, c.wantErr) {
				t.Fatalf("want %q, got failed=%v %q", c.wantErr, failed, errStr)
			}
		})
	}
}

// TestPlanLimits: declared and runtime limits halt the plan — steps,
// sub-agents, fan-out.
func TestPlanLimits(t *testing.T) {
	pol := `
policy:
  agent_authored:
    allow: [ svc.post, agent, code ]
    host: sandbox
    limits: { max_steps: 2, max_sub_agents: 1, max_fan_out: 2 }
hosts:
  sandbox: { addr: sandbox.local }
`
	cfg := planCfg(t, pol)

	// Too many declared steps.
	over := "```plan\n- uses: svc.post\n  options: {text: a}\n- uses: svc.post\n  options: {text: b}\n- uses: svc.post\n  options: {text: c}\n```"
	rig, fake := dispatchPlan(t, cfg, over)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "max_steps") {
		t.Fatalf("max_steps: %v %q", failed, errStr)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("an over-limit plan must not start")
	}

	// Too many declared sub-agents.
	agents := "```plan\n- {type: agent, agent: helper, prompt: a}\n- {type: agent, agent: helper, prompt: b}\n```"
	rig, _ = dispatchPlan(t, cfg, agents)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "max_sub_agents") {
		t.Fatalf("max_sub_agents: %v %q", failed, errStr)
	}

	// Runtime fan-out (a for_each list only knowable at run time).
	fan := "```plan\n- id: fan\n  for_each: \"a,b,c\"\n  uses: svc.post\n  options: { text: \"{{.item}}\" }\n```"
	rig, fake = dispatchPlan(t, cfg, fan)
	if failed, errStr := rig.workflowFailed(); !failed || !strings.Contains(errStr, "max_fan_out") {
		t.Fatalf("max_fan_out: %v %q", failed, errStr)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("an over-fan-out step must not fire")
	}
}

// TestPlanScopeHasNoSecrets: the plan's template scope carries neither named
// secrets nor preloaded vault values — even with the egress gate explicitly
// lifted, a .secrets reference renders empty.
func TestPlanScopeHasNoSecrets(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.post ]
    allow_secrets: ["*"]
    no_secret_egress: false
`)
	out := "```plan\n- id: leak\n  uses: svc.post\n  options: { text: \"tok={{.secrets.tok}}.\" }\n```"
	rig, fake := dispatchPlan(t, cfg, out)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := fake.snapshot()
	// The rig defines secrets.tok = s3kr1t-value in the TRIGGER scope; the
	// plan scope must render it empty.
	if len(calls) != 1 || calls[0].Opts["text"] != "tok=." {
		t.Fatalf("plan scope leaked secrets: %+v", calls)
	}
}

// TestPlanContinueOnErrorAndIf: plan steps honor if: and continue_on_error
// like any workflow step.
func TestPlanContinueOnErrorAndIf(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.* ]
`)
	out := "```plan\n" +
		"- id: skipme\n  if: \"kind == 'nope'\"\n  uses: svc.post\n  options: { text: skipped }\n" +
		"- id: boom\n  continue_on_error: true\n  uses: svc.fail\n  options: {}\n" +
		"- id: after\n  if: \"steps.boom.outputs.failed == true\"\n  uses: svc.post\n  options: { text: recovered }\n" +
		"```"
	rig, fake := dispatchPlan(t, cfg, out)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := fake.snapshot()
	// svc.fail records its call then errors; svc.post fires once (recovered).
	var texts []string
	for _, c := range calls {
		if c.Verb == "post" {
			texts = append(texts, fmt.Sprint(c.Opts["text"]))
		}
	}
	if len(texts) != 1 || texts[0] != "recovered" {
		t.Fatalf("if/continue_on_error: %+v", calls)
	}
}

// TestPlanFailureCompensatesAndEscalates: with no revise service wired, a
// failing step unwinds committed compensations in reverse and escalates.
func TestPlanFailureCompensatesAndEscalates(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.* ]
`)
	out := "```plan\n" +
		"- id: one\n  uses: svc.post\n  options: { text: one }\n  compensate: { uses: svc.post, options: { text: undo-one } }\n" +
		"- id: two\n  uses: svc.post\n  options: { text: two }\n  compensate: { uses: svc.post, options: { text: undo-two } }\n" +
		"- id: boom\n  uses: svc.fail\n  options: {}\n" +
		"```"
	rig, fake := dispatchPlan(t, cfg, out)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, `plan step "boom"`) {
		t.Fatalf("failure: %v %q", failed, errStr)
	}
	var texts []string
	for _, c := range fake.snapshot() {
		if c.Verb == "post" {
			texts = append(texts, fmt.Sprint(c.Opts["text"]))
		}
	}
	// Forward order, then compensations in REVERSE.
	want := []string{"one", "two", "undo-two", "undo-one"}
	if len(texts) != 4 || texts[2] != want[2] || texts[3] != want[3] {
		t.Fatalf("compensation order: %v, want %v", texts, want)
	}
	comps := rig.Store.auditsWithEvent("plan_compensate")
	if len(comps) != 2 {
		t.Fatalf("compensate audits: %+v", comps)
	}
	// The failure escalated to a human.
	var needs int
	for _, e := range rig.Notifier.snapshot() {
		if e.Event == "needs_input" {
			needs++
		}
	}
	if needs == 0 {
		t.Fatal("terminal plan failure must escalate needs_input")
	}
}

// TestPlanHybridClassification: a plan with a sub-agent step is hybrid
// (deterministic=false) and the sub-agent's dispatch flows through the
// normal agent path.
func TestPlanHybridClassification(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.post, agent ]
`)
	out := "```plan\n- id: sub\n  type: agent\n  agent: helper\n  prompt: \"go\"\n- id: done\n  uses: svc.post\n  options: { text: \"sub said {{.sub.done}}\" }\n```"
	rig, fake := dispatchPlan(t, cfg, out)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if calls := fake.snapshot(); len(calls) != 1 || calls[0].Opts["text"] != "sub said true" {
		t.Fatalf("sub-agent output flow: %+v", calls)
	}
	plans := rig.Store.auditsWithEvent("plan")
	if len(plans) != 1 || plans[0]["deterministic"] != false || plans[0]["sub_agents"] != 1 {
		t.Fatalf("hybrid classification: %+v", plans)
	}
}

// planDispatch returns a dispatchFunc emitting one canned plan output.
func planDispatch(output string) func(context.Context, dispatch.Request) (dispatch.RunRef, error) {
	return func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1", Output: output}, nil
	}
}

// TestRunLiveStep: the run_step live tool executes one guarded step against
// a trigger reconstructed from the tool's provenance — the same allowlist
// applies as for a plan: block.
func TestRunLiveStep(t *testing.T) {
	cfg := planCfg(t, allowPolicy)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	src := memory.Source{Agent: "planner", Repo: "o/r", Trigger: "new_comment"}

	out, err := rig.Runner.RunLiveStep(context.Background(), src, 7,
		map[string]any{"id": "hi", "uses": "svc.post", "options": map[string]any{"text": "live {{.repo}}#{{.pr}}"}})
	if err != nil {
		t.Fatalf("live step: %v", err)
	}
	if out["deterministic"] != true || out["executed"] != 1 {
		t.Fatalf("live outputs: %+v", out)
	}
	if calls := fake.snapshot(); len(calls) != 1 || calls[0].Opts["text"] != "live o/r#7" {
		t.Fatalf("live call: %+v", calls)
	}
	// The guard applies identically.
	_, err = rig.Runner.RunLiveStep(context.Background(), src, 7,
		map[string]any{"uses": "svc.ask", "options": map[string]any{"prompt": "p"}})
	if err == nil || !strings.Contains(err.Error(), "not in policy.agent_authored.allow") {
		t.Fatalf("live guard: %v", err)
	}
	// And the audit attributes the plan to the live agent.
	plans := rig.Store.auditsWithEvent("plan")
	if len(plans) != 2 || plans[0]["agent"] != "planner" {
		t.Fatalf("live audit: %+v", plans)
	}
}

// REGRESSION (audit finding #3): plan resume idempotency is REAL, not
// claimed. An agent-step plan checkpoints its committed progress per
// run+step; a restart resumes AFTER the last committed step — the agent is
// not re-dispatched and committed side effects never re-run. Checkpoints are
// removed on normal completion and terminal failure, and a resumed plan is
// re-guarded under the current policy.
func TestPlanResumeIdempotency(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.* ]
`)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)

	// 1. In-flight persistence: at the moment the revise round runs (step
	// "boom" failed), step "one" must already be checkpointed with its
	// outputs — that's what a crash would recover from.
	var midFlight *store.PlanRecord
	rig.Runner.Agents.Revise = func(ctx context.Context, agent string, tr core.Trigger, prompt string) (string, bool, error) {
		if rec, ok := rig.Store.GetPlan("flow:ping:o/r#7", "author"); ok {
			midFlight = &rec
		}
		return "no plan", true, nil // give up → terminal failure → checkpoint removed
	}
	plan := "```plan\n" +
		"- id: one\n  uses: svc.post\n  options: { text: one }\n" +
		"- id: boom\n  uses: svc.fail\n  options: {}\n" +
		"```"
	rig.Agents.dispatchFunc = planDispatch(plan)
	run := emptyRun()
	run.ID = "flow:ping:o/r#7"
	runTriggerWithRun(rig, run, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("plan should have failed terminally")
	}
	if midFlight == nil {
		t.Fatal("a committed step must be checkpointed before the revise round")
	}
	if midFlight.Next != 1 || midFlight.Agent != "planner" || midFlight.Outputs["one"] == nil {
		t.Fatalf("checkpoint content: %+v", midFlight)
	}
	if _, ok := rig.Store.GetPlan("flow:ping:o/r#7", "author"); ok {
		t.Fatal("a terminal failure must remove the checkpoint (compensations ran)")
	}

	// 2. THE RESUME PATH: a persisted checkpoint (as a crash would leave)
	// makes the next run of the same run+step SKIP the agent dispatch and
	// SKIP the committed step, running only the remainder.
	stepsYAML := "- id: one\n  uses: svc.post\n  options: { text: one }\n- id: two\n  uses: svc.post\n  options: { text: two }\n"
	rig2 := newTestRunner(t, cfg, reg)
	if err := rig2.Store.PutPlan(store.PlanRecord{
		RunID: "flow:ping:o/r#7", StepID: "author", Agent: "planner",
		Steps: []byte(stepsYAML), Next: 1,
		Outputs: map[string]map[string]any{"one": {"id": 1}},
	}); err != nil {
		t.Fatal(err)
	}
	fake2 := newFakeState(t, "svc")
	dispatched := 0
	rig2.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		dispatched++
		return dispatch.RunRef{AgentID: "a1", Output: "should not run"}, nil
	}
	run2 := emptyRun()
	run2.ID = "flow:ping:o/r#7"
	runTriggerWithRun(rig2, run2, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig2.workflowFailed(); failed {
		t.Fatalf("resume failed: %s", errStr)
	}
	if dispatched != 0 {
		t.Fatal("resume must NOT re-dispatch the authoring agent")
	}
	var texts []string
	for _, c := range fake2.snapshot() {
		texts = append(texts, fmt.Sprint(c.Opts["text"]))
	}
	// Only "two" runs — "one" was committed before the crash.
	if len(texts) != 1 || texts[0] != "two" {
		t.Fatalf("resume must skip committed steps: %v", texts)
	}
	if _, ok := rig2.Store.GetPlan("flow:ping:o/r#7", "author"); ok {
		t.Fatal("completion must remove the checkpoint")
	}
	resumes := rig2.Store.auditsWithEvent("plan")
	if len(resumes) == 0 || resumes[0]["outcome"] != "resumed" {
		t.Fatalf("resume audit: %+v", resumes)
	}

	// 3. A resumed plan is re-guarded under the CURRENT policy.
	tight := planCfg(t, `
policy:
  agent_authored:
    allow: [ kv.* ]
`)
	regT := buildRegistry(t, tight)
	rig3 := newTestRunner(t, tight, regT)
	_ = rig3.Store.PutPlan(store.PlanRecord{
		RunID: "flow:ping:o/r#7", StepID: "author", Agent: "planner",
		Steps: []byte(stepsYAML), Next: 1,
	})
	rig3.Agents.dispatchFunc = planDispatch("unused")
	run3 := emptyRun()
	run3.ID = "flow:ping:o/r#7"
	runTriggerWithRun(rig3, run3, newTrigger("ping", nil), mustSpec(t, planSpec))
	if failed, errStr := rig3.workflowFailed(); !failed || !strings.Contains(errStr, "not in policy.agent_authored.allow") {
		t.Fatalf("resume must re-guard: %v %q", failed, errStr)
	}
}

// REGRESSION (audit finding #4): a vault-read value NEVER persists cleartext
// in a checkpoint. The committed step stores a re-resolve marker instead,
// and a resume re-reads the vault so later steps' templating still sees the
// real value.
func TestCheckpointNeverPersistsVaultValues(t *testing.T) {
	t.Cleanup(vaults.Reset)
	vaultDir := t.TempDir()
	const secretVal = "hunter2-cleartext-secret"
	if err := os.WriteFile(filepath.Join(vaultDir, "apikey"), []byte(secretVal), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
vaults:
  hv: { type: file, dir: `+vaultDir+` }
`)
	// Production shares ONE secrets resolver between the connector registry
	// (whose vault reads Track values) and the runner (whose checkpoint
	// scrub Redacts them) — mirror that here.
	shared := testSecrets(nil)
	reg, err := connector.Build(cfg, connector.Deps{Secrets: shared, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Secrets = shared

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: r
    uses: hv.read
    options: { key: apikey }
  - id: use
    uses: svc.post
    options: { text: "got {{.r.value}}" }
`)
	run := emptyRun()
	run.ID = "flow:ping:o/r#7"
	runTriggerWithRun(rig, run, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	// The step itself saw the real value (templating unbroken live).
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].Opts["text"] != "got "+secretVal {
		t.Fatalf("live templating: %+v", calls)
	}
	// NOTHING persisted carries the secret; the vault step persisted a
	// re-resolve marker.
	rig.Store.mu.Lock()
	putLog := append([]store.WorkflowRun{}, rig.Store.putLog...)
	rig.Store.mu.Unlock()
	if len(putLog) == 0 {
		t.Fatal("expected checkpoints")
	}
	for _, rec := range putLog {
		b, _ := json.Marshal(rec.Outputs)
		if strings.Contains(string(b), secretVal) {
			t.Fatalf("vault value persisted cleartext: %s", b)
		}
	}
	last := putLog[len(putLog)-1]
	if last.Outputs["r"] == nil || last.Outputs["r"][reresolveMarker] != true {
		t.Fatalf("vault step must checkpoint a re-resolve marker: %+v", last.Outputs)
	}

	// RESUME: restore from the marker — the vault is re-read and the next
	// step's template sees the real value again.
	fake2 := newFakeState(t, "svc")
	rig2 := newTestRunner(t, cfg, reg)
	rig2.Runner.Secrets = shared
	resume := emptyRun()
	resume.ID = "flow:ping:o/r#7"
	resume.StepIndex = 1
	resume.Outputs = map[string]map[string]any{"r": {reresolveMarker: true}}
	runTriggerWithRun(rig2, resume, newTrigger("ping", nil), spec)
	if failed, errStr := rig2.workflowFailed(); failed {
		t.Fatalf("resume failed: %s", errStr)
	}
	calls = fake2.snapshot()
	if len(calls) != 1 || calls[0].Opts["text"] != "got "+secretVal {
		t.Fatalf("resume must re-resolve the vault value: %+v", calls)
	}

	// Non-vault outputs that happen to CONTAIN a tracked secret persist
	// redacted (never cleartext), by design at the cost of resume templating
	// for those exact values.
	fake3 := newFakeState(t, "svc")
	fake3.mu.Lock()
	fake3.outputs = map[string]map[string]any{"post": {"id": 1, "echo": secretVal}}
	fake3.mu.Unlock()
	rig3 := newTestRunner(t, cfg, reg)
	rig3.Runner.Secrets = shared
	run3 := emptyRun()
	run3.ID = "flow:ping:o/r#8"
	runTriggerWithRun(rig3, run3, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps: [ { id: echoer, uses: svc.post, options: { text: t } } ]
`))
	rig3.Store.mu.Lock()
	putLog3 := append([]store.WorkflowRun{}, rig3.Store.putLog...)
	rig3.Store.mu.Unlock()
	for _, rec := range putLog3 {
		b, _ := json.Marshal(rec.Outputs)
		if strings.Contains(string(b), secretVal) {
			t.Fatalf("tracked secret persisted cleartext in non-vault output: %s", b)
		}
	}
}

// REGRESSION (audit finding #7): plan nesting does NOT reset limits. A
// sub-agent whose output is itself a plan re-enters the runner with the
// PARENT's budget — cumulative sub-agents/steps across the tree — and
// nesting is depth-capped.
func TestNestedPlansShareBudget(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
memory: { type: memory }
agents:
  planner:  { model: x }
  helper:   { model: y }
  recurser: { model: z }
policy:
  agent_authored:
    allow: [ svc.post, agent ]
    limits: { max_sub_agents: 2, max_steps: 50 }
`)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")

	// Parent plan: one sub-agent (helper). Helper's output is a NESTED plan
	// with two more sub-agent steps — 3 cumulative > max_sub_agents 2.
	// Before the fix the child re-entered with a fresh budget and all ran.
	parent := "```plan\n- id: sub\n  type: agent\n  agent: helper\n  prompt: go\n- id: after\n  uses: svc.post\n  options: { text: parent-after }\n```"
	child := "```plan\n- {id: c1, type: agent, agent: recurser, prompt: a}\n- {id: c2, type: agent, agent: recurser, prompt: b}\n```"
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		switch req.Action.Agent {
		case "planner":
			return dispatch.RunRef{AgentID: "p", Output: parent}, nil
		case "helper":
			return dispatch.RunRef{AgentID: "h", Output: child}, nil
		}
		return dispatch.RunRef{AgentID: "r", Output: "leaf"}, nil
	}
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, planSpec))
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "cumulative across nested plans") || !strings.Contains(errStr, "max_sub_agents") {
		t.Fatalf("nested sub-agents must share the budget: %v %q", failed, errStr)
	}
	for _, c := range fake.snapshot() {
		if c.Opts["text"] == "parent-after" {
			t.Fatal("the halted tree must not keep executing")
		}
	}

	// Depth cap: an agent that always answers with a plan containing itself
	// halts at MaxPlanDepth instead of spinning (budget wide enough that
	// depth trips first).
	cfgDeep := loadConfig(t, `
connectors:
  svc: { type: fake }
memory: { type: memory }
agents:
  planner:  { model: x }
  recurser: { model: z }
policy:
  agent_authored:
    allow: [ svc.post, agent ]
    limits: { max_sub_agents: 50, max_steps: 100 }
`)
	regDeep := buildRegistry(t, cfgDeep)
	newFakeState(t, "svc")
	recurse := "```plan\n- {id: again, type: agent, agent: recurser, prompt: deeper}\n```"
	rig2 := newTestRunner(t, cfgDeep, regDeep)
	depthSeen := 0
	rig2.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		depthSeen++
		return dispatch.RunRef{AgentID: "r", Output: recurse}, nil
	}
	runTrigger(rig2, newTrigger("ping", nil), mustSpec(t, planSpec))
	failed, errStr = rig2.workflowFailed()
	if !failed || !strings.Contains(errStr, "nesting depth") {
		t.Fatalf("recursion must halt on the depth cap: %v %q", failed, errStr)
	}
	if depthSeen > MaxPlanDepth+1 {
		t.Fatalf("recursion dispatched %d agents — not bounded by depth", depthSeen)
	}

	// Cumulative step budget across the tree.
	cfgSteps := loadConfig(t, `
connectors:
  svc: { type: fake }
memory: { type: memory }
agents:
  planner: { model: x }
  helper:  { model: y }
policy:
  agent_authored:
    allow: [ svc.post, agent ]
    limits: { max_steps: 4, max_sub_agents: 5 }
`)
	regSteps := buildRegistry(t, cfgSteps)
	newFakeState(t, "svc")
	parent3 := "```plan\n- {id: a, uses: svc.post, options: {text: a}}\n- {id: sub, type: agent, agent: helper, prompt: go}\n```"
	child3 := "```plan\n- {id: b, uses: svc.post, options: {text: b}}\n- {id: c, uses: svc.post, options: {text: c}}\n- {id: d, uses: svc.post, options: {text: d}}\n```"
	rig3 := newTestRunner(t, cfgSteps, regSteps)
	rig3.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		if req.Action.Agent == "planner" {
			return dispatch.RunRef{AgentID: "p", Output: parent3}, nil
		}
		return dispatch.RunRef{AgentID: "h", Output: child3}, nil
	}
	runTrigger(rig3, newTrigger("ping", nil), mustSpec(t, planSpec))
	failed, errStr = rig3.workflowFailed()
	// Units: parent a(1) + sub(2) + child b(3) + c(4) + d(5) > 4.
	if !failed || !strings.Contains(errStr, "max_steps") || !strings.Contains(errStr, "cumulative") {
		t.Fatalf("cumulative step budget: %v %q", failed, errStr)
	}
}

// #57 M9: a team step is a whole fleet (planner + reconciler + workers), but
// the runtime cumulative budget only counted plain agent steps — so a nested
// plan could spin up teams whose fleets never charged against max_sub_agents.
// Here each plan passes its OWN static guard, yet the tree's cumulative
// sub-agent count (parent agent + child team fleet) exceeds the cap and must
// halt at runtime, before the child team dispatches.
func TestNestedPlanTeamCountsAgainstBudget(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
memory: { type: memory }
agents:
  planner:     { model: x }
  helper:      { model: y }
  architect:   { model: a }
  implementer: { model: b }
policy:
  agent_authored:
    allow: [ svc.post, agent, team ]
    limits: { max_sub_agents: 3, max_steps: 50 }
`)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")

	// Parent plan: one plain sub-agent (helper) → 1 unit (guard: 1 ≤ 3, ok).
	parent := "```plan\n- {id: sub, type: agent, agent: helper, prompt: go}\n```"
	// Helper's output is a NESTED plan whose single team step's fleet is
	// 2 + max_workers(1) = 3 — passing the child's OWN guard (3 ≤ 3), but the
	// tree cumulative is 1 + 3 = 4 > 3.
	child := "```plan\n- {id: tm, prompt: go, team: {planner: architect, worker: implementer, max_workers: 1}}\n```"
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		switch req.Action.Agent {
		case "planner":
			return dispatch.RunRef{AgentID: "p", Output: parent}, nil
		case "helper":
			return dispatch.RunRef{AgentID: "h", Output: child}, nil
		}
		// architect/implementer must never be reached — the budget halts the
		// child team before execTeam dispatches its planner.
		t.Errorf("team fleet dispatched despite over-budget: %s", req.Action.Agent)
		return dispatch.RunRef{AgentID: "x", Output: "leaf"}, nil
	}
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, planSpec))
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "cumulative across nested plans") || !strings.Contains(errStr, "max_sub_agents") {
		t.Fatalf("nested team fleet must share the cumulative budget: %v %q", failed, errStr)
	}
	if len(fake.snapshot()) != 0 {
		t.Fatal("the halted tree must not reach any external step")
	}
}

// A plan's sub-agent dispatches are marked AgentAuthored (the launch layer
// gives them deny-by-default network — #36 §15); the config-authored step
// that produced the plan is not.
func TestPlanSubAgentDispatchMarkedAgentAuthored(t *testing.T) {
	cfg := planCfg(t, `
policy:
  agent_authored:
    allow: [ svc.post, agent ]
`)
	out := "```plan\n- id: sub\n  type: agent\n  agent: helper\n  prompt: \"go\"\n```"
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	flags := map[string]bool{}
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		flags[req.Action.Agent] = req.AgentAuthored
		if req.Action.Agent == "planner" {
			return dispatch.RunRef{AgentID: "a1", Output: out}, nil
		}
		return dispatch.RunRef{AgentID: "sub", Output: `{"done":true}`}, nil
	}
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "m"}), mustSpec(t, planSpec))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if flags["planner"] {
		t.Fatal("config-authored dispatch must not be marked agent-authored")
	}
	if !flags["helper"] {
		t.Fatal("plan sub-agent dispatch must be marked agent-authored")
	}
}
