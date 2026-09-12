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
	// approvalClasses is the SET of approve-gated classes (verb/step classes
	// plus the egress markers) — an approval grants these classes, and a
	// supervised revision may keep using them without re-approval, but may
	// not introduce new ones mid-run (see splicePlan).
	approvalClasses map[string]bool
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
	res := guardResult{gate: "allow", approvalClasses: map[string]bool{}}
	if pol == nil {
		return res, fmt.Errorf("agent-authored plans are disabled — no policy.agent_authored block (safe default); add one to opt in")
	}
	if pol.TrustFull() {
		res.gate = "trust"
	}
	secretAccess, externalTouch, internalWrite := false, false, false
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
			if class == "team" {
				// A team is a fleet: planner + reconciler + up to max_workers
				// workers (the critic runs as a gate check per worker), all
				// counted against the plan's sub-agent budget.
				res.subAgents += 2 + step.Team.MaxWorkersOrDefault()
			}
			// An agent-authored step may NEVER carry its own gate: — a step's
			// gate wins over the inherited trigger/workflow default, so an
			// emitted `gate: {run: []}` (or any weakened variant) would let the
			// agent approve its own output. The operator-configured default is
			// the ONLY gate path for agent-authored steps; the same holds for a
			// team step's per-worker gate. (Config-authored steps keep setting
			// their own — this guard only sees agent-authored plans.)
			// Every field an agent-authored step may not carry is listed in
			// ONE place (agentauthored_fields.go) — gate:, team.gate:,
			// background:, handoff:, skill:. Each is a capability the
			// operator owns; a step that set its own would be widening its
			// authority with output it wrote itself. A new such field is
			// added there, not here, so no entry path can miss it.
			if err := checkAgentAuthoredFields(w, step); err != nil {
				return err
			}
			// Nor may an agent-authored step background itself or divert its
			// review to a hand-off channel — both escape the gate the same way a
			// weakened gate: would. A background step launches a live, interactive
			// agent that runs OUTSIDE the synchronous run (the gate can't hold
			// output that never returns), and handoff: routes the review draft to
			// an agent-nominated channel instead of the operator's gate. The
			// operator opts into these in config; an emitted plan may not.
			if !pol.TrustFull() {
				switch {
				case matchAny(pol.Approve, class):
					res.needsApproval = true
					res.approvalWhy = append(res.approvalWhy, fmt.Sprintf("%s: %q is approve-gated", w, class))
					res.approvalClasses[class] = true
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
			if isInternalWrite(step.Uses) {
				internalWrite = true
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
			// Step hooks are verb calls too — held to exactly the same
			// allow/approve rules, counted against max_steps, and included
			// in the egress scan. Otherwise an allowlisted step smuggles any
			// verb out through `hooks: [{at: done, uses: …}]`.
			for hi := range step.Hooks {
				h := &step.Hooks[hi]
				hw := fmt.Sprintf("%s hook[%d]", w, hi)
				res.declaredSteps++
				if !pol.TrustFull() {
					switch {
					case matchAny(pol.Approve, h.Uses):
						res.needsApproval = true
						res.approvalWhy = append(res.approvalWhy, fmt.Sprintf("%s: %q is approve-gated", hw, h.Uses))
						res.approvalClasses[h.Uses] = true
					case matchAny(pol.Allow, h.Uses):
						// admitted freely
					default:
						return fmt.Errorf("%s: %q is not in policy.agent_authored.allow (allowed: %s)",
							hw, h.Uses, patternList(pol.Allow, pol.Approve))
					}
				}
				if pol.Identity != "" {
					injectHookIdentity(reg, h, pol.Identity)
				}
				if hookReadsSecrets(cfg, h) {
					secretAccess = true
				}
				connName, _, _ := strings.Cut(h.Uses, ".")
				if !internalConnectors[connName] {
					externalTouch = true
				}
				if isInternalWrite(h.Uses) {
					internalWrite = true
				}
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
	// The exfiltration combinations the allowlist misses (approval-gated):
	// reading a secret AND touching the outside world in one plan — or
	// reading a secret AND writing DURABLE SHARED STATE (kv/sql/memory),
	// which parks the secret where a later, individually-innocent plan can
	// read it back and post it out (the two-plan kv laundering path). The
	// runtime write barrier (plan.go) backs this up for values the static
	// scan can't see.
	if pol.EgressGated() && secretAccess && !pol.TrustFull() {
		if externalTouch {
			res.needsApproval = true
			res.approvalWhy = append(res.approvalWhy, "no_secret_egress: the plan both reads secrets and touches an external service")
			res.approvalClasses["no_secret_egress:external"] = true
		}
		if internalWrite {
			res.needsApproval = true
			res.approvalWhy = append(res.approvalWhy, "no_secret_egress: the plan reads secrets and writes durable shared state (kv/sql/memory) — a later plan could read the secret back out")
			res.approvalClasses["no_secret_egress:park"] = true
		}
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
	case "team":
		return "team"
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
// ("kv.*", "*.write", bare "*"), or the "cli" alias for command steps.
//
// It is the single matcher behind BOTH the agent-authored plan allowlist
// and the skill grant (config.SkillPolicy.Verbs), which is what lets the
// capability card, `conductor discover`, and the enforcement point agree by
// construction — see internal/flow/capability.go.
//
// conductor.* verbs are EXACT-match only: they operate the daemon itself
// (update/pause/resume/restart/reload/run), so a broad `allow: ["*"]` — or
// any glob, including "conductor.*" — must never hand them to an agent.
// Admitting one requires naming it.
func matchAny(patterns []string, class string) bool {
	exactOnly := strings.HasPrefix(class, "conductor.")
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == class {
			return true
		}
		if exactOnly {
			continue
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

// injectHookIdentity mirrors injectIdentity for a hook's verb call.
func injectHookIdentity(reg *connector.Registry, h *config.Hook, identity string) {
	connName, verb, _ := strings.Cut(h.Uses, ".")
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
	if h.Options == nil {
		h.Options = map[string]any{}
	}
	h.Options["as"] = identity
}

// hookReadsSecrets scans a hook's verb + templated fields for secret access.
func hookReadsSecrets(cfg *config.Config, h *config.Hook) bool {
	if connName, _, _ := strings.Cut(h.Uses, "."); connName != "" {
		if _, isVault := cfg.Vaults[connName]; isVault {
			return true
		}
	}
	strs := []string{h.If}
	var addVal func(v any)
	addVal = func(v any) {
		switch x := v.(type) {
		case string:
			strs = append(strs, x)
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
	addVal(h.Options)
	for _, s := range strs {
		if s == "" {
			continue
		}
		if referencesSecretScope(s) {
			return true
		}
		if calls, err := templateVaultCalls(s); err == nil && len(calls) > 0 {
			return true
		}
	}
	return false
}

// referencesSecretScope reports whether a templated string reaches the
// secrets/vaults scopes — including the field-access evasions of the plain
// ".secrets" check: {{index . "secrets" "x"}}, {{$c := .}}{{$c.secrets.x}},
// quoted/backticked forms. Any occurrence of the bare word inside a template
// action counts (word-boundaried, so `.secretsummary` doesn't); the guard
// prefers a rare false positive (an approval prompt) over a laundered read.
func referencesSecretScope(s string) bool {
	if strings.Contains(s, ".secrets") || strings.Contains(s, ".vaults") {
		return true
	}
	rest := s
	for {
		i := strings.Index(rest, "{{")
		if i < 0 {
			return false
		}
		action := rest[i:]
		if j := strings.Index(action, "}}"); j >= 0 {
			action, rest = action[:j+2], rest[i+j+2:]
		} else {
			rest = ""
		}
		if containsWord(action, "secrets") || containsWord(action, "vaults") {
			return true
		}
		if rest == "" {
			return false
		}
	}
}

// containsWord reports whether word occurs in s with non-identifier
// characters (or the string edges) on both sides.
func containsWord(s, word string) bool {
	for from := 0; ; {
		i := strings.Index(s[from:], word)
		if i < 0 {
			return false
		}
		i += from
		before, after := byte(0), byte(0)
		if i > 0 {
			before = s[i-1]
		}
		if end := i + len(word); end < len(s) {
			after = s[end]
		}
		if !isIdentByte(before) && !isIdentByte(after) {
			return true
		}
		from = i + len(word)
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// builtin connectors whose verbs never leave the box.
var internalConnectors = map[string]bool{
	"kv": true, "sql": true, "memory": true, "workflow": true, "conductor": true,
	"blob": true,
}

// internalWriteVerbs are the value-carrying writes into durable shared state
// — the parking spots the two-plan kv laundering path abuses. blob.put is one
// of them: inline text lands in the artifact store and blob.read brings it
// back, so a plan could park secret material there like a kv value.
var internalWriteVerbs = map[string]bool{
	"kv.set": true, "kv.setnx": true, "kv.merge": true, "kv.append": true,
	"memory.remember": true, "sql.exec": true, "blob.put": true,
}

// isInternalWrite reports whether a verb writes a caller-supplied value into
// durable shared state.
func isInternalWrite(uses string) bool { return internalWriteVerbs[uses] }

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
		if referencesSecretScope(s) {
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
