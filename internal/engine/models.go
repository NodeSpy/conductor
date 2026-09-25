package engine

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/decider"
	"github.com/NodeSpy/conductor/internal/models"
	"github.com/NodeSpy/conductor/internal/systemone"
)

// The engine's side of model selection (docs/design/runtimes-models-packs.md
// §2.3). The resolver owns the ladder and the discovered rosters; the engine
// only supplies the step and turns a Decision into the string dispatch passes
// as --model.

// SetModelResolver installs the resolver (nil disables model selection:
// every dispatch then BARE LAUNCHES, which is the correct behavior for a
// daemon with no discovery wired — the runtime uses its own default).
func (e *Engine) SetModelResolver(r *models.Resolver) {
	e.modelResolver = r
	e.wireDeciders()
}

// SetDeciders installs the decision runtimes. The resolver (when set) learns
// which runtimes are decision-only and which protocols they speak, so agent
// resolution skips them and decide: resolution ranks them first.
// Either order of SetModelResolver/SetDeciders wires the same result.
func (e *Engine) SetDeciders(s decider.Set) {
	e.deciders = s
	e.wireDeciders()
}

func (e *Engine) wireDeciders() {
	if e.modelResolver != nil && len(e.deciders) > 0 {
		e.modelResolver.DecisionOnly = e.deciders.DecisionOnly
		e.modelResolver.NativeProtocols = e.deciders.NativeProtocols
	}
}

// decideCandidates ranks the candidates for a decide: step. With no resolver
// wired, the step's own resolution is the one (agent) candidate.
func (e *Engine) decideCandidates(ctx context.Context, spec config.ModelSpec, runtimeHint, protocol string) ([]models.Candidate, error) {
	if e.modelResolver == nil {
		model, rt, provider := e.resolveModel(ctx, config.Step{Model: spec, Runtime: runtimeHint})
		if rt == "" {
			rt = runtimeHint
		}
		return []models.Candidate{{Model: model, Runtime: rt, Provider: provider, Bare: model == ""}}, nil
	}
	return e.modelResolver.Candidates(ctx, spec, runtimeHint, protocol)
}

// decide asks a native decision runtime.
func (e *Engine) decide(ctx context.Context, runtime, protocol string, req systemone.Request) (decider.Answer, error) {
	rt, ok := e.deciders.Get(runtime)
	if !ok {
		return decider.Answer{}, fmt.Errorf("runtime %q is not a decision runtime", runtime)
	}
	return rt.Decide(ctx, protocol, req)
}

// SetModelFallback installs the unsupported-model cache dispatchAgent marks
// into on a provider refusal (nil disables the fleet fallback).
func (e *Engine) SetModelFallback(c *models.UnsupportedCache) { e.unsupported = c }

// resolveModel picks the model for one step. "" is a bare launch.
//
// A resolution ERROR — an unsatisfiable `required: true` fleet — is
// deliberately NOT fatal here: it is logged and audited, and the dispatch
// bare-launches. The hard-error surface for a required fleet is
// `conductor validate` and boot-time CheckRequired, where an operator is
// present; turning a transient discovery failure into a dropped trigger at
// dispatch time would make the fleet less reliable than no fleets at all.
// It returns the runtime alongside the model. A fleet can span runtimes —
// "the best of these models, wherever it lives" — so the resolver's choice
// of model and its choice of runtime are one decision. Returning only the
// model meant a step with no pinned `runtime:` dispatched the chosen model
// on the DEFAULT runtime, which may not offer it at all.
//
// The runtime is "" when the step pinned one (the caller's stays
// authoritative) or when there was nothing to resolve.
//
// provider is the resolved model's catalog provider (models.Decision.
// Provider) — "" for a bare launch or a pass-through pin no roster
// confirmed. It travels alongside model regardless of whether the runtime
// echo is suppressed: dispatch needs it (`paseo run --provider`) even when
// the step already pinned its own runtime.
func (e *Engine) resolveModel(ctx context.Context, step config.Step) (model, runtime, provider string) {
	if e.modelResolver == nil {
		// No model layer wired (a bare daemon, or a test). An EXACT PIN is
		// still an operator instruction, not a preference — honor it rather
		// than silently bare-launching something the config named. Anything
		// needing a roster (a fleet, a wildcard) has nothing to resolve
		// against and bare-launches. No roster means no confirmed provider
		// either.
		return exactPin(step.Model), "", ""
	}
	d, err := e.modelResolver.Resolve(ctx, step.Model, step.Runtime)
	if err != nil {
		e.log("model resolution for step %q: %v — dispatching bare", step.Name, err)
		e.store.Audit(map[string]any{"event": "model_unresolved",
			"step": step.Name, "runtime": step.Runtime, "error": err.Error()})
		return "", "", ""
	}
	if d.Notice != "" {
		e.log("model: %s", d.Notice)
	}
	// A step that pinned a runtime keeps it — the resolver was asked to
	// choose WITHIN that runtime, so echoing it back would be noise.
	if step.Runtime != "" {
		return d.Model, "", d.Provider
	}
	return d.Model, d.Runtime, d.Provider
}

// stepByIdentity finds the configured step carrying an identity, plus the
// model it resolves to. It backs the follow-up paths (gate revise, the
// supervise loop), which know only the identity string the dispatch carried.
//
// ok=false means no configured step owns that identity any more — the config
// changed under a live run — and the caller falls back to a non-session path
// rather than guessing.
func (e *Engine) stepByIdentity(ctx context.Context, identity string) (config.Step, string, bool) {
	if e.cfg == nil || identity == "" {
		return config.Step{}, "", false
	}
	var found config.Step
	ok := false
	e.cfg.WalkSteps(func(scope config.IdentityScope, slot int, s *config.Step) {
		if ok || s.Identity(scope, slot) != identity {
			return
		}
		found, ok = *s, true
	})
	if !ok {
		return config.Step{}, "", false
	}
	m, _, _ := e.resolveModel(ctx, found)
	return found, m, true
}

// exactPin returns the single literal model a spec names, if that is all it
// names. A fleet reference, a wildcard, or a multi-entry list returns "".
func exactPin(spec config.ModelSpec) string {
	acceptable := spec.Acceptable()
	if len(acceptable) != 1 || config.IsModelPattern(acceptable[0]) {
		return ""
	}
	return acceptable[0]
}
