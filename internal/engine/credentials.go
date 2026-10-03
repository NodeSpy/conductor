package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// credentialsFor is what work dispatched for t receives: the credentials the
// connector that emitted t DECLARES (plugin-contract.md §2.4) — each minted
// through the connector's own host-only verb for a target the platform
// assigned, else taken from the event's fact of the same name (a source may
// stamp one) — with their environment variables, template keys and prompt
// guidance. Minting happens at every dispatch, resume and retry included, so
// a resumed run never carries a stale credential (secret facts are never
// persisted).
//
// A conductor.* lifecycle event about another trigger's target carries that
// trigger's connector instance (origin_instance): its work gets that
// connector's credentials, minted for the same target. Work on an instance
// the registry does not know gets none.
//
// An error means a declared credential could not be minted (and no fact the
// source stamped stands in): the work must not run without it — a resumed
// run stays pending, a dispatch is not made, a flow step fails (its retry
// policy applies).
//
// credentialsFor is the SHARED-LOOP mode (process(), ResumeWorkflows' legacy
// path): mint never blocks here (see mint's doc). Per-run-goroutine callers
// (flow steps, flow resume) use credentialsForFlow instead, which retries a
// rate_limited/not_ready mint through connector.RetryContract because
// blocking THEIR goroutine does not stall any other queued trigger.
func (e *Engine) credentialsFor(ctx context.Context, t core.Trigger) (dispatch.Credentials, error) {
	return e.credentialsForMode(ctx, t, false)
}

// credentialsForFlow is credentialsFor for a flow step or flow resume: both
// run in their own per-run goroutine (internal/flow/flow.go, engine/flow.go
// resumeFlowRun), never on the engine's single shared dispatch loop, so a
// rate_limited/not_ready mint answer is retried (bounded by RetryContract's
// own budget) instead of failing the step immediately. Without this, a step
// whose mint answers rate_limited once would fail outright: noStepRetry
// (internal/flow/contract.go) excludes rate_limited/not_ready from the
// step's own retry:, and processFlow has already consumed the trigger's
// dedup, so a redelivery would be silently dropped.
func (e *Engine) credentialsForFlow(ctx context.Context, t core.Trigger) (dispatch.Credentials, error) {
	return e.credentialsForMode(ctx, t, true)
}

func (e *Engine) credentialsForMode(ctx context.Context, t core.Trigger, retry bool) (dispatch.Credentials, error) {
	if e.connectors == nil {
		return dispatch.Credentials{}, nil
	}
	instance := t.Instance
	if t.Source == "conductor" {
		if o, _ := t.Context["origin_instance"].(string); o != "" {
			instance = o
		}
	}
	in, ok := e.connectors.Get(instance)
	if !ok {
		return dispatch.Credentials{}, nil
	}
	return e.declaredCredentials(ctx, t, instance, in.Decl.Semantics, retry)
}

func (e *Engine) declaredCredentials(ctx context.Context, t core.Trigger, instance string, sem *sdk.ConnSemantics, retry bool) (dispatch.Credentials, error) {
	var c dispatch.Credentials
	var failed []error
	// Work for a target the platform did not assign — one the event's sender
	// chose — gets no credential in any form: not minted, and not taken from
	// the event's own facts either (a source may stamp one, but an attacker-
	// chosen target must never carry it).
	if sem == nil || !t.TargetTrusted {
		return c, nil
	}
	for _, cr := range sem.Credentials {
		v, err := e.mint(ctx, t, instance, cr, retry)
		if err != nil {
			e.log("%s credential %s: %v", tag(t), cr.Name, err)
		}
		val := v
		if val == "" && cr.Template != "" {
			val, _ = t.Context[cr.Template].(string)
		}
		if val == "" {
			if err != nil {
				failed = append(failed, fmt.Errorf("credential %s: %w", cr.Name, err))
			}
			continue
		}
		if c.Env == nil {
			c.Env, c.Templates = map[string]string{}, map[string]string{}
		}
		for _, k := range cr.Env {
			c.Env[k] = val
		}
		if cr.Template != "" {
			c.Templates[cr.Template] = val
		}
		c.Guidance += cr.Guidance
	}
	return c, errors.Join(failed...)
}

// mint resolves one declared credential through its mint verb.
//
// retry=false (credentialsFor, the SHARED-LOOP mode) never retries
// rate_limited/not_ready itself (unlike most invoke call sites, which go
// through connector.RetryContract): mint is called synchronously from
// process(), fed by Run's single `for t := <-e.ch` loop, and from
// ResumeWorkflows' sequential loop — a blocking sleep here would stall every
// OTHER queued trigger behind one slow connector. The caller decides what a
// rate_limited/not_ready answer means: process() defers a re-emit of the
// trigger (deferAndReemit) instead of dispatching now; ResumeWorkflows
// schedules its own bounded per-run re-check (resumeRecheck) instead of
// leaving the run for the next daemon start.
//
// retry=true (credentialsForFlow, the PER-RUN-GOROUTINE mode) retries a
// rate_limited/not_ready answer through connector.RetryContract, bounded by
// its own wait budget and attempt cap: blocking here only delays this one
// run's own goroutine, not any other queued trigger, so there is no reason
// to give up on the first rate_limited answer the way the shared-loop mode
// must.
//
// A target_gone answer is tagged dispatch.ErrTargetClosed so a run that
// can't mint because its target is gone stops instead of failing —
// execAgent (flow.go) returns this error straight out of a step, so the
// existing stop-hook switch (runSteps) and ResumeWorkflows both act on it
// with no further wiring.
func (e *Engine) mint(ctx context.Context, t core.Trigger, instance string, cr sdk.Credential, retry bool) (string, error) {
	if e.invokeVerb == nil {
		return "", fmt.Errorf("no connector registry to mint through")
	}
	invoke := func() (map[string]any, error) {
		return e.invokeVerb(ctx, instance, cr.Mint.Verb, core.DeclaredArgs(cr.Mint.Args, t.Facts()))
	}
	var out map[string]any
	var err error
	if retry {
		out, err = connector.RetryContract(ctx, invoke)
	} else {
		out, err = invoke()
	}
	if err != nil {
		return "", stopAsTargetGone(err)
	}
	key := cr.Value
	if key == "" {
		key = "token"
	}
	s, _ := out[key].(string)
	if s == "" {
		return "", fmt.Errorf("%s returned no %q", cr.Mint.Verb, key)
	}
	return s, nil
}
