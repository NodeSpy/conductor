package config

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

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
	// wfRefs collects every workflow name this rewriter namespaced, so the
	// instantiator can prove each one names a workflow the pack actually
	// ships. Recording HERE — at the single point that rewrites them — is
	// what makes the check complete: a reference shape that is namespaced is
	// checked, and one that is not namespaced is a bug this file's meta-test
	// catches.
	wfRefs []string
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

// resolveWorkflowRef namespaces a pack-authored WORKFLOW NAME to the pack's
// own instance — the Terraform-module rule: a pack addresses only workflows it
// ships.
//
// Prefixing happens even when the name is TEMPLATED (`{{.pick}}` becomes
// `<ns>/{{.pick}}`), and that is the point: the confinement survives a name
// chosen at runtime by an agent or an `if:`, because whatever the template
// renders to lands under the pack's prefix and only the pack's own workflows
// live there.
func (rw *refRewriter) resolveWorkflowRef(ref string) string {
	if ref == "" {
		return ref
	}
	out := rw.ns + "/" + ref
	rw.wfRefs = append(rw.wfRefs, out)
	return out
}

// workflowNameOptions maps a VERB to the option keys whose value NAMES a
// workflow, for the `uses:`-with-`options:` spelling of a workflow call.
//
// This table is the reason the meta-test exists. The step-call form
// (`workflow: <name>`) was namespaced from the start; the verb form was not,
// and the two are the same act written two ways:
//
//   - { workflow: review-flow }                        # namespaced
//   - { uses: workflow.run, options: {name: review-flow} }   # WAS NOT
//
// `workflow` is a packOpenNamespace precisely BECAUSE a pack's workflow names
// are namespaced to its instance (see packNamespaces) — so the open namespace
// was resting on an invariant that the verb form broke. A pack could name the
// consumer's `review-flow` and run it, connectors and all, with no
// `requires.connectors` entry for anything inside it.
//
// Adding a workflow-naming verb without adding it here reopens that hole, so
// TestEveryPackWorkflowRefShapeIsNamespaced enumerates the shapes and fails.
var workflowNameOptions = map[string][]string{
	// run: the name of the workflow to execute.
	"workflow.run": {"name"},
	// save: the name a promoted workflow is STORED under. Namespaced for the
	// mirror-image reason — an un-namespaced save would let a pack plant a
	// bare name in the shared saved-workflow registry for the consumer (or
	// another pack) to later call. The pack can still call what it saved:
	// its own workflow.run{name} is namespaced identically.
	"workflow.save": {"name"},
}

// rewriteWorkflowRefOptions namespaces a workflow name carried in a verb's
// options. Applied AFTER rebindVerb, so it keys on the verb that will actually
// dispatch at runtime rather than the one the author typed.
func (rw *refRewriter) rewriteWorkflowRefOptions(uses string, opts map[string]any) {
	if opts == nil {
		return
	}
	for _, key := range workflowNameOptions[uses] {
		name, ok := opts[key].(string)
		if !ok || name == "" {
			continue
		}
		opts[key] = rw.resolveWorkflowRef(name)
	}
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

// permissionSets are the keys whose VALUE IS A SET OF PERMISSIONS, addressed
// by their path from a step. The override's entry replaces the base's whole,
// map form included: the set of keys the consumer wrote is the final set, so
// omitting one REMOVES it.
//
// Without this, deep-merge quietly reverses the narrowing guarantee for any
// permission key that grew a map form. `skill.verbs` did exactly that: the
// list form narrowed correctly (lists replace), while the map form — same
// key, same meaning, one extra dimension of detail — merged, so a consumer
// override naming one verb kept every verb the pack bundled. It failed OPEN,
// and it failed open only in the newer spelling, which is the worst place for
// a permission rule to differ.
//
// Anything added here must be a permission set, not configuration: replace
// semantics are right for "which verbs may this agent call" and wrong for
// "what are this agent's settings".
var permissionSets = [][]string{
	{"skill", "verbs"},
}

// isPermissionSet reports whether a key path names one — as a SUFFIX, so a
// permission set is recognized wherever it nests.
//
// It used to match the full path exactly, which meant it recognized
// `skill.verbs` on a top-level step and nowhere else. A step's
// `compensate.skill.verbs` is three segments deep, a parallel branch's is
// deeper still, and a for_each body's deeper again — each of those fell
// through to the generic deep-merge, so a consumer override could not NARROW
// a grant it inherited. It failed open, in exactly the places a reviewer is
// least likely to look, and the doc above already claimed otherwise.
//
// Suffix matching is what "wherever it sits in the step" means. A key path
// ending in skill/verbs IS the grant, whatever carried it there.
func isPermissionSet(path []string) bool {
	for _, p := range permissionSets {
		if len(path) < len(p) {
			continue
		}
		tail := path[len(path)-len(p):]
		same := true
		for i := range p {
			if p[i] != tail[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

// deepOverride merges a pack OVERRIDE map onto a base map with replace
// semantics: nested maps deep-merge, but scalars and LISTS are replaced (not
// appended), and a permissionSets path is replaced WHOLE whichever form it
// takes. This differs from mergeMaps (which appends lists) because a pack
// override must be able to NARROW a bundled grant — e.g. restrict a bundled
// agent's skill.verbs — not only widen it. Security-relevant: an override that
// appended (or merged) could never remove a permission.
func deepOverride(base, ov map[string]any) map[string]any {
	return deepOverrideAt(base, ov, nil)
}

// deepOverrideAt is deepOverride tracking its path, so a permission set can be
// recognized wherever it sits in the step.
func deepOverrideAt(base, ov map[string]any, path []string) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range ov {
		here := append(append([]string(nil), path...), k)
		if isPermissionSet(here) {
			out[k] = v // the override's set is the final set
			continue
		}
		if bm, ok := out[k].(map[string]any); ok {
			if om, ok2 := v.(map[string]any); ok2 {
				out[k] = deepOverrideAt(bm, om, here)
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
	// A hook is a verb action unit, so it reaches workflow.run exactly as a
	// step does — and is the easier place to overlook.
	rw.rewriteWorkflowRefOptions(h.Uses, h.Options)
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
	rw.rewriteWorkflowRefOptions(s.Uses, s.Options)
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

// checkOwnedWorkflowRefs proves every workflow name this rewriter namespaced
// addresses a workflow the pack actually ships.
//
// Namespacing alone already makes a foreign reference UNRUNNABLE — `<ns>/x`
// only ever resolves among the pack's own workflows. This turns that runtime
// dead end into a LOAD error, so a pack that names the consumer's
// `review-flow` fails at install the way an undeclared connector does, instead
// of installing cleanly and dying the first time the step fires.
//
// A DECLARED DEPENDENCY counts as the pack's own: `requires.packs: {base: …}`
// instantiates that pack at `<ns>/base`, so `base/fetch` namespaces to
// `<ns>/base/fetch` and lands inside the dependency — the submodule call, and
// one the consumer read in the manifest before installing. What stays closed
// is the UNDECLARED reach: a bare name, or a sibling pack's namespace.
//
// A TEMPLATED name is skipped: it resolves at runtime, and the prefix already
// confines whatever it renders to. The runtime unknown-name error covers it,
// exactly as it does for a templated step-call name.
func (rw *refRewriter) checkOwnedWorkflowRefs(man *PackManifest) []string {
	owned := map[string]bool{}
	for name := range man.Workflows {
		owned[rw.workflowName(name)] = true
	}
	// Prefixes a declared dependency's workflows live under.
	var depPrefixes []string
	for alias := range man.Pack.Requires.Packs {
		depPrefixes = append(depPrefixes, rw.ns+"/"+alias+"/")
	}
	seen, problems := map[string]bool{}, []string(nil)
	for _, ref := range rw.wfRefs {
		if owned[ref] || seen[ref] || strings.Contains(ref, "{{") {
			continue
		}
		if slices.ContainsFunc(depPrefixes, func(p string) bool { return strings.HasPrefix(ref, p) }) {
			continue
		}
		seen[ref] = true
		problems = append(problems,
			fmt.Sprintf("references workflow %q, which this pack does not ship (its workflows: %s; "+
				"declared pack dependencies: %s) — a pack may call only its OWN workflows and those "+
				"of a declared dependency, so a bare name resolves inside the pack, never against "+
				"the consumer's config or another pack's",
				strings.TrimPrefix(ref, rw.ns+"/"), sortedKeys(man.Workflows),
				depNames(man.Pack.Requires.Packs)))
	}
	sort.Strings(problems)
	return problems
}
