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
	granted := r.GrantedVerbs(patterns)
	out := make([]map[string]any, 0, len(granted))
	for _, g := range granted {
		out = append(out, map[string]any{
			"name":        strings.ReplaceAll(g.Uses, ".", "_"),
			"uses":        g.Uses,
			"description": g.Description(),
			"inputSchema": optionsJSONSchema(g.Decl),
		})
	}
	return out
}

// GrantedVerb is one verb a grant admits, with the registry declaration it
// was resolved from.
type GrantedVerb struct {
	// Uses is the "<connector>.<verb>" id, using the CONSUMER's connector
	// instance name (what `conductor call` takes).
	Uses string
	// Connector is the instance name half of Uses.
	Connector string
	// Decl is the verb's registry declaration — options, outputs, usage.
	Decl connector.VerbDecl
}

// Description is what the agent is told this verb is for: its Usage hint
// when the verb author wrote one, else its Desc. This is the single place
// that precedence is decided, so the capability card and the MCP tool
// description can never disagree about it.
func (g GrantedVerb) Description() string {
	if u := strings.TrimSpace(g.Decl.Usage); u != "" {
		return u
	}
	return g.Decl.Desc
}

// GrantedVerbs is THE resolution of a skill grant against the live verb
// registry: every verb the patterns admit, in a stable order.
//
// It is the single source the three agent-facing surfaces are built from —
// the capability card injected into the prompt (CapabilityCard), the MCP
// tool list and `conductor discover` (SkillVerbCatalog → the verb_list op),
// and, because RunSkillVerb enforces with the same matchAny over the same
// patterns, what the daemon will actually run. A verb can therefore never
// appear in one and be missing from another.
//
// An empty pattern list grants nothing: the surface is deny-by-default, and
// a step with no `skill:` block has no grant at all.
//
// `workflow.*` and `conductor.*` are excluded here exactly as RunSkillVerb
// refuses them — conductor's own orchestration is not reachable from the
// skill surface at any breadth, so a `["*"]` grant cannot reach it either.
func (r *Runner) GrantedVerbs(patterns []string) []GrantedVerb {
	var out []GrantedVerb
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
		// A vault is its own capability class: its verbs read and write
		// SECRET material. A broad `["*"]` grant is written to mean "the
		// ordinary connectors", and silently folding every vault into it
		// hands an agent the secret store. Vaults are therefore reachable
		// only by a pattern that NAMES the vault — an operator can still
		// grant `myvault.read` deliberately, which is the point; what they
		// can't do is grant it by accident with a wildcard.
		vault := connector.IsVault(in)
		for _, vd := range in.Decl.Verbs {
			uses := connName + "." + vd.Name
			if !matchAny(patterns, uses) {
				continue
			}
			if vault && !namesConnectorExplicitly(patterns, connName) {
				continue
			}
			out = append(out, GrantedVerb{Uses: uses, Connector: connName, Decl: vd})
		}
	}
	return out
}

// namesConnectorExplicitly reports whether any pattern names this connector
// literally in its connector segment (`v.read`, `v.*`) rather than reaching it
// through a wildcard (`*`, `*.read`).
func namesConnectorExplicitly(patterns []string, connName string) bool {
	for _, p := range patterns {
		seg, _, ok := strings.Cut(strings.TrimSpace(p), ".")
		if !ok {
			seg = strings.TrimSpace(p)
		}
		if seg == connName {
			return true
		}
	}
	return false
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
	var ferr error
	cfg.WalkSteps(func(scope config.IdentityScope, slot int, sp *config.Step) {
		if ferr != nil {
			return
		}
		name, p := config.StepLabel(scope, slot, *sp), *sp
		if p.Skill == nil || len(p.Skill.Verbs) == 0 {
			return
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
				ferr = fmt.Errorf("config: %s: skill.verbs pattern %q is not a verb — use connector.verb or a pattern like %q", name, pat, pat+".*")
				return
			}
			switch connPart {
			case "workflow", "conductor":
				ferr = fmt.Errorf("config: %s: skill.verbs pattern %q — %s.* is never served on the skill surface (agent-authored orchestration goes through run_step and its policy guard)", name, pat, connPart)
				return
			}
			if _, ok := reg.Get(connPart); !ok {
				ferr = fmt.Errorf("config: %s: skill.verbs pattern %q names unknown connector %q", name, pat, connPart)
				return
			}
		}
		for _, uses := range skillVerbUniverse(reg) {
			if !matchAny(p.Skill.Verbs, uses) {
				continue
			}
			if approveActive && matchAny(pol.Approve, uses) {
				ferr = fmt.Errorf("config: %s: skill.verbs admits %q, which policy.agent_authored.approve gates behind human approval — the skill tool surface has no approval hand-off, so this would silently skip the gate; remove it from skill.verbs (or from approve)", name, uses)
				return
			}
		}
	})
	return ferr
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
	var warns []string
	cfg.WalkSteps(func(scope config.IdentityScope, slot int, sp *config.Step) {
		name, p := config.StepLabel(scope, slot, *sp), *sp
		if p.Skill == nil {
			return
		}
		// A skill: step on a runtime the surface can't reach (#123). Every
		// known runtime IS reachable — local paseo/agent-deck via the CLI face,
		// opencode/acp via MCP tools, a remote host: via the SSH reverse tunnel
		// — so this only fires for an unresolvable runtime, caught here rather
		// than shipping a silently tool-less skill.
		if rt, ok := cfg.SkillToolsSupported(p); !ok {
			warns = append(warns, fmt.Sprintf("%s: skill: is configured but runtime %q cannot reach the conductor skill surface — the verb tools and secret broker will NOT reach this agent; point it at a defined runtime or drop the skill: block", name, rt))
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
				warns = append(warns, fmt.Sprintf("%s: skill.verbs pattern %q matches no verb on this daemon — the tool list it implies is empty (typo, or the connector is disabled)", name, pat))
			}
		}
	})
	return warns
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
	// RESOURCE SCOPING. The gate above answers "may this profile call this
	// verb"; it says nothing about WHICH repo or store the call names. The
	// plan surface has always checked that (checkVerbResources), so a
	// `skill.verbs: [gh.submit_review]` grant intended for the PR under
	// review could be turned on any repo the connector could reach simply by
	// passing a different `repo:` option. Same function, same allowlists, so
	// the two surfaces cannot drift: the dispatch's own target is implicitly
	// allowed and anything beyond it needs allow_targets/allow_stores.
	if err := r.checkVerbResources(r.planPolicy(), t, uses, options); err != nil {
		return deny(r.redactErr(err))
	}
	// Identity is a per-verb concern: a verb's own `as:` option (when it has
	// one) travels through as the agent supplied it, and the connector applies
	// its own default when it's absent — e.g. gh writes default to `me`
	// (identity.write_token). The skill layer imposes no identity of its own.
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
