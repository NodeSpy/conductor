package controller

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// A controller's runner is where the live-agent tracking lives (byPR). Every
// non-paseo Runner() used to build a fresh one per call, so the engine's
// duplicate-dispatch gate asked a tracker that had never seen the dispatch and
// always got "no" — a second review event for an in-flight PR spawned another
// agent on the same worktree. Before the fix, both halves of this test fail:
// the two runners are distinct pointers and the second one's table is empty.
func TestARunnerIsStableAcrossLookupsSoLivenessSurvives(t *testing.T) {
	reg := NewRegistry(map[string]config.ControllerConfig{
		"gem": {Agent: "gemini"}, // ACP transport — the non-paseo path
	}, "", &recordRunner{}, nil)

	first, err := reg.RunnerFor("gem")
	if err != nil {
		t.Fatalf("RunnerFor: %v", err)
	}
	second, err := reg.RunnerFor("gem")
	if err != nil {
		t.Fatalf("RunnerFor (again): %v", err)
	}
	if first != second {
		t.Fatalf("each lookup built a new runner (%p vs %p); liveness tracking "+
			"cannot survive between events", first, second)
	}

	// The state the engine actually reads has to carry over with it: mark a PR
	// live through the runner we got first, then ask the one we got second.
	cr, ok := first.(*controllerRunner)
	if !ok {
		t.Fatalf("expected a *controllerRunner, got %T", first)
	}
	const bucket = "acme/api#7"
	cr.mu.Lock()
	cr.byPR[bucket+"\x00review_requested"]++
	cr.mu.Unlock()

	if !second.(*controllerRunner).HasLiveAgent(context.Background(), bucket, "review_requested") {
		t.Fatal("a second lookup does not see the agent dispatched through the first: " +
			"the dedup gate would spawn a duplicate")
	}
}
