package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/expr"
)

// Agent-driven workflows (#36 §11): an agent programs conductor. Its final
// output may carry a `plan:` block — steps in the normal trigger grammar —
// which conductor validates against the connector schemas, admits through
// policy.agent_authored (guard.go), and runs through this same flow runner:
// deterministic, no further tokens on the happy path. Two accepted shapes,
// mirroring the memory output contract:
//
//  1. A fenced block in a text output:
//
//     ```plan
//     - id: fetch
//       uses: gh.comment
//       options: { repo: "{{.repo}}", number: "{{.pr}}", body: "on it" }
//     - run: js
//       code: return { n: 1 };
//     ```
//
//  2. A JSON output object carrying a "plan" key (works with output_schema
//     steps): a list of step objects (the standard wrapper keys unwrap).

// ParsePlan extracts an agent output's plan: contract. found=false when the
// output carries none; a malformed block is an error (the plan was the
// step's purpose — never silently dropped).
func ParsePlan(output string) ([]config.Step, bool, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, false, nil
	}
	// Shape 2: a JSON object with a "plan" key.
	var obj map[string]any
	if err := json.Unmarshal([]byte(output), &obj); err == nil {
		for _, k := range []string{"output", "result", "outputs"} {
			if inner, ok := obj[k].(map[string]any); ok {
				obj = inner
				break
			}
		}
		raw, ok := obj["plan"]
		if !ok {
			return nil, false, nil
		}
		steps, err := stepsFromAny(raw)
		if err != nil {
			return nil, true, err
		}
		return steps, true, nil
	}
	// Shape 1: a fenced ```plan block.
	body, found, err := fencedBlock(output, "plan")
	if err != nil || !found {
		return nil, found, err
	}
	var steps []config.Step
	if err := yaml.Unmarshal([]byte(body), &steps); err != nil {
		return nil, true, fmt.Errorf("plan: bad ```plan block: %w", err)
	}
	return steps, true, nil
}

// fencedBlock extracts one ```<tag> fence's body from text output.
func fencedBlock(output, tag string) (string, bool, error) {
	rest := output
	for {
		_, after, ok := strings.Cut(rest, "```"+tag)
		if !ok {
			return "", false, nil
		}
		nl := strings.IndexByte(after, '\n')
		if nl < 0 {
			if strings.TrimSpace(after) == "" {
				return "", true, fmt.Errorf("plan: unterminated ```%s block", tag)
			}
			rest = after
			continue
		}
		if strings.TrimSpace(after[:nl]) != "" { // "```plans…" prose
			rest = after
			continue
		}
		body, _, closed := strings.Cut(after[nl+1:], "```")
		if !closed {
			return "", true, fmt.Errorf("plan: unterminated ```%s block", tag)
		}
		return body, true, nil
	}
}

// stepsFromAny converts a JSON-shaped list of step objects into config.Steps
// (via a YAML re-marshal so the step grammar's tags apply).
func stepsFromAny(raw any) ([]config.Step, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("plan: must be a list of steps, got %T", raw)
	}
	b, err := yaml.Marshal(list)
	if err != nil {
		return nil, err
	}
	var steps []config.Step
	if err := yaml.Unmarshal(b, &steps); err != nil {
		return nil, fmt.Errorf("plan: bad step shape: %w", err)
	}
	return steps, nil
}

// ValidatePlanSteps checks an agent-authored step list against the connector
// schemas at emit time — the same checks `conductor validate` applies to
// config steps, minus the position-scoped reference pass (a plan renders
// against live data; a bad reference surfaces as its step's render error).
func ValidatePlanSteps(cfg *config.Config, reg *connector.Registry, steps []config.Step) error {
	var check func(where string, list []config.Step) error
	check = func(where string, list []config.Step) error {
		for i, step := range list {
			w := fmt.Sprintf("%s[%d]", where, i)
			if step.ID != "" {
				w = fmt.Sprintf("%s(%s)", w, step.ID)
			}
			switch step.Form() {
			case "verb":
				connName, verb, okCut := strings.Cut(step.Uses, ".")
				if !okCut || connName == "" || verb == "" {
					return fmt.Errorf("%s: `uses: %s` must be <connector>.<verb>", w, step.Uses)
				}
				in, ok := reg.Get(connName)
				if !ok {
					return fmt.Errorf("%s: unknown connector %q", w, connName)
				}
				vd, ok := in.Decl.Verb(verb)
				if !ok {
					return fmt.Errorf("%s: connector %q (%s) has no verb %q (verbs: %s)",
						w, connName, in.Decl.Type, verb, strings.Join(in.Decl.VerbNames(), ", "))
				}
				if err := checkStoreSelector(cfg, w, connName, step.Options); err != nil {
					return err
				}
				if !vd.Open {
					if err := connector.ValidateCallOptions(w+" options", vd.Options, step.Options, in.DefaultOptions); err != nil {
						return err
					}
				}
			case "workflow":
				if !strings.Contains(step.Workflow, "{{") {
					if _, ok := cfg.Workflows[step.Workflow]; !ok {
						if _, ok := savedWorkflowDef(step.Workflow); !ok {
							return fmt.Errorf("%s: unknown workflow %q", w, step.Workflow)
						}
					}
				}
			case "agent":
				if step.Agent != "" {
					if _, ok := cfg.Agents[step.Agent]; !ok {
						return fmt.Errorf("%s: unknown agent %q", w, step.Agent)
					}
				}
			case "code":
				if strings.TrimSpace(step.Run) == "" {
					return fmt.Errorf("%s: empty run:", w)
				}
			case "command":
				if len(step.Command) == 0 {
					return fmt.Errorf("%s: command step has no command", w)
				}
			default:
				return fmt.Errorf("%s: no recognizable step form", w)
			}
			if step.Parallel != nil {
				for bi, branch := range step.Parallel.Branches {
					if err := check(fmt.Sprintf("%s branch %d", w, bi+1), branch); err != nil {
						return err
					}
				}
			}
			if step.Compensate != nil {
				if err := check(w+" compensate", []config.Step{*step.Compensate}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return check("plan", steps)
}

// planPolicy resolves the agent_authored policy a plan runs under (the
// global policy block; scoped blocks merge most-specific-wins when a trigger
// spec is in reach).
func (r *Runner) planPolicy() *config.AgentAuthoredPolicy {
	if r.Cfg == nil || r.Cfg.Policy == nil {
		return nil
	}
	return r.Cfg.Policy.AgentAuthored
}

// planState carries one plan execution across revisions (supervise splices
// into it; compensation unwinds it).
type planState struct {
	agent     string        // authoring agent profile name
	steps     []config.Step // current (possibly revised) plan
	next      int           // resume index — committed steps never re-run
	revisions int
	committed []committedStep // for reverse-order compensation
	scope     map[string]any
	tokens    int // approximate token spend of sub-agent steps
	subAgents int // executed sub-agent units
}

type committedStep struct {
	id   string
	step config.Step
}

// runPlan validates, guards, audits, and executes one agent-authored plan.
// Returns the plan's outputs map ({"steps": n, "deterministic": bool, plus
// each step's outputs by id}).
func (r *Runner) runPlan(ctx context.Context, t core.Trigger, agentName string, plan []config.Step, shadow bool) (map[string]any, error) {
	pol := r.planPolicy()
	if err := ValidatePlanSteps(r.Cfg, r.Conns, plan); err != nil {
		r.auditPlan(t, agentName, "rejected", guardResult{}, err)
		return nil, err
	}
	res, err := guardPlan(r.Cfg, r.Conns, pol, plan)
	if err != nil {
		r.auditPlan(t, agentName, "rejected", res, err)
		return nil, err
	}
	r.auditPlan(t, agentName, "admitted", res, nil)

	if res.needsApproval && !shadow {
		if aerr := r.approvePlan(ctx, t, pol, agentName, plan, res); aerr != nil {
			return nil, aerr
		}
	}

	ctx, cancel := context.WithTimeout(ctx, pol.TimeoutOrDefault())
	defer cancel()

	st := &planState{
		agent: agentName,
		steps: plan,
		scope: r.planScope(t, agentName),
	}
	if err := r.executePlan(ctx, t, pol, st, shadow); err != nil {
		return planOutputs(st, res), err
	}
	return planOutputs(st, res), nil
}

// planScope is the child scope a plan renders in: the trigger context, but
// NO named secrets and NO preloaded vault values — agent-authored templates
// don't get ambient secret material (vault reads inside a plan are what the
// no_secret_egress gate governs).
func (r *Runner) planScope(t core.Trigger, agentName string) map[string]any {
	scope := baseData(t, nil)
	scope["steps"] = map[string]any{}
	scope["plan_author"] = agentName
	// Present-but-empty so a stray {{.secrets.x}} renders "" instead of a
	// confusing nil-deref template error; the values simply aren't here.
	scope["secrets"] = map[string]any{}
	scope["vaults"] = map[string]any{}
	return scope
}

// executePlan runs the plan's steps from the resume index, enforcing the
// runtime limits (fan-out, sub-agents, token budget). On a step failure it
// consults the supervise loop (revise/splice — see supervise.go) and, when
// that's exhausted, unwinds compensations and fails.
func (r *Runner) executePlan(ctx context.Context, t core.Trigger, pol *config.AgentAuthoredPolicy, st *planState, shadow bool) error {
	for st.next < len(st.steps) {
		i := st.next
		step := st.steps[i]
		id := stepID(step, i)

		if step.If != "" {
			ok, err := expr.Eval(step.If, st.scope)
			if err != nil {
				if ferr := r.planStepFailed(ctx, t, pol, st, id, fmt.Errorf("if: %w", err), shadow); ferr != nil {
					return ferr
				}
				continue
			}
			if !ok {
				r.audit(map[string]any{"event": "plan_step", "repo": t.Target.Repo, "number": t.Target.Number,
					"agent": st.agent, "step": id, "outcome": "skipped", "if": step.If})
				st.next = i + 1
				continue
			}
		}
		// Runtime fan-out and sub-agent limits (a for_each's size is only
		// knowable now).
		if step.ForEach != "" {
			if items, err := resolveList(step.ForEach, st.scope); err == nil && len(items) > pol.MaxFanOutOrDefault() {
				return r.haltPlan(ctx, t, st, fmt.Errorf("step %q fans out to %d items, over limits.max_fan_out %d", id, len(items), pol.MaxFanOutOrDefault()), shadow)
			}
		}
		if step.Type == "agent" {
			units := 1
			if step.ForEach != "" {
				if items, err := resolveList(step.ForEach, st.scope); err == nil {
					units = len(items)
				}
			}
			if st.subAgents+units > pol.MaxSubAgentsOrDefault() {
				return r.haltPlan(ctx, t, st, fmt.Errorf("step %q would run sub-agent %d, over limits.max_sub_agents %d", id, st.subAgents+units, pol.MaxSubAgentsOrDefault()), shadow)
			}
			st.subAgents += units
			st.tokens += len(step.Prompt) / 4
		}

		outputs, err := r.execStepWithFlow(ctx, t, step, "plan:"+id, st.scope, shadow)
		if err != nil {
			r.audit(map[string]any{"event": "plan_step", "repo": t.Target.Repo, "number": t.Target.Number,
				"agent": st.agent, "step": id, "outcome": "failed", "error": err.Error()})
			if step.ContinueOnError {
				r.recordOutputs(st.scope, id, map[string]any{"error": err.Error(), "failed": true})
				st.next = i + 1
				continue
			}
			if ferr := r.planStepFailed(ctx, t, pol, st, id, err, shadow); ferr != nil {
				return ferr
			}
			continue // a revision was spliced in; retry from st.next
		}
		r.recordOutputs(st.scope, id, outputs)
		if step.Type == "agent" {
			if b, jerr := json.Marshal(outputs); jerr == nil {
				st.tokens += len(b) / 4
			}
			if st.tokens > pol.TokensOrDefault() {
				return r.haltPlan(ctx, t, st, fmt.Errorf("plan exceeded its token budget (~%d > %d)", st.tokens, pol.TokensOrDefault()), shadow)
			}
		}
		entry := map[string]any{"event": "plan_step", "repo": t.Target.Repo, "number": t.Target.Number,
			"agent": st.agent, "step": id, "outcome": "ok"}
		if len(outputs) > 0 && r.Secrets != nil {
			entry["outputs"] = r.Secrets.RedactValue(outputs)
		}
		r.audit(entry)
		st.committed = append(st.committed, committedStep{id: id, step: step})
		st.next = i + 1

		// A mid-plan check-in: the step asks the authoring agent to review
		// its outputs (and possibly revise what remains) even on success.
		if step.EscalateTo == "agent" && !shadow {
			r.planCheckIn(ctx, t, pol, st, id, outputs)
		}
	}
	return nil
}

// planOutputs summarizes a finished (or failed) plan for the emitting step's
// outputs.
func planOutputs(st *planState, res guardResult) map[string]any {
	stepsOut := map[string]any{}
	if so, ok := st.scope["steps"].(map[string]any); ok {
		for id, v := range so {
			if m, ok := v.(map[string]any); ok {
				stepsOut[id] = m["outputs"]
			}
		}
	}
	return map[string]any{
		"steps":         len(st.steps),
		"executed":      len(st.committed),
		"revisions":     st.revisions,
		"deterministic": res.deterministic,
		"outputs":       stepsOut,
	}
}

// auditPlan records the emitted plan's admission (or rejection): the
// authorizing gate, the structural counts, and the determinism class — so
// the efficiency claim ("deterministic, token-free") is measured, not
// assumed, and every agent-authored run is attributable.
func (r *Runner) auditPlan(t core.Trigger, agent, outcome string, res guardResult, err error) {
	entry := map[string]any{
		"event": "plan", "repo": t.Target.Repo, "number": t.Target.Number, "kind": t.Kind,
		"agent": agent, "outcome": outcome, "gate": res.gate,
		"steps": res.declaredSteps, "sub_agents": res.subAgents, "deterministic": res.deterministic,
	}
	if len(res.approvalWhy) > 0 {
		entry["approval_why"] = strings.Join(res.approvalWhy, "; ")
	}
	if err != nil {
		entry["error"] = err.Error()
	}
	r.audit(entry)
}

// haltPlan stops a plan that exceeded a limit: compensations unwind, the
// failure escalates — never spin.
func (r *Runner) haltPlan(ctx context.Context, t core.Trigger, st *planState, cause error, shadow bool) error {
	r.compensatePlan(ctx, t, st, shadow)
	if r.Notif != nil {
		r.Notif.Emit(ctx, "needs_input", t, fmt.Sprintf("agent plan halted: %v", cause))
	}
	return cause
}

// compensatePlan runs committed steps' compensate: actions in reverse order,
// best-effort — an undo must never cascade the failure.
func (r *Runner) compensatePlan(ctx context.Context, t core.Trigger, st *planState, shadow bool) {
	// ctx may already be cancelled/expired (timeout halts) — compensations
	// still get a bounded window to undo.
	ctx = context.WithoutCancel(ctx)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for i := len(st.committed) - 1; i >= 0; i-- {
		c := st.committed[i]
		if c.step.Compensate == nil {
			continue
		}
		comp := *c.step.Compensate
		id := c.id + ".compensate"
		_, err := r.execStepWithFlow(ctx, t, comp, "plan:"+id, st.scope, shadow)
		outcome := "ok"
		var errStr string
		if err != nil {
			outcome, errStr = "failed", err.Error()
			r.Log("%s plan compensate %s failed (best-effort): %v", flowTag(t), id, err)
		}
		entry := map[string]any{"event": "plan_compensate", "repo": t.Target.Repo, "number": t.Target.Number,
			"agent": st.agent, "step": c.id, "outcome": outcome}
		if errStr != "" {
			entry["error"] = errStr
		}
		r.audit(entry)
	}
}
