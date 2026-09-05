package flow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
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
