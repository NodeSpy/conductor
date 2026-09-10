package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
	// Allow lists step classes the agent may emit freely: step forms
	// ("code", "cli"/"command", "agent", "workflow") and verb patterns
	// ("gh.comment", "kv.*", "*.read").
	Allow []string `yaml:"allow,omitempty"`
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
	// AllowSecrets / AllowStores / AllowTargets are the RESOURCE allowlists
	// for agent-authored workflows (#124): which secrets an agent-authored
	// step may reference (vault entries "<vault>/<key>", "<vault>/*", legacy
	// named secrets), which kv/sql stores it may touch, and which
	// repos/targets it may address ("owner/repo", "owner/*") BEYOND the one
	// the workflow was triggered for (the triggering target is implicitly
	// allowed). DENY BY DEFAULT: an unset/empty list means agent-authored
	// steps may not reference that resource kind at all. "*" grants all of
	// one kind; trust: full lifts all three. Config-authored steps are
	// untouched by these lists.
	AllowSecrets []string `yaml:"allow_secrets,omitempty"`
	AllowStores  []string `yaml:"allow_stores,omitempty"`
	AllowTargets []string `yaml:"allow_targets,omitempty"`
	// AllowMemoryScopes is the same allowlist for shared memory: which
	// memory scopes an agent-authored step may read, write or forget BEYOND
	// its own triggering scope (implicitly allowed, as the triggering target
	// is for AllowTargets). Deny-by-default like the rest — unset means an
	// agent-authored step touches only its own scope, so an unscoped recall
	// returns its own entries rather than every tenant's. "*" grants all;
	// trust: full lifts it. The reserved `global` bucket is NOT grantable
	// here: it is refused unconditionally on write (memory.CheckAgentScope).
	AllowMemoryScopes []string `yaml:"allow_memory_scopes,omitempty"`
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

// validateAgentAuthored checks the block's shape at load time.
func validateAgentAuthored(where string, p *AgentAuthoredPolicy, hosts map[string]HostConfig) error {
	if p == nil {
		return nil
	}
	if p.Trust != "" && p.Trust != "full" {
		return fmt.Errorf("config: %s: agent_authored.trust must be \"full\" or unset, got %q", where, p.Trust)
	}
	for _, list := range [][]string{p.Allow, p.Approve} {
		for _, pat := range list {
			if strings.TrimSpace(pat) == "" {
				return fmt.Errorf("config: %s: agent_authored allow/approve: empty pattern", where)
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
