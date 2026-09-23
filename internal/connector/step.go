package connector

import (
	"context"
	"fmt"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// The step connector is the done signal every conductor-launched agent carries:
// `step.done` says "my task is complete — conductor can reclaim my workspace."
// It is the general form of handoff.done (same daemon-side handler; handoff.done
// additionally tears down the live hand-off state), auto-granted to every
// dispatch (dispatch.EffectiveSkillPolicy), and it is how a workspace conductor
// is NOT already blocked on gets reclaimed: there is no background sweep.
//
// The target is resolved from the caller's token identity — the dispatch id the
// daemon minted at launch — never from an option the agent supplies, so an
// agent can only ever release itself. Agents conductor did not launch hold no
// token and cannot reach the verb at all; on top of that, the archive itself is
// refused for any agent not in the ownership ledger (dispatch.OwnedSet).
var stepDecl = &TypeDecl{
	Type: "step",
	Desc: "The dispatched step's own lifecycle: step.done signals the agent's task is complete (optionally delivering its structured output) so conductor reclaims its workspace; step.bail / step.rerun are watch-rule actions on a live step. Always available; done is auto-granted to every conductor-launched agent.",
	Events: []EventDecl{
		{Name: "bailed", Desc: "a live step was torn down because its reason went away", Context: stepEventContext},
		{Name: "superseded", Desc: "a live step was replaced by a re-run on new state", Context: stepEventContext},
		{Name: "done", Desc: "a live step's agent signalled done and it was released", Context: stepEventContext},
	},
	Verbs: []VerbDecl{
		{
			Name: "bail", Desc: "tear down this live step (cancel the agent, release it) — the reason it existed is gone. A watch-step action.",
			Options: Schema{},
			Outputs: Schema{"bailed": {Type: TBool}},
		},
		{
			Name: "rerun", Desc: "supersede this live step by re-running the SAME step on the current state. A watch-step action. To run a DIFFERENT workflow, use a `workflow:` step instead.",
			Options: Schema{"prompt": {Type: TString, Desc: "extra text appended to the step's prompt on the re-run"}},
			Outputs: Schema{"superseded": {Type: TBool}},
		},
		{
			Name: "done", Desc: "your task is fully complete — deliver your result (when a schema was required) and release this agent's workspace back to conductor. Call as your final action.",
			Options: Schema{
				"reason": {Type: TString, Desc: "optional one-line summary of what was completed"},
				"output": {Type: TAny, Desc: "the step's structured result — any JSON value matching the output schema you were given (required when the task specified one; validated by this call)"},
			},
			Outputs: Schema{"released": {Type: TBool}},
		},
	},
}

func init() { RegisterType(stepDecl, newStepImpl) }

// StepOps is the daemon-side surface step.done acts through, wired at boot.
type StepOps struct {
	// Done delivers the calling dispatch's structured output (when it carries
	// one — any JSON value, validated against the waiting schema; hasOutput
	// distinguishes an explicit false/0/"" delivery from no output at all) and
	// archives the agent that dispatch launched. dispatchID is the caller's
	// token-bound dispatch id (daemon-injected, never agent-supplied); agentID
	// is a legacy fallback identity; reason is the agent's optional summary.
	Done func(ctx context.Context, dispatchID, agentID, reason string, output any, hasOutput bool) error
}

var (
	stepOpsMu sync.RWMutex
	stepOpsFn func() *StepOps
)

// SetStepOps wires the daemon's step lifecycle operations (main/engine boot).
func SetStepOps(fn func() *StepOps) {
	stepOpsMu.Lock()
	stepOpsFn = fn
	stepOpsMu.Unlock()
}

func stepOps() *StepOps {
	stepOpsMu.RLock()
	fn := stepOpsFn
	stepOpsMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

type stepImpl struct{}

func newStepImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	return stepImpl{}, nil
}

func (stepImpl) Validate() error          { return nil }
func (stepImpl) DeclaredEvents() []string { return nil }
func (stepImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	// step has no events yet; nothing to lower.
	return nil, nil
}

// stepEventContext is the shared context for the step lifecycle events.
var stepEventContext = Schema{
	"ref":    {Type: TString, Desc: "repo#number"},
	"repo":   {Type: TString},
	"number": {Type: TInt},
	"step":   {Type: TString, Desc: "the step id"},
	"reason": {Type: TString, Desc: "why (e.g. \"pr merged\")"},
}

func (stepImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	switch verb {
	case "bail", "rerun":
		// Watch-step actions: the engine performs these directly (it holds the
		// live step in scope). They are not reachable through the skill surface.
		return nil, fmt.Errorf("step.%s runs from a watch step, not as a direct call", verb)
	case "done":
		ops := stepOps()
		if ops == nil || ops.Done == nil {
			return nil, fmt.Errorf("step.done: only runs inside a live daemon")
		}
		// Both identity options are injected by the skill path from the caller's
		// token (leading-underscore options are daemon-internal; an agent cannot
		// set them).
		dispatchID, _ := opts["__dispatch"].(string)
		agentID, _ := opts["__handoff_agent"].(string)
		reason, _ := opts["reason"].(string)
		output, hasOutput := opts["output"]
		if err := ops.Done(ctx, dispatchID, agentID, reason, output, hasOutput); err != nil {
			return nil, err
		}
		return map[string]any{"released": true}, nil
	}
	return nil, fmt.Errorf("step: unknown verb %q", verb)
}
