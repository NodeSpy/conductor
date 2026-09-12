package connector

import (
	"sort"

	"github.com/NodeSpy/conductor/internal/core"
)

// RESOURCE SCOPING (docs/design/skill-verb-scope.md). Conductor gates two
// orthogonal axes. Which VERBS a caller may run is `skill.verbs` /
// policy.agent_authored.allow. Which RESOURCE one call may name — the channel
// a message lands in, the repo a review is submitted to, the store a key is
// written in — is a constraint on an option VALUE, and only the connector
// knows which of its options name a resource at all.
//
// So the connector says so, once, in its option schema: a non-empty
// Field.Scope marks the option as a destination and names its DIMENSION.
// Content options (text, body, prompt) stay unmarked and are never gated.
// Both enforcement surfaces (the plan surface's checkVerbResources and the
// skill surface's RunSkillVerb) walk the CALLED verb's scoped options and
// refuse a value outside the allowed set — neither hardcodes an option name,
// so a connector that tags a new option is enforced the day it ships.

// DimRepo is the one dimension core.Trigger models natively (Target.Repo), so
// ContextScope can answer it for every connector without a per-connector hook:
// a dispatch is always allowed to act on the repo it fired for. Every other
// dimension comes from the connector.
const DimRepo = "repo"

// ScopedOption is one scope-tagged option of a verb: the option's name and
// the resource dimension its value lives in.
type ScopedOption struct {
	Name string
	Dim  string
}

// ScopedOptions lists a verb's scope-tagged options, ordered by option name so
// a refusal is deterministic (the first out-of-scope option is always the same
// one).
func (v VerbDecl) ScopedOptions() []ScopedOption {
	var out []ScopedOption
	for name, f := range v.Options {
		if f.Scope == "" {
			continue
		}
		out = append(out, ScopedOption{Name: name, Dim: f.Scope})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ScopeDims lists every dimension this declaration's verbs tag, sorted. It is
// what config validation lints an operator's allow_scopes keys against.
func (d *TypeDecl) ScopeDims() []string {
	seen := map[string]bool{}
	var out []string
	if d == nil {
		return out
	}
	for _, v := range d.Verbs {
		for _, so := range v.ScopedOptions() {
			if !seen[so.Dim] {
				seen[so.Dim] = true
				out = append(out, so.Dim)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ScopeContexter is the adapter hook a connector implements to map a dispatch
// to the value it is IMPLICITLY allowed to name in one dimension: github → the
// repo the trigger fired for, slack → the channel the event came from. Empty
// means the dispatch carries no such value — and with nothing allow-listed,
// that is a refusal (deny-by-default; the design chose this over
// allow-with-warning, so a fixed-channel post from a non-slack trigger has to
// say which channel).
type ScopeContexter interface {
	ContextScope(dim string, t core.Trigger) string
}

// ContextScope resolves the implicitly-allowed value for one dimension on this
// connector instance, in precedence order:
//
//  1. the implementation's own hook (the event's channel, …);
//  2. Target.Repo for the repo dimension — the dispatch's own target, which
//     core.Trigger models for every connector;
//  3. the operator's configured default for an option in that dimension
//     (`connectors.slack.options.channel`) — the operator wrote it with their
//     own credential, so a call that lands there names nothing new.
//
// "" means the dimension has no implicit value for this dispatch.
func (in *Instance) ContextScope(dim string, t core.Trigger) string {
	if in == nil || dim == "" {
		return ""
	}
	if sc, ok := in.Impl.(ScopeContexter); ok {
		if v := sc.ContextScope(dim, t); v != "" {
			return v
		}
	}
	// The dispatch's own repo — UNLESS the target was derived from untrusted
	// request data (a webhook whose `repo:` templates from the POST body).
	// "Act on your own PR" is only a safe default while the platform decides
	// which PR is yours; when the sender decides, there is no own.
	if dim == DimRepo && t.Target.Repo != "" && t.TargetTrusted {
		return t.Target.Repo
	}
	return in.defaultScope(dim)
}

// defaultScope returns the connector's own default option value for a
// dimension (deterministic: the lowest option name that carries one).
func (in *Instance) defaultScope(dim string) string {
	if len(in.DefaultOptions) == 0 || in.Decl == nil {
		return ""
	}
	best := ""
	bestOpt := ""
	for _, v := range in.Decl.Verbs {
		for _, so := range v.ScopedOptions() {
			if so.Dim != dim {
				continue
			}
			s, _ := in.DefaultOptions[so.Name].(string)
			if s == "" {
				continue
			}
			if bestOpt == "" || so.Name < bestOpt {
				best, bestOpt = s, so.Name
			}
		}
	}
	return best
}
