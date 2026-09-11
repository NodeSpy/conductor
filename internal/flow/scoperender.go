package flow

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/NodeSpy/conductor/internal/core"
)

// Rendering a SCOPE ALLOWLIST entry (docs/design/scope-templating.md).
//
// `allow_scopes: {channel: ["#pr-{{.number}}"]}` is a security check that
// happens to be a template, which makes it a different job from rendering a
// step's options — and the differences all point the same way: a check must
// not be steerable by what it is checking, must not read anything it could
// leak, and must not do work.
//
//	CONTEXT   a CLOSED SET of platform-assigned facts (scopeFacts): the
//	          number, owner, name, repo and kind. Not the option value being
//	          checked — that is the thing matched, never a source — and not
//	          any free text the PR author chose.
//	FUNCS     default and coalesce, and nothing else. No kv, no vault, no
//	          secret: an authorization check that performs a read is a check
//	          that can be made to do work, and one that reads a secret is one
//	          that can be made to leak it a character at a time.
//	FAILURE   fail-closed. A template that errors or renders empty matches
//	          nothing (see resourcePolicy.expand).

// scopeTemplateFuncs is the RESTRICTED function set an allowlist entry may
// call — a strict subset of templateFuncs, pinned here rather than derived
// from it so that adding a side-effecting func to the step renderer can never
// silently hand it to a security check.
var scopeTemplateFuncs = template.FuncMap{
	"default":  templateFuncs["default"],
	"coalesce": templateFuncs["coalesce"],
}

// renderScopePattern renders one allowlist entry. The caller treats an error
// (and an empty result) as "matches nothing".
func renderScopePattern(pattern string, data map[string]any) (string, error) {
	if !strings.Contains(pattern, "{{") {
		return pattern, nil
	}
	t, err := template.New("scope").Option("missingkey=zero").Funcs(scopeTemplateFuncs).Parse(pattern)
	if err != nil {
		return "", fmt.Errorf("scope pattern %q: %w", snippet(pattern), err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("scope pattern %q: %w", snippet(pattern), err)
	}
	// An absent key renders as "<no value>"; a pattern built from a fact this
	// dispatch doesn't have must match nothing, not match a literal marker.
	out := b.String()
	if strings.Contains(out, "<no value>") {
		return "", nil
	}
	return out, nil
}

// scopeFacts is the CLOSED SET of facts a scope allowlist entry may
// interpolate. It is an allowlist, and that is the whole point of it.
//
// The first cut was a denylist — baseData minus a handful of secret-bearing
// keys — which passed everything else through, including `head_ref`,
// `title`, `comment_body`, `author` and `labels`. Those are free text the PR
// AUTHOR chooses, copied verbatim out of the webhook. An operator writing the
// natural extension of the documented idioms —
//
//	channel: ["{{.head_ref}}"]        # or repo: ["{{.head_ref}}/prod"]
//
// could then be FORGED: an attacker names their branch `general` and the
// entry renders to a channel they were never granted. A denylist also fails
// in the direction that hurts — the next enriched context fact a connector
// adds is silently interpolatable, and nobody reviews a field for that.
//
// So: only facts the PLATFORM assigns, which the person who opened the PR
// cannot choose.
//
//	number  the issue/PR number GitHub allocated
//	owner   the repo's owner, from the repo the event fired for
//	name    that repo's name
//	repo    owner/name
//	kind    the trigger kind conductor itself resolved (review_requested, …)
//
// Everything else renders empty and, fail-closed, matches nothing. Adding a
// fact here is a deliberate act with one question attached: can the author of
// a pull request choose this value? If yes, it does not belong.
var scopeFacts = []string{"number", "owner", "name", "repo", "kind"}

// scopeRenderData builds the data a templated allowlist entry renders
// against: the closed set above, and nothing else.
//
// What it does NOT carry, each for its own reason:
//
//	title/head_ref/author/labels/comment_body   the PR author writes them
//	url/head/base                               author-influenced, and not
//	                                            a resource name anyway
//	any enriched t.Context fact                 not reviewed for forgeability
//	inputs                                      a workflow input can carry
//	                                            event text verbatim
//	previous-step outputs                       agent-authored
//	the option value being checked               it is the thing matched
//
// The secret redactor still runs over the result. Nothing in the closed set
// should ever hold secret material, which is exactly why it is cheap to keep
// the belt on: if one ever does, the render shows its marker, not its value.
func (r *Runner) scopeRenderData(t core.Trigger) map[string]any {
	facts := map[string]any{
		"number": t.Target.Number,
		"owner":  t.Target.Owner,
		"name":   t.Target.Name,
		"repo":   t.Target.Repo,
		"kind":   t.Kind,
	}
	// Built from the struct fields directly, never from baseData: a fact that
	// is not in scopeFacts must be absent by CONSTRUCTION, not by deletion.
	d := make(map[string]any, len(scopeFacts))
	for _, k := range scopeFacts {
		d[k] = facts[k]
	}
	if r != nil && r.Secrets != nil {
		if red, ok := r.Secrets.RedactValue(d).(map[string]any); ok {
			d = red
		}
	}
	return d
}
