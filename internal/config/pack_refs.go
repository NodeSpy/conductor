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
	// agentBind maps a pack agent role to a consumer global (bind form); refs to
	// it resolve to the global directly, not to a namespaced copy.
	agentBind map[string]string
	// packAgents/packWorkflows/packChecks are the names the pack defines, so a
	// ref is only rewritten when it actually names a pack-local resource.
	packAgents    map[string]bool
	packWorkflows map[string]bool
	packChecks    map[string]bool
	// env rebinds required environment names to consumer globals.
	env envBindings
}

func newRefRewriter(ns string, man *PackManifest, inst PackInstance, env envBindings) *refRewriter {
	rw := &refRewriter{
		ns:            ns,
		agentBind:     map[string]string{},
		packAgents:    map[string]bool{},
		packWorkflows: map[string]bool{},
		packChecks:    map[string]bool{},
		env:           env,
	}
	for role := range man.Agents {
		rw.packAgents[role] = true
	}
	for name := range man.Workflows {
		rw.packWorkflows[name] = true
	}
	for name := range man.Checks {
		rw.packChecks[name] = true
	}
	for role, b := range inst.Agents {
		if b.IsBind() {
			rw.agentBind[role] = b.Bind
		}
	}
	return rw
}

func (rw *refRewriter) agentName(role string) string    { return rw.ns + "/" + role }
func (rw *refRewriter) workflowName(name string) string { return rw.ns + "/" + name }
func (rw *refRewriter) checkName(name string) string    { return rw.ns + "/" + name }

// resolveAgentRef maps a bare pack agent ref to its final name: a bound global,
// a namespaced pack agent, or (for a dependency alias like "base/fetcher") a
// nested-namespace name. Unknown refs are left untouched for later validation.
func (rw *refRewriter) resolveAgentRef(ref string) string {
	if ref == "" {
		return ref
	}
	if g, ok := rw.agentBind[ref]; ok {
		return g
	}
	if rw.packAgents[ref] {
		return rw.agentName(ref)
	}
	// A dependency-qualified ref (alias/name) namespaces under this instance.
	if strings.Contains(ref, "/") {
		return rw.ns + "/" + ref
	}
	return ref
}

func (rw *refRewriter) resolveWorkflowRef(ref string) string {
	if ref == "" {
		return ref
	}
	if rw.packWorkflows[ref] {
		return rw.workflowName(ref)
	}
	if strings.Contains(ref, "/") {
		return rw.ns + "/" + ref
	}
	return ref
}

func (rw *refRewriter) resolveCheckRef(ref string) string {
	if rw.packChecks[ref] {
		return rw.checkName(ref)
	}
	if strings.Contains(ref, "/") {
		return rw.ns + "/" + ref
	}
	return ref
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

// rebindAgent rewrites the environment references a pack agent profile carries
// (secret allowlist, handoff — provider/model/runtime are left to the consumer
// default). Agent-to-agent refs live on steps, not profiles.
func (rw *refRewriter) rebindAgent(p *AgentProfile) {
	if p.Skill != nil && len(p.Skill.AllowSecrets) > 0 {
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
}

func (rw *refRewriter) rewriteWorkflow(w *WorkflowDef) {
	if w.Gate != nil {
		rw.rewriteGate(w.Gate)
	}
	for i := range w.Steps {
		rw.rewriteStep(&w.Steps[i])
	}
}

func (rw *refRewriter) rewriteTrigger(t *TriggerSpec) {
	t.On = rw.rebindSource(t.On)
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
	s.Agent = rw.resolveAgentRef(s.Agent)
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
		s.Team.Planner = rw.resolveAgentRef(s.Team.Planner)
		s.Team.Worker = rw.resolveAgentRef(s.Team.Worker)
		s.Team.Critic = rw.resolveAgentRef(s.Team.Critic)
		s.Team.Reconcile = rw.resolveAgentRef(s.Team.Reconcile)
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
