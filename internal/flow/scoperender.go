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
//	CONTEXT   the dispatch's own trusted facts only (scopeRenderData): the
//	          target, the event, the workflow's inputs. Never the option value
//	          being checked — that is the thing matched, never a source — and
//	          never secret material.
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

// secretBearingKeys are the template-scope keys that hold SECRET MATERIAL.
// They are removed from the render data outright: `secrets`/`vaults` are the
// declared scopes, and slack's `slack_bot_token` is the shape a connector's
// own trigger context takes when it publishes a credential for a step to use.
// Tracked values anywhere else are redacted separately, so this list is a
// belt, not the whole trousers.
var secretBearingKeys = []string{"secrets", "vaults", "slack_bot_token", "gh_token", "app_token"}

// scopeRenderData builds the data a templated allowlist entry renders against:
// the dispatch's own facts, minus everything a security check must not see.
//
// It starts from baseData with NO secrets map, drops the secret-bearing keys a
// trigger context can carry, adds the workflow's `inputs` when the caller has
// them, and finally runs the whole map through the secret redactor — so even a
// credential that reached the trigger context under a name nobody listed comes
// out as its redaction marker rather than its value.
//
// What it deliberately does NOT carry: the step scope's previous-step outputs
// (an agent step's output is agent-authored text, and an allowlist that could
// be steered by it would be steerable by the agent) and, above all, the option
// value being checked.
func (r *Runner) scopeRenderData(t core.Trigger, stepData map[string]any) map[string]any {
	d := baseData(t, nil) // nil secrets → no `secrets` key at all
	for _, k := range secretBearingKeys {
		delete(d, k)
	}
	// Workflow inputs are operator-authored plumbing for this run, so an
	// allowlist may key off them: `repo: ["{{.inputs.org}}/*"]`.
	if inputs, ok := stepData["inputs"]; ok {
		d["inputs"] = inputs
	}
	if r != nil && r.Secrets != nil {
		if red, ok := r.Secrets.RedactValue(d).(map[string]any); ok {
			d = red
		}
	}
	return d
}
