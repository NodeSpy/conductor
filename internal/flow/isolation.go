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
	outer, _ := ctx.Value(isolationKey{}).(*config.IsolationConfig)
	return context.WithValue(ctx, isolationKey{}, config.LayerIsolation(outer, iso))
}

// withCalledWorkflowIsolation is withDefaultIsolation across a workflow:
// call. The called workflow inherits its caller's isolation, but not its
// allow:: an allowance is the operator's for the scope that declares it,
// never for whatever that scope calls (another pack's workflow among
// them), so a called workflow that needs one declares its own.
func withCalledWorkflowIsolation(ctx context.Context, iso *config.IsolationConfig) context.Context {
	if outer, _ := ctx.Value(isolationKey{}).(*config.IsolationConfig); outer != nil && outer.Allow != nil {
		o := *outer
		o.Allow = nil
		ctx = context.WithValue(ctx, isolationKey{}, &o)
	}
	return withDefaultIsolation(ctx, iso)
}

// effectiveIsolation resolves the isolation: one agent dispatch carries on
// its step: the step's own, else its trigger's / workflow's, else — for a
// paseo runtime, whose launch the paseo dispatcher alone decides — that
// runtime's. A block of only the agent sandbox's knobs (allow:, detect:,
// egress_limits:) layers onto the scope outside it instead of replacing it
// (config.LayerIsolation). (A runtime conductor launches itself applies its
// own isolation: under the step's, in the controller.)
func (r *Runner) effectiveIsolation(ctx context.Context, step config.Step) *config.IsolationConfig {
	var rt *config.IsolationConfig
	if r.Cfg != nil {
		if rc, ok := r.Cfg.Runtimes[r.runtimeOf(step)]; ok && rc.BuiltinType() == "paseo" {
			rt = rc.Isolation
		}
	}
	scope, _ := ctx.Value(isolationKey{}).(*config.IsolationConfig)
	return config.LayerIsolation(config.LayerIsolation(rt, scope), step.Isolation)
}

// packNarrowed is iso as a pack's trigger or workflow (pack != "") sets
// it: its egress_only only narrows what the launch inherits. The mark is
// made here, from where the block came from, never carried from load.
func packNarrowed(iso *config.IsolationConfig, pack string) *config.IsolationConfig {
	if iso == nil || pack == "" || iso.EgressOnly == nil {
		return iso
	}
	c := *iso
	c.EgressOnlyNarrow = true
	return &c
}
