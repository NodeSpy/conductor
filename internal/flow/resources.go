package flow

import (
	"fmt"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// Resource allowlists for AGENT-AUTHORED workflows (#124): plans (any entry
// path — inline, live run_step, saved/promoted), never config-authored
// steps. policy.agent_authored gains allow_secrets / allow_stores /
// allow_targets, each DENY BY DEFAULT: an empty list means an agent-authored
// step may not reference that resource kind at all, so an agent can't choose
// to manage things the operator didn't intend it to manage. "*" grants all
// of one kind, `trust: full` lifts all three, and the TRIGGERING target (the
// repo the workflow fired for) is implicitly allowed — the target list only
// constrains ADDITIONAL repos the agent picks.
//
// Enforcement is two-layered: guardPlanResources rejects a plan statically
// from the literal references it can see, and the runtime belt
// (checkVerbResources in execVerb/hooks + the code-step DataGuard) refuses a
// step whose RENDERED options reach outside the lists — the templated store
// or repo name the static scan can't evaluate.

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
	secrets []string
	stores  []string
	targets []string
	// trigger is the implicitly-allowed triggering repo ("" when the trigger
	// has no repo context).
	trigger string
}

// planResourcePolicy resolves the allowlists for one trigger, or nil when
// they don't apply (trust: full).
func planResourcePolicy(pol *config.AgentAuthoredPolicy, t core.Trigger) *resourcePolicy {
	if pol == nil || pol.TrustFull() {
		return nil
	}
	return &resourcePolicy{
		secrets: pol.AllowSecrets,
		stores:  pol.AllowStores,
		targets: pol.AllowTargets,
		trigger: t.Target.Repo,
	}
}

func (rp *resourcePolicy) secretOK(name string) bool {
	return rp == nil || resourceAllowed(rp.secrets, name)
}

func (rp *resourcePolicy) storeOK(name string) bool {
	return rp == nil || resourceAllowed(rp.stores, name)
}

func (rp *resourcePolicy) targetOK(repo string) bool {
	if rp == nil || repo == "" {
		return true
	}
	if rp.trigger != "" && repo == rp.trigger {
		return true // the triggering target is always in scope
	}
	return resourceAllowed(rp.targets, repo)
}

// guardPlanResources is the STATIC half: it walks an agent-authored plan's
// steps (parallel branches, compensations, and hooks included) and rejects
// the plan when a literal reference falls outside the allowlists. Templated
// names it can't evaluate fall through to the runtime belt.
func guardPlanResources(pol *config.AgentAuthoredPolicy, t core.Trigger, steps []config.Step) error {
	rp := planResourcePolicy(pol, t)
	if rp == nil {
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
			// Store and target references from literal option values.
			if store := literalOption(step.Options, "store"); store != "" && !rp.storeOK(store) {
				return fmt.Errorf("%s: touches store %q — not in policy.agent_authored.allow_stores (agent-authored workflows may only touch listed stores; trust: full lifts this)", w, store)
			}
			if repo := literalOption(step.Options, "repo"); repo != "" && !rp.targetOK(repo) {
				return fmt.Errorf("%s: addresses %q — not the triggering target and not in policy.agent_authored.allow_targets (trust: full lifts this)", w, repo)
			}
			for hi := range step.Hooks {
				h := &step.Hooks[hi]
				hw := fmt.Sprintf("%s.hooks[%d]", w, hi)
				for _, name := range optionSecretRefs(h.Options) {
					if !rp.secretOK(name) {
						return fmt.Errorf("%s: references secret %q — not in policy.agent_authored.allow_secrets", hw, name)
					}
				}
				if store := literalOption(h.Options, "store"); store != "" && !rp.storeOK(store) {
					return fmt.Errorf("%s: touches store %q — not in policy.agent_authored.allow_stores", hw, store)
				}
				if repo := literalOption(h.Options, "repo"); repo != "" && !rp.targetOK(repo) {
					return fmt.Errorf("%s: addresses %q — not the triggering target and not in policy.agent_authored.allow_targets", hw, repo)
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

// checkVerbResources is the RUNTIME belt for verb steps: the rendered
// options carry the CONCRETE store/repo names (a template the static scan
// couldn't evaluate has resolved by now). Secrets need no runtime half here
// — handles never resolve in agent-authored steps and the plan scope carries
// no secret values.
func (r *Runner) checkVerbResources(pol *config.AgentAuthoredPolicy, t core.Trigger, uses string, rendered map[string]any) error {
	rp := planResourcePolicy(pol, t)
	if rp == nil {
		return nil
	}
	if store, _ := rendered["store"].(string); store != "" && !rp.storeOK(store) {
		return fmt.Errorf("%s: touches store %q — not in policy.agent_authored.allow_stores", uses, store)
	}
	if repo, _ := rendered["repo"].(string); repo != "" && !rp.targetOK(repo) {
		return fmt.Errorf("%s: addresses %q — not the triggering target and not in policy.agent_authored.allow_targets", uses, repo)
	}
	return nil
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
