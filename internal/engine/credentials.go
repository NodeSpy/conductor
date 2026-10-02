package engine

import (
	"context"
	"fmt"

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
func (e *Engine) credentialsFor(ctx context.Context, t core.Trigger) dispatch.Credentials {
	if e.connectors == nil {
		return dispatch.Credentials{}
	}
	instance := t.Instance
	if t.Source == "conductor" {
		if o, _ := t.Context["origin_instance"].(string); o != "" {
			instance = o
		}
	}
	in, ok := e.connectors.Get(instance)
	if !ok {
		return dispatch.Credentials{}
	}
	return e.declaredCredentials(ctx, t, instance, in.Decl.Semantics)
}

func (e *Engine) declaredCredentials(ctx context.Context, t core.Trigger, instance string, sem *sdk.ConnSemantics) dispatch.Credentials {
	var c dispatch.Credentials
	if sem == nil {
		return c
	}
	for _, cr := range sem.Credentials {
		val := ""
		if t.TargetTrusted {
			v, err := e.mint(ctx, t, instance, cr)
			if err != nil {
				e.log("%s credential %s: %v", tag(t), cr.Name, err)
			}
			val = v
		}
		if val == "" && cr.Template != "" {
			val, _ = t.Context[cr.Template].(string)
		}
		if val == "" {
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
	return c
}

// mint resolves one declared credential through its mint verb.
func (e *Engine) mint(ctx context.Context, t core.Trigger, instance string, cr sdk.Credential) (string, error) {
	if e.invokeVerb == nil {
		return "", fmt.Errorf("no connector registry to mint through")
	}
	out, err := e.invokeVerb(ctx, instance, cr.Mint.Verb, core.DeclaredArgs(cr.Mint.Args, t.Facts()))
	if err != nil {
		return "", err
	}
	key := cr.Value
	if key == "" {
		key = "token"
	}
	s, _ := out[key].(string)
	return s, nil
}
