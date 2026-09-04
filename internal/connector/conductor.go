package connector

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/inbound"
)

// The conductor connector makes conductor ITSELF a first-class connector:
// its lifecycle events are a built-in source (`on: conductor.escalate` —
// alerting is an ordinary trigger, not a separate notify: subsystem), and
// its own operations are verbs (`uses: conductor.update` — a workflow can
// act on conductor, not just react to it). Always-on like kv/sql; the name
// is reserved.

// lifecycleContext is the context schema every lifecycle event shares.
var lifecycleContext = Schema{
	"message":     {Type: TString, Desc: "the composed notification line"},
	"event":       {Type: TString},
	"ref":         {Type: TString, Desc: "repo#number"},
	"repo":        {Type: TString},
	"number":      {Type: TInt},
	"kind":        {Type: TString, Desc: "the lifecycle event (same as {{.event}})"},
	"origin_kind": {Type: TString, Desc: "the originating work's kind (merge_conflict, …)"},
	"title":       {Type: TString},
}

// versionContext extends the lifecycle context for the update events.
func versionContext() Schema {
	s := Schema{}
	for k, v := range lifecycleContext {
		s[k] = v
	}
	s["version"] = Field{Type: TString, Desc: "the release tag"}
	return s
}

var conductorDecl = &TypeDecl{
	Type: "conductor",
	Desc: "Conductor itself — lifecycle events as a source, daemon operations as verbs; always available, no configuration.",
	Events: []EventDecl{
		{Name: "dispatch", Desc: "work started on a target", Context: lifecycleContext},
		{Name: "escalate", Desc: "a target gave up after retries", Context: lifecycleContext},
		{Name: "needs_input", Desc: "a workflow handed a PR to a live agent and is waiting on you", Context: lifecycleContext},
		{Name: "complete", Desc: "a run finished", Context: lifecycleContext},
		{Name: "failed", Desc: "a run errored", Context: lifecycleContext},
		{Name: "updated", Desc: "conductor self-updated (emitted on the first boot of the new release)", Context: versionContext()},
		{Name: "update_available", Desc: "a newer release was detected (update.apply: workflow)", Context: versionContext()},
	},
	Verbs: []VerbDecl{
		{
			Name: "update", Desc: "download the latest release, apply it, and restart into it",
			Options: Schema{},
			Outputs: Schema{
				"updated": {Type: TBool, Desc: "false when already on the latest release"},
				"version": {Type: TString, Desc: "the release now installed"},
			},
		},
		{Name: "pause", Desc: "stop dispatch at runtime (the pause control file)", Options: Schema{}, Outputs: Schema{}},
		{Name: "resume", Desc: "resume dispatch", Options: Schema{}, Outputs: Schema{}},
		{Name: "restart", Desc: "restart the daemon (re-exec in place)", Options: Schema{}, Outputs: Schema{}},
		{Name: "reload", Desc: "re-read the config (restarts into the same binary; config loads at boot)", Options: Schema{}, Outputs: Schema{}},
		{
			Name: "run", Desc: "fire a named `on: manual` trigger",
			Options: Schema{
				"name":   {Type: TString, Required: true, Desc: "the trigger's name:"},
				"inputs": {Type: TMap, Desc: "values for {{.inputs.*}}"},
			},
			Outputs: Schema{"message": {Type: TString}},
		},
	},
}

// ConductorOps are the daemon facilities behind the conductor.* verbs,
// injected by main (the CLI/daemon owns update/pause/restart machinery; a
// validate-only build leaves them nil and verbs error clearly). Update
// returns whether anything was installed and the tag now current; appliers
// that restart the process must do so AFTER returning, so the step
// checkpoints first.
type ConductorOps struct {
	Update  func(ctx context.Context) (updated bool, version string, err error)
	Pause   func() error
	Resume  func() error
	Restart func() error
	Reload  func() error
	Run     func(ctx context.Context, name string, inputs map[string]any) (string, error)
}

// SweepHook runs the github catch-up sweep now (all github connectors),
// wired by the daemon; the gh.sweep verb calls it.
var (
	opsMu     sync.Mutex
	ops       *ConductorOps
	sweepHook func(ctx context.Context) (int, error)
)

// SetConductorOps wires the daemon facilities (nil clears; tests inject
// fakes).
func SetConductorOps(o *ConductorOps) {
	opsMu.Lock()
	ops = o
	opsMu.Unlock()
}

// SetSweepHook wires the on-demand github sweep the gh.sweep verb runs.
func SetSweepHook(fn func(ctx context.Context) (int, error)) {
	opsMu.Lock()
	sweepHook = fn
	opsMu.Unlock()
}

func conductorOps() *ConductorOps {
	opsMu.Lock()
	defer opsMu.Unlock()
	return ops
}

func runSweepHook(ctx context.Context) (int, error) {
	opsMu.Lock()
	fn := sweepHook
	opsMu.Unlock()
	if fn == nil {
		return 0, fmt.Errorf("gh.sweep: the sweep runs inside the daemon — not available in this context")
	}
	return fn(ctx)
}

func init() { RegisterType(conductorDecl, newConductorImpl) }

type conductorImpl struct{}

func newConductorImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	return conductorImpl{}, nil
}

func (conductorImpl) Validate() error          { return nil }
func (conductorImpl) DeclaredEvents() []string { return nil }

// Source lowers the conductor.* triggers: the returned integration registers
// them (plus the engine's emit) in the lifecycle registry EmitLifecycle
// publishes through.
func (conductorImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	return &conductorSource{triggers: triggers}, nil
}

func (c conductorImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	o := conductorOps()
	if o == nil {
		return nil, fmt.Errorf("conductor.%s: daemon operations are not available in this context (the verb runs inside `conductor run`)", verb)
	}
	need := func(fn any) error {
		if fn == nil {
			return fmt.Errorf("conductor.%s: not wired in this daemon", verb)
		}
		return nil
	}
	switch verb {
	case "update":
		if o.Update == nil {
			return nil, need(nil)
		}
		updated, version, err := o.Update(ctx)
		if err != nil {
			return nil, err
		}
		return map[string]any{"updated": updated, "version": version}, nil
	case "pause":
		if o.Pause == nil {
			return nil, need(nil)
		}
		return map[string]any{}, o.Pause()
	case "resume":
		if o.Resume == nil {
			return nil, need(nil)
		}
		return map[string]any{}, o.Resume()
	case "restart":
		if o.Restart == nil {
			return nil, need(nil)
		}
		return map[string]any{}, o.Restart()
	case "reload":
		if o.Reload == nil {
			return nil, need(nil)
		}
		return map[string]any{}, o.Reload()
	case "run":
		if o.Run == nil {
			return nil, need(nil)
		}
		name, _ := opts["name"].(string)
		if name == "" {
			return nil, fmt.Errorf("conductor.run: name: is required")
		}
		inputs, _ := opts["inputs"].(map[string]any)
		msg, err := o.Run(ctx, name, inputs)
		if err != nil {
			return nil, err
		}
		return map[string]any{"message": msg}, nil
	}
	return nil, fmt.Errorf("conductor: no verb %q", verb)
}

// ---------------------------------------------------------------------------
// The lifecycle source. One process-global registry: the lowered source's
// Start registers (emit, triggers); EmitLifecycle fans an event out to every
// matching trigger. LOOP GUARD: an event whose originating trigger is itself
// a conductor.* run (t.Source == "conductor") is never re-fed — a
// notification workflow's own dispatch/complete/failed cannot storm.
// ---------------------------------------------------------------------------

type conductorSource struct {
	triggers []CompiledTrigger
}

func (s *conductorSource) Name() string { return "conductor" }

func (s *conductorSource) Validate() error { return nil }

func (s *conductorSource) Start(ctx context.Context, emit core.EmitFunc) error {
	lifecycleMu.Lock()
	lifecycleEmit = emit
	lifecycleTriggers = s.triggers
	lifecycleMu.Unlock()
	<-ctx.Done()
	lifecycleMu.Lock()
	lifecycleEmit = nil
	lifecycleTriggers = nil
	lifecycleMu.Unlock()
	return nil
}

var (
	lifecycleMu       sync.Mutex
	lifecycleEmit     core.EmitFunc
	lifecycleTriggers []CompiledTrigger
)

// EmitLifecycle publishes one conductor lifecycle event to every
// `on: conductor.<event>` trigger. t is the ORIGINATING trigger (its target
// becomes the event's target context); line is the composed human line
// ({{.message}}); extra adds event-specific context (version, …). Safe with
// no source running (no conductor.* triggers configured) — it's a no-op.
func EmitLifecycle(ctx context.Context, event string, t core.Trigger, line string, extra map[string]any) {
	if t.Source == "conductor" {
		return // loop guard: a lifecycle run's own lifecycle never re-feeds
	}
	lifecycleMu.Lock()
	emit := lifecycleEmit
	triggers := append([]CompiledTrigger(nil), lifecycleTriggers...)
	lifecycleMu.Unlock()
	if emit == nil {
		return
	}
	for _, ct := range triggers {
		if ct.Spec.Event() != event || !ct.Spec.IsEnabled() {
			continue
		}
		trigCtx := map[string]any{
			"message": line, "event": event,
			"ref":  fmt.Sprintf("%s#%d", t.Target.Repo, t.Target.Number),
			"repo": t.Target.Repo, "number": t.Target.Number,
			"origin_kind": t.Kind, "title": t.Title,
		}
		for k, v := range extra {
			trigCtx[k] = v
		}
		act := config.Action{Name: ct.Spec.Name, Enabled: ct.Spec.Enabled, Shadow: ct.Spec.Shadow, FlowRef: ct.Ref()}
		act = inbound.ForceNoCheckout(act)
		target := t.Target
		if target.Repo == "" {
			target = inbound.SyntheticTarget("conductor:"+event, fmt.Sprintf("%d", time.Now().UnixNano()))
		}
		emit(ctx, core.Trigger{
			Source:   "conductor",
			Instance: "conductor",
			Kind:     event,
			Variant:  ct.Spec.Name,
			Target:   target,
			Title:    line,
			Context:  trigCtx,
			Force:    true, // lifecycle notifications always fire (no dedup gate)
			Action:   act,
		})
	}
}
