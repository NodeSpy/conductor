package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/code"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/expr"
	"github.com/NodeSpy/conductor/internal/memory"
	"github.com/NodeSpy/conductor/internal/store"
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
			// A backgrounded or handed-off step escapes the gate on agent output
			// (H3): the live agent runs outside the synchronous run, and handoff:
			// diverts the review draft to an agent-nominated channel. guardPlan
			// rejects both too — this validator refuses them independently so no
			// plan path admits one (matching the gate: posture).
			if step.Background {
				return fmt.Errorf("%s: agent-authored steps may not set background:", w)
			}
			if step.Handoff != "" {
				return fmt.Errorf("%s: agent-authored steps may not set handoff:", w)
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
				// `agent:` is a free-form attribution label now, not a
				// profile reference — nothing to resolve. What a dispatchable
				// agent step still needs is a prompt.
				if strings.TrimSpace(step.Prompt) == "" && step.Team == nil {
					return fmt.Errorf("%s: agent step has no prompt", w)
				}
			case "code":
				if strings.TrimSpace(step.Run) == "" {
					return fmt.Errorf("%s: empty run:", w)
				}
			case "command":
				if len(step.Command) == 0 {
					return fmt.Errorf("%s: command step has no command", w)
				}
			case "team":
				// A team's roles must name known agents (guardPlan admits the
				// class and counts the fleet; this validates the references,
				// mirroring config.validateTeam). The per-worker gate is not
				// checked here — guardPlan forbids agent-authored teams from
				// setting one at all.
				roles := []struct{ role, name string }{
					{"planner", step.Team.Planner}, {"worker", step.Team.Worker},
					{"critic", step.Team.Critic}, {"reconcile", step.Team.Reconcile},
				}
				for _, ro := range roles {
					if ro.name == "" {
						if ro.role == "planner" || ro.role == "worker" {
							return fmt.Errorf("%s: team needs `%s:` — a step reference, `<workflow>/<step-id>` or `<workflow>[<n>]`", w, ro.role)
						}
						continue
					}
					if _, err := cfg.FindStepRef(ro.name); err != nil {
						return fmt.Errorf("%s: team.%s: %w", w, ro.role, err)
					}
				}
			case "parallel":
				// Branches are validated by the step.Parallel recursion below.
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
			// Step hooks are verb calls — validated like any verb step.
			for hi, h := range step.Hooks {
				hw := fmt.Sprintf("%s hook[%d]", w, hi)
				switch h.At {
				case "start", "done", "fail":
				default:
					return fmt.Errorf("%s: at must be start|done|fail, got %q", hw, h.At)
				}
				connName, verb, okCut := strings.Cut(h.Uses, ".")
				if !okCut || connName == "" || verb == "" {
					return fmt.Errorf("%s: `uses: %s` must be <connector>.<verb>", hw, h.Uses)
				}
				in, ok := reg.Get(connName)
				if !ok {
					return fmt.Errorf("%s: unknown connector %q", hw, connName)
				}
				vd, ok := in.Decl.Verb(verb)
				if !ok {
					return fmt.Errorf("%s: connector %q has no verb %q", hw, connName, verb)
				}
				if err := checkStoreSelector(cfg, hw, connName, h.Options); err != nil {
					return err
				}
				if !vd.Open {
					if err := connector.ValidateCallOptions(hw+" options", vd.Options, h.Options, in.DefaultOptions); err != nil {
						return err
					}
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
// into it; compensation unwinds it) and — when the plan belongs to a
// persisted workflow run — checkpoints to disk after every committed step
// and splice, so a daemon crash/auto-update resumes AFTER the last
// committed side effect.
type planState struct {
	agent     string        // authoring agent profile name
	steps     []config.Step // current (possibly revised) plan
	next      int           // resume index — committed steps never re-run
	revisions int
	committed []committedStep // for reverse-order compensation
	scope     map[string]any
	tokens    int         // this plan's approximate sub-agent token spend (reporting)
	subAgents int         // this plan's executed sub-agent units (reporting)
	budget    *planBudget // the TREE-wide budget nested plans share (enforcement)
	// granted is the approval grant covering this plan: the approve-gated
	// classes the operator cleared at admission. A revision may reuse them;
	// only NEW classes reject (finding #8 — an approved plan stays revisable).
	granted map[string]bool
	// secretTainted flips when a step's OUTPUTS carried tracked secret
	// material (a kv.get/sql.query/memory.recall of a parked secret — the
	// read half no static scan can see). Once tainted, an unapproved plan may
	// not touch the outside world for the remainder of the run, even when the
	// relayed value was transformed past exact-substring matching.
	secretTainted bool
	persist       func() // checkpoint hook (nil = ephemeral: shadow, live, inline)
}

// checkpoint persists the plan's progress (no-op for ephemeral plans).
func (st *planState) checkpoint() {
	if st.persist != nil {
		st.persist()
	}
}

type committedStep struct {
	id   string
	step config.Step
}

// runPlan validates, guards, audits, and executes one agent-authored plan.
// runID/stepID key the crash-resume checkpoint (empty = ephemeral — shadow
// runs, the live run_step tool, workflow.run inline steps). Returns the
// plan's outputs map ({"steps": n, "deterministic": bool, plus each step's
// outputs by id}).
func (r *Runner) runPlan(ctx context.Context, t core.Trigger, agentName, runID, stepID string, plan []config.Step, shadow bool) (map[string]any, error) {
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
	// The resource allowlists (#124): deny-by-default gates on which
	// secrets/stores/targets an agent-authored plan may reference.
	if rerr := guardPlanResources(pol, t, plan); rerr != nil {
		r.auditPlan(t, agentName, "rejected", res, rerr)
		return nil, rerr
	}
	r.auditPlan(t, agentName, "admitted", res, nil)

	granted := map[string]bool{}
	if res.needsApproval && !shadow {
		if aerr := r.approvePlan(ctx, t, pol, agentName, plan, res); aerr != nil {
			return nil, aerr
		}
		granted = res.approvalClasses
	}

	budget, ctx := planBudgetFrom(ctx)
	if depth := budget.enter(); depth > MaxPlanDepth {
		budget.leave()
		err := fmt.Errorf("plan nesting depth %d exceeds the limit %d — nested plans share one budget, never a fresh one", depth, MaxPlanDepth)
		r.auditPlan(t, agentName, "rejected", res, err)
		return nil, err
	}
	defer budget.leave()

	ctx, cancel := context.WithTimeout(ctx, pol.TimeoutOrDefault())
	defer cancel()

	st := &planState{
		agent:   agentName,
		steps:   plan,
		scope:   r.planScope(t, agentName),
		budget:  budget,
		granted: granted,
	}
	return r.runPlanState(ctx, t, pol, st, res, runID, stepID, shadow)
}

// planBudget is ONE budget for a whole plan tree, threaded through the
// context: a sub-agent step whose output is itself a plan re-enters runPlan,
// and without sharing it would reset limits at every level — 5 sub-agents
// each allowed 5 sub-agents. Nested plans instead decrement the root's
// budget (steps, sub-agents, approximate tokens) and are depth-capped.
type planBudget struct {
	mu        sync.Mutex
	depth     int
	steps     int
	subAgents int
	tokens    int
}

type planBudgetKey struct{}

// MaxPlanDepth caps plan-in-agent-in-plan nesting.
const MaxPlanDepth = 4

func (b *planBudget) enter() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.depth++
	return b.depth
}

func (b *planBudget) leave() {
	b.mu.Lock()
	b.depth--
	b.mu.Unlock()
}

// addSteps/addSubAgents/addTokens increment the tree-wide counters and
// return the new totals.
func (b *planBudget) addSteps(n int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.steps += n
	return b.steps
}

func (b *planBudget) addSubAgents(n int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subAgents += n
	return b.subAgents
}

func (b *planBudget) addTokens(n int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tokens += n
	return b.tokens
}

func (b *planBudget) subAgentTotal() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.subAgents
}

// planBudgetFrom returns the tree budget on the context, creating (and
// attaching) the root one when this is the outermost plan.
func planBudgetFrom(ctx context.Context) (*planBudget, context.Context) {
	if b, ok := ctx.Value(planBudgetKey{}).(*planBudget); ok {
		return b, ctx
	}
	b := &planBudget{}
	return b, context.WithValue(ctx, planBudgetKey{}, b)
}

// planBarrierKey marks a plan execution whose internal writes must not carry
// secret material (the runtime half of no_secret_egress): active when the
// gate is on, trust isn't full, and the plan did NOT go through the approval
// hand-off (an approved plan's dry-run showed the operator exactly what
// moves where). The static guard gates the combinations it can SEE; this
// barrier catches values the scan can't (a secret laundered through a step
// output or the trigger context into kv.set).
type planBarrierKey struct{}

// planBarrier reports whether the write barrier is active on this context.
func planBarrier(ctx context.Context) bool {
	on, _ := ctx.Value(planBarrierKey{}).(bool)
	return on
}

// containsTrackedSecret reports whether any tracked secret value appears in
// a rendered options tree (exact-substring, like all redaction).
func (r *Runner) containsTrackedSecret(v map[string]any) bool {
	if r.Secrets == nil || len(v) == 0 {
		return false
	}
	red, ok := r.Secrets.RedactValue(v).(map[string]any)
	return ok && !reflect.DeepEqual(red, v)
}

// planDataGuard is the code-sandbox face of two gates. The plan WRITE
// barrier (active for unapproved+untrusted plans) vets durable value writes
// for tracked secret material — without it, an agent plan's code step could
// park secret material that `uses: kv.set` would have refused. The resource
// allowlist (#124, active for ALL agent-authored execution unless trust:
// full) vets every kv/sql touch against policy.agent_authored.allow_stores —
// the runtime belt behind the static plan scan, catching `ctx.store(name)`
// with a name no scan could see. nil for config-authored steps.
func (r *Runner) planDataGuard(ctx context.Context) code.DataGuard {
	barrier := planBarrier(ctx)
	var rp *resourcePolicy
	if agentAuthored(ctx) {
		rp = planResourcePolicy(r.planPolicy(), core.Trigger{})
	}
	if !barrier && rp == nil {
		return nil
	}
	return func(kind, op, resource string, args []any) error {
		if rp != nil && (kind == "kv" || kind == "sql") && !rp.storeOK(resource) {
			return fmt.Errorf("agent_authored allowlist: code step touches store %q — not in policy.agent_authored.allow_stores (trust: full lifts this)", resource)
		}
		if barrier && dataValueWrite(kind, op) && r.containsTrackedSecret(map[string]any{"args": args}) {
			return fmt.Errorf("no_secret_egress: refusing to write secret material into %s.%s from an agent plan code step — approval required", kind, op)
		}
		return nil
	}
}

// dataValueWrite mirrors the code bindings' value-write set: the ops that
// persist caller-supplied values (the write barrier's scope; reads and
// non-value ops pass it but still face the store allowlist).
func dataValueWrite(kind, op string) bool {
	switch kind {
	case "kv":
		return op == "set" || op == "setnx" || op == "merge" || op == "append"
	case "sql":
		return op == "exec"
	case "memory":
		return op == "remember"
	}
	return false
}

// runPlanState executes a (fresh or restored) plan state with checkpointing
// wired: persisted after every committed step and splice, removed on any
// normal completion or terminal failure — only a crash leaves a record, and
// the resume path picks it up (see execAgent).
func (r *Runner) runPlanState(ctx context.Context, t core.Trigger, pol *config.AgentAuthoredPolicy, st *planState, res guardResult, runID, stepID string, shadow bool) (map[string]any, error) {
	// Plan steps are agent-authored whatever the trust level: {{secret}}
	// boundary handles never resolve inside them (see handles.go).
	ctx = markAgentAuthored(ctx)
	// The write barrier: an unapproved plan may not persist secret material
	// into shared state (kv/sql/memory). Approved plans cleared the hand-off.
	ctx = context.WithValue(ctx, planBarrierKey{},
		pol.EgressGated() && !pol.TrustFull() && !res.needsApproval)
	if !shadow && runID != "" && stepID != "" && r.Store != nil {
		st.persist = func() {
			if err := r.Store.PutPlan(r.planRecord(st, runID, stepID)); err != nil {
				r.Log("%s plan checkpoint: %v", flowTag(t), err)
			}
		}
		st.checkpoint() // the admitted plan itself survives a crash
		defer func() { _ = r.Store.DeletePlan(runID, stepID) }()
	}
	if err := r.executePlan(ctx, t, pol, st, shadow); err != nil {
		return planOutputs(st, res), err
	}
	return planOutputs(st, res), nil
}

// planRecord snapshots a plan state for persistence, with the same taint
// scrub as workflow checkpoints: a committed vault read persists a
// re-resolve marker, other tainted values persist redacted — secrets never
// reach plans.json cleartext.
func (r *Runner) planRecord(st *planState, runID, stepID string) store.PlanRecord {
	raw, _ := yaml.Marshal(st.steps)
	stepByID := map[string]config.Step{}
	for _, c := range st.committed {
		stepByID[c.id] = c.step
	}
	outputs := map[string]map[string]any{}
	if so, ok := st.scope["steps"].(map[string]any); ok {
		for id, v := range so {
			if m, ok := v.(map[string]any); ok {
				if out, ok := m["outputs"].(map[string]any); ok {
					outputs[id] = r.scrubOutputs(stepByID[id], out)
				}
			}
		}
	}
	return store.PlanRecord{
		RunID: runID, StepID: stepID, Agent: st.agent,
		Steps: raw, Next: st.next, Revisions: st.revisions, Outputs: outputs,
	}
}

// resumePlan continues a crash-interrupted plan from its checkpoint: the
// steps are re-guarded under the CURRENT policy (approval is not re-asked —
// the original run cleared it before any step committed), committed outputs
// are restored to the scope, and execution continues at the resume index.
func (r *Runner) resumePlan(ctx context.Context, t core.Trigger, rec store.PlanRecord, shadow bool) (map[string]any, error) {
	var steps []config.Step
	if err := yaml.Unmarshal(rec.Steps, &steps); err != nil {
		_ = r.Store.DeletePlan(rec.RunID, rec.StepID)
		return nil, fmt.Errorf("plan resume: corrupt checkpoint: %w", err)
	}
	pol := r.planPolicy()
	res, err := guardPlan(r.Cfg, r.Conns, pol, steps)
	if err != nil {
		_ = r.Store.DeletePlan(rec.RunID, rec.StepID)
		r.auditPlan(t, rec.Agent, "rejected", res, fmt.Errorf("on resume: %w", err))
		return nil, fmt.Errorf("plan resume: %w", err)
	}
	r.audit(map[string]any{"event": "plan", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "agent": rec.Agent, "outcome": "resumed", "next": rec.Next, "steps": len(steps)})
	budget, ctx := planBudgetFrom(ctx)
	budget.enter()
	defer budget.leave()
	st := &planState{
		agent:     rec.Agent,
		steps:     steps,
		next:      rec.Next,
		revisions: rec.Revisions,
		scope:     r.planScope(t, rec.Agent),
		budget:    budget,
		granted:   res.approvalClasses, // the original run cleared these pre-commit
	}
	for id, out := range rec.Outputs {
		r.recordOutputs(st.scope, id, r.restoreOutputs(ctx, t, steps, id, out, st.scope))
	}
	// Committed steps are restored for scope/audit purposes but NOT for
	// compensation — their compensate: actions already ran or will only run
	// for steps committed in THIS process (undo state may be stale after a
	// crash; escalation covers the gap).
	ctx, cancel := context.WithTimeout(ctx, pol.TimeoutOrDefault())
	defer cancel()
	return r.runPlanState(ctx, t, pol, st, res, rec.RunID, rec.StepID, shadow)
}

// guardSavedWorkflow re-admits a saved (agent-promoted) workflow's steps
// under the current policy at every run: a deep copy takes the guard's
// host/identity rewrites, a guard rejection refuses the run, and
// approval-gated content goes through the dry-run + hand-off gate. The
// admission is audited like any plan.
func (r *Runner) guardSavedWorkflow(ctx context.Context, t core.Trigger, name string, saved *SavedWorkflow, shadow bool) ([]config.Step, error) {
	pol := r.planPolicy()
	steps, err := deepCopySteps(saved.Steps)
	if err != nil {
		return nil, fmt.Errorf("saved workflow %q: %w", name, err)
	}
	agent := saved.Source.Step
	if agent == "" {
		agent = "saved"
	}
	res, err := guardPlan(r.Cfg, r.Conns, pol, steps)
	if err != nil {
		r.auditPlan(t, agent, "rejected", res, fmt.Errorf("saved workflow %q: %w", name, err))
		return nil, fmt.Errorf("saved workflow %q: %w", name, err)
	}
	// A saved workflow is agent-authored: the resource allowlists (#124)
	// apply on every run, under the CURRENT policy.
	if rerr := guardPlanResources(pol, t, steps); rerr != nil {
		r.auditPlan(t, agent, "rejected", res, fmt.Errorf("saved workflow %q: %w", name, rerr))
		return nil, fmt.Errorf("saved workflow %q: %w", name, rerr)
	}
	r.auditPlan(t, agent, "admitted", res, nil)
	if res.needsApproval && !shadow {
		if aerr := r.approvePlan(ctx, t, pol, agent, steps, res); aerr != nil {
			return nil, fmt.Errorf("saved workflow %q: %w", name, aerr)
		}
	}
	return steps, nil
}

// deepCopySteps clones a step list (nested maps included) so guard rewrites
// never mutate the persisted registry copy.
func deepCopySteps(steps []config.Step) ([]config.Step, error) {
	b, err := yaml.Marshal(steps)
	if err != nil {
		return nil, err
	}
	var out []config.Step
	if err := yaml.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// RunLiveStep executes ONE agent-authored step right now — the run_step
// live tool (#36 §11). The step is validated and guarded exactly like a
// plan: block; the trigger is reconstructed from the tool's baked-in
// dispatch provenance, so key rendering and audit attribution match the
// launching run.
func (r *Runner) RunLiveStep(ctx context.Context, src memory.Source, number int, stepMap map[string]any) (map[string]any, error) {
	steps, err := stepsFromAny([]any{stepMap})
	if err != nil {
		return nil, err
	}
	t := core.Trigger{
		Source: "live", Instance: "live", Kind: src.Trigger,
		Target: core.Target{Repo: src.Repo, Number: number, PR: number},
	}
	agent := src.Step
	if agent == "" {
		agent = "live"
	}
	return r.runPlan(ctx, t, agent, "", "", steps, false)
}

// WorkflowCatalog exposes the workflow.list catalog (the workflow_list live
// tool reads it through the daemon wiring).
func (r *Runner) WorkflowCatalog() map[string]any { return r.workflowCatalog() }

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
		if total := st.budget.addSteps(1); total > pol.MaxStepsOrDefault() {
			return r.haltPlan(ctx, t, st, fmt.Errorf("step %q is execution unit %d, over limits.max_steps %d (cumulative across nested plans)", id, total, pol.MaxStepsOrDefault()), shadow)
		}
		if step.Type == "agent" {
			units := 1
			if step.ForEach != "" {
				if items, err := resolveList(step.ForEach, st.scope); err == nil {
					units = len(items)
				}
			}
			if total := st.budget.addSubAgents(units); total > pol.MaxSubAgentsOrDefault() {
				return r.haltPlan(ctx, t, st, fmt.Errorf("step %q would run sub-agent %d, over limits.max_sub_agents %d (cumulative across nested plans)", id, total, pol.MaxSubAgentsOrDefault()), shadow)
			}
			st.subAgents += units
			st.tokens += len(step.Prompt) / 4
			st.budget.addTokens(len(step.Prompt) / 4)
		}
		if step.Team != nil {
			// A team step is a whole fleet — a planner, a reconcile pass, and up
			// to MaxWorkers parallel workers — not one sub-agent. guardPlan counts
			// it statically (guard.go: 2 + MaxWorkers), but the runtime cumulative
			// budget only counted agent steps, so a nested plan could spin up
			// teams without them ever charging against max_sub_agents / the token
			// budget. Count the fleet here too (#57 M9).
			per := 2 + step.Team.MaxWorkersOrDefault()
			units := per
			if step.ForEach != "" {
				if items, err := resolveList(step.ForEach, st.scope); err == nil {
					units = per * len(items)
				}
			}
			if total := st.budget.addSubAgents(units); total > pol.MaxSubAgentsOrDefault() {
				return r.haltPlan(ctx, t, st, fmt.Errorf("step %q spawns a team of %d sub-agents (execution unit %d), over limits.max_sub_agents %d (cumulative across nested plans)", id, units, total, pol.MaxSubAgentsOrDefault()), shadow)
			}
			st.subAgents += units
			st.tokens += len(step.Prompt) / 4
			st.budget.addTokens(len(step.Prompt) / 4)
		}

		// Once a step's outputs carried tracked secret material, an unapproved
		// plan may not touch the outside world at all (the exact-substring
		// relay barrier in execVerbStep misses transformed values; the taint
		// doesn't).
		if planBarrier(ctx) && st.secretTainted && stepTouchesOutside(&step) {
			terr := fmt.Errorf("no_secret_egress: this plan read secret material (see the audit) — refusing the external step %q; approval required", id)
			r.audit(map[string]any{"event": "plan_step", "repo": t.Target.Repo, "number": t.Target.Number,
				"agent": st.agent, "step": id, "outcome": "blocked", "barrier": "secret_read_taint"})
			if ferr := r.planStepFailed(ctx, t, pol, st, id, terr, shadow); ferr != nil {
				return ferr
			}
			continue
		}

		// Plan-step hooks fire like any workflow step's (they were guarded
		// with the plan — see guardPlan's hook walk).
		r.runHooks(ctx, t, step.Hooks, "start", st.scope, "plan step "+id)
		outputs, err := r.execStepWithFlow(ctx, t, step, "plan:"+id, "plan:"+id, st.scope, shadow)
		if err != nil {
			errStr := r.redactErr(err)
			fdata := cloneData(st.scope)
			fdata["error"] = errStr
			fdata["failed_step"] = id
			r.runHooks(ctx, t, step.Hooks, "fail", fdata, "plan step "+id)
			r.audit(map[string]any{"event": "plan_step", "repo": t.Target.Repo, "number": t.Target.Number,
				"agent": st.agent, "step": id, "outcome": "failed", "error": errStr})
			if step.ContinueOnError {
				r.recordOutputs(st.scope, id, map[string]any{"error": errStr, "failed": true})
				st.next = i + 1
				continue
			}
			if ferr := r.planStepFailed(ctx, t, pol, st, id, err, shadow); ferr != nil {
				return ferr
			}
			continue // a revision was spliced in; retry from st.next
		}
		if planBarrier(ctx) && !st.secretTainted && r.containsTrackedSecret(outputs) {
			st.secretTainted = true
			r.audit(map[string]any{"event": "plan_step", "repo": t.Target.Repo, "number": t.Target.Number,
				"agent": st.agent, "step": id, "outcome": "secret_read", "barrier": "secret_read_taint"})
		}
		r.recordOutputs(st.scope, id, outputs)
		r.runHooks(ctx, t, step.Hooks, "done", st.scope, "plan step "+id)
		if step.Type == "agent" || step.Team != nil {
			n := 0
			if b, jerr := json.Marshal(outputs); jerr == nil {
				n = len(b) / 4
			}
			st.tokens += n
			if total := st.budget.addTokens(n); total > pol.TokensOrDefault() {
				return r.haltPlan(ctx, t, st, fmt.Errorf("plan tree exceeded its token budget (~%d > %d, cumulative across nested plans)", total, pol.TokensOrDefault()), shadow)
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
		st.checkpoint()

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
		entry["error"] = r.redactErr(err)
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
		_, err := r.execStepWithFlow(ctx, t, comp, "plan:"+id, "plan:"+id, st.scope, shadow)
		outcome := "ok"
		var errStr string
		if err != nil {
			outcome, errStr = "failed", r.redactErr(err)
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
