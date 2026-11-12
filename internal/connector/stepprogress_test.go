package connector

import (
	"context"
	"testing"
)

// step.progress hands the note to the caller's own dispatch — the id the
// skill path injects from the token, never one the agent names — and says
// whether a run took it.
func TestStepProgressVerb(t *testing.T) {
	var gotID, gotText string
	SetStepOps(func() *StepOps {
		return &StepOps{Progress: func(id, text string) bool { gotID, gotText = id, text; return id == "d-1" }}
	})
	defer SetStepOps(nil)
	out, err := stepImpl{}.Invoke(context.Background(), "progress", map[string]any{"text": "running the tests", "__dispatch": "d-1"})
	if err != nil || out["ok"] != true || gotID != "d-1" || gotText != "running the tests" {
		t.Fatalf("out=%v err=%v id=%q text=%q", out, err, gotID, gotText)
	}
	out, _ = stepImpl{}.Invoke(context.Background(), "progress", map[string]any{"text": "x", "__dispatch": "other"})
	if out["ok"] != false {
		t.Fatalf("a dispatch no run reports took the note: %v", out)
	}
	// No dispatch identity: nothing to report to.
	out, _ = stepImpl{}.Invoke(context.Background(), "progress", map[string]any{"text": "x"})
	if out["ok"] != false {
		t.Fatalf("an unidentified call took the note: %v", out)
	}
	SetStepOps(nil)
	if out, err := (stepImpl{}).Invoke(context.Background(), "progress", map[string]any{"text": "x", "__dispatch": "d-1"}); err != nil || out["ok"] != false {
		t.Fatalf("outside a daemon: %v %v", out, err)
	}
	if _, ok := stepDecl.Verb("progress"); !ok {
		t.Fatal("step.progress is not declared")
	}
}
