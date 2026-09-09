package dispatch

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
)

// TestAgentTitle proves the paseo agent title carries the PR/issue number so a
// hand-off waiting on a review is identifiable (a misleading branch name is
// exactly what confused a real hand-off).
func TestAgentTitle(t *testing.T) {
	withNum := agentTitle(Request{Trigger: core.Trigger{
		Kind:   "review_requested",
		Target: core.Target{Repo: "acme/app", Number: 5399},
	}})
	if withNum != "conductor: acme/app#5399 review_requested" {
		t.Fatalf("with number: got %q", withNum)
	}
	noNum := agentTitle(Request{Trigger: core.Trigger{
		Kind:   "release",
		Target: core.Target{Repo: "acme/app"},
	}})
	if noNum != "conductor: acme/app release" {
		t.Fatalf("no number: got %q", noNum)
	}
}
