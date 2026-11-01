package flow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/memory"
)

// A STEP'S OWN MEMORY SCOPES.
//
// `skill.verbs` already scopes a connector verb per step: the map form's
// `{option: [values]}` lists what one call may name. Memory's `scope` is the
// same sentence about the memory verbs —
//
//	skill:
//	  verbs:
//	    memory.remember: { scope: ["team:{{.inputs.team}}", "standard:{{.owner}}"] }
//
// — with one difference in what the list MEANS. For a connector option the
// grant widens the dispatch's own context. For memory it is a restriction:
// a step that lists scopes may touch those and nothing else, the dispatch's
// own scope and the daemon-wide policy included.
//
// Each entry is a scope key (memory.RenderScopeKey): a static entry keeps its
// glob; a templated one renders over the STEP's facts (memory.ScopeFacts —
// the data its prompt sees, minus secrets, with the target facts taken from
// its own target) and then matches literally. An entry that reads a fact the
// step has no value for admits nothing.
//
// The step's facts exist where it is dispatched, not where its agent later
// calls a verb, so the list is rendered at dispatch (dispatch.Request
// .ScopeFacts → memory.RenderGrantScopes) and carried with the session
// (skill.Identity.MemoryScopes). Every face the agent writes through — the
// verb, the live memory tool, the ```remember output contract — matches
// that one rendered list, inside memory.CheckOp.

// memoryScopeOption is the grant key a memory verb's scope list lives under.
const memoryScopeOption = memory.ScopeOption

// MemoryGrantFrom builds the step-grant matcher for one memory verb (uses,
// e.g. "memory.remember") from the step's skill.verbs map form and its
// rendered lists. nil — no restriction — unless some verb pattern that
// matches uses carries a `scope` key; lists from several matching patterns
// are their union (SkillPolicy.ScopesFor's rule). A pattern with no rendered
// list (a session minted without facts) keeps only its static entries:
// templated ones admit nothing.
func MemoryGrantFrom(verbScopes map[string]map[string][]string, rendered map[string][]memory.ScopePattern, uses string) memory.GrantMatcher {
	pats := make([]string, 0, len(verbScopes))
	for p := range verbScopes {
		pats = append(pats, p)
	}
	sort.Strings(pats)
	found := false
	var list []scopePattern
	for _, pat := range pats {
		raw, ok := verbScopes[pat][memoryScopeOption]
		if !ok || !matchAny([]string{pat}, uses) {
			continue
		}
		found = true
		if r, ok := rendered[pat]; ok {
			for _, p := range r {
				list = append(list, scopePattern{text: p.Text, literal: p.Literal})
			}
			continue
		}
		for _, e := range raw {
			if !memory.IsScopeTemplate(e) {
				list = append(list, scopePattern{text: e})
			}
		}
	}
	if !found {
		return nil
	}
	return func(scope string) bool {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			return false
		}
		for _, p := range list {
			if p.text = strings.TrimSpace(p.text); p.text != "" && p.matches(scope) {
				return true
			}
		}
		return false
	}
}

// StepMemoryGrant is MemoryGrantFrom for a step's own skill policy, rendered
// over facts on the spot — what the ```remember output contract needs, which
// runs where the facts are rather than inside a session.
func StepMemoryGrant(sk *config.SkillPolicy, op string, facts map[string]any) memory.GrantMatcher {
	if sk == nil {
		return nil
	}
	return MemoryGrantFrom(sk.VerbScopes, memory.RenderGrantScopes(sk.VerbScopes, facts), "memory."+op)
}

// MemoryFacts is memory.ScopeFacts with this runner's tracked-secret
// redactor — the one place flow builds the facts a step's scope keys render
// over.
func (r *Runner) MemoryFacts(data map[string]any, t core.Trigger, workflow, step string) map[string]any {
	var redact func(any) any
	if r != nil && r.Secrets != nil {
		redact = r.Secrets.RedactValue
	}
	return memory.ScopeFacts(data, t, workflow, step, redact)
}

// memoryVerbScope renders the `scope:` option of a memory verb step as a
// scope key (memory.RenderScopeKey) over the step's facts, instead of the
// general option renderer: the same template language and facts as the
// step's selector and grant, and fail-closed — a key that reads a fact this
// run has no value for refuses the call rather than writing (or recalling)
// under a partial key like `team:`. keyed=false leaves the option to the
// ordinary renderer (not a memory verb, or no templated scope).
//
// The rendered value goes into the options as-is and is never rendered
// again, so template text a fact carries stays text.
func (r *Runner) memoryVerbScope(ctx context.Context, in *connector.Instance, id string, opts map[string]any, t core.Trigger, data map[string]any) (string, bool, error) {
	if in.Decl == nil || in.Decl.Type != "memory" {
		return "", false, nil
	}
	raw, ok := opts[memoryScopeOption].(string)
	if !ok || !memory.IsScopeTemplate(raw) {
		return "", false, nil
	}
	_, wfScope := budgetFrom(ctx)
	key, err := memory.RenderScopeKey(raw, r.MemoryFacts(data, t, wfScope, id))
	if err != nil {
		return "", false, fmt.Errorf("scope: %w — refused rather than using a partial key", err)
	}
	return key, true, nil
}

// withoutOption copies opts without one key.
func withoutOption(opts map[string]any, key string) map[string]any {
	out := make(map[string]any, len(opts))
	for k, v := range opts {
		if k != key {
			out[k] = v
		}
	}
	return out
}
