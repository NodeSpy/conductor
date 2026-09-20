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
// hand-off down; the reason went away), `refresh` (re-run the producer on the
// new state), `done` (the interaction concluded; release it so the reaper
// reclaims the workspace). `done` is deliberately reachable from the SKILL
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
		{Name: "refreshed", Desc: "a hand-off's producer was re-run on new state", Context: handoffContext},
		{Name: "done", Desc: "a hand-off's interaction concluded and it was released", Context: handoffContext},
	},
	Verbs: []VerbDecl{
		{
			Name: "bail", Desc: "tear down this hand-off (cancel the agent, close the draft, release it) — the reason it existed is gone",
			Options: Schema{"notify": {Type: TString, Desc: "message to post on the hand-off channel as it closes"}},
			Outputs: Schema{"bailed": {Type: TBool}},
		},
		{
			Name: "refresh", Desc: "supersede this hand-off: re-run the step that produced it on the current state, replacing the stale draft",
			Options: Schema{"notify": {Type: TString}},
			Outputs: Schema{"refreshed": {Type: TBool}},
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

// HandoffOps is the daemon-side surface the handoff verbs act through, wired at
// boot. Each takes the target hand-off's session key (resolved by the caller —
// the engine for a watch rule, the skill session identity for an agent call).
type HandoffOps struct {
	Bail    func(ctx context.Context, sessionKey, notify string) error
	Refresh func(ctx context.Context, sessionKey, notify string) error
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
	ops := handoffOps()
	if ops == nil {
		return nil, fmt.Errorf("handoff.%s: only runs inside a live daemon's hand-off", verb)
	}
	notify, _ := opts["notify"].(string)
	// sessionKey/agent target is injected by the caller (the engine watch loop
	// sets it; the skill path resolves it from the caller's identity).
	key, _ := opts["__handoff_session"].(string)
	agent, _ := opts["__handoff_agent"].(string)
	switch verb {
	case "bail":
		if ops.Bail == nil {
			return nil, fmt.Errorf("handoff.bail: not wired in this daemon")
		}
		if err := ops.Bail(ctx, key, notify); err != nil {
			return nil, err
		}
		return map[string]any{"bailed": true}, nil
	case "refresh":
		if ops.Refresh == nil {
			return nil, fmt.Errorf("handoff.refresh: not wired in this daemon")
		}
		if err := ops.Refresh(ctx, key, notify); err != nil {
			return nil, err
		}
		return map[string]any{"refreshed": true}, nil
	case "done":
		if ops.Done == nil {
			return nil, fmt.Errorf("handoff.done: not wired in this daemon")
		}
		if err := ops.Done(ctx, agent); err != nil {
			return nil, err
		}
		return map[string]any{"released": true}, nil
	}
	return nil, fmt.Errorf("handoff: unknown verb %q", verb)
}
