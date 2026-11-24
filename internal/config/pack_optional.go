package config

import "strings"

// Optional connectors a pack USES (not only listens to).
//
// `required: false` lets a pack degrade when the consumer binds nothing for a
// connector. For a connector the pack listens to, bindPackSources parks the
// triggers on it. A connector the pack only CALLS (`uses: backup.put`) had no
// such path: the step kept its unbound name and the load failed on "unknown
// connector". dormantByOptional closes that gap: every workflow whose steps
// (or hooks) call an unbound optional connector — directly or through a
// workflow it calls — goes dormant, and so does every trigger that would run
// one. Dormant parts are not instantiated at all, with one warning naming
// them; the rest of the pack loads and runs.

// unboundOptional is the set of optional connectors (single or list) this
// instance left unbound.
func unboundOptional(reqs ConnectorReqs, env envBindings) map[string]bool {
	out := map[string]bool{}
	for name, r := range reqs {
		if r.Required {
			continue
		}
		if r.Many {
			if len(env.many[name]) == 0 {
				out[name] = true
			}
			continue
		}
		if _, ok := env.conn[name]; !ok {
			out[name] = true
		}
	}
	return out
}

// stepsCall reports the unbound connectors a step list calls, and the
// pack-local workflows it calls.
func stepsCall(steps []Step, hooks []Hook, unbound map[string]bool) (conns map[string]bool, calls []string) {
	conns = map[string]bool{}
	note := func(uses string) {
		if c, _, ok := strings.Cut(uses, "."); ok && unbound[c] {
			conns[c] = true
		}
	}
	for _, h := range hooks {
		note(h.Uses)
	}
	walkSteps(steps, func(s *Step) {
		note(s.Uses)
		for _, h := range s.Hooks {
			note(h.Uses)
		}
		if s.Workflow != "" {
			calls = append(calls, s.Workflow)
		}
	})
	return conns, calls
}

// dormantByOptional returns the workflows and triggers (by the names the pack
// gave them) that need an unbound optional connector, each with the
// connectors it needs.
func dormantByOptional(man *PackManifest, unbound map[string]bool) (wfs, trs map[string][]string) {
	wfs, trs = map[string][]string{}, map[string][]string{}
	if len(unbound) == 0 {
		return wfs, trs
	}
	direct := map[string]map[string]bool{}
	calls := map[string][]string{}
	for name, wf := range man.Workflows {
		direct[name], calls[name] = stepsCall(wf.Steps, nil, unbound)
	}
	// needs(name) = the workflow's own unbound connectors plus those of every
	// workflow it calls, transitively (cycles are cut by `seen`).
	var needs func(name string, seen map[string]bool) map[string]bool
	needs = func(name string, seen map[string]bool) map[string]bool {
		out := map[string]bool{}
		if seen[name] {
			return out
		}
		seen[name] = true
		for c := range direct[name] {
			out[c] = true
		}
		for _, callee := range calls[name] {
			if _, ok := man.Workflows[callee]; !ok {
				continue // not pack-local; checkOwnedWorkflowRefs reports it
			}
			for c := range needs(callee, seen) {
				out[c] = true
			}
		}
		return out
	}
	for name := range man.Workflows {
		if n := needs(name, map[string]bool{}); len(n) > 0 {
			wfs[name] = sortedSet(n)
		}
	}
	for _, t := range man.Triggers {
		own, callees := stepsCall(t.Steps, t.Hooks, unbound)
		for _, callee := range callees {
			for _, c := range wfs[callee] {
				own[c] = true
			}
		}
		if len(own) > 0 {
			trs[t.Name] = sortedSet(own)
		}
	}
	return wfs, trs
}
