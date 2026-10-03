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
func (e *Engine) credentialsFor(ctx context.Context, t core.Trigger) (dispatch.Credentials, error) {
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
	return e.declaredCredentials(ctx, t, instance, in.Decl.Semantics)
}

func (e *Engine) declaredCredentials(ctx context.Context, t core.Trigger, instance string, sem *sdk.ConnSemantics) (dispatch.Credentials, error) {
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
		v, err := e.mint(ctx, t, instance, cr)
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
func (e *Engine) mint(ctx context.Context, t core.Trigger, instance string, cr sdk.Credential) (string, error) {
	if e.invokeVerb == nil {
		return "", fmt.Errorf("no connector registry to mint through")
	}
	// rate_limited/not_ready (§1.11) are retried here, independently of any
	// step retry: policy (a credential mint has none of its own); a
	// target_gone answer is tagged dispatch.ErrTargetClosed so a run that
	// can't mint because its target is gone stops instead of failing —
	// execAgent (flow.go) returns this error straight out of a step, so the
	// existing stop-hook switch (runSteps) and ResumeWorkflows both act on
	// it with no further wiring.
	out, err := connector.RetryContract(ctx, func() (map[string]any, error) {
		return e.invokeVerb(ctx, instance, cr.Mint.Verb, core.DeclaredArgs(cr.Mint.Args, t.Facts()))
	})
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
