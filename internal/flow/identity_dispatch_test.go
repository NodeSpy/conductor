package flow

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// C1: an unnamed list-form trigger past index 0 must dispatch under the
// identity every lookup path computes for it.
func TestDispatchIdentityMatchesLookupForSecondTrigger(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
triggers:
  - on: svc.ping
    steps: [{ type: agent, prompt: one }]
  - on: svc.pong
    steps: [{ type: agent, prompt: two }]
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	var got string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = req.Identity
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	// What every LOOKUP path (WalkSteps, the affinity sweep) computes.
	var want string
	cfg.WalkSteps(func(scope config.IdentityScope, slot int, s *config.Step) {
		if s.Prompt == "two" {
			want = s.Identity(scope, slot)
		}
	})
	runTrigger(rig, newTrigger("pong", nil), cfg.Triggers[1])
	if got != want {
		t.Fatalf("dispatch identity %q != lookup identity %q", got, want)
	}
}

// H7: the dispatch-time identity of a step inside a parallel branch must
// equal what WalkSteps computes for it — and the two branches must differ.
func TestParallelBranchDispatchIdentityMatchesLookup(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	var got []string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = append(got, req.Identity)
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
name: t
steps:
  - id: fan
    parallel:
      - [ { type: agent, prompt: left } ]
      - [ { type: agent, prompt: right } ]
`)
	runTrigger(rig, newTrigger("ping", nil), spec)
	if len(got) != 2 {
		t.Fatalf("both branches should dispatch, got %v", got)
	}
	if got[0] == got[1] {
		t.Fatalf("parallel branch steps must not share an identity: %q", got[0])
	}
	// The lookup path — the affinity sweep walks exactly this way.
	cfg.Triggers = []config.TriggerSpec{spec}
	want := map[string]bool{}
	cfg.WalkSteps(func(scope config.IdentityScope, slot int, s *config.Step) {
		if s.Prompt != "" {
			want[s.Identity(scope, slot)] = true
		}
	})
	for _, id := range got {
		if !want[id] {
			t.Fatalf("dispatch identity %q is not one the lookup path computes (%v)", id, want)
		}
	}
}

// §2: a `workflow:` call runs the CALLED workflow's steps, so they carry
// that workflow's identity scope — the one WalkSteps computes. Inheriting
// the caller's scope gave a shared helper a different identity per call
// site, and its sessions bound where nothing would look for them.
func TestCalledWorkflowStepsUseTheCalledWorkflowScope(t *testing.T) {
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
workflows:
  helper:
    steps:
      - { id: work, type: agent, prompt: helping }
`)
	reg := buildRegistry(t, cfg)
	rig := newTestRunner(t, cfg, reg)
	var got string
	rig.Agents.dispatchFunc = func(ctx context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		got = req.Identity
		return dispatch.RunRef{AgentID: "a1", Output: "done"}, nil
	}
	spec := mustSpec(t, `
on: svc.ping
name: caller
steps:
  - { id: call, workflow: helper }
`)
	runTrigger(rig, newTrigger("ping", nil), spec)

	// What the lookup path — the affinity sweep — computes for that step.
	var want string
	cfg.WalkSteps(func(scope config.IdentityScope, slot int, s *config.Step) {
		if s.Prompt == "helping" {
			want = s.Identity(scope, slot)
		}
	})
	if want == "" {
		t.Fatal("the helper step should be walkable")
	}
	if got != want {
		t.Fatalf("dispatch identity %q != lookup identity %q — a called workflow's steps must use ITS scope", got, want)
	}
}
