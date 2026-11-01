package flow

import (
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
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
//	    memory.remember: { scope: ["{{.repo}}", "standard:{{.repo}}", "emphasis:{{.repo}}"] }
//
// — with one difference in what the list MEANS. For a connector option the
// grant widens the dispatch's own context. For memory it is a restriction:
// a step that lists scopes may touch those and nothing else, the dispatch's
// own scope and the daemon-wide policy included. That is what makes the list
// worth writing — a calibration step limited to the repo it is looking at
// cannot leave notes for any other.
//
// Memory keeps its own dimension rather than tagging the verb option with
// Field.Scope (see memoryscope.go for why), so the list is enforced where the
// other memory rules are, inside memory.CheckOp, on every face an agent can
// write through: the verb, the live memory tool, and the ```remember output
// contract. This file only builds the matcher; memory.Caller.WithGrant
// carries it.

// memoryScopeOption is the grant key a memory verb's scope list lives under —
// the verbs' own option name, and memory's dimension.
const memoryScopeOption = config.DimScope

// memoryGrantRefs maps the memory selector's placeholder spelling onto the
// allowlist template that renders the same fact, so a step can write its
// scopes the way it writes its memory selector. Only facts in the closed
// render set (scopeRenderData) can appear here.
var memoryGrantRefs = strings.NewReplacer("${repo}", "{{.repo}}")

// MemoryGrant builds the step-grant matcher for one memory verb from the
// verb's resolved grant (SkillPolicy.ScopesFor) and the dispatch it runs for.
// nil — no restriction — unless the grant has a `scope` key; an empty list
// under the key is a restriction that admits nothing.
//
// Entries match like every other allowlist entry: a static entry keeps its
// glob, a `{{ }}` entry renders against the dispatch's trusted facts and then
// matches literally, and one that renders empty (a dispatch whose target the
// sender chose has no `.repo`) matches nothing.
func (r *Runner) MemoryGrant(grant map[string][]string, t core.Trigger) memory.GrantMatcher {
	list, ok := grant[memoryScopeOption]
	if !ok {
		return nil
	}
	data := r.scopeRenderData(t)
	templated := make([]string, 0, len(list))
	for _, p := range list {
		p = memoryGrantRefs.Replace(p)
		if usesEmptyFact(p, data) {
			continue // fail closed: `standard:{{.repo}}` with no repo is not `standard:`
		}
		templated = append(templated, p)
	}
	rp := &resourcePolicy{render: data}
	pats := rp.expand(templated)
	return func(scope string) bool {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			return false
		}
		for _, p := range pats {
			if p.text = strings.TrimSpace(p.text); p.text != "" && p.matches(scope) {
				return true
			}
		}
		return false
	}
}

// emptyFactProbe stands in for a fact with no value when usesEmptyFact
// renders an entry a second time.
const emptyFactProbe = "\x00empty-fact\x00"

// usesEmptyFact reports whether a templated entry reads a fact this dispatch
// has no value for. The generic allowlist render only drops an entry that
// renders to nothing at all; a memory scope is a key prefix plus a fact, and
// `standard:{{.repo}}` on a dispatch without a trusted repo renders to
// `standard:` — a key the operator never meant to grant. Rendering once more
// with every empty fact replaced by a probe shows whether one was used.
func usesEmptyFact(pattern string, data map[string]any) bool {
	if !strings.Contains(pattern, "{{") {
		return false
	}
	probe := make(map[string]any, len(data))
	for k, v := range data {
		switch x := v.(type) {
		case string:
			if x == "" {
				v = emptyFactProbe
			}
		case int:
			if x == 0 {
				v = emptyFactProbe
			}
		}
		probe[k] = v
	}
	out, err := renderScopePattern(pattern, probe)
	return err != nil || strings.Contains(out, emptyFactProbe)
}

// StepMemoryGrant is MemoryGrant for a step's own skill policy and one memory
// op — what the engine needs for the ```remember output contract, which runs
// outside any verb call.
func (r *Runner) StepMemoryGrant(sk *config.SkillPolicy, op string, t core.Trigger) memory.GrantMatcher {
	if sk == nil {
		return nil
	}
	return r.MemoryGrant(sk.ScopesFor("memory."+op, func(pattern, uses string) bool {
		return matchAny([]string{pattern}, uses)
	}), t)
}
