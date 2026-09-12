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
	man.WalkPackSteps(func(role string, s *Step) {
		sk := s.Skill
		if sk == nil {
			return
		}
		for _, pat := range sk.Verbs {
			conn, verb := splitGrant(pat)
			switch {
			case conn == "": // globbed connector — bounded at instantiate
				if len(declared) == 0 {
					problems = append(problems, fmt.Sprintf(
						"step %s: skill.verbs %q grants everything but the pack declares no requires.connectors — a pack's wildcard is bounded by its declared connectors, so this grants nothing; declare what it needs", role, pat))
					break
				}
				// A bare `*` means "everything I declared" by definition —
				// saying so would fire on every correct pack. What is
				// worth surfacing is a pattern that READS narrow but is
				// not: `*.read` looks like one grant and becomes one per
				// declared connector, on connectors the author never
				// enumerated and where `read` may mean something else.
				if verb != "" && len(declared) > 1 {
					problems = append(problems, fmt.Sprintf(
						"step %s: skill.verbs %q expands to one grant per declared connector (%s) — %d in total. Name them explicitly if you meant fewer",
						role, pat, strings.Join(prefixEach(declared, "."+verb), ", "), len(declared)))
				}
			case !set[conn]:
				problems = append(problems, fmt.Sprintf(
					"step %s: skill.verbs %q names connector %q, which is not in requires.connectors (declared: %s) — a pack may only grant access to connectors it declares", role, pat, conn, orNone(declared)))
			}
		}
	})
	return problems
}

// grantConnector is the connector half of a grant pattern, or "" when that
// half is globbed or absent. A caller that also needs the VERB half (to
// keep a `*.read` restriction through expansion) wants splitGrant.
//
// Historical note kept deliberately: a pattern whose connector half is
// globbed (`*.read`) is treated as
// the full wildcard: it can reach any connector, so it must be bounded the
// same way.
func grantConnector(pattern string) string {
	conn, _ := splitGrant(pattern)
	return conn
}

// splitGrant separates a grant pattern into its connector and verb halves.
// conn is "" when the connector half is globbed (or absent); verb is "" for
// the bare `*` and for a `<glob>.*`, i.e. only when the pattern places NO
// restriction on which verb.
//
// The distinction is the whole point: `*` and `*.read` both have a globbed
// connector, but the second still says "reads only". Collapsing them —
// which is what returning "" for both did — turned `skill.verbs: ["*.read"]`
// into full read-write on every declared connector.
func splitGrant(pattern string) (conn, verb string) {
	p := strings.TrimSpace(pattern)
	if p == "" || p == "*" {
		return "", ""
	}
	c, v, ok := strings.Cut(p, ".")
	if !ok || c == "" {
		return "", ""
	}
	if strings.ContainsAny(c, "*?[") {
		if v == "*" {
			return "", "" // `*.*` is the bare wildcard, spelled long
		}
		return "", v
	}
	return c, v
}

// boundGrant intersects a pack step's skill grant with the connectors the
// pack declared — the runtime belt behind the lint check.
//
// The rules, which mirror what an author would expect:
//
//   - a full wildcard (`*`, or `*.*`) EXPANDS to one `<conn>.*` per
//     declared connector — "all verbs of my required connectors";
//   - a globbed connector half WITH a verb suffix (`*.read`) expands the
//     same way but KEEPS the suffix — `<conn>.read` per declared
//     connector. It said "reads only" and it still means that;
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
		conn, verb := splitGrant(p)
		if conn == "" {
			// A globbed connector half is bounded to the declared ones —
			// carrying its verb restriction with it, if it had one.
			if len(declared) == 0 {
				dropped = append(dropped, p)
				continue
			}
			suffix := ".*"
			if verb != "" {
				suffix = "." + verb
			}
			for _, n := range declared {
				add(n + suffix)
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
	man.WalkPackSteps(func(role string, step *Step) {
		if step.Skill == nil || len(step.Skill.Verbs) == 0 {
			return
		}
		kept, dropped := boundGrant(step.Skill.Verbs, declared)
		if len(dropped) > 0 {
			st.warnf("pack %q: step %s skill.verbs %s dropped — a pack may only grant access to the connectors it declares in requires.connectors (declared: %s)",
				ns, role, strings.Join(dropped, ", "), orNone(declared))
		}
		if len(kept) == len(step.Skill.Verbs) && sameOrder(kept, step.Skill.Verbs) {
			return
		}
		// Copy the policy before narrowing: the manifest's own value may be
		// shared with the lint/show paths.
		sk := *step.Skill
		sk.Verbs = kept
		sk.VerbScopes = pruneVerbScopes(sk.VerbScopes, kept)
		step.Skill = &sk
	})
}

// prefixEach renders what a globbed pattern expands to, for the lint note.
func prefixEach(names []string, suffix string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, n+suffix)
	}
	return out
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
