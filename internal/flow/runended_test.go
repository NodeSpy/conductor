package flow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/dispatch"
)

const runEndedSpec = `
on: %[1]s.ping
steps:
  - id: fix
    type: agent
    prompt: p
`

// RunEnded hears a recorded run's history id — the run part of every
// dispatch id it issued — once the run ends for good, however it ends; a run
// a shutdown interrupts (it resumes on restart) has not ended.
func TestRunEndedHearsTheRunsEndNotAShutdown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcome  string
		endsOnce bool
	}{
		{"reok", "ok", true},
		{"refail", "fail", true},
		{"reshut", "shutdown", false},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			rig, _ := headRig(t, tc.name, "aaaaaaa1111")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var dispatched string
			rig.Agents.dispatchFunc = func(_ context.Context, req dispatch.Request) (dispatch.RunRef, error) {
				dispatched = req.DispatchID
				switch tc.outcome {
				case "fail":
					return dispatch.RunRef{}, errors.New("boom")
				case "shutdown":
					cancel()
					return dispatch.RunRef{}, context.Canceled
				}
				return dispatch.RunRef{Output: "{}", AgentID: "a1"}, nil
			}
			var ended []string
			rig.Runner.RunEnded = func(id string) { ended = append(ended, id) }
			run := emptyRun()
			run.ID = "run-" + tc.name
			spec := mustSpec(t, fmt.Sprintf(runEndedSpec, tc.name))
			rig.Runner.Run(ctx, run, headTrigger(tc.name), spec, rig.Runner.IndexOf(spec), nil, false)
			runPart, _, _ := strings.Cut(dispatched, ":")
			if dispatched == "" || runPart == "" {
				t.Fatalf("dispatch id %q", dispatched)
			}
			switch {
			case tc.endsOnce && (len(ended) != 1 || ended[0] != runPart):
				t.Fatalf("RunEnded heard %v, want [%s] (dispatch %s)", ended, runPart, dispatched)
			case !tc.endsOnce && len(ended) != 0:
				t.Fatalf("an interrupted run was reported ended: %v", ended)
			}
		})
	}
}
