package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// The team step (#36 §19): ONE unit of work split across agents.
//
//	plan      — the planner agent decomposes the step's prompt into subtasks
//	            (a structured `subtasks:` output, schema-enforced);
//	work      — each subtask dispatches the worker profile in parallel; every
//	            worker gets its own isolated worktree through the ordinary
//	            dispatch machinery (and the profile's §15 isolation), plus a
//	            per-worker gate — the team's explicit checks and the critic
//	            judge (§16 machinery: verdicts, revise loop, escalation);
//	reconcile — the reconciler agent (default: the planner) merges, seeing
//	            every worker's outputs, workdir, and proposed diff.
//
// This is deliberately DISTINCT from for_each/parallel: those fan many
// events/items over the same step; a team decomposes one task at runtime.
// Budgets (§14), usage accounting, history, and events all apply per agent
// dispatch, because every role runs through the same execAgent path.

// teamSubtask is one planner-emitted unit.
type teamSubtask struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

// teamPlanSchema is enforced on the planner's output.
var teamPlanSchema = map[string]any{
	"type":     "object",
	"required": []any{"subtasks"},
	"properties": map[string]any{
		"subtasks": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type":     "object",
				"required": []any{"id", "prompt"},
				"properties": map[string]any{
					"id":     map[string]any{"type": "string"},
					"prompt": map[string]any{"type": "string"},
				},
			},
		},
	},
}

// execTeam runs one team step to completion.
func (r *Runner) execTeam(ctx context.Context, t core.Trigger, step config.Step, id string, data map[string]any, shadow bool) (map[string]any, string, error) {
	spec := step.Team
	maxWorkers := spec.MaxWorkersOrDefault()
	// Team roles carry their own gates (workers: team.gate + critic;
	// reconciler: the step's gate) — a trigger/workflow default gate must not
	// leak onto the planner, whose output is a plan, not a change.
	ctx = context.WithValue(ctx, gateKey{}, (*config.GateSpec)(nil))

	// ---- plan ---------------------------------------------------------
	planPrompt := step.Prompt + fmt.Sprintf(`

You are the PLANNER of an agent team. Decompose the task above into at most %d
independent subtasks that can be implemented in parallel by separate agents in
separate worktrees (avoid overlapping files where possible). Output JSON:
{"subtasks": [{"id": "short-slug", "prompt": "full instructions for one worker"}]}`, maxWorkers)
	plannerStep := config.Step{Type: "agent", Agent: spec.Planner, Prompt: planPrompt,
		Checkout: step.Checkout, WorkDir: step.WorkDir, Env: step.Env, OutputSchema: teamPlanSchema}
	planOut, _, err := r.execAgent(ctx, t, plannerStep, id+":plan", data, shadow)
	if err != nil {
		return nil, "", fmt.Errorf("team plan: %w", err)
	}
	subtasks, err := parseSubtasks(planOut, maxWorkers)
	if err != nil {
		return nil, "", fmt.Errorf("team plan: %w", err)
	}
	r.Log("%s team %s: planner %s emitted %d subtask(s)", flowTag(t), id, spec.Planner, len(subtasks))
	r.audit(map[string]any{"event": "team", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "step": id, "phase": "plan", "planner": spec.Planner, "subtasks": len(subtasks)})

	// ---- work (parallel, gated) ---------------------------------------
	workerGate, extraChecks := r.teamWorkerGate(spec)
	type workerResult struct {
		Subtask teamSubtask
		Outputs map[string]any
		Err     error
	}
	results := make([]workerResult, len(subtasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxWorkers)
	for i, st := range subtasks {
		wg.Add(1)
		go func(i int, st teamSubtask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			local := cloneData(data)
			local["team"] = map[string]any{
				"task":    step.Prompt,
				"subtask": map[string]any{"id": st.ID, "prompt": st.Prompt},
			}
			wstep := config.Step{Type: "agent", Agent: spec.Worker,
				Prompt:   fmt.Sprintf("You are one WORKER of an agent team on this overall task:\n\n%s\n\nYOUR subtask (%s):\n\n%s\n\nWork only your subtask, in this worktree.", step.Prompt, st.ID, st.Prompt),
				Checkout: step.Checkout, Env: step.Env, Gate: workerGate}
			wctx := withTeamChecks(ctx, extraChecks)
			// Parallel workers off one trigger derive the same branch name
			// under checkout branch-off; the subtask id keeps each worker's
			// branch/worktree distinct (#36 review M10).
			wctx = dispatch.WithBranchSuffix(wctx, st.ID)
			out, _, werr := r.execAgent(wctx, t, wstep, fmt.Sprintf("%s:%s", id, st.ID), local, shadow)
			results[i] = workerResult{Subtask: st, Outputs: out, Err: werr}
		}(i, st)
	}
	wg.Wait()

	workers := map[string]any{}
	var failed []string
	for _, res := range results {
		if res.Outputs == nil {
			res.Outputs = map[string]any{}
		}
		workers[res.Subtask.ID] = res.Outputs
		if res.Err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", res.Subtask.ID, r.redactErr(res.Err)))
		}
	}
	outputs := map[string]any{
		"subtasks": subtasksAsScope(subtasks),
		"workers":  workers,
	}
	r.audit(map[string]any{"event": "team", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "step": id, "phase": "work", "worker": spec.Worker,
		"subtasks": len(subtasks), "failed": len(failed)})
	if len(failed) > 0 {
		return outputs, "", fmt.Errorf("team workers failed (%d/%d): %s", len(failed), len(subtasks), strings.Join(failed, "; "))
	}

	// ---- reconcile ----------------------------------------------------
	reconciler := spec.ReconcilerOrPlanner()
	var b strings.Builder
	fmt.Fprintf(&b, "You are the RECONCILER of an agent team. The overall task:\n\n%s\n\n", step.Prompt)
	b.WriteString("Each worker completed its subtask in its own worktree. Merge their work into ONE coherent change in YOUR worktree (cherry-pick/apply/replicate as appropriate), resolve conflicts and inconsistencies, and finish the task. Worker results:\n")
	for _, res := range results {
		fmt.Fprintf(&b, "\n### subtask %s\nworktree: %v\n", res.Subtask.ID, res.Outputs["workdir"])
		if diff, ok := res.Outputs["diff"].(string); ok && diff != "" {
			fmt.Fprintf(&b, "proposed diff:\n%s\n", clipText(diff, 16<<10))
		}
		if text, ok := res.Outputs["text"].(string); ok && text != "" {
			// A worker's raw reply feeds the reconciler (another agent). Scrub
			// tracked secrets first — the same boundary the diff is scrubbed at
			// (its source redacts before it lands in Outputs["diff"]). Redact
			// before clipping so a secret straddling the clip point can't survive.
			fmt.Fprintf(&b, "worker notes: %s\n", clipText(r.redactText(text), 2000))
		}
	}
	rstep := config.Step{Type: "agent", Agent: reconciler, Prompt: b.String(),
		Checkout: step.Checkout, WorkDir: step.WorkDir, Env: step.Env, Gate: step.Gate}
	recOut, raw, err := r.execAgent(ctx, t, rstep, id+":reconcile", data, shadow)
	if err != nil {
		return outputs, raw, fmt.Errorf("team reconcile: %w", err)
	}
	for k, v := range recOut {
		// The reconciler's outputs are the team's face (its diff/workdir are
		// the merged change); plan/worker detail stays under subtasks/workers.
		outputs[k] = v
	}
	r.audit(map[string]any{"event": "team", "repo": t.Target.Repo, "number": t.Target.Number,
		"kind": t.Kind, "step": id, "phase": "reconcile", "reconciler": reconciler})
	return outputs, raw, nil
}

// teamWorkerGate composes each worker's gate: the team's explicit checks
// plus, when a critic is set, the implicit critic check (a §16 agent check
// with a mandatory pass verdict) injected via the ctx check registry.
func (r *Runner) teamWorkerGate(spec *config.TeamSpec) (*config.GateSpec, map[string]config.Step) {
	var run []string
	var maxRev *int
	if spec.Gate != nil {
		run = append(run, spec.Gate.Run...)
		maxRev = spec.Gate.MaxRevisions
	}
	extra := map[string]config.Step{}
	if spec.Critic != "" {
		extra[teamCriticCheck] = config.Step{Type: "agent", Agent: spec.Critic,
			Prompt: "You are the CRITIC of an agent team. Review the worker's proposed change in {{.gate.workdir}} " +
				"for its subtask:\n\n{{.team.subtask.prompt}}\n\nJudge correctness, scope discipline, and quality. " +
				`Output JSON: {"pass": true|false, "reason": "…"}`,
			OutputSchema: map[string]any{"type": "object", "required": []any{"pass"},
				"properties": map[string]any{"pass": map[string]any{"type": "boolean"}}}}
		run = append(run, teamCriticCheck)
	}
	if len(run) == 0 {
		return nil, nil
	}
	return &config.GateSpec{Run: run, MaxRevisions: maxRev}, extra
}

// teamCriticCheck is the implicit critic check's registry name.
// The ':' is load-rejected in config check names, so no operator config can
// shadow (or be shadowed by) this name (#36 review L11).
const teamCriticCheck = "team:critic"

// teamChecksKey carries a team's ephemeral checks to runGate's lookup.
type teamChecksKey struct{}

func withTeamChecks(ctx context.Context, checks map[string]config.Step) context.Context {
	if len(checks) == 0 {
		return ctx
	}
	return context.WithValue(ctx, teamChecksKey{}, checks)
}

// teamCheck resolves an ephemeral team check by name.
func teamCheck(ctx context.Context, name string) (config.Step, bool) {
	m, _ := ctx.Value(teamChecksKey{}).(map[string]config.Step)
	chk, ok := m[name]
	return chk, ok
}

// parseSubtasks reads the planner's structured output.
func parseSubtasks(outputs map[string]any, max int) ([]teamSubtask, error) {
	raw, ok := outputs["subtasks"]
	if !ok {
		return nil, fmt.Errorf("planner output carries no subtasks list (it must emit {\"subtasks\": [{id, prompt}, …]})")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var subs []teamSubtask
	if err := json.Unmarshal(b, &subs); err != nil {
		return nil, fmt.Errorf("unparseable subtasks list: %w", err)
	}
	if len(subs) == 0 {
		return nil, fmt.Errorf("planner emitted zero subtasks")
	}
	if len(subs) > max {
		return nil, fmt.Errorf("planner emitted %d subtasks, over the team's max_workers %d", len(subs), max)
	}
	seen := map[string]bool{}
	for i, st := range subs {
		if strings.TrimSpace(st.ID) == "" || strings.TrimSpace(st.Prompt) == "" {
			return nil, fmt.Errorf("subtask %d: id and prompt are required", i)
		}
		if seen[st.ID] {
			return nil, fmt.Errorf("duplicate subtask id %q", st.ID)
		}
		seen[st.ID] = true
	}
	return subs, nil
}

// subtasksAsScope renders the plan for the template scope.
func subtasksAsScope(subs []teamSubtask) []any {
	out := make([]any, len(subs))
	for i, st := range subs {
		out[i] = map[string]any{"id": st.ID, "prompt": st.Prompt}
	}
	return out
}
