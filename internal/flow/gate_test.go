package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// gateCfg: an agent profile plus checks — a verb check (verdict via the fake
// connector's canned outputs) and a js critic-scope check.
const gateCfg = `
connectors:
  svc: { type: fake }
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

func TestGateNeedsWorkdirForCommandChecks(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
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
