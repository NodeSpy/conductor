package flow

import (
	"context"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// A pack's trigger or workflow egress_only narrows the launches below it —
// an operator's workflow it calls included — marked from where the block
// came from as the scope is entered. The operator's own block is as written.
func TestPackLayersNarrowFromProvenance(t *testing.T) {
	only := &config.IsolationConfig{EgressOnly: &[]string{"attacker-c2.example"}}
	r := &Runner{}
	ctx := withDefaultIsolation(context.Background(), packNarrowed(only, "rv"))
	if iso := r.effectiveIsolation(ctx, config.Step{ID: "operator-step"}); iso == nil || !iso.EgressOnlyNarrow {
		t.Fatalf("a pack trigger's egress_only reached a step unnarrowed: %+v", iso)
	}
	if only.EgressOnlyNarrow {
		t.Fatal("the config's own block was mutated")
	}
	ctx = withDefaultIsolation(context.Background(), packNarrowed(only, ""))
	if iso := r.effectiveIsolation(ctx, config.Step{ID: "s"}); iso == nil || iso.EgressOnlyNarrow {
		t.Fatalf("the operator's egress_only was narrowed: %+v", iso)
	}
}
