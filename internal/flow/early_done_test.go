package flow

import (
	"context"
	"errors"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

// An agent that calls step.done while its foreground dispatch is still in
// flight is reclaimed at the step boundary even when the step keeps agents
// (archive_when_done: false) — nothing else would ever archive it. One that
// didn't signal done is kept, as the step asked.
func TestEarlyDoneArchivesAtStepBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		signaled bool
		want     int
	}{
		{"done signalled", true, 1},
		{"no done signal", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
			rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
			rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
				return dispatch.RunRef{AgentID: "ag-1", Output: "{}"}, nil
			}
			var taken []string
			rig.Runner.Agents.TakeDone = func(dispatchID string) bool {
				taken = append(taken, dispatchID)
				return tc.signaled
			}
			spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, prompt: "fix it", archive_when_done: false }
`)
			runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
			if failed, errStr := rig.workflowFailed(); failed {
				t.Fatalf("workflow failed: %s", errStr)
			}
			reqs := rig.Agents.requests()
			if len(reqs) != 1 || len(taken) != 1 || taken[0] != reqs[0].DispatchID {
				t.Fatalf("the step must consume its own dispatch's done mark once: taken %v, dispatched %+v", taken, reqs)
			}
			rig.Agents.mu.Lock()
			got := len(rig.Agents.archived)
			rig.Agents.mu.Unlock()
			if got != tc.want {
				t.Fatalf("archived %d agents, want %d", got, tc.want)
			}
		})
	}
}

// The mark is consumed on every outcome, a failed dispatch included: a mark
// left behind would outlive its dispatch.
func TestEarlyDoneConsumedWhenTheDispatchFails(t *testing.T) {
	cfg := loadConfig(t, "connectors:\n  svc: { use: fake }\n")
	rig := newTestRunner(t, cfg, buildRegistry(t, cfg))
	rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
		return dispatch.RunRef{}, errors.New("runtime unreachable")
	}
	var taken []string
	rig.Runner.Agents.TakeDone = func(dispatchID string) bool {
		taken = append(taken, dispatchID)
		return true
	}
	spec := mustSpec(t, `
on: svc.ping
steps:
  - { id: fix, type: agent, prompt: "fix it", archive_when_done: false }
`)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "x"}), spec)
	reqs := rig.Agents.requests()
	if len(reqs) == 0 || len(taken) != len(reqs) || taken[0] != reqs[0].DispatchID {
		t.Fatalf("a failed dispatch must still consume its own mark: taken %v, dispatched %d", taken, len(reqs))
	}
}
