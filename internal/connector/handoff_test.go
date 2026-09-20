package connector

import (
	"context"
	"testing"
)

func TestHandoffDoneRoutesToOps(t *testing.T) {
	var gotAgent string
	SetHandoffOps(func() *HandoffOps {
		return &HandoffOps{
			Done: func(_ context.Context, agentID string) error { gotAgent = agentID; return nil },
		}
	})
	t.Cleanup(func() { SetHandoffOps(nil) })

	impl := handoffImpl{}
	out, err := impl.Invoke(context.Background(), "done", map[string]any{"__handoff_agent": "agent-7"})
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	if gotAgent != "agent-7" {
		t.Fatalf("ops.Done got agent %q, want agent-7", gotAgent)
	}
	if released, _ := out["released"].(bool); !released {
		t.Fatalf("done outputs = %v, want released:true", out)
	}
}

func TestHandoffVerbsWithoutOps(t *testing.T) {
	SetHandoffOps(nil)
	impl := handoffImpl{}
	if _, err := impl.Invoke(context.Background(), "done", nil); err == nil {
		t.Fatal("done with no ops wired should error (only runs inside a live daemon)")
	}
	if _, err := impl.Invoke(context.Background(), "bogus", nil); err == nil {
		t.Fatal("unknown verb should error")
	}
}
