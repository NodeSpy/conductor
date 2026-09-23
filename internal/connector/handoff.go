package connector

import (
	"context"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// DEPRECATED ALIAS of the step connector. The hand-off's reactive lifecycle
// (bail/rerun as watch actions, done as the agent's release+output call, and
// the bailed/superseded/done events) generalized to EVERY live step and moved
// to the `step` connector — a hand-off is just a step whose agent a human
// drives. This type stays registered so existing packs, prompts, and triggers
// that say handoff.* keep working: verbs forward to the same handlers, and the
// config layer canonicalizes handoff.* watch actions to step.*. New config and
// prompts should say step.*.
var handoffDecl = &TypeDecl{
	Type: "handoff",
	Desc: "DEPRECATED alias of the step connector (a hand-off is a step). handoff.done = step.done; handoff.bail/rerun = step.bail/rerun. Use step.* in new config.",
	Events: []EventDecl{
		{Name: "bailed", Desc: "alias of step.bailed", Context: stepEventContext},
		{Name: "superseded", Desc: "alias of step.superseded", Context: stepEventContext},
		{Name: "done", Desc: "alias of step.done", Context: stepEventContext},
	},
	Verbs: []VerbDecl{
		{
			Name: "bail", Desc: "alias of step.bail (a watch-step action)",
			Options: Schema{},
			Outputs: Schema{"bailed": {Type: TBool}},
		},
		{
			Name: "rerun", Desc: "alias of step.rerun (a watch-step action)",
			Options: Schema{"prompt": {Type: TString, Desc: "extra text appended to the step's prompt on the re-run"}},
			Outputs: Schema{"superseded": {Type: TBool}},
		},
		{
			Name: "done", Desc: "alias of step.done — release this hand-off (and deliver your output, when a schema was required)",
			Options: Schema{
				"reason": {Type: TString, Desc: "optional one-line summary of the outcome"},
				"output": {Type: TAny, Desc: "the step's structured result — any JSON value matching the output schema you were given (only when the task specified one; validated by this call)"},
			},
			Outputs: Schema{"released": {Type: TBool}},
		},
	},
}

func init() { RegisterType(handoffDecl, newHandoffImpl) }

type handoffImpl struct{}

func newHandoffImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	return handoffImpl{}, nil
}

func (handoffImpl) Validate() error          { return nil }
func (handoffImpl) DeclaredEvents() []string { return nil }
func (handoffImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	// Lifecycle events are emitted by the engine (like conductor.*), not
	// sourced here; nothing to lower.
	return nil, nil
}

// Invoke forwards every verb to the step connector's behavior — one handler,
// two names.
func (handoffImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	return stepImpl{}.Invoke(ctx, verb, opts)
}
