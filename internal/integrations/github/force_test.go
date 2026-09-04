package github

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/core"
)

func TestForceUnconfiguredKind(t *testing.T) {
	g := newTestIntegration(t, baseConfig())
	// A kind with no configured action errors before any network call.
	n, err := g.Force(context.Background(), "nonexistent_kind", "acme/widget", 1,
		func(context.Context, core.Trigger) {})
	if err == nil || n != 0 {
		t.Fatalf("force should error for an unconfigured kind, got n=%d err=%v", n, err)
	}
}
