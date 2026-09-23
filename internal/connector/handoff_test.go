package connector

import (
	"context"
	"testing"
)

func TestHandoffDoneRoutesToOps(t *testing.T) {
	var gotAgent, gotDispatch string
	var gotOutput any
	var gotHas bool
	// handoff is a DEPRECATED ALIAS of step: its verbs forward to the step ops.
	SetStepOps(func() *StepOps {
		return &StepOps{
			Done: func(_ context.Context, dispatchID, agentID, _ string, output any, hasOutput bool) error {
				gotDispatch, gotAgent, gotOutput, gotHas = dispatchID, agentID, output, hasOutput
				return nil
			},
		}
	})
	t.Cleanup(func() { SetStepOps(nil) })

	impl := handoffImpl{}
	out, err := impl.Invoke(context.Background(), "done", map[string]any{
		"__handoff_agent": "agent-7", "__dispatch": "d-1",
		"output": map[string]any{"decision": "approve"},
	})
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	if gotAgent != "agent-7" || gotDispatch != "d-1" {
		t.Fatalf("ops.Done got (%q, %q), want (d-1, agent-7)", gotDispatch, gotAgent)
	}
	// handoff.done carries the SAME output contract as step.done.
	if !gotHas || gotOutput.(map[string]any)["decision"] != "approve" {
		t.Fatalf("output must ride handoff.done too: has=%v out=%v", gotHas, gotOutput)
	}
	if released, _ := out["released"].(bool); !released {
		t.Fatalf("done outputs = %v, want released:true", out)
	}
}

func TestHandoffVerbsWithoutOps(t *testing.T) {
	SetStepOps(nil)
	impl := handoffImpl{}
	if _, err := impl.Invoke(context.Background(), "done", nil); err == nil {
		t.Fatal("done with no ops wired should error (only runs inside a live daemon)")
	}
	if _, err := impl.Invoke(context.Background(), "bail", nil); err == nil {
		t.Fatal("bail is a watch action — not callable directly")
	}
	if _, err := impl.Invoke(context.Background(), "bogus", nil); err == nil {
		t.Fatal("unknown verb should error")
	}
}
