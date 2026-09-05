package flow

import (
	"fmt"
	"path"
	"strings"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
)

// The plan guard: every agent-authored step (a plan: output block, the live
// run_step tool, workflow.run { steps }) is checked STRUCTURALLY against
// policy.agent_authored BEFORE anything runs — a plan referencing a
// non-allowed verb is rejected up front, never left to the agent's honor.
//
// Safe by default: no agent_authored block anywhere → plans are rejected
// entirely. The allowlist admits step classes ("code", "cli", "agent",
// "workflow", "gh.comment", "kv.*", "*.read"); the approve list admits them
// only behind a dry-run + hand-off approval; `trust: full` is the deliberate
// opt-in that lifts allow/approve/host (limits still bind). The guard also
// REWRITES what it admits: code/cli steps are forced onto the sandbox host,
// and the configured identity is injected on verbs that take `as:`.

// guardResult reports how a plan was admitted.
type guardResult struct {
	// gate is the authorizing gate for the audit trail: allow | approve | trust.
	gate string
	// needsApproval: at least one step (or the secret-egress combination)
	// requires the dry-run + hand-off approval before a real run.
	needsApproval bool
	// approvalWhy names what tripped the approval gate.
	approvalWhy []string
	// declaredSteps / subAgents are the structural counts checked against limits.
	declaredSteps int
	subAgents     int
	// deterministic: no sub-agent steps — the plan runs token-free.
	deterministic bool
}

// guardPlan admits or rejects an agent-authored plan, mutating steps in
// place with the policy's host/identity rewrites. cfg/reg supply the
// connector and vault universe for classification.
func guardPlan(cfg *config.Config, reg *connector.Registry, pol *config.AgentAuthoredPolicy, steps []config.Step) (guardResult, error) {
	res := guardResult{gate: "allow"}
	if pol == nil {
		return res, fmt.Errorf("agent-authored plans are disabled — no policy.agent_authored block (safe default); add one to opt in")
	}
	if pol.TrustFull() {
		res.gate = "trust"
	}
	secretAccess, externalTouch := false, false
	var walk func(where string, list []config.Step) error
	walk = func(where string, list []config.Step) error {
		for i := range list {
			step := &list[i]
			w := fmt.Sprintf("%s[%d]", where, i)
			if step.ID != "" {
				w = fmt.Sprintf("%s(%s)", w, step.ID)
			}
			res.declaredSteps++
			class := stepClass(cfg, step)
			if class == "" {
				return fmt.Errorf("%s: no recognizable step form", w)
			}
			if class == "agent" {
				res.subAgents++
			}
			if !pol.TrustFull() {
				switch {
				case matchAny(pol.Approve, class):
					res.needsApproval = true
					res.approvalWhy = append(res.approvalWhy, fmt.Sprintf("%s: %q is approve-gated", w, class))
				case matchAny(pol.Allow, class):
					// admitted freely
				default:
					return fmt.Errorf("%s: %q is not in policy.agent_authored.allow (allowed: %s)",
						w, class, patternList(pol.Allow, pol.Approve))
				}
				// Agent code/cli never runs on the main box: force the
				// sandbox host, or reject when none is configured.
				if class == "code" || class == "command" {
					if pol.Host == "" {
						return fmt.Errorf("%s: agent-authored %s steps need policy.agent_authored.host (the sandbox) — refusing to run on the main box", w, class)
					}
					step.Host = pol.Host
					step.SSH = nil
				}
			}
			// Identity: emitted verbs post as the policy identity, not you.
			if pol.Identity != "" && step.Uses != "" {
				injectIdentity(reg, step, pol.Identity)
			}
			if stepReadsSecrets(cfg, step) {
				secretAccess = true
			}
			if stepTouchesOutside(step) {
				externalTouch = true
			}
			// Nested agent-authored actions are held to the same rules.
			if step.Parallel != nil {
				if len(step.Parallel.Branches) > pol.MaxFanOutOrDefault() {
					return fmt.Errorf("%s: %d parallel branches exceed limits.max_fan_out %d", w, len(step.Parallel.Branches), pol.MaxFanOutOrDefault())
				}
				for bi, branch := range step.Parallel.Branches {
					if err := walk(fmt.Sprintf("%s branch %d", w, bi+1), branch); err != nil {
						return err
					}
				}
			}
			if step.Compensate != nil {
				comp := []config.Step{*step.Compensate}
				if err := walk(w+" compensate", comp); err != nil {
					return err
				}
				*step.Compensate = comp[0] // keep the guard's host/identity rewrites
			}
			if step.EscalateTo != "" && step.EscalateTo != "agent" {
				return fmt.Errorf("%s: escalate_to must be \"agent\", got %q", w, step.EscalateTo)
			}
		}
		return nil
	}
	if err := walk("plan", steps); err != nil {
		return res, err
	}
	if res.declaredSteps == 0 {
		return res, fmt.Errorf("plan has no steps")
	}
	if res.declaredSteps > pol.MaxStepsOrDefault() {
		return res, fmt.Errorf("plan declares %d steps, over limits.max_steps %d", res.declaredSteps, pol.MaxStepsOrDefault())
	}
	if res.subAgents > pol.MaxSubAgentsOrDefault() {
		return res, fmt.Errorf("plan declares %d sub-agent steps, over limits.max_sub_agents %d", res.subAgents, pol.MaxSubAgentsOrDefault())
	}
	// The exfiltration combination the allowlist misses: reading a secret AND
	// touching the outside world in one plan is approval-gated (even under
	// trust: full the audit records it; the gate applies in allowlist mode).
	if pol.EgressGated() && secretAccess && externalTouch && !pol.TrustFull() {
		res.needsApproval = true
		res.approvalWhy = append(res.approvalWhy, "no_secret_egress: the plan both reads secrets and touches an external service")
	}
	res.deterministic = res.subAgents == 0
	if res.needsApproval && res.gate == "allow" {
		res.gate = "approve"
	}
	return res, nil
}

// stepClass names a step for allow/approve matching: "workflow", "agent",
// "command", "code", or "<connector>.<verb>".
func stepClass(cfg *config.Config, step *config.Step) string {
	switch step.Form() {
	case "workflow":
		return "workflow"
	case "agent":
		return "agent"
	case "command":
		return "command"
	case "code":
		return "code"
	case "verb":
		return step.Uses
	}
	return ""
}

// matchAny reports whether class matches any pattern: exact, path-glob
// ("kv.*", "*.write"), or the "cli" alias for command steps.
func matchAny(patterns []string, class string) bool {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == class {
			return true
		}
		if (p == "cli" || p == "command") && class == "command" {
			return true
		}
		if ok, err := path.Match(p, class); err == nil && ok {
			return true
		}
	}
	return false
}

func patternList(allow, approve []string) string {
	out := append(append([]string{}, allow...), approve...)
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

// injectIdentity sets options.as on a verb step when its declaration takes
// the option — gh writes post as the policy identity, never as you.
func injectIdentity(reg *connector.Registry, step *config.Step, identity string) {
	connName, verb, _ := strings.Cut(step.Uses, ".")
	in, ok := reg.Get(connName)
	if !ok || in.Decl == nil {
		return
	}
	vd, ok := in.Decl.Verb(verb)
	if !ok {
		return
	}
	if _, takesAs := vd.Options["as"]; !takesAs {
		return
	}
	if step.Options == nil {
		step.Options = map[string]any{}
	}
	step.Options["as"] = identity
}

// builtin connectors whose verbs never leave the box.
var internalConnectors = map[string]bool{
	"kv": true, "sql": true, "memory": true, "workflow": true, "conductor": true,
}

// stepTouchesOutside reports whether a step can reach the outside world: any
// verb on a non-builtin connector, or a code/command step (which can call
// out even from the sandbox).
func stepTouchesOutside(step *config.Step) bool {
	switch step.Form() {
	case "code", "command":
		return true
	case "verb":
		connName, _, _ := strings.Cut(step.Uses, ".")
		return !internalConnectors[connName]
	}
	return false
}

// stepReadsSecrets reports whether a step reaches secret material: a vault
// connector verb, a {{ vault … }} template call, or a .secrets/.vaults
// reference anywhere in its templated fields.
func stepReadsSecrets(cfg *config.Config, step *config.Step) bool {
	if step.Uses != "" {
		connName, _, _ := strings.Cut(step.Uses, ".")
		if _, isVault := cfg.Vaults[connName]; isVault {
			return true
		}
	}
	for _, s := range stepTemplateStrings(step) {
		if strings.Contains(s, ".secrets") || strings.Contains(s, ".vaults") {
			return true
		}
		if calls, err := templateVaultCalls(s); err == nil && len(calls) > 0 {
			return true
		}
	}
	return false
}

// stepTemplateStrings collects every templated string a step carries.
func stepTemplateStrings(step *config.Step) []string {
	var out []string
	add := func(s string) {
		if s != "" {
			out = append(out, s)
		}
	}
	add(step.If)
	add(step.Prompt)
	add(step.Code)
	add(step.ForEach)
	add(step.WorkDir)
	out = append(out, step.Command...)
	out = append(out, step.Args...)
	for _, v := range step.Env {
		add(v)
	}
	var addVal func(v any)
	addVal = func(v any) {
		switch x := v.(type) {
		case string:
			add(x)
		case map[string]any:
			for _, e := range x {
				addVal(e)
			}
		case []any:
			for _, e := range x {
				addVal(e)
			}
		}
	}
	addVal(step.Options)
	addVal(step.With)
	return out
}
