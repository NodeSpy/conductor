package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

const isolationCfg = `
connectors:
  svc: { use: fake }
runtimes:
  pz: { use: paseo, default: true }
  pzout: { use: paseo, isolation: { mode: none } }
  acpout: { use: acp, agent: claude-code, transport: acp, isolation: { mode: none } }
workflows:
  wfout:
    isolation: { mode: none }
    steps: [ { id: w1, type: agent, name: w1, prompt: p } ]
  wfplain:
    steps: [ { id: w2, type: agent, name: w2, prompt: p } ]
`

// isoRig runs a trigger and records each agent dispatch's isolation: by
// step identity.
func isoRig(t *testing.T) (*testRig, func() map[string]*config.IsolationConfig) {
	t.Helper()
	cfg := loadConfig(t, isolationCfg)
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	newFakeState(t, "svc")
	var mu sync.Mutex
	got := map[string]*config.IsolationConfig{}
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		mu.Lock()
		got[req.Identity] = req.Step.Isolation
		mu.Unlock()
		return dispatch.RunRef{AgentID: "a-" + req.Identity, Output: "done"}, nil
	}
	return rig, func() map[string]*config.IsolationConfig {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

func optsOut(m map[string]*config.IsolationConfig, id string) bool {
	return m[id].OptsOut()
}

// A trigger's isolation: is its agent steps' default — the workflows it
// calls included — and a step's own wins.
func TestIsolationTriggerDefault(t *testing.T) {
	rig, got := isoRig(t)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
isolation: { mode: none }
steps:
  - { id: a, type: agent, name: a, prompt: p }
  - { id: b, type: agent, name: b, prompt: p, isolation: { mode: namespace } }
  - { id: c, workflow: wfplain }
`))
	if failed, e := rig.workflowFailed(); failed {
		t.Fatal(e)
	}
	g := got()
	if !optsOut(g, "a") || !optsOut(g, "w2") {
		t.Fatalf("the trigger's opt-out reaches its steps and the workflow it calls: %+v", g)
	}
	if g["b"] == nil || g["b"].Mode != "namespace" {
		t.Fatalf("a step's own isolation wins: %+v", g["b"])
	}
}

// With no trigger default: a workflow's own applies to its steps; a paseo
// runtime's applies to the steps on it; nothing else adds one.
func TestIsolationWorkflowAndPaseoRuntime(t *testing.T) {
	rig, got := isoRig(t)
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
steps:
  - { id: d, type: agent, name: d, prompt: p }
  - { id: e, type: agent, name: e, prompt: p, runtime: pzout }
  - { id: f, workflow: wfout }
  - { id: g, workflow: wfplain }
`))
	if failed, e := rig.workflowFailed(); failed {
		t.Fatal(e)
	}
	g := got()
	if g["d"] != nil || g["w2"] != nil {
		t.Fatalf("no isolation anywhere: none on the dispatch: %+v %+v", g["d"], g["w2"])
	}
	if !optsOut(g, "e") {
		t.Fatalf("a paseo runtime's opt-out: %+v", g["e"])
	}
	if !optsOut(g, "w1") {
		t.Fatalf("a workflow's own opt-out: %+v", g["w1"])
	}
}

// A runtime conductor launches itself keeps its isolation: in the
// controller (under the step's); flow does not copy it onto the step.
func TestEffectiveIsolationLeavesOtherRuntimesToTheirController(t *testing.T) {
	cfg := loadConfig(t, isolationCfg)
	r := &Runner{Cfg: cfg}
	ctx := context.Background()
	if iso := r.effectiveIsolation(ctx, config.Step{Runtime: "acpout"}); iso != nil {
		t.Fatalf("acp runtime: %+v", iso)
	}
	if iso := r.effectiveIsolation(ctx, config.Step{Runtime: "pzout"}); !iso.OptsOut() {
		t.Fatalf("paseo runtime: %+v", iso)
	}
	if iso := r.effectiveIsolation(ctx, config.Step{}); iso != nil {
		t.Fatalf("default paseo runtime with none: %+v", iso)
	}
	own := &config.IsolationConfig{Mode: "namespace"}
	ctx = withDefaultIsolation(ctx, &config.IsolationConfig{Mode: "none"})
	if iso := r.effectiveIsolation(ctx, config.Step{Runtime: "pzout", Isolation: own}); iso != own {
		t.Fatal("the step's own wins over everything")
	}
	if iso := r.effectiveIsolation(ctx, config.Step{Runtime: "acpout"}); !iso.OptsOut() {
		t.Fatal("the scope default beats a runtime's")
	}
	if withDefaultIsolation(ctx, nil) != ctx {
		t.Fatal("no isolation: the caller's default stands")
	}
	if iso := (&Runner{}).effectiveIsolation(context.Background(), config.Step{}); iso != nil {
		t.Fatal("no config")
	}
}

// An agent-authored dispatch never carries an opt-out: the scope's default
// is applied, then stripped with the rest of its inheritable grants.
func TestIsolationStrippedFromAgentAuthored(t *testing.T) {
	cfg := loadConfig(t, isolationCfg)
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	var got []dispatch.Request
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = append(got, req)
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	ctx := withDefaultIsolation(context.Background(), &config.IsolationConfig{Mode: "none"})
	for _, authored := range []bool{false, true} {
		c := ctx
		if authored {
			c = context.WithValue(ctx, agentAuthoredKey{}, true)
		}
		if _, _, err := rig.Runner.execAgent(c, newTrigger("ping", nil), config.Step{Type: "agent", Prompt: "x"}, "s", "s", map[string]any{}, false); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || !got[0].Step.Isolation.OptsOut() || got[1].Step.Isolation != nil || !got[1].AgentAuthored {
		t.Fatalf("operator-authored keeps the opt-out, agent-authored loses it: %+v / %+v", got[0].Step.Isolation, got[1].Step.Isolation)
	}
}
