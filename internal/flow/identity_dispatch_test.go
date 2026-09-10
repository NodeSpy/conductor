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
