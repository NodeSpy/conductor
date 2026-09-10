package flow

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/cost"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// gateCfg: an agent profile plus checks — a verb check (verdict via the fake
// connector's canned outputs) and a js critic-scope check.
const gateCfg = `
connectors:
  svc: { use: fake }
agents:
  fixer:  { model: m }
  critic: { model: m }
checks:
  verdict: { uses: svc.post, options: { text: "check {{.gate.attempt}}" } }
  scope:
    run: js
    code: |
      return { pass: ctx.gate.workdir.length > 0, detail: "wd=" + ctx.gate.workdir };
  critic: { type: agent, agent: critic, prompt: "review the change in {{.gate.workdir}}" }
`

var gateSpecYAML = `
on: svc.ping
steps:
  - id: fix
    type: agent
    agent: fixer
    prompt: "fix it"
    gate: { run: [ verdict ], max_revisions: %d }
`

// gateRig wires the harness: the fixer dispatch returns a worktree, the
// verdict check's pass/fail comes from the fake connector's canned outputs,
// and FollowUp is a recording fake.
func gateRig(t *testing.T, cfgYAML string) (*testRig, *fakeState, *[]string) {
	t.Helper()
	cfg := loadConfig(t, cfgYAML)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a-" + req.Action.Agent, Output: "done", Workdir: t.TempDir()}, nil
	}
	var followUps []string
	rig.Runner.Agents.FollowUp = func(_ context.Context, agentID, agentName string, _ core.Trigger, prompt string) (string, bool, error) {
		followUps = append(followUps, agentName+"|"+prompt)
		return "revised", true, nil
	}
	return rig, fake, &followUps
}

func specf(t *testing.T, format string, maxRev int) config.TriggerSpec {
	t.Helper()
	return mustSpec(t, strings.ReplaceAll(format, "%d", itoaTest(maxRev)))
}

func itoaTest(n int) string { return string(rune('0' + n)) }

func TestGatePassesFirstRound(t *testing.T) {
	rig, fake, followUps := gateRig(t, gateCfg)
	fake.outputs["post"] = map[string]any{"id": 1, "pass": true}

	runTrigger(rig, newTrigger("ping", nil), specf(t, gateSpecYAML, 2))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if len(*followUps) != 0 {
		t.Fatalf("no revise expected: %v", *followUps)
	}
	gates := rig.Store.auditsWithEvent("gate")
	if len(gates) != 1 || gates[0]["outcome"] != "pass" || gates[0]["step"] != "fix" {
		t.Fatalf("gate audit: %+v", gates)
	}
}

func TestGateRevisesThenPasses(t *testing.T) {
	rig, fake, followUps := gateRig(t, gateCfg)
	fake.outputs["post"] = map[string]any{"id": 1, "pass": false, "reason": "tests are red"}
	rig.Runner.Agents.FollowUp = func(_ context.Context, agentID, agentName string, _ core.Trigger, prompt string) (string, bool, error) {
		*followUps = append(*followUps, agentName+"|"+prompt)
		fake.mu.Lock()
		fake.outputs["post"] = map[string]any{"id": 2, "pass": true} // the revision fixed it
		fake.mu.Unlock()
		return "revised", true, nil
	}

	runTrigger(rig, newTrigger("ping", nil), specf(t, gateSpecYAML, 2))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	if len(*followUps) != 1 {
		t.Fatalf("one revise round expected: %v", *followUps)
	}
	fu := (*followUps)[0]
	if !strings.HasPrefix(fu, "fixer|") || !strings.Contains(fu, "tests are red") ||
		!strings.Contains(fu, "verdict") {
		t.Fatalf("revise prompt must carry the failing check's detail: %q", fu)
	}
	// Audit trail: fail → revise → pass.
	var outcomes []string
	for _, g := range rig.Store.auditsWithEvent("gate") {
		outcomes = append(outcomes, g["outcome"].(string))
	}
	if strings.Join(outcomes, ",") != "fail,revise,pass" {
		t.Fatalf("gate outcomes: %v", outcomes)
	}
}

// TestGateReviseTurnIsMetered is the F2 regression (#36 §146): a gate-revise
// follow-up is a real agent turn and must be metered like the initial dispatch
// — its estimated spend reserved before, its real usage (the captured revise
// transcript) recorded after under a :revise step id.
func TestGateReviseTurnIsMetered(t *testing.T) {
	rig, fake, _ := gateRig(t, gateCfg)
	fake.outputs["post"] = map[string]any{"id": 1, "pass": false, "reason": "red"}
	rig.Runner.Agents.FollowUp = func(_ context.Context, agentID, agentName string, _ core.Trigger, prompt string) (string, bool, error) {
		fake.mu.Lock()
		fake.outputs["post"] = map[string]any{"id": 2, "pass": true} // the revision fixed it
		fake.mu.Unlock()
		// The captured revise turn is a real transcript carrying usage.
		return `{"usage":{"input_tokens":80,"output_tokens":40},"total_cost_usd":0.3}`, true, nil
	}
	var checks []string
	var recorded []string
	rig.Runner.Agents.CheckBudget = func(agentName string, wf *config.BudgetPolicy, wfScope string, est cost.Usage) (*cost.Reservation, error) {
		checks = append(checks, agentName)
		return nil, nil
	}
	rig.Runner.Agents.RecordUsage = func(_ core.Trigger, agentName, stepID, _, _, _ string, res *cost.Reservation, u cost.Usage) {
		recorded = append(recorded, stepID+"|"+strconv.Itoa(u.TotalTokens))
	}

	runTrigger(rig, newTrigger("ping", nil), specf(t, gateSpecYAML, 2))
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	// Two budget checks: the initial dispatch AND the revise turn.
	if len(checks) != 2 {
		t.Fatalf("want dispatch + revise budget checks, got %v", checks)
	}
	// The revise turn's real usage (80+40) was recorded under fix:revise.
	sawRevise := false
	for _, r := range recorded {
		if r == "fix:revise|120" {
			sawRevise = true
		}
	}
	if !sawRevise {
		t.Fatalf("revise turn usage must be recorded under fix:revise: %v", recorded)
	}
}

// TestGateReviseOverBudgetEscalates: when the revise turn is over the spend
// cap it sheds — the follow-up never reaches the agent and the gate escalates
// instead of promoting an unmetered change (#36 §146 F2).
func TestGateReviseOverBudgetEscalates(t *testing.T) {
	rig, fake, _ := gateRig(t, gateCfg)
	fake.outputs["post"] = map[string]any{"id": 1, "pass": false, "reason": "red"}
	followUpCalled := false
	rig.Runner.Agents.FollowUp = func(_ context.Context, agentID, agentName string, _ core.Trigger, prompt string) (string, bool, error) {
		followUpCalled = true
		return "revised", true, nil
	}
	n := 0
	rig.Runner.Agents.CheckBudget = func(agentName string, wf *config.BudgetPolicy, wfScope string, est cost.Usage) (*cost.Reservation, error) {
		n++
		if n >= 2 { // the initial dispatch clears; the revise sheds
			return nil, fmt.Errorf("spend budget: profile:fixer over cap — shedding")
		}
		return nil, nil
	}

	runTrigger(rig, newTrigger("ping", nil), specf(t, gateSpecYAML, 2))
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "gate failed") {
		t.Fatalf("over-budget revise must escalate the gate: failed=%v err=%q", failed, errStr)
	}
	if followUpCalled {
		t.Fatal("a shed revise must never reach the agent")
	}
	gates := rig.Store.auditsWithEvent("gate")
	if len(gates) == 0 || gates[len(gates)-1]["outcome"] != "escalated" {
		t.Fatalf("gate must escalate on a shed revise: %+v", gates)
	}
}

func TestGateEscalatesAfterMaxRevisions(t *testing.T) {
	rig, fake, followUps := gateRig(t, gateCfg)
	fake.outputs["post"] = map[string]any{"pass": false, "reason": "still broken"}

	runTrigger(rig, newTrigger("ping", nil), specf(t, gateSpecYAML, 1))
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "gate failed: verdict") {
		t.Fatalf("gate must fail the step: %v %q", failed, errStr)
	}
	if len(*followUps) != 1 {
		t.Fatalf("max_revisions=1 → exactly one revise: %v", *followUps)
	}
	sawNeedsInput := false
	for _, ev := range rig.Notifier.snapshot() {
		if ev.Event == "needs_input" {
			sawNeedsInput = true
		}
	}
	if !sawNeedsInput {
		t.Fatal("escalation must notify needs_input")
	}
	gates := rig.Store.auditsWithEvent("gate")
	last := gates[len(gates)-1]
	if last["outcome"] != "escalated" {
		t.Fatalf("final gate audit: %+v", last)
	}
}

func TestGateEscalatesWhenNoFollowUp(t *testing.T) {
	rig, fake, _ := gateRig(t, gateCfg)
	fake.outputs["post"] = map[string]any{"pass": false}
	rig.Runner.Agents.FollowUp = nil // runtime can't revise

	runTrigger(rig, newTrigger("ping", nil), specf(t, gateSpecYAML, 3))
	if failed, _ := rig.workflowFailed(); !failed {
		t.Fatal("no follow-up transport → escalate on first fail")
	}
	if fake.count("post") != 1 {
		t.Fatalf("checks must not re-run without a revision: %d", fake.count("post"))
	}
}

func TestGateScopeReachesCodeCheck(t *testing.T) {
	rig, _, _ := gateRig(t, gateCfg)
	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: fix
    type: agent
    agent: fixer
    prompt: "fix it"
    gate: { run: [ scope ] }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	// The js check read ctx.gate.workdir (non-empty) → pass.
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("scope check failed: %s", errStr)
	}
}

func TestGateCriticWithoutVerdictFails(t *testing.T) {
	rig, _, _ := gateRig(t, gateCfg)
	rig.Runner.Agents.FollowUp = nil
	// The critic dispatch returns prose with no pass output.
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		if req.Action.Agent == "critic" {
			return dispatch.RunRef{AgentID: "c1", Output: "looks fine to me"}, nil
		}
		return dispatch.RunRef{AgentID: "a1", Output: "done", Workdir: t.TempDir()}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, agent: fixer, prompt: "fix", gate: { run: [ critic ], max_revisions: 0 } }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "gate failed: critic") {
		t.Fatalf("verdict-less critic must fail the gate: %v %q", failed, errStr)
	}
}

func TestGateCriticVerdictPasses(t *testing.T) {
	rig, _, _ := gateRig(t, gateCfg)
	var criticReq dispatch.Request
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		if req.Action.Agent == "critic" {
			criticReq = req
			return dispatch.RunRef{AgentID: "c1", Output: `{"pass": true, "reason": "clean"}`}, nil
		}
		return dispatch.RunRef{AgentID: "a1", Output: "done", Workdir: "/wt/agent"}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, agent: fixer, prompt: "fix", gate: { run: [ critic ] } }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("critic pass: %s", errStr)
	}
	// The critic ran against the agent's worktree, without provisioning a
	// second checkout, and its template scope carries the gate block (the
	// prompt itself renders at dispatch, against req.Data).
	if criticReq.Action.WorkDir != "/wt/agent" || criticReq.Action.Checkout != "none" {
		t.Fatalf("critic placement: %+v", criticReq.Action)
	}
	gateScope, _ := criticReq.Data["gate"].(map[string]any)
	if gateScope["workdir"] != "/wt/agent" || gateScope["check"] != "critic" {
		t.Fatalf("critic gate scope: %+v", gateScope)
	}
}

// Regression (#36 iso-review round 2, item 5): the agent's raw reply is fed to
// an agent-form critic via {{.gate.output}}. A tracked secret in that reply
// must be scrubbed before it enters the critic's template scope — the same
// standard the proposed diff already meets — so a secret in one agent's output
// never reaches another agent's model.
func TestGateCriticOutputIsRedacted(t *testing.T) {
	rig, _, _ := gateRig(t, gateCfg)
	const secret = "ghp_gate_secret_TOKEN_1234567890"
	rig.Runner.Secrets.Track(secret)
	var criticReq dispatch.Request
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		if req.Action.Agent == "critic" {
			criticReq = req
			return dispatch.RunRef{AgentID: "c1", Output: `{"pass": true, "reason": "clean"}`}, nil
		}
		// The fixer leaks a tracked secret in its raw reply.
		return dispatch.RunRef{AgentID: "a1", Output: "patched; used token " + secret, Workdir: "/wt/agent"}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, agent: fixer, prompt: "fix", gate: { run: [ critic ] } }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("critic pass: %s", errStr)
	}
	gateScope, _ := criticReq.Data["gate"].(map[string]any)
	out, _ := gateScope["output"].(string)
	if strings.Contains(out, secret) {
		t.Fatalf("critic gate.output must not carry the raw secret: %q", out)
	}
	if !strings.Contains(out, "«redacted»") {
		t.Fatalf("critic gate.output should show the secret scrubbed to a placeholder: %q", out)
	}
}

func TestGateNeedsWorkdirForCommandChecks(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
agents:
  fixer: { model: m }
checks:
  build: { type: command, command: ["make", "build"] }
`)
	reg := buildRegistry(t, cfg)
	newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Agents.FollowUp = nil
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil // NO workdir (remote/none)
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, agent: fixer, prompt: "fix", gate: { run: [ build ], max_revisions: 0 } }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "gate failed: build") {
		t.Fatalf("workdir-less command check must fail loudly: %v %q", failed, errStr)
	}
}

func TestTriggerLevelGateAppliesAndStepGateWins(t *testing.T) {
	rig, fake, _ := gateRig(t, gateCfg)
	fake.outputs["post"] = map[string]any{"pass": true}
	spec := mustSpec(t, `
on: svc.ping
gate: { run: [ verdict ] }
steps:
  - { id: a, type: agent, agent: fixer, prompt: "one" }
  - { id: b, type: agent, agent: fixer, prompt: "two", gate: { run: [ scope ] } }
  - { id: c, uses: svc.post, options: { text: "not gated" } }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	// Step a: trigger default gate (verdict → one svc.post call from the
	// check). Step b: its own gate (scope, js — no svc call). Step c: the
	// plain verb step (one svc.post call, not gated).
	gates := rig.Store.auditsWithEvent("gate")
	if len(gates) != 2 || gates[0]["step"] != "a" || gates[1]["step"] != "b" {
		t.Fatalf("gate audits: %+v", gates)
	}
	if n := fake.count("post"); n != 2 {
		t.Fatalf("post calls (a's check + c): %d", n)
	}
}

// CRITICAL regression (#36 review): an agent-authored plan may not set (or
// empty out) its own gate — a step's gate wins over the inherited default,
// so an emitted `gate: {run: []}` would be self-approval. The operator's
// trigger/workflow default must remain the only gate path for agent output.
func TestAgentAuthoredStepsCannotCarryTheirOwnGate(t *testing.T) {
	cfg := loadConfig(t, gateCfg+`
policy:
  agent_authored:
    allow: [ agent, team, svc.post ]
`)
	reg := buildRegistry(t, cfg)
	pol := cfg.Policy.AgentAuthored

	// A plan step with its own (empty!) gate is rejected structurally.
	steps := []config.Step{{ID: "s", Type: "agent", Agent: "fixer", Prompt: "p",
		Gate: &config.GateSpec{Run: []string{}}}}
	if _, err := guardPlan(cfg, reg, pol, steps); err == nil ||
		!strings.Contains(err.Error(), "may not set gate:") {
		t.Fatalf("self-gated plan step: %v", err)
	}
	// Same for a team step's per-worker gate.
	steps = []config.Step{{ID: "tm", Prompt: "p", Team: &config.TeamSpec{
		Planner: "fixer", Worker: "critic", Gate: &config.GateSpec{Run: []string{}}}}}
	if _, err := guardPlan(cfg, reg, pol, steps); err == nil ||
		!strings.Contains(err.Error(), "may not set team.gate:") {
		t.Fatalf("self-gated team plan step: %v", err)
	}
	// Without a gate the same steps are admitted (the inherited default is
	// what governs them at run time).
	steps = []config.Step{{ID: "s", Type: "agent", Agent: "fixer", Prompt: "p"}}
	if _, err := guardPlan(cfg, reg, pol, steps); err != nil {
		t.Fatalf("ungated plan step must be admitted: %v", err)
	}
}

// CRITICAL regression (H3): background: and handoff: escape the gate on agent
// output the same way a weakened gate: would — a background step runs a live
// agent OUTSIDE the synchronous run (nothing returns for the gate to hold), and
// handoff: diverts the review draft to an agent-nominated channel. An emitted
// plan may set neither; both guardPlan and ValidatePlanSteps refuse them.
func TestAgentAuthoredStepsCannotBackgroundOrHandoff(t *testing.T) {
	cfg := loadConfig(t, gateCfg+`
policy:
  agent_authored:
    allow: [ agent, team, svc.post ]
`)
	reg := buildRegistry(t, cfg)
	pol := cfg.Policy.AgentAuthored

	bg := []config.Step{{ID: "s", Type: "agent", Agent: "fixer", Prompt: "p", Background: true}}
	ho := []config.Step{{ID: "s", Type: "agent", Agent: "fixer", Prompt: "p", Handoff: "phone"}}

	// guardPlan (the security guard) rejects both outright.
	if _, err := guardPlan(cfg, reg, pol, bg); err == nil || !strings.Contains(err.Error(), "may not set background:") {
		t.Fatalf("backgrounded plan step (guardPlan): %v", err)
	}
	if _, err := guardPlan(cfg, reg, pol, ho); err == nil || !strings.Contains(err.Error(), "may not set handoff:") {
		t.Fatalf("handed-off plan step (guardPlan): %v", err)
	}
	// ValidatePlanSteps refuses them independently (no plan path admits one).
	if err := ValidatePlanSteps(cfg, reg, bg); err == nil || !strings.Contains(err.Error(), "may not set background:") {
		t.Fatalf("backgrounded plan step (ValidatePlanSteps): %v", err)
	}
	if err := ValidatePlanSteps(cfg, reg, ho); err == nil || !strings.Contains(err.Error(), "may not set handoff:") {
		t.Fatalf("handed-off plan step (ValidatePlanSteps): %v", err)
	}
}

// …and the inherited trigger-level default gate really does govern a plan's
// agent-authored sub-step: the sub-agent's output fails the default gate and
// the run fails — the agent could not promote unchecked work.
func TestInheritedDefaultGateGovernsPlanSubAgents(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
memory: { type: memory }
agents:
  fixer:   { model: m }
  planner: { model: m }
checks:
  verdict: { uses: svc.post, options: { text: "check" } }
policy:
  agent_authored:
    allow: [ agent, svc.post ]
`)
	reg := buildRegistry(t, cfg)
	fake := newFakeState(t, "svc")
	rig := newTestRunner(t, cfg, reg)
	rig.Runner.Agents.FollowUp = nil // gate fail → escalate immediately

	// The verdict check passes for the PLANNER's gate round, then flips to
	// failing right after the SUB-agent's dispatch — so the failing round is
	// provably the agent-authored sub-step's.
	fake.mu.Lock()
	fake.outputs["post"] = map[string]any{"pass": true}
	fake.mu.Unlock()
	planOut := "```plan\n- id: sub\n  type: agent\n  agent: fixer\n  prompt: \"go\"\n```"
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		if req.Action.Agent == "planner" {
			return dispatch.RunRef{AgentID: "p1", Output: planOut}, nil
		}
		// The sub-agent ran — its gate round must FAIL.
		fake.mu.Lock()
		fake.outputs["post"] = map[string]any{"pass": false, "reason": "unchecked"}
		fake.mu.Unlock()
		return dispatch.RunRef{AgentID: "sub", Output: "did work", Workdir: t.TempDir()}, nil
	}

	spec := mustSpec(t, `
on: svc.ping
gate: { run: [ verdict ], max_revisions: 0 }
steps:
  - { id: author, type: agent, agent: planner, prompt: "plan it" }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "m"}), spec)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "gate failed: verdict") {
		t.Fatalf("default gate must govern plan sub-agents: %v %q", failed, errStr)
	}
	// The failing gate round was the SUB-agent's, not the planner's.
	gates := rig.Store.auditsWithEvent("gate")
	last := gates[len(gates)-1]
	if last["step"] != "plan:sub" || last["outcome"] != "escalated" {
		t.Fatalf("failing gate must be the plan sub-step's: %+v", last)
	}
}

// Regression (#36 review H6): an agent-form critic with NO worktree to
// review must fail the check loudly — a critic that reviewed nothing can
// still emit pass: true, which would be a silent pass.
func TestGateCriticWithoutWorkdirFailsLoudly(t *testing.T) {
	rig, _, _ := gateRig(t, gateCfg)
	rig.Runner.Agents.FollowUp = nil
	criticRan := false
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		if req.Action.Agent == "critic" {
			criticRan = true
			return dispatch.RunRef{AgentID: "c1", Output: `{"pass": true}`}, nil
		}
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil // NO Workdir
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, agent: fixer, prompt: "fix", gate: { run: [ critic ], max_revisions: 0 } }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	failed, errStr := rig.workflowFailed()
	if !failed || !strings.Contains(errStr, "gate failed: critic") {
		t.Fatalf("workdir-less critic must fail the gate: %v %q", failed, errStr)
	}
	if criticRan {
		t.Fatal("the critic must not even dispatch with nothing to review")
	}
}
