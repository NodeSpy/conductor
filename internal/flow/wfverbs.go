package flow

import (
	"context"
	"fmt"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/memory"
)

// The workflow.* verbs (#36 §11), executed by the flow runner itself — it
// owns the runner, the trigger scope, the depth guard, and the plan guard,
// so `uses: workflow.run` composes with everything a workflow step can do.
//
//   - workflow.list — the catalog an agent Chooses from: every config +
//     saved workflow's name, description, inputs, and (saved) health.
//   - workflow.run { name, with, reason } — run a named workflow (the
//     Choose path; the rationale is audited).
//     workflow.run { steps } — run an inline agent-authored plan, guarded
//     by policy.agent_authored exactly like a plan: output block.
//   - workflow.save { name, description, steps } — Promote: persist a
//     durable, versioned reusable workflow (unreviewed until cleared).

// execWorkflowVerb dispatches one intercepted workflow.* call.
func (r *Runner) execWorkflowVerb(ctx context.Context, t core.Trigger, verb string, opts map[string]any, data map[string]any, shadow bool) (map[string]any, error) {
	switch verb {
	case "list":
		return r.workflowCatalog(), nil
	case "run":
		return r.workflowRun(ctx, t, opts, data, shadow)
	case "save":
		return r.workflowSave(ctx, t, opts, data, shadow)
	}
	return nil, fmt.Errorf("workflow: no verb %q", verb)
}

// workflowCatalog builds the self-describing catalog: config workflows
// first, then saved ones sorted healthy-first — a rotting workflow is
// flagged and deprioritized so Choose stops picking it.
func (r *Runner) workflowCatalog() map[string]any {
	var entries []map[string]any
	names := make([]string, 0, len(r.Cfg.Workflows))
	for n := range r.Cfg.Workflows {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		wf := r.Cfg.Workflows[n]
		inputs := map[string]any{}
		for in, spec := range wf.Inputs {
			inputs[in] = map[string]any{"type": spec.Type, "required": spec.Required}
		}
		entries = append(entries, map[string]any{
			"name": n, "description": wf.Description, "inputs": inputs, "source": "config",
		})
	}
	if st := SavedWorkflows(); st != nil {
		saved := st.All()
		// Healthy first; flagged (rotting) last so Choose deprioritizes them.
		sort.SliceStable(saved, func(i, j int) bool {
			if saved[i].Rotting() != saved[j].Rotting() {
				return !saved[i].Rotting()
			}
			return saved[i].Name < saved[j].Name
		})
		for _, w := range saved {
			e := map[string]any{
				"name": w.Name, "description": w.Description, "inputs": map[string]any{},
				"source": "saved", "version": w.Version, "reviewed": w.Reviewed,
				"runs": w.Runs(), "flagged": w.Rotting(),
			}
			if w.Runs() > 0 {
				e["success_rate"] = float64(w.Successes) / float64(w.Runs())
			}
			if w.Rotting() {
				e["description"] = w.Description + " [FLAGGED: failing lately — prefer another]"
			}
			entries = append(entries, e)
		}
	}
	out := make([]any, len(entries))
	for i, e := range entries {
		out[i] = e
	}
	return map[string]any{"workflows": out, "count": len(out)}
}

// workflowRun runs a named workflow ({name, with}) or an inline plan
// ({steps}). The choice rationale is audited either way.
func (r *Runner) workflowRun(ctx context.Context, t core.Trigger, opts map[string]any, data map[string]any, shadow bool) (map[string]any, error) {
	name, _ := opts["name"].(string)
	rawSteps, hasSteps := opts["steps"]
	reason, _ := opts["reason"].(string)
	if (name == "") == !hasSteps {
		return nil, fmt.Errorf("workflow.run: pass exactly one of name: (with with:) or steps:")
	}
	author := planAuthor(data)
	entry := map[string]any{"event": "workflow_choice", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "agent": author}
	if reason != "" {
		entry["reason"] = reason
	}
	if name != "" {
		entry["workflow"] = name
		r.audit(entry)
		with, _ := opts["with"].(map[string]any)
		// Reuse the workflow-call step path: depth guard, review gate, input
		// checks, health recording. Options were already rendered.
		return r.execWorkflowCall(ctx, t, config.Step{Workflow: name, With: with}, "workflow.run", data, shadow)
	}
	steps, err := stepsFromAny(rawSteps)
	if err != nil {
		return nil, err
	}
	entry["inline_steps"] = len(steps)
	r.audit(entry)
	// An inline plan through the verb is agent-authored by definition —
	// the same guard as a plan: output block. (No crash checkpoint here —
	// only agent-step plans persist; see Workflows.md.)
	return r.runPlan(ctx, t, author, "", "", steps, shadow)
}

// workflowSave promotes steps into the saved registry: validated against the
// connector schemas, versioned, provenance-stamped, unreviewed until cleared.
func (r *Runner) workflowSave(ctx context.Context, t core.Trigger, opts map[string]any, data map[string]any, shadow bool) (map[string]any, error) {
	st := SavedWorkflows()
	if st == nil {
		return nil, fmt.Errorf("workflow.save: no saved-workflow store configured")
	}
	name, _ := opts["name"].(string)
	if _, exists := r.Cfg.Workflows[name]; exists {
		return nil, fmt.Errorf("workflow.save: %q is a config workflow — saved workflows may not shadow it", name)
	}
	desc, _ := opts["description"].(string)
	steps, err := stepsFromAny(opts["steps"])
	if err != nil {
		return nil, err
	}
	if err := ValidatePlanSteps(r.Cfg, r.Conns, steps); err != nil {
		return nil, fmt.Errorf("workflow.save: %w", err)
	}
	// A promoted workflow is agent-authored FOREVER: it must pass the plan
	// guard at save (host/identity rewrites are persisted, non-allowed steps
	// reject) — and it is re-guarded at every run under the then-current
	// policy (see execWorkflowCall), so saving is never a laundering step.
	if _, gerr := guardPlan(r.Cfg, r.Conns, r.planPolicy(), steps); gerr != nil {
		return nil, fmt.Errorf("workflow.save: %w", gerr)
	}
	if shadow {
		r.Log("%s [dry-run] would save workflow %q (%d steps)", flowTag(t), name, len(steps))
		return map[string]any{"name": name, "reviewed": false, "stubbed": true}, nil
	}
	src := memory.SourceFrom(ctx)
	src.Step = planAuthor(data)
	w, err := st.Save(name, desc, steps, src)
	if err != nil {
		return nil, err
	}
	r.audit(map[string]any{"event": "workflow_save", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "agent": src.Step, "workflow": w.Name, "version": w.Version, "steps": len(steps)})
	return map[string]any{"name": w.Name, "version": w.Version, "reviewed": w.Reviewed}, nil
}

// planAuthor names who is driving this scope: the plan's authoring agent
// when inside one, else "config" (a human-authored step).
func planAuthor(data map[string]any) string {
	if a, ok := data["plan_author"].(string); ok && a != "" {
		return a
	}
	return "config"
}
