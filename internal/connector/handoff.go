package connector

import (
	"context"
	"fmt"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// The handoff connector exposes a hand-off's reactive lifecycle as verbs and
// events. Its verbs are the actions a `watch:` rule runs — `bail` (tear the
// hand-off down; the reason went away), `rerun` (re-run the same step on the new
// state), and `done` (the interaction concluded; release it so the reaper
// reclaims the workspace). To re-run a DIFFERENT workflow, a watch uses a native
// `workflow:` step, not a verb. `done` is deliberately reachable from the SKILL
// surface too, so a hand-off agent can `conductor call handoff.done` when it has
// nothing more for the reviewer, instead of the workspace lingering until a
// manual archive. Its events (`bailed`/`refreshed`/`done`) let a trigger react
// to a hand-off's outcome. Always-on; the name is reserved.
//
// The verbs act on a SPECIFIC live hand-off. In a `watch:` rule the engine knows
// which one (it owns the watch loop) and acts directly. Through the skill surface
// the target is resolved from the caller's session identity. The connector Impl
// therefore delegates to daemon ops wired at boot (handoffOps), mirroring the
// conductor connector; with no ops wired (e.g. `conductor run`) the verbs report
// that they only run inside a live daemon's hand-off.

var handoffDecl = &TypeDecl{
	Type: "handoff",
	Desc: "A hand-off's reactive lifecycle: bail/refresh/done as verbs, bailed/refreshed/done as events. Always available; used from a step's watch: rules and (for done) the agent skill surface.",
	Events: []EventDecl{
		{Name: "bailed", Desc: "a hand-off was torn down because its reason went away", Context: handoffContext},
		{Name: "superseded", Desc: "a hand-off was replaced by a re-run (step or workflow) on new state", Context: handoffContext},
		{Name: "done", Desc: "a hand-off's interaction concluded and it was released", Context: handoffContext},
	},
	Verbs: []VerbDecl{
		{
			Name: "bail", Desc: "tear down this hand-off (cancel the agent, close the draft, release it) — the reason it existed is gone. A watch-step action.",
			Options: Schema{"notify": {Type: TString, Desc: "message to post on the hand-off channel as it closes"}},
			Outputs: Schema{"bailed": {Type: TBool}},
		},
		{
			Name: "rerun", Desc: "supersede this hand-off by re-running the SAME step on the current state (surface-agnostic). A watch-step action. To run a DIFFERENT workflow, use a `workflow:` step instead.",
			Options: Schema{"notify": {Type: TString}, "prompt": {Type: TString, Desc: "extra text appended to the step's prompt on the re-run"}},
			Outputs: Schema{"superseded": {Type: TBool}},
		},
		{
			Name: "done", Desc: "the interaction is finished — release this hand-off so its workspace is reclaimed (agent-callable when it has nothing more for the reviewer)",
			Options: Schema{},
			Outputs: Schema{"released": {Type: TBool}},
		},
	},
}

func init() { RegisterType(handoffDecl, newHandoffImpl) }

// handoffContext is the shared event context for the lifecycle events.
var handoffContext = Schema{
	"ref":    {Type: TString, Desc: "repo#number"},
	"repo":   {Type: TString},
	"number": {Type: TInt},
	"step":   {Type: TString, Desc: "the hand-off step id"},
	"reason": {Type: TString, Desc: "why (e.g. \"pr merged\")"},
}

// HandoffOps is the daemon-side surface the SKILL-reachable handoff verbs act
// through, wired at boot. Only `done` is agent-callable; bail/rerun_step/
// run_workflow are watch-rule actions the engine performs directly (it holds the
// hand-off in scope), so they are not routed through here.
type HandoffOps struct {
	// Done releases the hand-off owning the given AGENT (skill identity), so the
	// reaper reclaims it. Empty agentID means "the caller's own session."
	Done func(ctx context.Context, agentID string) error
}

var (
	handoffOpsMu sync.RWMutex
	handoffOpsFn func() *HandoffOps
)

// SetHandoffOps wires the daemon's live hand-off operations (main/engine boot).
func SetHandoffOps(fn func() *HandoffOps) {
	handoffOpsMu.Lock()
	handoffOpsFn = fn
	handoffOpsMu.Unlock()
}

func handoffOps() *HandoffOps {
	handoffOpsMu.RLock()
	fn := handoffOpsFn
	handoffOpsMu.RUnlock()
	if fn == nil {
		return nil
	}
	return fn()
}

type handoffImpl struct{}

func newHandoffImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	return handoffImpl{}, nil
}

func (handoffImpl) Validate() error          { return nil }
func (handoffImpl) DeclaredEvents() []string { return nil }
func (handoffImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	// Hand-off lifecycle events are emitted by the engine (like conductor.*),
	// not sourced here; nothing to lower.
	return nil, nil
}

func (handoffImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	switch verb {
	case "bail", "rerun":
		// Watch-step actions: the engine performs these directly (it holds the
		// hand-off in scope). They are not reachable through the skill surface.
		return nil, fmt.Errorf("handoff.%s runs from a watch step, not as a direct call", verb)
	case "done":
		ops := handoffOps()
		if ops == nil || ops.Done == nil {
			return nil, fmt.Errorf("handoff.done: only runs inside a live daemon's hand-off")
		}
		// The agent target is injected by the skill path from the caller identity.
		agent, _ := opts["__handoff_agent"].(string)
		if err := ops.Done(ctx, agent); err != nil {
			return nil, err
		}
		return map[string]any{"released": true}, nil
	}
	return nil, fmt.Errorf("handoff: unknown verb %q", verb)
}
