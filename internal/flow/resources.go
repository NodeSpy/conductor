package flow

import (
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// Resource allowlists for AGENT-AUTHORED workflows (#124), generalized to
// connector-declared dimensions (docs/design/skill-verb-scope.md): plans (any
// entry path — inline, live run_step, saved/promoted) and skill grants, never
// config-authored steps.
//
// policy.agent_authored carries allow_scopes (dimension → allowed values),
// with allow_secrets / allow_stores / allow_targets kept as the legacy
// spellings of the secret / store / repo dimensions. DENY BY DEFAULT: an
// empty list means an agent-authored step may not reference that dimension at
// all, so an agent can't choose to manage things the operator didn't intend
// it to manage. "*" grants a whole dimension, `trust: full` lifts them all,
// and whatever the DISPATCH itself points at — the repo it fired for, the
// channel its event came from, the connector's configured default — is
// implicitly in scope (connector.Instance.ContextScope). The lists only
// constrain the ADDITIONAL resources an agent picks.
//
// WHICH options carry a resource is the connector's own declaration
// (Field.Scope), never a name written here: scopeOK takes a dimension, and
// checkVerbResources walks the called verb's scoped options to find them. A
// connector that tags a new option is enforced on both surfaces the day it
// ships, which is what the meta-test pins.
//
// Enforcement is two-layered: guardPlanResources rejects a plan statically
// from the literal references it can see, and the runtime belt
// (checkVerbResources in execVerb/hooks/RunSkillVerb + the code-step
// DataGuard) refuses a step whose RENDERED options reach outside the lists —
// the templated store or repo name the static scan can't evaluate.

// resourceAllowed reports whether one name matches an allowlist: exact,
// path.Match glob ("house/*", "org/*"), or the whole-kind wildcard "*"
// (spelled out because path.Match's * never crosses a "/").
func resourceAllowed(patterns []string, name string) bool {
	if name == "" {
		return true
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "*" || p == name {
			return true
		}
		if matchAny([]string{p}, name) {
			return true
		}
	}
	return false
}

// resourcePolicy is the resolved allowlist set for one plan execution. nil
// (trust: full, or no policy — guardPlan already rejects that) = unrestricted.
type resourcePolicy struct {
	// allow is the per-dimension allowlist (allow_scopes plus the legacy
	// allow_targets/allow_stores/allow_secrets aliases).
	allow  map[string][]string
	scopes []string // memory scopes (allow_memory_scopes)
	// trigger is the implicitly-allowed triggering repo ("" when the trigger
	// has no repo context). Memory scopes derive from it.
	trigger string
	// t is the dispatch the policy is being applied to — what a connector's
	// ContextScope hook maps to its implicitly-allowed value per dimension.
	t core.Trigger
	// render is the data a TEMPLATED allowlist entry renders against: this
	// dispatch's trusted facts, secret-free, with no agent-supplied value in
	// it. Built by scopeRenderData; see expand().
	render map[string]any
}

// planResourcePolicy resolves the allowlists for one PLAN, or nil when they
// don't apply: `trust: full` (the deliberate lift), or no policy at all —
// which is moot on this surface, because guardPlan refuses an agent-authored
// plan outright without a policy.agent_authored block, so there is no
// unguarded plan for a nil to wave through. TestAgentAuthoredPlansNeedAPolicy
// pins that, since this nil depends on it.
func planResourcePolicy(pol *config.AgentAuthoredPolicy, t core.Trigger) *resourcePolicy {
	if pol == nil || pol.TrustFull() {
		return nil
	}
	return &resourcePolicy{
		allow:   pol.ScopeAllow(),
		scopes:  pol.AllowMemoryScopes,
		trigger: t.Target.Repo,
		t:       t,
	}
}

// skillResourcePolicy is the SKILL surface's equivalent, and it is never nil.
//
// The difference is not an oversight, it is the whole point: on the skill
// surface the scoping is INTRINSIC TO THE GRANT, not a plan-policy feature.
// `skill.verbs: {slack.post: {channel: ["#x"]}}` is a sentence the operator
// wrote about this agent; it means the same thing whether or not the config
// also has a policy.agent_authored block, which governs a different surface
// entirely (agent-authored plans). Resolving to nil here — as the plan
// surface legitimately does — silently turned every per-verb constraint and
// the deny-by-default itself into a no-op for any config without that block.
//
// So: the walk ALWAYS runs. A policy, when present, only ever WIDENS it
// through allow_scopes.
//
// `trust: full` is read the same way. It is a plan-latitude knob — "let the
// agent's own plans reach further" — and it does NOT lift a constraint the
// operator wrote onto a named verb, nor the dispatch-context default. An
// operator who wants a dimension open on the skill surface says so where the
// surface is configured: `{channel: ["*"]}` on the grant, or allow_scopes
// (which still widens under trust: full, so the escape hatch stays one line).
func skillResourcePolicy(pol *config.AgentAuthoredPolicy, t core.Trigger) *resourcePolicy {
	rp := &resourcePolicy{trigger: t.Target.Repo, t: t}
	if pol != nil {
		rp.allow = pol.ScopeAllow()
		rp.scopes = pol.AllowMemoryScopes
	}
	return rp
}

// scopeOK is THE resource question, asked once for every dimension: may this
// dispatch name this value in this dimension of this connector?
//
// Allowed = the connector's ContextScope for the dimension (the dispatch's own
// repo / channel / configured default) ∪ the operator's allow list for it ∪
// `extra` (the calling verb's own skill grant, which widens but never narrows).
// Everything else is refused — including a dimension with no context value and
// no list, which is the deliberate strong default: a fixed-channel post from a
// non-slack trigger has to say which channel.
//
// An empty value means the option wasn't supplied, which names no resource.
func (rp *resourcePolicy) scopeOK(in *connector.Instance, dim, value string, extra []string) bool {
	if rp == nil {
		return true
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if ctx := in.ContextScope(dim, rp.t); ctx != "" && ctx == value {
		return true
	}
	return scopeListed(in, dim, value, rp.expand(rp.allow[dim])) ||
		scopeListed(in, dim, value, rp.expand(extra))
}

// expand renders the `{{ }}` entries of one allowlist against THIS DISPATCH's
// facts — the per-event counterpart to the load-time `${settings.X}`
// substitution (docs/design/scope-templating.md). `channel: ["#pr-{{.number}}"]`
// is one line that means a different channel per PR.
//
// Three rules hold it inside the trust boundary:
//
//   - Entries WITHOUT "{{" are returned untouched, so nothing an existing
//     config does changes and the common path renders nothing at all.
//   - The render data is rp.render — the dispatch's own trusted facts, with
//     no secrets and no agent input in it (see scopeRenderData). The
//     agent-supplied value being checked is never a render source; it is only
//     ever the thing matched.
//   - FAIL-CLOSED: a template that errors, or renders to empty, becomes ""
//     and is dropped from the list rather than matched. A pattern that can't
//     be evaluated must deny, never admit — and an empty pattern that reached
//     the matcher would be a near-miss away from matching anyway.
func (rp *resourcePolicy) expand(allow []string) []string {
	if len(allow) == 0 {
		return allow
	}
	templated := false
	for _, p := range allow {
		if strings.Contains(p, "{{") {
			templated = true
			break
		}
	}
	if !templated {
		return allow // fast path: no rendering, byte-identical behavior
	}
	out := make([]string, 0, len(allow))
	for _, p := range allow {
		if !strings.Contains(p, "{{") {
			out = append(out, p)
			continue
		}
		rendered, err := renderScopePattern(p, rp.render)
		if err != nil || strings.TrimSpace(rendered) == "" {
			continue // fail closed: this entry matches nothing
		}
		out = append(out, rendered)
	}
	return out
}

// scopeListed reports whether an allowlist names this value in this dimension
// OF THIS CONNECTOR.
//
// For most dimensions that is a plain match. The SECRET dimension is the one
// that has to think about who is asking, because its allowlist is flat while
// its entries are vault-qualified: `allow_scopes.secret: ["house/prod-token"]`
// means the prod-token in the vault named `house`, and nothing else.
//
// Matching the bare key against that list let a DIFFERENT vault claim the
// entry — `shared.read {key: "house/prod-token"}` read "house/prod-token" out
// of `shared` because the spelling matched. The vault an entry belongs to is
// half of its identity; dropping that half made one grant authorize two
// secrets (round-4 F1).
//
// So the secret dimension is matched CONNECTOR-BOUND:
//
//	a QUALIFIED entry ("house/*", "house/prod-token") is matched against the
//	  calling vault's own qualified spelling of the key, so it can only ever
//	  authorize the vault it names — from `shared` the candidate is
//	  "shared/house/prod-token", which "house/prod-token" does not match;
//	a BARE entry ("prod-token", "*") is a key within whichever vault is
//	  calling, and is matched against the key alone.
func scopeListed(in *connector.Instance, dim, value string, allow []string) bool {
	if dim != config.DimSecret || in == nil || in.Name == "" {
		return resourceAllowed(allow, value)
	}
	qualified := in.Name + "/" + value
	for _, p := range allow {
		p = strings.TrimSpace(p)
		target := value
		if strings.Contains(p, "/") {
			target = qualified
		}
		if resourceAllowed([]string{p}, target) {
			return true
		}
	}
	return false
}

func (rp *resourcePolicy) secretOK(name string) bool {
	return rp == nil || resourceAllowed(rp.allow[config.DimSecret], name)
}

// storeOK is the CODE-step question (ctx.store(name) inside a run: js step):
// a store touch with no verb and no connector behind it, so it asks the store
// dimension straight rather than through a verb's option schema.
func (rp *resourcePolicy) storeOK(name string) bool {
	return rp == nil || resourceAllowed(rp.allow[config.DimStore], name)
}

// memoryScopeOK reports whether an agent-authored step may touch a memory
// scope. Deny-by-default, with the triggering repo's own scope implicitly
// allowed — the same shape as targetOK, because it is the same question about
// a different resource.
//
// An UNSCOPED op ("") is refused whenever a policy applies: a recall that
// names no scope would otherwise read every scope on the daemon, which is the
// cross-tenant read this closes. The caller names the scope it means.
func (rp *resourcePolicy) memoryScopeOK(scope string) bool {
	if rp == nil {
		return true
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return false
	}
	if rp.trigger != "" && scope == memoryScopeFor(rp.trigger) {
		return true // the triggering repo's own scope is always in scope
	}
	return resourceAllowed(rp.scopes, scope)
}

// memoryScopeFor is the scope a repo's memories live under.
func memoryScopeFor(repo string) string { return "repo:" + repo }

// scopeDenial is the refusal one out-of-scope option value earns, phrased for
// the operator who has to widen it. The legacy key name is named alongside the
// general one for the three dimensions that have one, so an existing config's
// error still points at the line it would edit.
func scopeDenial(where, uses, opt, dim, value string) error {
	key := "policy.agent_authored.allow_scopes." + dim
	switch dim {
	case config.DimRepo:
		key += " (legacy: allow_targets)"
	case config.DimStore:
		key += " (legacy: allow_stores)"
	case config.DimSecret:
		key += " (legacy: allow_secrets)"
	}
	return fmt.Errorf("%s: %s names %s %q — not this dispatch's own %s and not in %s (trust: full lifts this)",
		where, uses, opt, value, dim, key)
}

// verbScopedOptions resolves the scope-tagged options of the verb a step
// calls: (connector instance, option→dimension). ok is false when the verb
// can't be resolved — an unknown connector or verb, which the caller decides
// how to treat (the static scan skips it; the runtime belt refuses).
func verbScopedOptions(reg *connector.Registry, uses string) (*connector.Instance, []connector.ScopedOption, bool) {
	connName, verb, cut := strings.Cut(strings.TrimSpace(uses), ".")
	if !cut || reg == nil {
		return nil, nil, false
	}
	in, ok := reg.Get(connName)
	if !ok || in.Decl == nil {
		return nil, nil, false
	}
	vd, ok := in.Decl.Verb(verb)
	if !ok {
		return in, nil, false
	}
	return in, vd.ScopedOptions(), true
}

// guardPlanResources is the STATIC half: it walks an agent-authored plan's
// steps (parallel branches, compensations, and hooks included) and rejects
// the plan when a literal reference falls outside the allowlists. Templated
// names it can't evaluate fall through to the runtime belt.
func (r *Runner) guardPlanResources(pol *config.AgentAuthoredPolicy, t core.Trigger, steps []config.Step) error {
	reg := r.Conns
	rp := planResourcePolicy(pol, t)
	if rp == nil {
		return nil
	}
	rp.render = r.scopeRenderData(t, nil)
	// scopedLiterals judges the literal values of one options map against the
	// called verb's DECLARED scope options. A verb it cannot resolve is left
	// to the runtime belt, which refuses rather than guesses.
	scopedLiterals := func(where, uses string, opts map[string]any) error {
		in, scoped, ok := verbScopedOptions(reg, uses)
		if !ok {
			return nil
		}
		for _, so := range scoped {
			val := literalOption(opts, so.Name)
			if val == "" || rp.scopeOK(in, so.Dim, val, nil) {
				continue
			}
			return scopeDenial(where, uses, so.Name, so.Dim, val)
		}
		return nil
	}
	var walk func(where string, list []config.Step) error
	walk = func(where string, list []config.Step) error {
		for i := range list {
			step := &list[i]
			w := fmt.Sprintf("%s[%d]", where, i)
			if step.ID != "" {
				w = fmt.Sprintf("%s(%s)", w, step.ID)
			}
			// Secret references: {{secret "…"}} handles, {{ vault … }} calls,
			// and the {{.secrets.x}} / {{.vaults.v.k}} field spellings —
			// anywhere in the step's authored strings.
			for _, name := range stepSecretRefs(step) {
				if !rp.secretOK(name) {
					return fmt.Errorf("%s: references secret %q — not in policy.agent_authored.allow_secrets (agent-authored workflows may only touch listed secrets; trust: full lifts this)", w, name)
				}
			}
			// Resource references from literal option values, by the called
			// verb's own scope declaration.
			if err := scopedLiterals(w, step.Uses, step.Options); err != nil {
				return err
			}
			for hi := range step.Hooks {
				h := &step.Hooks[hi]
				hw := fmt.Sprintf("%s.hooks[%d]", w, hi)
				for _, name := range optionSecretRefs(h.Options) {
					if !rp.secretOK(name) {
						return fmt.Errorf("%s: references secret %q — not in policy.agent_authored.allow_secrets", hw, name)
					}
				}
				if err := scopedLiterals(hw, h.Uses, h.Options); err != nil {
					return err
				}
			}
			if step.Parallel != nil {
				for bi, branch := range step.Parallel.Branches {
					if err := walk(fmt.Sprintf("%s.parallel[%d]", w, bi), branch); err != nil {
						return err
					}
				}
			}
			if step.Compensate != nil {
				if err := walk(w+".compensate", []config.Step{*step.Compensate}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk("plan", steps)
}

// checkVerbScopes is THE CHOKEPOINT — the one walk both agent-facing surfaces
// run a verb call through: the plan surface (execVerb/hooks, where the
// rendered options carry the CONCRETE names a template the static scan
// couldn't evaluate has resolved into) and the skill surface (RunSkillVerb,
// where the agent supplied the options literally). Neither names an option:
// the walk comes from the CALLED verb's connector-declared Scope tags, so the
// two surfaces cannot disagree about what a grant means, and a connector that
// tags a new option is enforced on both at once.
//
// The two surfaces differ only in WHICH resourcePolicy they hand it — see
// planResourcePolicy (nil when it doesn't apply) and skillResourcePolicy
// (never nil, because the grant's own scoping is not the plan policy's to
// switch off). Everything downstream of that is shared.
//
// grant is the calling skill grant's per-option allowlist (skill.verbs map
// form), or nil on the plan surface. It only ever widens.
//
// Secrets need no runtime half for the TEMPLATE spellings — handles never
// resolve in agent-authored steps and the plan scope carries no secret values
// — but a vault verb's key: option is an ordinary scoped option and is walked
// here like any other.
func (r *Runner) checkVerbScopes(rp *resourcePolicy, uses string, opts map[string]any, grant map[string][]string) error {
	if rp == nil {
		return nil
	}
	in, scoped, ok := verbScopedOptions(r.Conns, uses)
	if !ok {
		// Scoping applies and the verb's declaration is out of reach (no
		// registry, unknown connector/verb), so its scoped options can't be
		// known. Refuse: this is the belt, and a belt that can't see must
		// not wave the call through.
		return fmt.Errorf("%s: cannot resolve the verb's option schema to scope-check it — refusing", uses)
	}
	for _, so := range scoped {
		val, _ := opts[so.Name].(string)
		if rp.scopeOK(in, so.Dim, val, grant[so.Name]) {
			continue
		}
		return scopeDenial(uses, uses, so.Name, so.Dim, strings.TrimSpace(val))
	}
	return nil
}

// checkVerbResources is the PLAN surface's entry into the shared walk: the
// runtime belt for an agent-authored step's rendered options. stepData is the
// step's own template scope, used ONLY to carry the workflow's inputs into a
// templated allowlist entry (see scopeRenderData) — never to render the value
// being checked.
func (r *Runner) checkVerbResources(pol *config.AgentAuthoredPolicy, t core.Trigger, uses string, rendered map[string]any, grant map[string][]string, stepData map[string]any) error {
	rp := planResourcePolicy(pol, t)
	if rp != nil {
		rp.render = r.scopeRenderData(t, stepData)
	}
	return r.checkVerbScopes(rp, uses, rendered, grant)
}

// checkSkillVerbResources is the SKILL surface's entry into the same walk. It
// exists as its own entry point for exactly one reason: the policy it builds
// is never nil, so a config with no policy.agent_authored block — or one with
// trust: full — still gets the grant's per-verb constraints and the
// deny-by-default. See skillResourcePolicy.
func (r *Runner) checkSkillVerbResources(pol *config.AgentAuthoredPolicy, t core.Trigger, uses string, options map[string]any, grant map[string][]string) error {
	rp := skillResourcePolicy(pol, t)
	// The skill surface has no step scope: its facts are the dispatch the
	// token was minted for, which RunSkillVerb has already rebuilt into t
	// (identity + captured trigger context). The agent's own options are NOT
	// passed — they are what is being checked.
	rp.render = r.scopeRenderData(t, nil)
	return r.checkVerbScopes(rp, uses, options, grant)
}

// stepSecretRefs extracts every literal secret reference in a step's
// authored strings.
func stepSecretRefs(step *config.Step) []string {
	var vals []any
	vals = append(vals, step.Prompt, step.If, step.Options, step.Env, step.Args, step.Code, step.With)
	return secretRefsIn(vals...)
}

// optionSecretRefs extracts secret references from one options map.
func optionSecretRefs(opts map[string]any) []string {
	if len(opts) == 0 {
		return nil
	}
	return secretRefsIn(opts)
}

// secretRefsIn walks raw values collecting every secret spelling:
// {{secret "name"}}, {{ vault "v" "k" }} → "v/k", {{.secrets.x}} → "x", and
// {{.vaults.v.k}} → "v/k".
func secretRefsIn(vs ...any) []string {
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	var walkString func(s string)
	walkString = func(s string) {
		if !strings.Contains(s, "{{") {
			return
		}
		if names, err := templateSecretCalls(s); err == nil {
			for _, n := range names {
				add(n)
			}
		}
		if calls, err := templateVaultCalls(s); err == nil {
			for _, c := range calls {
				if c[0] != "" {
					add(c[0] + "/" + c[1])
				}
			}
		}
		if refs, err := templateRefs(s); err == nil {
			for _, ref := range refs {
				parts := strings.Split(ref, ".")
				switch {
				case parts[0] == "secrets" && len(parts) >= 2:
					add(parts[1])
				case parts[0] == "vaults" && len(parts) >= 3:
					add(parts[1] + "/" + parts[2])
				}
			}
		}
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			walkString(x)
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

// literalOption returns a NON-TEMPLATED string option value ("" otherwise) —
// what the static scan can judge; templated values resolve at the runtime
// belt.
func literalOption(opts map[string]any, key string) string {
	if opts == nil {
		return ""
	}
	s, _ := opts[key].(string)
	if strings.Contains(s, "{{") {
		return ""
	}
	return s
}
