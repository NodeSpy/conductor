package config

import "strings"

// refRewriter namespaces a pack's internal names and rebinds its environment
// references while instantiating it under an instance namespace.
//
// Namespacing (§3): everything a pack defines is auto-scoped under the instance
// name — agent `handoff` -> `<ns>/handoff`, workflow `review-flow` ->
// `<ns>/review-flow`. Refs inside the pack are written BARE and resolve
// pack-local; the loader scopes them so the pack author writes no prefixes.
//
// The one boundary that reaches global names is `requires:` (§3): a required
// connector/store/secret/handoff, or an agent role BOUND to a consumer global,
// resolves in the consumer namespace, not prefixed.
type refRewriter struct {
	ns string
	// fleets are the pack's OWN `models:` names, so a step's `model:` that
	// refers to one can be namespaced with it. Anything else — an exact
	// model id, a wildcard, a consumer fleet — is left alone.
	fleets map[string]bool
	// env rebinds required environment names to consumer globals.
	env envBindings
}

func newRefRewriter(ns string, man *PackManifest, inst PackInstance, env envBindings) *refRewriter {
	rw := &refRewriter{ns: ns, env: env, fleets: map[string]bool{}}
	if man != nil {
		for name := range man.Models {
			rw.fleets[name] = true
		}
	}
	return rw
}

// rewriteStepModel points a step's `model:` at the pack's OWN fleet when it
// names one.
//
// The pack's `models:` block is instantiated as `<ns>/<name>` fleets, but
// nothing rewrote the reference — so a pack step saying `model: reviewer`
// resolved against the CONSUMER's globals instead. That is either a silent
// mis-resolution or, if the consumer happens to have a fleet by that name,
// a reach across the namespace boundary the rest of this file exists to
// enforce.
//
// Only a name the pack actually declares is rewritten: `model:` is
// map-key-wins, so an exact id (`claude-opus-5`), a wildcard, or an inline
// list must pass through untouched.
func (rw *refRewriter) rewriteStepModel(s *Step) {
	if s.Model.Ref == "" || !rw.fleets[s.Model.Ref] {
		return
	}
	s.Model.Ref = rw.agentName(s.Model.Ref)
}

func (rw *refRewriter) agentName(role string) string    { return rw.ns + "/" + role }
func (rw *refRewriter) workflowName(name string) string { return rw.ns + "/" + name }
func (rw *refRewriter) checkName(name string) string    { return rw.ns + "/" + name }

// resolveStepRef scopes a pack-local step reference under this instance:
// `review-flow/review` -> `<ns>/review-flow/review`. A pack writes its refs
// BARE and they resolve pack-local; a ref that names nothing the pack ships
// becomes `<ns>/…` and FAILS validation loudly rather than silently
// resolving to a consumer workflow of the same name — that would be a
// privilege reach past `requires:`.
func (rw *refRewriter) resolveStepRef(ref string) string {
	if ref == "" {
		return ref
	}
	return NamespaceStepRef(rw.ns, ref)
}

func (rw *refRewriter) resolveWorkflowRef(ref string) string {
	if ref == "" {
		return ref
	}
	return rw.ns + "/" + ref
}

func (rw *refRewriter) resolveCheckRef(ref string) string {
	if ref == "" {
		return ref
	}
	return rw.ns + "/" + ref
}

// rebindVerb rewrites the connector prefix of a `conn.verb` reference when
// `conn` is a required-and-bound connector. Built-in verb namespaces (blob, kv,
// sql, memory, workflow, conductor, manual) are never in env.conn, so they pass
// through untouched.
func (rw *refRewriter) rebindVerb(ref string) string {
	conn, verb, ok := strings.Cut(ref, ".")
	if !ok {
		return ref
	}
	if bound, ok := rw.env.conn[conn]; ok {
		return bound + "." + verb
	}
	return ref
}

// rebindSource rewrites the connector of a trigger `on: conn.event` reference.
func (rw *refRewriter) rebindSource(ref string) string { return rw.rebindVerb(ref) }

// rebindStep rewrites the environment references a pack step carries: the
// secret allowlist and the connector prefix of every skill.verbs pattern
// (model/runtime are left to the consumer default).
func (rw *refRewriter) rebindStep(p *Step) {
	if p.Skill == nil {
		return
	}
	if len(p.Skill.AllowSecrets) > 0 {
		out := make([]string, len(p.Skill.AllowSecrets))
		for i, s := range p.Skill.AllowSecrets {
			if bound, ok := rw.env.secret[s]; ok {
				out[i] = bound
			} else {
				out[i] = s
			}
		}
		p.Skill.AllowSecrets = out
	}
	if len(p.Skill.Verbs) > 0 {
		out := make([]string, len(p.Skill.Verbs))
		scopes := make(map[string]map[string][]string, len(p.Skill.VerbScopes))
		for i, v := range p.Skill.Verbs {
			out[i] = rw.rebindVerb(v) // rebinds the connector prefix of conn.verb / conn.*
			// A per-verb resource constraint is keyed by the same pattern,
			// so it moves with it — or the grant would keep its access and
			// silently lose its scope.
			if c, ok := p.Skill.VerbScopes[v]; ok {
				scopes[out[i]] = c
			}
		}
		p.Skill.Verbs = out
		p.Skill.VerbScopes = pruneVerbScopes(scopes, out)
	}
	// A session `end_on: [<connector>.<kind>]` eviction rule names connectors
	// too — rebind their prefixes, or the rule would name a connector that does
	// not exist in the consumer config and silently never fire.
	if p.Session != nil && len(p.Session.EndOn) > 0 {
		for i, e := range p.Session.EndOn {
			p.Session.EndOn[i] = rw.rebindVerb(e)
		}
	}
}

// rewriteStepName namespaces a pack step's identity-pinning `name:`, so a
// pack's track records, memory, and sessions stay inside the instance's
// namespace rather than colliding with a consumer's.
func (rw *refRewriter) rewriteStepName(p *Step) {
	if p.Name != "" {
		p.Name = rw.agentName(p.Name)
	}
}

// deepOverride merges a pack OVERRIDE map onto a base map with replace
// semantics: nested maps deep-merge, but scalars and LISTS are replaced (not
// appended). This differs from mergeMaps (which appends lists) because a pack
// override must be able to NARROW a bundled list — e.g. restrict a bundled
// agent's skill.verbs — not only widen it. Security-relevant: an override that
// appended could never remove a permission.
func deepOverride(base, ov map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range ov {
		if bm, ok := out[k].(map[string]any); ok {
			if om, ok2 := v.(map[string]any); ok2 {
				out[k] = deepOverride(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}

func (rw *refRewriter) rewriteWorkflow(w *WorkflowDef) {
	if w.Extends != "" {
		w.Extends = rw.resolveWorkflowRef(w.Extends)
	}
	if w.Gate != nil {
		rw.rewriteGate(w.Gate)
	}
	for i := range w.Steps {
		rw.rewriteStep(&w.Steps[i])
	}
}

func (rw *refRewriter) rewriteTrigger(t *TriggerSpec) {
	t.On = rw.rebindSource(t.On)
	// A list-form `on:` decodes into OnSources (expanded later by
	// NormalizeTriggers); rebind each source's connector too.
	for i := range t.OnSources {
		t.OnSources[i].Source = rw.rebindSource(t.OnSources[i].Source)
	}
	// A pack-local `extends:` target namespaces under the instance.
	if t.Extends != "" {
		t.Extends = rw.ns + "/" + t.Extends
	}
	if t.Gate != nil {
		rw.rewriteGate(t.Gate)
	}
	for i := range t.Steps {
		rw.rewriteStep(&t.Steps[i])
	}
	for i := range t.Hooks {
		rw.rewriteHook(&t.Hooks[i])
	}
}

func (rw *refRewriter) rewriteGate(g *GateSpec) {
	for i, name := range g.Run {
		g.Run[i] = rw.resolveCheckRef(name)
	}
}

func (rw *refRewriter) rewriteHook(h *Hook) {
	h.Uses = rw.rebindVerb(h.Uses)
	rw.rebindStore(h.Options)
}

// rewriteStep rewrites every reference a step carries, recursing into nested
// step forms (compensate, parallel branches, step hooks).
func (rw *refRewriter) rewriteStep(s *Step) {
	rw.rewriteStepName(s)
	rw.rewriteStepModel(s)
	rw.rebindStep(s)
	s.Workflow = rw.resolveWorkflowRef(s.Workflow)
	s.Uses = rw.rebindVerb(s.Uses)
	if s.Handoff != "" {
		if bound, ok := rw.env.handoff[s.Handoff]; ok {
			s.Handoff = bound
		}
	}
	rw.rebindStore(s.Options)
	if s.Gate != nil {
		rw.rewriteGate(s.Gate)
	}
	if s.Team != nil {
		s.Team.Planner = rw.resolveStepRef(s.Team.Planner)
		s.Team.Worker = rw.resolveStepRef(s.Team.Worker)
		s.Team.Critic = rw.resolveStepRef(s.Team.Critic)
		s.Team.Reconcile = rw.resolveStepRef(s.Team.Reconcile)
		if s.Team.Gate != nil {
			rw.rewriteGate(s.Team.Gate)
		}
	}
	if s.Compensate != nil {
		rw.rewriteStep(s.Compensate)
	}
	if s.Parallel != nil {
		for bi := range s.Parallel.Branches {
			for si := range s.Parallel.Branches[bi] {
				rw.rewriteStep(&s.Parallel.Branches[bi][si])
			}
		}
	}
	for i := range s.Hooks {
		rw.rewriteHook(&s.Hooks[i])
	}
}

// rebindStore rewrites a `store:` selector in a verb's options when it names a
// required-and-bound store.
func (rw *refRewriter) rebindStore(opts map[string]any) {
	if opts == nil {
		return
	}
	if sel, ok := opts["store"].(string); ok {
		if bound, ok := rw.env.store[sel]; ok {
			opts["store"] = bound
		}
	}
}
