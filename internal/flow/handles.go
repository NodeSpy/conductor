package flow

import (
	"context"

	"fmt"
	"github.com/NodeSpy/conductor/internal/core"
	"strings"

	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/vaults"
)

// Boundary resolution for {{secret "name"}} handles (#36 §12). The template
// func renders an OPAQUE handle («secret:name») everywhere; the real value
// replaces it only here — immediately before conductor's own egress (verb
// invocation, code-step env/args, remote-command env/argv) — and only when
// BOTH hold:
//
//  1. The step is config-authored: agent-authored execution (plans, live
//     run_step, saved workflows) never resolves, whatever its trust level.
//  2. The handle's name was LITERALLY called in the step's own raw template
//     source (the eligibility list). A handle that arrives through data — an
//     agent echoing its env into a step output that a config step relays —
//     is not in the list and passes through as inert text.
//
// Rendered values that are audited/logged are captured BEFORE resolution, so
// audit shows handles; the resolver's redaction still backstops the value.

// agentAuthoredKey marks execution of agent-authored steps — plans (any
// trust level), live run_step calls, and saved workflows. Distinct from
// planBarrier, which is only active for unapproved+untrusted plans.
type agentAuthoredKey struct{}

// markAgentAuthored tags a context as executing agent-authored steps, and
// stamps the TRUSTED SCOPE those steps' identities are namespaced under (see
// agentAuthoredNamespace).
func markAgentAuthored(ctx context.Context, t core.Trigger) context.Context {
	ctx = context.WithValue(ctx, agentAuthoredKey{}, true)
	return context.WithValue(ctx, agentScopeKey{}, agentAuthoredNamespace(ctx, t))
}

// agentScopeKey carries the namespace an agent-authored step's identity is
// confined to.
type agentScopeKey struct{}

// agentScopeFrom reads it back ("" outside an agent-authored execution).
func agentScopeFrom(ctx context.Context) string {
	ns, _ := ctx.Value(agentScopeKey{}).(string)
	return ns
}

// agentAuthoredNamespace is the scope an agent-authored step's identity — and
// therefore its SESSION BINDING KEY — may address, and no further.
//
// A step's identity is author-pinned by `name:` (config.IdentityFor rung 1,
// verbatim, with no scope of its own), and a session binding key is
// (runtime, model, StepSessionKey(identity, rendered key)). Those are the
// only inputs. So an agent-authored step writing
//
//	{type: agent, name: "review", session: {key: "acme/other-repo#42"}}
//
// computed exactly the key a legitimate operator step named "review" produces
// in ANOTHER repo, and the affinity registry's live-binding fast path
// delivered this agent's prompt into that repo's running session. Repo and PR
// number are public; the step-name convention comes from a shared pack. An
// agent could read and steer another tenant's agent by guessing neither.
//
// The identity of an agent-authored step is therefore prefixed with the
// dispatch's own trusted scope, so a name it chooses can only ever collide
// INSIDE its own dispatch — which is where session continuity is legitimate
// and where every party is already the same one.
//
// The namespace is the trusted repo and trigger kind. When the target is NOT
// trusted (a webhook `repo:` the sender chose — round-7 #3) the repo is the
// attacker's to pick, so it cannot be the wall: the run id is used instead,
// which confines such a plan to itself.
func agentAuthoredNamespace(ctx context.Context, t core.Trigger) string {
	if repo := t.OwnRepo(); repo != "" {
		// The TARGET, not just the repo. Two pull requests on one repo are
		// two different untrusted contributors: a namespace of repo#kind put
		// PR #42's agent-authored step and PR #99's in the same one, so a
		// name or session.key chosen in either landed on the other's live
		// session. The number is the per-dispatch discriminator the platform
		// assigns, and it travels with the repo whose trust it inherits.
		return fmt.Sprintf("agent:%s#%s#%d", repo, t.Kind, t.Target.Number)
	}
	// A reconstructed trigger (run_step) carries the DAEMON-ASSIGNED id of the
	// dispatch that launched it. That is the anchor for an untrusted target:
	// the repo is the sender's to pick, this is not. Without it every
	// run_step fell through to the literals below — Source and Instance are
	// both "live" — so two dispatches' agent-authored steps collided onto one
	// namespace, which is the round-10 #2 class reopened on this path.
	if t.DispatchID != "" {
		return "agent:dispatch:" + t.DispatchID
	}
	if h := histFrom(ctx); h != nil {
		if id := h.runHistoryID(); id != "" {
			return "agent:run:" + id
		}
	}
	// Nothing trustworthy to anchor to: confine to the SOURCE's own identity,
	// which the event's sender does not choose.
	return "agent:" + t.Source + ":" + t.Instance + ":" + t.Kind
}

// agentAuthored reports whether this execution runs agent-authored steps.
func agentAuthored(ctx context.Context) bool {
	on, _ := ctx.Value(agentAuthoredKey{}).(bool)
	return on
}

// secretCallsIn walks raw (pre-render) step values — options maps, env maps,
// arg lists — and returns the union of literal {{secret "name"}} calls in
// their template strings: the boundary-resolution eligibility list. Parse
// errors are ignored here; rendering the same string reports them.
func secretCallsIn(vs ...any) []string {
	seen := map[string]bool{}
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			names, err := templateSecretCalls(x)
			if err != nil {
				return
			}
			for _, n := range names {
				if n != "" && !seen[n] {
					seen[n] = true
					out = append(out, n)
				}
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		case map[string]string:
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case []string:
			for _, e := range x {
				walk(e)
			}
		}
	}
	for _, v := range vs {
		walk(v)
	}
	return out
}

// resolveSecretHandles swaps the eligible handles in a rendered value for
// their real secret values — the egress boundary. Under agent-authored
// execution it returns the value untouched (handles stay opaque). A named
// secret that didn't resolve at boot fails the step loudly rather than
// egressing a literal handle.
func (r *Runner) resolveSecretHandles(ctx context.Context, v any, eligible []string) (any, error) {
	if len(eligible) == 0 || agentAuthored(ctx) {
		return v, nil
	}
	var walk func(v any) (any, error)
	walk = func(v any) (any, error) {
		switch x := v.(type) {
		case string:
			return r.resolveHandleString(ctx, x, eligible)
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, e := range x {
				res, err := walk(e)
				if err != nil {
					return nil, err
				}
				out[k] = res
			}
			return out, nil
		case []any:
			out := make([]any, len(x))
			for i, e := range x {
				res, err := walk(e)
				if err != nil {
					return nil, err
				}
				out[i] = res
			}
			return out, nil
		case []string:
			out := make([]string, len(x))
			for i, e := range x {
				res, err := r.resolveHandleString(ctx, e, eligible)
				if err != nil {
					return nil, err
				}
				out[i] = res
			}
			return out, nil
		case map[string]string:
			out := make(map[string]string, len(x))
			for k, e := range x {
				res, err := r.resolveHandleString(ctx, e, eligible)
				if err != nil {
					return nil, err
				}
				out[k] = res
			}
			return out, nil
		}
		return v, nil
	}
	return walk(v)
}

// resolveHandleString replaces each eligible handle occurrence in one string.
func (r *Runner) resolveHandleString(ctx context.Context, s string, eligible []string) (string, error) {
	for _, name := range eligible {
		h := secrets.Handle(name)
		if !strings.Contains(s, h) {
			continue
		}
		val, err := r.secretHandleValue(ctx, name)
		if err != nil {
			return "", err
		}
		s = strings.ReplaceAll(s, h, val)
	}
	return s, nil
}

// secretHandleValue resolves one handle name to its value. The current model
// names a vault entry — "<vault>/<key>", read through the vaults registry
// (which taints the value for redaction) — with a bare name still resolving
// against the retired named-secrets block for back-compat.
func (r *Runner) secretHandleValue(ctx context.Context, name string) (string, error) {
	if vault, key, ok := strings.Cut(name, "/"); ok {
		val, err := vaults.Read(ctx, vault, key)
		if err != nil {
			return "", fmt.Errorf("secret %q: %w", name, err)
		}
		if val == "" {
			return "", fmt.Errorf("secret %q resolved empty — see `conductor secrets check`", name)
		}
		return val, nil
	}
	val, ok := r.SecretVals[name]
	if !ok || val == "" {
		return "", fmt.Errorf("secret %q is not configured (or did not resolve) — name a vault entry as \"<vault>/<key>\"", name)
	}
	return val, nil
}

// resolveHandleStringMap is resolveSecretHandles for a rendered env map.
func (r *Runner) resolveHandleStringMap(ctx context.Context, m map[string]string, eligible []string) (map[string]string, error) {
	v, err := r.resolveSecretHandles(ctx, m, eligible)
	if err != nil {
		return nil, err
	}
	return v.(map[string]string), nil
}

// resolveHandleStrings is resolveSecretHandles for rendered args/argv.
func (r *Runner) resolveHandleStrings(ctx context.Context, ss []string, eligible []string) ([]string, error) {
	v, err := r.resolveSecretHandles(ctx, ss, eligible)
	if err != nil {
		return nil, err
	}
	return v.([]string), nil
}
