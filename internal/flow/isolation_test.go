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

// A detached launch carries the isolation: its scope gives it (the
// operator's opt-out reaches the paseo dispatcher), a step's own winning.
func TestIsolationReachesADetachedLaunch(t *testing.T) {
	cfg := loadConfig(t, isolationCfg)
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	var got []dispatch.Request
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = append(got, req)
		return dispatch.RunRef{AgentID: "a1", Detached: true}, nil
	}
	step := config.Step{Type: "agent", Detach: true, Repo: "a/b", Prompt: "x"}
	if _, _, err := rig.Runner.execAgent(context.Background(), newTrigger("ping", nil), step, "s", "s", map[string]any{}, false); err != nil {
		t.Fatal(err)
	}
	ctx := withDefaultIsolation(context.Background(), &config.IsolationConfig{Mode: "none"})
	if _, _, err := rig.Runner.execAgent(ctx, newTrigger("ping", nil), step, "s", "s", map[string]any{}, false); err != nil {
		t.Fatal(err)
	}
	own := step
	own.Isolation = &config.IsolationConfig{Mode: "namespace"}
	if _, _, err := rig.Runner.execAgent(ctx, newTrigger("ping", nil), own, "s", "s", map[string]any{}, false); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got[0].Step.Detach {
		t.Fatalf("three detached launches: %+v", got)
	}
	if got[0].Step.Isolation != nil || !got[1].Step.Isolation.OptsOut() || got[2].Step.Isolation.Mode != "namespace" {
		t.Fatalf("isolation: %+v / %+v / %+v", got[0].Step.Isolation, got[1].Step.Isolation, got[2].Step.Isolation)
	}
}

// A block of only the sandbox's knobs layers onto the scope outside it —
// a paseo runtime's opt-out, a trigger's or workflow's allowance — and the
// most specific setting of each knob wins.
func TestSandboxKnobsLayerOntoTheOuterScope(t *testing.T) {
	cfg := loadConfig(t, isolationCfg)
	r := &Runner{Cfg: cfg}
	tunnels := &config.SandboxAllow{Tunnels: []string{"cloudflared"}}
	ctx := withDefaultIsolation(context.Background(), &config.IsolationConfig{Allow: tunnels})
	ctx = withDefaultIsolation(ctx, &config.IsolationConfig{Detect: &config.IsolationDetect{Action: "alert"}})
	iso := r.effectiveIsolation(ctx, config.Step{})
	if iso.Allow != tunnels || iso.Detect == nil || iso.Detect.Action != "alert" {
		t.Fatalf("trigger then workflow knobs: %+v", iso)
	}
	pty := &config.SandboxAllow{Pty: true}
	iso = r.effectiveIsolation(ctx, config.Step{Isolation: &config.IsolationConfig{Allow: pty}})
	if iso.Allow != pty || iso.Detect.Action != "alert" {
		t.Fatalf("the step's allowance over the scope's: %+v", iso)
	}
	if iso := r.effectiveIsolation(context.Background(), config.Step{Runtime: "pzout", Isolation: &config.IsolationConfig{Allow: pty}}); !iso.OptsOut() || iso.Allow != pty {
		t.Fatalf("knobs over a paseo runtime's opt-out: %+v", iso)
	}
	// A block with a mode still replaces the scope's whole.
	ctx = withDefaultIsolation(ctx, &config.IsolationConfig{Mode: "none"})
	if iso := r.effectiveIsolation(ctx, config.Step{}); iso.Allow != nil || !iso.OptsOut() {
		t.Fatalf("a mode block layered: %+v", iso)
	}
}

const allowCrossCfg = `
connectors:
  svc: { use: fake }
runtimes:
  pz: { use: paseo, default: true }
workflows:
  deploy:
    isolation: { allow: { tunnels: [cloudflared] } }
    steps:
      - { id: dep, type: agent, name: dep, prompt: p }
      - { id: callhelper, workflow: helper }
      - { id: callown, workflow: ownallow }
  helper:
    steps: [ { id: help, type: agent, name: help, prompt: p } ]
  ownallow:
    isolation: { allow: { pty: true } }
    steps: [ { id: own, type: agent, name: own, prompt: p } ]
`

// An allow: is the scope's that declares it: a workflow's reaches its own
// agent steps, never a workflow it calls (which declares its own), while
// the rest of the caller's isolation (detection here) is still inherited.
func TestAllowDoesNotCrossAWorkflowCall(t *testing.T) {
	cfg := loadConfig(t, allowCrossCfg)
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
	runTrigger(rig, newTrigger("ping", nil), mustSpec(t, `
on: svc.ping
isolation: { allow: { reverse_shell: true }, detect: { action: alert } }
steps:
  - { id: top, type: agent, name: top, prompt: p }
  - { id: calldeploy, workflow: deploy }
`))
	if failed, e := rig.workflowFailed(); failed {
		t.Fatal(e)
	}
	mu.Lock()
	defer mu.Unlock()
	allowOf := func(id string) *config.SandboxAllow {
		if got[id] == nil {
			return nil
		}
		return got[id].Allow
	}
	if a := allowOf("top"); a == nil || !a.ReverseShell {
		t.Fatalf("the trigger's own step keeps its allowance: %+v", a)
	}
	if a := allowOf("dep"); a == nil || a.ReverseShell || len(a.Tunnels) != 1 {
		t.Fatalf("deploy's step has deploy's allowance only, not its caller's: %+v", a)
	}
	if a := allowOf("help"); a != nil {
		t.Fatalf("a called workflow inherited its caller's allowance: %+v", a)
	}
	if a := allowOf("own"); a == nil || !a.Pty || len(a.Tunnels) != 0 || a.ReverseShell {
		t.Fatalf("a called workflow's own allowance, alone: %+v", a)
	}
	for _, id := range []string{"top", "dep", "help", "own"} {
		if got[id] == nil || got[id].Detect == nil || got[id].Detect.Action != "alert" {
			t.Fatalf("%s: the rest of the caller's isolation is inherited: %+v", id, got[id])
		}
	}
}
