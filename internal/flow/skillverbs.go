package flow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
)

// The skill verb surface (#36 §12): conductor's own verbs served to a
// dispatched agent as MCP tools — the DEFAULT way an agent acts with
// conductor's credentials, because the credential never enters the agent
// session at all: the daemon executes the verb and only inputs/outputs
// cross the socket.
//
// Gating is per-profile config (skill.verbs patterns, same matching rules as
// policy.agent_authored: path globs, conductor.* exact-match only), bound to
// the dispatch identity the session token stands for. Options arrive as
// LITERAL values — never template-rendered (an agent must not evaluate
// {{.secrets…}} scopes) and never handle-resolved (see handles.go). Outputs
// are redacted before they cross back.

// SkillIdentity is the token-bound dispatch identity the daemon hands in
// (see internal/skill; flow stays free of that dependency).
type SkillIdentity struct {
	Agent   string
	Repo    string
	Trigger string
	Number  int
	// Verbs are the profile's skill.verbs patterns.
	Verbs []string
}

// SkillVerbCatalog lists the verbs the given patterns expose, shaped as MCP
// tool declarations: {name, uses, description, inputSchema}. workflow.* and
// conductor-internal orchestration stay off this surface — agent-authored
// steps go through run_step and its policy guard instead.
func (r *Runner) SkillVerbCatalog(patterns []string) []map[string]any {
	var out []map[string]any
	if r.Conns == nil || len(patterns) == 0 {
		return out
	}
	names := append([]string(nil), r.Conns.Names()...)
	sort.Strings(names)
	for _, connName := range names {
		if connName == "workflow" || connName == "conductor" {
			continue
		}
		in, ok := r.Conns.Get(connName)
		if !ok || in.Decl == nil {
			continue
		}
		for _, vd := range in.Decl.Verbs {
			uses := connName + "." + vd.Name
			if !matchAny(patterns, uses) {
				continue
			}
			out = append(out, map[string]any{
				"name":        strings.ReplaceAll(uses, ".", "_"),
				"uses":        uses,
				"description": vd.Desc,
				"inputSchema": optionsJSONSchema(vd),
			})
		}
	}
	return out
}

// optionsJSONSchema shapes a verb's option schema as MCP tool input schema.
func optionsJSONSchema(vd connector.VerbDecl) map[string]any {
	props := map[string]any{}
	var required []string
	for name, f := range vd.Options {
		p := map[string]any{"type": jsonSchemaType(f.Type)}
		if f.Desc != "" {
			p["description"] = f.Desc
		}
		if len(f.Enum) > 0 {
			p["enum"] = f.Enum
		}
		props[name] = p
		if f.Required {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	if vd.Open {
		schema["additionalProperties"] = true
	}
	return schema
}

func jsonSchemaType(t connector.FieldType) string {
	switch t {
	case connector.TInt:
		return "integer"
	case connector.TBool:
		return "boolean"
	case connector.TFloat:
		return "number"
	case connector.TList:
		return "array"
	case connector.TMap:
		return "object"
	case connector.TString, connector.TDuration:
		return "string"
	}
	return "string"
}

// RunSkillVerb executes one verb on behalf of a token-authorized agent.
func (r *Runner) RunSkillVerb(ctx context.Context, id SkillIdentity, uses string, options map[string]any) (map[string]any, error) {
	t := core.Trigger{
		Source: "skill", Instance: "skill", Kind: id.Trigger,
		Target: core.Target{Repo: id.Repo, Number: id.Number, PR: id.Number},
	}
	deny := func(reason string) (map[string]any, error) {
		err := fmt.Errorf("skill verb %s: %s", uses, reason)
		r.auditSkillVerb(t, id.Agent, uses, options, "denied", err)
		return nil, err
	}
	connName, verb, okCut := strings.Cut(uses, ".")
	if !okCut || connName == "" || verb == "" {
		return deny("malformed verb (want connector.verb)")
	}
	// The profile gate: same matching rules as policy.agent_authored —
	// path globs, conductor.* exact-match only. workflow.* never serves
	// here (run_step owns agent-authored orchestration, with its guard).
	if connName == "workflow" || connName == "conductor" {
		return deny("not available on the skill surface (use run_step, which runs under policy.agent_authored)")
	}
	if !matchAny(id.Verbs, uses) {
		return deny("not allowed by this profile's skill.verbs")
	}
	if r.Conns == nil {
		return deny("no connectors on this daemon")
	}
	in, ok := r.Conns.Get(connName)
	if !ok {
		return deny(fmt.Sprintf("unknown connector %q", connName))
	}
	if options == nil {
		options = map[string]any{}
	}
	// Options are agent-supplied and used LITERALLY: no template rendering,
	// no {{secret}} handle resolution. The write/relay barriers from the plan
	// surface apply unconditionally — the skill surface has no approval
	// hand-off to clear them.
	if isInternalWrite(uses) && r.containsTrackedSecret(options) {
		return deny("refusing to write secret material into shared state from an agent tool call")
	}
	if !internalConnectors[connName] && r.containsTrackedSecret(options) {
		return deny("refusing to relay secret material to an external connector from an agent tool call")
	}
	// Writes post as the policy identity, never as the operator (same as
	// plan steps): inject `as:` when the verb takes it.
	if pol := r.planPolicy(); pol != nil && pol.Identity != "" {
		step := config.Step{Uses: uses, Options: options}
		injectIdentity(r.Conns, &step, pol.Identity)
		options = step.Options
	}
	merged := connector.MergeOptions(in.DefaultOptions, options)
	if r.DryRun {
		r.auditSkillVerb(t, id.Agent, uses, merged, "stubbed", nil)
		return stubOutputs(in, verb), nil
	}
	out, err := in.InvokeFinal(ctx, verb, merged)
	if err != nil {
		r.auditSkillVerb(t, id.Agent, uses, merged, "failed", err)
		return nil, fmt.Errorf("skill verb %s: %s", uses, r.redactErr(err))
	}
	r.auditSkillVerb(t, id.Agent, uses, merged, "ok", nil)
	// Outputs cross back into the agent's context: redact like every other
	// agent-visible surface.
	if r.Secrets != nil {
		if red, okRed := r.Secrets.RedactValue(out).(map[string]any); okRed {
			out = red
		}
	}
	return out, nil
}

// auditSkillVerb records one skill tool call, options redacted.
func (r *Runner) auditSkillVerb(t core.Trigger, agent, uses string, opts map[string]any, outcome string, err error) {
	entry := map[string]any{
		"event": "verb", "via": "skill", "uses": uses, "outcome": outcome,
		"agent": agent, "repo": t.Target.Repo, "number": t.Target.Number,
	}
	if r.Secrets != nil {
		entry["options"] = r.Secrets.RedactValue(opts)
	}
	if err != nil {
		entry["error"] = r.redactErr(err)
	}
	r.audit(entry)
}
