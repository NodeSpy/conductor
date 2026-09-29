package github

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/targets"
)

// TestReopenedClearsTargetLifecycleMark: GitHub surfaces a reopen as a plain
// pull_request "reopened" action (there is no dedicated `_closed`-shaped
// signal for it), but a merged/closed mark internal/targets is holding for
// this PR must not keep refusing its writes once it's live again.
func TestReopenedClearsTargetLifecycleMark(t *testing.T) {
	targets.Default.MarkClosed("acme/w", 6, false)
	t.Cleanup(func() { targets.Default.Reopen("acme/w", 6) })
	if _, ok := targets.Default.Closed("acme/w", 6); !ok {
		t.Fatal("setup: expected the target to read as closed before the reopen event")
	}

	g := newTestIntegration(t, richConfig())
	body := `{"action":"reopened","repository":{"full_name":"acme/w","name":"w","owner":{"login":"acme"}},
		"pull_request":{"number":6,"head":{"sha":"h","ref":"feature"},"base":{"ref":"main"}}}`
	g.triggersFor(context.Background(), "pull_request", []byte(body))

	if _, ok := targets.Default.Closed("acme/w", 6); ok {
		t.Fatal("a reopened PR must clear its closed/merged mark")
	}
}
