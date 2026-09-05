package connector

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// workflowDecl declares the built-in workflow verbs (#36 §11) — the Choose
// and Promote surfaces. Like kv/sql/memory/conductor the connector is
// registered unconditionally (the name is reserved); unlike them its verbs
// only mean something inside a running workflow, so the flow runner
// intercepts them (it owns the runner, the trigger scope, and the guard) and
// this Impl only serves validation/introspection.
var workflowDecl = &TypeDecl{
	Type: "workflow",
	Desc: "Built-in workflow verbs: the self-describing catalog (list), run-by-name or run-an-inline-plan (run), and agent promotion (save).",
	Verbs: []VerbDecl{
		{
			Name: "list", Desc: "the workflow catalog: every config + saved workflow's name, description, inputs, and health",
			Options: Schema{},
			Outputs: Schema{
				"workflows": {Type: TList, Desc: "entries: {name, description, inputs, source, version?, reviewed?, runs?, success_rate?, flagged?}"},
				"count":     {Type: TInt},
			},
		},
		{
			Name: "run", Desc: "run a named workflow ({name, with} — the Choose path) or an inline agent-authored plan ({steps}, guarded by policy.agent_authored)",
			Options: Schema{
				"name":   {Type: TString, Desc: "a config or saved workflow name"},
				"with":   {Type: TMap, Desc: "the named workflow's inputs"},
				"steps":  {Type: TList, Desc: "an inline plan (steps in the normal grammar)"},
				"reason": {Type: TString, Desc: "the choice rationale — audited"},
			},
			Outputs: Schema{},
		},
		{
			Name: "save", Desc: "promote steps to a durable, versioned reusable workflow (unreviewed until cleared)",
			Options: Schema{
				"name":        {Type: TString, Required: true},
				"description": {Type: TString, Desc: "what it does and when to use it — the catalog entry Choose reads"},
				"steps":       {Type: TList, Required: true},
			},
			Outputs: Schema{
				"name":     {Type: TString},
				"version":  {Type: TInt},
				"reviewed": {Type: TBool},
			},
		},
	},
}

func init() { RegisterType(workflowDecl, newWorkflowImpl) }

type workflowImpl struct{}

func newWorkflowImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	return workflowImpl{}, nil
}

func (workflowImpl) Validate() error          { return nil }
func (workflowImpl) DeclaredEvents() []string { return nil }
func (workflowImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("workflow has no source events")
}

// Invoke: the flow runner intercepts workflow.* before this is reached (it
// owns the execution scope); a call landing here came from outside a
// workflow (e.g. notify.via) where these verbs have no scope to run in.
func (workflowImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	return nil, fmt.Errorf("workflow.%s runs only inside a workflow's steps/hooks", verb)
}
