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
	// Identity is the profile's skill.identity — the `as:` every as-taking
	// verb call posts under ("" falls through to agent_authored.identity).
	Identity string
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

// validateSkillProfiles rejects a config whose skill surface would EXCEED
// the plan surface (#122 R2): a verb pattern in a profile's skill.verbs must
// not admit any verb policy.agent_authored.approve gates behind human
// approval — the skill surface has no approval hand-off, so the overlap
// would silently skip the gate the operator configured. Both sides are
// pattern lists, so the check expands them against the REAL registry's verb
// universe and errors on any concrete verb both admit.
func validateSkillProfiles(cfg *config.Config, reg *connector.Registry) error {
	if cfg == nil {
		return nil
	}
	var pol *config.AgentAuthoredPolicy
	if cfg.Policy != nil {
		pol = cfg.Policy.AgentAuthored
	}
	approveActive := pol != nil && !pol.TrustFull() && len(pol.Approve) > 0
	polIdentity := ""
	if pol != nil {
		polIdentity = pol.Identity
	}
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := cfg.Agents[name]
		if p.Skill == nil || len(p.Skill.Verbs) == 0 {
			continue
		}
		// Fail-safe pattern shape checks (#122 R5a): a literal connector
		// prefix must exist in the registry, and the surfaces the skill
		// never serves are named errors instead of silent dead config.
		for _, pat := range p.Skill.Verbs {
			connPart, _, hasDot := strings.Cut(pat, ".")
			if strings.ContainsAny(connPart, "*?[") {
				continue // a globbed connector half checks at match time
			}
			if !hasDot {
				return fmt.Errorf("config: agent %q: skill.verbs pattern %q is not a verb — use connector.verb or a pattern like %q", name, pat, pat+".*")
			}
			switch connPart {
			case "workflow", "conductor":
				return fmt.Errorf("config: agent %q: skill.verbs pattern %q — %s.* is never served on the skill surface (agent-authored orchestration goes through run_step and its policy guard)", name, pat, connPart)
			}
			if _, ok := reg.Get(connPart); !ok {
				return fmt.Errorf("config: agent %q: skill.verbs pattern %q names unknown connector %q", name, pat, connPart)
			}
		}
		for _, uses := range skillVerbUniverse(reg) {
			if !matchAny(p.Skill.Verbs, uses) {
				continue
			}
			if approveActive && matchAny(pol.Approve, uses) {
				return fmt.Errorf("config: agent %q: skill.verbs admits %q, which policy.agent_authored.approve gates behind human approval — the skill tool surface has no approval hand-off, so this would silently skip the gate; remove it from skill.verbs (or from approve)", name, uses)
			}
			// A write verb (one that posts `as:` an identity) must never
			// silently post as the operator (#122 R4): require a
			// distinguished identity when such a verb is admitted.
			if p.Skill.Identity == "" && polIdentity == "" && verbTakesAs(reg, uses) {
				return fmt.Errorf("config: agent %q: skill.verbs admits %q, which posts as an identity — set skill.identity (or policy.agent_authored.identity) so skill writes post as a distinguished bot identity, never as the operator", name, uses)
			}
		}
	}
	return nil
}

// SkillWarnings lints skill.verbs patterns that match NOTHING in the built
// registry (#122 R5a) — legal (a credential-disabled connector's verbs
// vanish from the registry), but silent dead config the operator should see
// at validate time rather than as an agent's missing tool.
func SkillWarnings(cfg *config.Config, reg *connector.Registry) []string {
	if cfg == nil {
		return nil
	}
	universe := skillVerbUniverse(reg)
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	var warns []string
	for _, name := range names {
		p := cfg.Agents[name]
		if p.Skill == nil {
			continue
		}
		// A skill: profile on a runtime with no MCP launch surface (#123):
		// the tools and broker cannot reach the agent there — say so at
		// validate time instead of shipping a silently tool-less skill.
		if rt, ok := cfg.SkillToolsSupported(p); !ok {
			warns = append(warns, fmt.Sprintf("agent %q: skill: is configured but runtime %q cannot carry the conductor MCP tools — the verb tools and secret broker will NOT reach this agent; use an acp or opencode runtime, a paseo runtime with provider: claude + workspace: worktree, or drop the skill: block", name, rt))
		}
		for _, pat := range p.Skill.Verbs {
			matched := false
			for _, uses := range universe {
				if matchAny([]string{pat}, uses) {
					matched = true
					break
				}
			}
			if !matched {
				warns = append(warns, fmt.Sprintf("agent %q: skill.verbs pattern %q matches no verb on this daemon — the tool list it implies is empty (typo, or the connector is disabled)", name, pat))
			}
		}
	}
	return warns
}

// verbTakesAs reports whether a verb declares the `as:` identity option.
func verbTakesAs(reg *connector.Registry, uses string) bool {
	connName, verb, _ := strings.Cut(uses, ".")
	in, ok := reg.Get(connName)
	if !ok || in.Decl == nil {
		return false
	}
	vd, ok := in.Decl.Verb(verb)
	if !ok {
		return false
	}
	_, takesAs := vd.Options["as"]
	return takesAs
}

// skillVerbUniverse is every conn.verb class the skill surface could serve
// (workflow/conductor are never served there).
func skillVerbUniverse(reg *connector.Registry) []string {
	var out []string
	if reg == nil {
		return out
	}
	for _, connName := range reg.Names() {
		if connName == "workflow" || connName == "conductor" {
			continue
		}
		in, ok := reg.Get(connName)
		if !ok || in.Decl == nil {
			continue
		}
		for _, vd := range in.Decl.Verbs {
			out = append(out, connName+"."+vd.Name)
		}
	}
	sort.Strings(out)
	return out
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
	// The skill surface must never EXCEED the plan surface: a verb the
	// operator gated behind human approval (policy.agent_authored.approve)
	// has no approval hand-off here, so it does not run here — config
	// validation rejects the overlap up front (validateSkillProfiles); this
	// is the runtime belt for saved-policy drift.
	if pol := r.planPolicy(); pol != nil && !pol.TrustFull() && matchAny(pol.Approve, uses) {
		return deny("gated behind approval by policy.agent_authored.approve — the skill surface has no approval hand-off; use a plan step")
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
	// Writes post as the configured bot identity, never as the operator —
	// and never as whatever `as:` the agent supplied (injectIdentity
	// overwrites it). skill.identity wins; agent_authored.identity is the
	// fallback; load validation guarantees one exists when an as-taking
	// verb is admitted.
	identity := id.Identity
	if identity == "" {
		if pol := r.planPolicy(); pol != nil {
			identity = pol.Identity
		}
	}
	if identity != "" {
		step := config.Step{Uses: uses, Options: options}
		injectIdentity(r.Conns, &step, identity)
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
