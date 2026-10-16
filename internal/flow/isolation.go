package flow

import (
	"context"

	"github.com/NodeSpy/conductor/internal/config"
)

type isolationKey struct{}

// withDefaultIsolation stashes a trigger/workflow-level isolation: as the
// default for its agent steps (a step's own wins — see effectiveIsolation).
// A called workflow without one inherits its caller's.
func withDefaultIsolation(ctx context.Context, iso *config.IsolationConfig) context.Context {
	if iso == nil {
		return ctx
	}
	return context.WithValue(ctx, isolationKey{}, iso)
}

// effectiveIsolation resolves the isolation: one agent dispatch carries on
// its step: the step's own, else its trigger's / workflow's, else — for a
// paseo runtime, whose launch the paseo dispatcher alone decides — that
// runtime's. (A runtime conductor launches itself applies its own
// isolation: under the step's, in the controller.)
func (r *Runner) effectiveIsolation(ctx context.Context, step config.Step) *config.IsolationConfig {
	if step.Isolation != nil {
		return step.Isolation
	}
	if iso, _ := ctx.Value(isolationKey{}).(*config.IsolationConfig); iso != nil {
		return iso
	}
	if r.Cfg == nil {
		return nil
	}
	rn := r.runtimeOf(step)
	if rt, ok := r.Cfg.Runtimes[rn]; ok && rt.BuiltinType() == "paseo" {
		return rt.Isolation
	}
	return nil
}
