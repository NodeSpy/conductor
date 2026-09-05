package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// The supervise loop (#36 §11): the authoring agent is the plan's exception
// handler. A failing step routes back to the agent's session (§10) as a
// follow-up carrying STRUCTURED failure context; the agent replies with a
// revised plan that conductor validates, guards, and splices in — resuming
// from the failed step, never re-running committed side-effecting steps.
// After max_revisions rounds the run escalates to a human (needs_input) and
// compensations unwind.

// failureContext is what a revising agent gets — structured, not a string.
type failureContext struct {
	FailedStep string           `json:"failed_step"`
	Error      string           `json:"error"`
	Executed   []string         `json:"executed_steps"`            // committed step ids, in order
	Outputs    map[string]any   `json:"outputs"`                   // committed steps' outputs by id
	Remaining  []map[string]any `json:"remaining_steps,omitempty"` // the failed step + what followed
	Revision   int              `json:"revision"`                  // 1-based revise round
	Limit      int              `json:"max_revisions"`
}

// planStepFailed handles one plan-step failure: consult the authoring
// agent's session for a revision while the budget lasts; otherwise unwind
// compensations and fail with an escalation. On a successful revision the
// plan is spliced (committed steps keep their place; the failed step and
// everything after are replaced) and execution continues at st.next.
func (r *Runner) planStepFailed(ctx context.Context, t core.Trigger, pol *config.AgentAuthoredPolicy, st *planState, id string, cause error, shadow bool) error {
	if !shadow && st.revisions < pol.MaxRevisionsOrDefault() {
		st.revisions++
		revised, ok := r.requestRevision(ctx, t, st, id, cause, pol)
		if ok {
			if err := r.splicePlan(t, pol, st, revised); err == nil {
				r.audit(map[string]any{"event": "plan_revise", "repo": t.Target.Repo, "number": t.Target.Number,
					"agent": st.agent, "failed_step": id, "revision": st.revisions, "outcome": "spliced",
					"steps": len(revised)})
				return nil // resume from st.next with the revised tail
			} else {
				r.Log("%s plan revision rejected: %v", flowTag(t), err)
				r.audit(map[string]any{"event": "plan_revise", "repo": t.Target.Repo, "number": t.Target.Number,
					"agent": st.agent, "failed_step": id, "revision": st.revisions, "outcome": "rejected",
					"error": err.Error()})
				// fall through to further rounds / escalation on the next loop
				return r.planStepFailed(ctx, t, pol, st, id, cause, shadow)
			}
		}
	}
	r.compensatePlan(ctx, t, st, shadow)
	msg := fmt.Sprintf("agent plan failed at step %q after %d revision(s): %v", id, st.revisions, cause)
	if r.Notif != nil {
		r.Notif.Emit(ctx, "needs_input", t, msg)
	}
	return fmt.Errorf("plan step %q: %w", id, cause)
}

// requestRevision delivers the structured failure context to the authoring
// agent's live session and parses the revised plan out of its reply.
// ok=false when no session is bound (no session: profile, evicted, or a
// runtime without follow-up output) or the reply carries no plan.
func (r *Runner) requestRevision(ctx context.Context, t core.Trigger, st *planState, id string, cause error, pol *config.AgentAuthoredPolicy) ([]config.Step, bool) {
	if r.Agents.Revise == nil {
		return nil, false
	}
	fc := r.failureContext(st, id, cause, pol)
	b, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return nil, false
	}
	prompt := "A step of the plan you emitted failed. Structured context:\n\n```json\n" + string(b) + "\n```\n\n" +
		"Reply with a revised plan in a ```plan fenced block (the normal step grammar). " +
		"It REPLACES the failed step and everything after it — already-executed steps will not re-run. " +
		"Reply without a plan block to give up and escalate to a human."
	output, ok, err := r.Agents.Revise(ctx, st.agent, t, prompt)
	if err != nil || !ok {
		if err != nil {
			r.Log("%s plan revise: session follow-up failed: %v", flowTag(t), err)
		}
		return nil, false
	}
	steps, found, perr := ParsePlan(output)
	if perr != nil {
		r.Log("%s plan revise: bad plan in revision: %v", flowTag(t), perr)
		return nil, false
	}
	if !found || len(steps) == 0 {
		return nil, false
	}
	return steps, true
}

// failureContext assembles the structured revise payload.
func (r *Runner) failureContext(st *planState, id string, cause error, pol *config.AgentAuthoredPolicy) failureContext {
	fc := failureContext{
		FailedStep: id,
		Error:      cause.Error(),
		Outputs:    map[string]any{},
		Revision:   st.revisions,
		Limit:      pol.MaxRevisionsOrDefault(),
	}
	for _, c := range st.committed {
		fc.Executed = append(fc.Executed, c.id)
	}
	if so, ok := st.scope["steps"].(map[string]any); ok {
		for sid, v := range so {
			if m, ok := v.(map[string]any); ok {
				out := m["outputs"]
				if r.Secrets != nil {
					if red, ok := r.Secrets.RedactValue(map[string]any{"v": out}).(map[string]any); ok {
						out = red["v"]
					}
				}
				fc.Outputs[sid] = out
			}
		}
	}
	for _, s := range st.steps[st.next:] {
		if m, err := stepAsMap(s); err == nil {
			fc.Remaining = append(fc.Remaining, m)
		}
	}
	return fc
}

// splicePlan validates + guards a revision and splices it in: committed
// steps stay, the failed step and everything after are replaced.
func (r *Runner) splicePlan(t core.Trigger, pol *config.AgentAuthoredPolicy, st *planState, revised []config.Step) error {
	if err := ValidatePlanSteps(r.Cfg, r.Conns, revised); err != nil {
		return err
	}
	full := append(append([]config.Step{}, st.steps[:st.next]...), revised...)
	res, err := guardPlan(r.Cfg, r.Conns, pol, full)
	if err != nil {
		return err
	}
	// A revision must not smuggle in approval-gated work the original run
	// never cleared: reject it back to the agent instead of silently gating.
	if res.needsApproval {
		return fmt.Errorf("revision adds approval-gated steps (%s) — not allowed mid-run", strings.Join(res.approvalWhy, "; "))
	}
	st.steps = full
	return nil
}

// planCheckIn routes a successful escalate_to: agent step's outputs back to
// the authoring session as a mid-plan check-in; a plan block in the reply
// revises the REMAINING steps (the check-in step itself is committed).
func (r *Runner) planCheckIn(ctx context.Context, t core.Trigger, pol *config.AgentAuthoredPolicy, st *planState, id string, outputs map[string]any) {
	if r.Agents.Revise == nil {
		return
	}
	b, _ := json.MarshalIndent(map[string]any{"step": id, "outputs": outputs}, "", "  ")
	prompt := "Plan check-in (step marked escalate_to: agent) — review this step's result:\n\n```json\n" + string(b) + "\n```\n\n" +
		"Reply with a ```plan block to REPLACE the remaining steps, or without one to continue as planned."
	output, ok, err := r.Agents.Revise(ctx, st.agent, t, prompt)
	if err != nil || !ok {
		return
	}
	steps, found, perr := ParsePlan(output)
	if perr != nil || !found || len(steps) == 0 {
		return
	}
	if err := r.splicePlan(t, pol, st, steps); err != nil {
		r.Log("%s plan check-in revision rejected: %v", flowTag(t), err)
		return
	}
	r.audit(map[string]any{"event": "plan_revise", "repo": t.Target.Repo, "number": t.Target.Number,
		"agent": st.agent, "failed_step": id, "revision": st.revisions, "outcome": "check_in_spliced",
		"steps": len(steps)})
}

// approvePlan is the dry-run + hand-off gate for approval-needing plans: a
// full shadow pass first (every outbound verb stubbed, audited), then the
// plan summary presented on the approve_via ask channel. No channel → the
// plan is rejected with a needs_input escalation — never run unapproved.
func (r *Runner) approvePlan(ctx context.Context, t core.Trigger, pol *config.AgentAuthoredPolicy, agentName string, plan []config.Step, res guardResult) error {
	// Dry-run preview: what WOULD run, in the audit, before anyone approves.
	preview := &planState{agent: agentName, steps: append([]config.Step{}, plan...), scope: r.planScope(t, agentName)}
	if err := r.executePlan(ctx, t, pol, preview, true); err != nil {
		r.Log("%s plan dry-run preview failed: %v", flowTag(t), err)
	}
	if pol.ApproveVia == "" {
		msg := fmt.Sprintf("agent plan by %q needs approval (%s) but policy.agent_authored.approve_via is not set — rejected after dry-run",
			agentName, strings.Join(res.approvalWhy, "; "))
		if r.Notif != nil {
			r.Notif.Emit(ctx, "needs_input", t, msg)
		}
		return fmt.Errorf("%s", msg)
	}
	in, ok := r.Conns.Get(pol.ApproveVia)
	if !ok {
		return fmt.Errorf("policy.agent_authored.approve_via: unknown connector %q", pol.ApproveVia)
	}
	askVerb := ""
	for _, v := range in.Decl.Verbs {
		if v.Ask {
			askVerb = v.Name
			break
		}
	}
	if askVerb == "" {
		return fmt.Errorf("policy.agent_authored.approve_via: connector %q has no ask verb", pol.ApproveVia)
	}
	summary := planSummary(agentName, plan, res)
	out, err := in.Invoke(ctx, askVerb, map[string]any{"prompt": summary})
	outcome := "ok"
	if err != nil {
		outcome = "failed"
	}
	r.auditVerb(t, pol.ApproveVia, askVerb, map[string]any{"prompt": "(plan approval)"}, outcome, err)
	if err != nil {
		return fmt.Errorf("plan approval ask failed: %w", err)
	}
	action, _ := out["action"].(string)
	if action != "approve" {
		r.audit(map[string]any{"event": "plan", "repo": t.Target.Repo, "number": t.Target.Number,
			"agent": agentName, "outcome": "approval_denied", "action": action})
		return fmt.Errorf("plan approval denied (%s)", action)
	}
	r.audit(map[string]any{"event": "plan", "repo": t.Target.Repo, "number": t.Target.Number,
		"agent": agentName, "outcome": "approved", "gate": "approve"})
	return nil
}

// planSummary renders the human-facing approval text.
func planSummary(agent string, plan []config.Step, res guardResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Agent %q proposes a plan (%d steps, dry-run complete). Needs approval: %s\n",
		agent, len(plan), strings.Join(res.approvalWhy, "; "))
	for i, s := range plan {
		desc := s.Uses
		switch {
		case s.Workflow != "":
			desc = "workflow " + s.Workflow
		case s.Run != "":
			desc = "run: " + s.Run
		case s.Type != "":
			desc = "type: " + s.Type
		}
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, stepID(s, i), desc)
	}
	b.WriteString("Approve to run for real; anything else rejects.")
	return b.String()
}

// stepAsMap renders one step as a JSON-shaped map for the failure context
// (via YAML so the step grammar's field names appear).
func stepAsMap(s config.Step) (map[string]any, error) {
	b, err := yaml.Marshal(s)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
