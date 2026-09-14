package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// AgentAuthoredPolicy governs agent-authored plans (#36 §11) — the steps an
// agent emits at runtime (a `plan:` output block, the live run_step tool,
// `workflow.run { steps }`). Safe by default, unlockable:
//
//	policy:
//	  agent_authored:
//	    allow:   [ code, kv.*, sql.query, gh.comment ]
//	    approve: [ cli, gh.merge, "*.write" ]
//	    approve_via: slack-ops                # ask-capable connector for the approval hand-off
//	    host: sandbox                         # agent code/cli NEVER on the main box
//	    identity: bot
//	    limits: { max_steps: 50, max_fan_out: 20, max_sub_agents: 5, timeout: 30m, tokens: 200k }
//	    max_revisions: 3
//	    no_secret_egress: true
//
// With NO agent_authored block, agent-authored plans are rejected entirely —
// the operator opts in. `trust: full` is the deliberate opposite: lift the
// allowlist/approve/host gates for this scope (limits still bound the run).
type AgentAuthoredPolicy struct {
	// Trust: "" (allowlist mode, the default) or "full" (lift the
	// allow/approve/host restrictions — a deliberate operator opt-in).
	Trust string `yaml:"trust,omitempty"`
	// Verbs is WHICH VERBS, ON WHAT — the access list and the per-verb
	// resource scope in one place, the same list-or-map shape `skill.verbs`
	// uses (docs/design/skill-verb-scope.md), parsed by the same code.
	//
	//	verbs: [gh.comment, kv.*, code]          access only
	//
	//	verbs:                                   access + resource scope
	//	  gh.submit_review: { repo: ["org/docs"] }
	//	  slack.post:       { channel: ["#code-reviews"] }
	//	  kv.*:             { store: [cache] }
	//	  memory.*:         { scope: ["repo:acme/shared"] }
	//	  code:             { store: [cache], scope: ["repo:acme/shared"] }
	//	  cli:              {}
	//
	// Keys are verb patterns ("gh.comment", "kv.*", "*.read") AND step
	// classes ("code", "cli"/"command", "agent", "workflow"). A step class
	// carries no connector scope — except `code`, whose `store`/`scope`
	// entries are the `run: code` ctx.kv/ctx.memory data allowlists.
	//
	// This REPLACES allow: + allow_scopes: + allow_memory_scopes:, which
	// were three shapes for one idea and left the scope sitting a long way
	// from the verb it constrained. Scope now rides WITH its verb, so
	// widening access and widening reach are the same edit and a reviewer
	// sees both at once.
	Verbs []string `yaml:"verbs"`
	// VerbScopes carries the MAP form's per-verb resource constraints,
	// keyed by verb pattern then by option name or scope dimension. Filled
	// by UnmarshalYAML; see ScopesFor.
	VerbScopes map[string]map[string][]string `yaml:"-"`
	// Approve lists classes allowed only after a dry-run + hand-off
	// approval. Approve wins over Allow when both match.
	Approve []string `yaml:"approve,omitempty"`
	// ApproveVia names an ask-capable connector the approval is presented
	// on. Empty → an approval-needing plan is rejected with a needs_input
	// escalation instead of running.
	ApproveVia string `yaml:"approve_via,omitempty"`
	// Host is the `hosts:` entry agent-authored code/command steps are
	// FORCED onto (the sandbox). Empty (without trust: full) rejects
	// code/command steps entirely — agent code never runs on the main box.
	Host string `yaml:"host,omitempty"`
	// Identity is injected as `as:` on emitted verbs that declare the
	// option (e.g. gh writes post as the bot, never as you).
	Identity string `yaml:"identity,omitempty"`
	// Limits bound a plan's size and runtime.
	Limits *AgentAuthoredLimits `yaml:"limits,omitempty"`
	// MaxRevisions caps the supervise loop (failure → agent revise) before
	// the run escalates to a human (default 3).
	MaxRevisions *int `yaml:"max_revisions,omitempty"`
	// NoSecretEgress (default true) gates the read-a-secret + write-outside
	// combination behind approval — the exfiltration pattern the allowlist
	// alone misses.
	NoSecretEgress *bool `yaml:"no_secret_egress,omitempty"`
}

// AgentAuthoredLimits bound one plan.
type AgentAuthoredLimits struct {
	MaxSteps     int        `yaml:"max_steps,omitempty"`
	MaxFanOut    int        `yaml:"max_fan_out,omitempty"`
	MaxSubAgents int        `yaml:"max_sub_agents,omitempty"`
	Timeout      Duration   `yaml:"timeout,omitempty"`
	Tokens       TokenCount `yaml:"tokens,omitempty"`
}

// Plan limit defaults — bounded even when the operator doesn't say so.
const (
	DefaultPlanMaxSteps     = 50
	DefaultPlanMaxFanOut    = 20
	DefaultPlanMaxSubAgents = 5
	DefaultPlanTimeout      = 30 * time.Minute
	DefaultPlanTokens       = 200_000
	DefaultPlanMaxRevisions = 3
)

func (p *AgentAuthoredPolicy) limits() AgentAuthoredLimits {
	if p != nil && p.Limits != nil {
		return *p.Limits
	}
	return AgentAuthoredLimits{}
}

// MaxStepsOrDefault bounds a plan's declared step count.
func (p *AgentAuthoredPolicy) MaxStepsOrDefault() int {
	if n := p.limits().MaxSteps; n > 0 {
		return n
	}
	return DefaultPlanMaxSteps
}

// MaxFanOutOrDefault bounds one for_each/parallel fan-out.
func (p *AgentAuthoredPolicy) MaxFanOutOrDefault() int {
	if n := p.limits().MaxFanOut; n > 0 {
		return n
	}
	return DefaultPlanMaxFanOut
}

// MaxSubAgentsOrDefault bounds a plan's agent steps.
func (p *AgentAuthoredPolicy) MaxSubAgentsOrDefault() int {
	if n := p.limits().MaxSubAgents; n > 0 {
		return n
	}
	return DefaultPlanMaxSubAgents
}

// TimeoutOrDefault bounds a plan's wall clock.
func (p *AgentAuthoredPolicy) TimeoutOrDefault() time.Duration {
	if d := p.limits().Timeout.D(); d > 0 {
		return d
	}
	return DefaultPlanTimeout
}

// TokensOrDefault bounds a plan's approximate token spend (agent-step
// prompts + outputs, chars/4).
func (p *AgentAuthoredPolicy) TokensOrDefault() int {
	if n := int(p.limits().Tokens); n > 0 {
		return n
	}
	return DefaultPlanTokens
}

// MaxRevisionsOrDefault caps the supervise loop.
func (p *AgentAuthoredPolicy) MaxRevisionsOrDefault() int {
	if p != nil && p.MaxRevisions != nil {
		return *p.MaxRevisions
	}
	return DefaultPlanMaxRevisions
}

// EgressGated reports whether the secret-read + external-write combination
// requires approval (default true).
func (p *AgentAuthoredPolicy) EgressGated() bool {
	return p == nil || p.NoSecretEgress == nil || *p.NoSecretEgress
}

// TrustFull reports the deliberate lift-the-allowlist opt-in.
func (p *AgentAuthoredPolicy) TrustFull() bool { return p != nil && p.Trust == "full" }

// Scope dimension names the legacy allow_* lists are aliases for. They are
// spelled out here, once, so the enforcement path never learns them: it asks
// ScopeAllow for a dimension and gets whichever spelling the operator used.
const (
	DimRepo   = "repo"
	DimStore  = "store"
	DimSecret = "secret"
	// DimScope is shared memory's dimension — which memory scope an
	// agent-facing op may touch. It is memory's own rather than a
	// connector-declared one (see flow.MemoryScopeGuard for why), but it is
	// spelled here with the rest so the `verbs:` map has one vocabulary.
	DimScope = "scope"
)

// Step CLASSES usable as `verbs:` keys alongside verb patterns. A class names
// a step FORM rather than a connector verb, so it carries no connector scope —
// except StepClassCode, whose `store`/`scope` entries are the `run: code`
// ctx.kv / ctx.memory data allowlists.
const (
	StepClassCode     = "code"
	StepClassCLI      = "cli"
	StepClassCommand  = "command"
	StepClassAgent    = "agent"
	StepClassWorkflow = "workflow"
)

// ScopesFor returns the per-option allowlists this policy attaches to one
// CONCRETE verb id or step class — every `verbs:` entry whose pattern admits
// it, merged. `kv.*: {store: [s]}` therefore constrains kv.get and kv.set
// alike, and a verb named twice gets the union: listing only ever widens.
//
// This is the plan surface's half of the rename. It replaces ScopeAllow(),
// which returned ONE flat per-dimension map for the whole policy — so
// `allow_scopes: {repo: [...]}` applied to every verb that had a repo option,
// whether or not the operator was thinking about that verb. The scope now
// belongs to the verb it was written next to.
func (p *AgentAuthoredPolicy) ScopesFor(uses string, match func(pattern, uses string) bool) map[string][]string {
	if p == nil {
		return nil
	}
	return scopesFor(p.VerbScopes, uses, match)
}

// scopesFor is the shared merge behind SkillPolicy.ScopesFor and
// AgentAuthoredPolicy.ScopesFor — one implementation so the two surfaces
// cannot drift on what a grant means.
func scopesFor(verbScopes map[string]map[string][]string, uses string, match func(pattern, uses string) bool) map[string][]string {
	if len(verbScopes) == 0 {
		return nil
	}
	pats := make([]string, 0, len(verbScopes))
	for pat := range verbScopes {
		pats = append(pats, pat)
	}
	sort.Strings(pats) // deterministic merge order
	var out map[string][]string
	for _, pat := range pats {
		if !match(pat, uses) {
			continue
		}
		for opt, vals := range verbScopes[pat] {
			if out == nil {
				out = map[string][]string{}
			}
			out[opt] = append(out[opt], vals...)
		}
	}
	return out
}

// UnmarshalYAML decodes the block, splitting the polymorphic `verbs:` key out
// of the strict field decode of everything else — the same split SkillPolicy
// does, through the same parser.
func (p *AgentAuthoredPolicy) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		*p = AgentAuthoredPolicy{}
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("agent_authored: want a block with verbs:/approve:/…, got a %s", nodeKindName(n))
	}
	rest := *n
	rest.Content = nil
	var verbs *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == "verbs" {
			verbs = n.Content[i+1]
			continue
		}
		rest.Content = append(rest.Content, n.Content[i], n.Content[i+1])
	}
	type plain AgentAuthoredPolicy
	var q plain
	if err := strictNodeDecode(&rest, &q); err != nil {
		return err
	}
	*p = AgentAuthoredPolicy(q)
	pats, scopes, err := decodeVerbGrant("policy.agent_authored.verbs", verbs)
	if err != nil {
		return err
	}
	p.Verbs, p.VerbScopes = pats, scopes
	return nil
}

// MarshalYAML re-emits the form the grant was written in, so a round-trip
// (migration emit, pack instantiation, config rewrite) does not quietly lose
// the per-verb constraints.
func (p AgentAuthoredPolicy) MarshalYAML() (any, error) {
	type plain AgentAuthoredPolicy
	q := plain(p)
	if len(p.VerbScopes) == 0 {
		return q, nil
	}
	q.Verbs = nil
	b, err := yaml.Marshal(q)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	verbs := make(map[string]any, len(p.Verbs))
	for _, pat := range p.Verbs {
		entry := map[string]any{}
		for opt, vals := range p.VerbScopes[pat] {
			entry[opt] = vals
		}
		verbs[pat] = entry
	}
	m["verbs"] = verbs
	return m, nil
}

// validateAgentAuthored checks the block's shape at load time.
func validateAgentAuthored(where string, p *AgentAuthoredPolicy, hosts map[string]HostConfig) error {
	if p == nil {
		return nil
	}
	if p.Trust != "" && p.Trust != "full" {
		return fmt.Errorf("config: %s: agent_authored.trust must be \"full\" or unset, got %q", where, p.Trust)
	}
	for _, list := range [][]string{p.Verbs, p.Approve} {
		for _, pat := range list {
			if strings.TrimSpace(pat) == "" {
				return fmt.Errorf("config: %s: agent_authored verbs/approve: empty pattern", where)
			}
		}
	}
	// The `verbs:` map's own shape (empty pattern, duplicate verb, empty
	// value) is checked by the shared decoder at unmarshal; what is left is
	// the cross-field check the decoder cannot see.
	for pat, cons := range p.VerbScopes {
		for opt, list := range cons {
			if strings.TrimSpace(opt) == "" {
				return fmt.Errorf("config: %s: agent_authored.verbs.%s: empty option name (want an option or scope dimension, e.g. repo/channel/store/scope)", where, pat)
			}
			for _, v := range list {
				if strings.TrimSpace(v) == "" {
					return fmt.Errorf("config: %s: agent_authored.verbs.%s.%s: empty value", where, pat, opt)
				}
			}
		}
	}
	// hosts == nil means the caller can't see the hosts: section (a scoped
	// policy block) — the global pass re-checks with it.
	if p.Host != "" && hosts != nil {
		hc, ok := hosts[p.Host]
		if !ok {
			return fmt.Errorf("config: %s: agent_authored.host %q is not a hosts: entry", where, p.Host)
		}
		// The sandbox host must actually sandbox (#36 iso-review H6): agent
		// code forced onto it needs the host's isolation: wrapper as the
		// second wall, or it's just a plain remote shell wearing the name.
		// `trust: full` is the documented opt-out — the same knob that lifts
		// the allow/approve/host gates lifts this requirement, deliberately.
		if hc.Isolation == nil && !p.TrustFull() {
			return fmt.Errorf("config: %s: agent_authored.host %q has no isolation: block — a sandbox host must actually isolate the agent code it runs (add `isolation: {mode: user|namespace, ...}` to hosts.%s, or opt out deliberately with agent_authored `trust: full`)", where, p.Host, p.Host)
		}
	}
	if p.MaxRevisions != nil && *p.MaxRevisions < 0 {
		return fmt.Errorf("config: %s: agent_authored.max_revisions must be >= 0", where)
	}
	return nil
}

// TokenCount parses a token budget: a number, or "200k"/"1.5m" shorthand.
type TokenCount int

// UnmarshalYAML parses "200k", "1m", or a plain integer.
func (t *TokenCount) UnmarshalYAML(unmarshal func(any) error) error {
	var n int
	if err := unmarshal(&n); err == nil {
		*t = TokenCount(n)
		return nil
	}
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("tokens: want a number or \"200k\"-style shorthand")
	}
	s = strings.TrimSpace(strings.ToLower(s))
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1_000, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1_000_000, strings.TrimSuffix(s, "m")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("tokens: bad value %q", s)
	}
	*t = TokenCount(f * mult)
	return nil
}
