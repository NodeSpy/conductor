package config

import (
	"fmt"
	"sort"
	"strings"
)

// A pack's skill grant is bounded by `requires.connectors`
// (docs/design/skill-capability-and-pack-interface.md §C).
//
// `requires.connectors` is the pack's CAPABILITY BOUNDARY, not just a list of
// sockets to plug in. A distributable pack that could grant its agent
// `skill.verbs: ["*"]` would quietly reach the consumer's pagerduty, their
// secrets connector, everything — from a pack they installed to review PRs.
// So a pack's grant may only ever name connectors it declared, and a
// wildcard inside a pack means "all verbs of MY required connectors":
//
//	requires: { connectors: { github: "*" } }
//	steps:
//	  reviewer:
//	    skill: { verbs: ["*"] }        # => github.* only
//	    # skill: { verbs: [github.*] } # fine — github is declared
//	    # skill: { verbs: [pagerduty.*] }  # LINT ERROR — not declared
//
// Enforced twice, deliberately: statically at `conductor pack lint` (so a
// pack author sees it while authoring) and again at instantiate (so a
// hand-authored pack that never ran lint still cannot exceed its declared
// interface). The instantiate pass INTERSECTS rather than erroring on the
// wildcard case, because `["*"]` is a legitimate authoring shorthand — it is
// the undeclared NAMED connector that is a mistake worth failing on.

// lintPackSkillGrants reports every `skill.verbs` pattern in a pack that
// names a connector the pack did not declare in requires.connectors.
func lintPackSkillGrants(man *PackManifest) []string {
	declared := man.Pack.Requires.ConnectorNames()
	set := make(map[string]bool, len(declared))
	for _, n := range declared {
		set[n] = true
	}
	var problems []string
	for _, role := range sortedNames(man.Steps) {
		sk := man.Steps[role].Skill
		if sk == nil {
			continue
		}
		for _, pat := range sk.Verbs {
			conn := grantConnector(pat)
			switch {
			case conn == "": // "*" — bounded to the declared set at instantiate
				if len(declared) == 0 {
					problems = append(problems, fmt.Sprintf(
						"steps.%s: skill.verbs %q grants everything but the pack declares no requires.connectors — a pack's wildcard is bounded by its declared connectors, so this grants nothing; declare what it needs", role, pat))
				}
			case !set[conn]:
				problems = append(problems, fmt.Sprintf(
					"steps.%s: skill.verbs %q names connector %q, which is not in requires.connectors (declared: %s) — a pack may only grant access to connectors it declares", role, pat, conn, orNone(declared)))
			}
		}
	}
	return problems
}

// grantConnector is the connector half of a grant pattern, or "" for the
// full wildcard (which names no connector and is bounded elsewhere). A
// pattern whose connector half is itself globbed (`*.read`) is treated as
// the full wildcard: it can reach any connector, so it must be bounded the
// same way.
func grantConnector(pattern string) string {
	p := strings.TrimSpace(pattern)
	if p == "" || p == "*" {
		return ""
	}
	conn, _, ok := strings.Cut(p, ".")
	if !ok || conn == "" || strings.ContainsAny(conn, "*?[") {
		return ""
	}
	return conn
}

// boundGrant intersects a pack step's skill grant with the connectors the
// pack declared — the runtime belt behind the lint check.
//
// The rules, which mirror what an author would expect:
//
//   - a full wildcard (`*`, or anything whose connector half is globbed)
//     EXPANDS to one `<conn>.*` per declared connector — "all verbs of my
//     required connectors";
//   - a pattern naming a declared connector passes through unchanged;
//   - a pattern naming an undeclared connector is DROPPED (lint already
//     reports it as an error; dropping is what keeps a hand-authored pack
//     inside its interface);
//   - with nothing declared, the grant is empty — deny-by-default, and a
//     pack that declared no connectors has no interface to grant through.
//
// The returned patterns are still in the PACK's vocabulary; the ref
// rewriter rebinds their connector prefixes to the consumer's instance
// names afterwards.
func boundGrant(patterns, declared []string) (kept []string, dropped []string) {
	if len(patterns) == 0 {
		return nil, nil
	}
	set := make(map[string]bool, len(declared))
	for _, n := range declared {
		set[n] = true
	}
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			kept = append(kept, p)
		}
	}
	for _, pat := range patterns {
		p := strings.TrimSpace(pat)
		if p == "" {
			continue
		}
		conn := grantConnector(p)
		if conn == "" {
			// A wildcard is bounded to the declared connectors.
			if len(declared) == 0 {
				dropped = append(dropped, p)
				continue
			}
			for _, n := range declared {
				add(n + ".*")
			}
			continue
		}
		if !set[conn] {
			dropped = append(dropped, p)
			continue
		}
		add(p)
	}
	sort.Strings(kept)
	return kept, dropped
}

// applyPackSkillBoundary bounds every shipped step's grant to the pack's
// declared connectors, warning about anything it had to drop so a pack whose
// grant exceeded its interface is visible rather than silently narrowed.
func (st *packInstantiation) applyPackSkillBoundary(ns string, man *PackManifest) {
	declared := man.Pack.Requires.ConnectorNames()
	for _, role := range sortedNames(man.Steps) {
		step := man.Steps[role]
		if step.Skill == nil || len(step.Skill.Verbs) == 0 {
			continue
		}
		kept, dropped := boundGrant(step.Skill.Verbs, declared)
		if len(dropped) > 0 {
			st.warnf("pack %q: step %q skill.verbs %s dropped — a pack may only grant access to the connectors it declares in requires.connectors (declared: %s)",
				ns, role, strings.Join(dropped, ", "), orNone(declared))
		}
		if len(kept) == len(step.Skill.Verbs) && sameOrder(kept, step.Skill.Verbs) {
			continue
		}
		// Copy the policy before narrowing: the manifest's own value may be
		// shared with the lint/show paths.
		sk := *step.Skill
		sk.Verbs = kept
		step.Skill = &sk
		man.Steps[role] = step
	}
}

func sameOrder(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
