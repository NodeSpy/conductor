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
	// Capture the operator's effective gate for this team step (its own gate:,
	// else the inherited trigger/workflow default) BEFORE the default is cleared
	// from ctx just below. It governs the change-producing roles — the
	// reconciler always, and each worker when the team declares no gate/critic
	// of its own — so an agent-authored team step (which guardPlan forbids from
	// setting its own gate) cannot emit an UNGATED merged change (#36 §146 F4).
	opGate := r.effectiveGate(ctx, step)
	// Team roles carry their own gates (workers: team.gate + critic, else
	// opGate; reconciler: the step's gate, else opGate) — the inherited default
	// must not leak onto the PLANNER, whose output is a plan, not a change, so
	// clear it from ctx and gate each role explicitly below.
	ctx = context.WithValue(ctx, gateKey{}, (*config.GateSpec)(nil))

	// ---- plan ---------------------------------------------------------
	planPrompt := step.Prompt + fmt.Sprintf(`

You are the PLANNER of an agent team. Decompose the task above into at most %d
independent subtasks that can be implemented in parallel by separate agents in
separate worktrees (avoid overlapping files where possible). Output JSON:
{"subtasks": [{"id": "short-slug", "prompt": "full instructions for one worker"}]}`, maxWorkers)
	plannerStep, err := r.roleStep(spec.Planner, config.Step{Type: "agent", Agent: spec.Planner, Prompt: planPrompt,
		Checkout: step.Checkout, WorkDir: step.WorkDir, Env: step.Env, OutputSchema: teamPlanSchema})
	if err != nil {
		return nil, "", err
	}
	planOut, _, err := r.execAgent(ctx, t, plannerStep, id+":plan", id+":plan", data, shadow)
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
	workerGate, extraChecks, err := r.teamWorkerGate(spec)
	if err != nil {
		return nil, "", err
	}
	if workerGate == nil {
		// No team-declared gate or critic: the operator's default gate still
		// governs each worker's change rather than leaving it ungated (F4).
		workerGate = opGate
	}
	type workerResult struct {
		Subtask teamSubtask
		Outputs map[string]any
		Err     error
	}
	// Each parallel worker off one trigger derives the SAME branch name under
	// checkout branch-off; the subtask id distinguishes them. But the id is
	// slugified for the ref, and a non-ASCII/punctuation-only id sanitizes to ""
	// (or two ids collapse to the same slug) — collapsing distinct workers onto
	// one branch/worktree. parseSubtasks only dedups RAW ids, so precompute a
	// collision-free branch suffix per worker, falling back to the worker index
	// (#36 §146 F6, review M10).
	branchSuffixes := teamBranchSuffixes(subtasks)

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
			wstep, rerr := r.roleStep(spec.Worker, config.Step{Type: "agent", Agent: spec.Worker,
				Prompt:   fmt.Sprintf("You are one WORKER of an agent team on this overall task:\n\n%s\n\nYOUR subtask (%s):\n\n%s\n\nWork only your subtask, in this worktree.", step.Prompt, st.ID, st.Prompt),
				Checkout: step.Checkout, Env: step.Env, Gate: workerGate})
			if rerr != nil {
				results[i] = workerResult{Subtask: st, Err: rerr}
				return
			}
			wctx := withTeamChecks(ctx, extraChecks)
			// A precomputed, collision-free suffix keeps each worker's
			// branch/worktree distinct even when subtask ids sanitize to the
			// same (or an empty) slug (#36 §146 F6, review M10).
			wctx = dispatch.WithBranchSuffix(wctx, branchSuffixes[i])
			out, _, werr := r.execAgent(wctx, t, wstep, fmt.Sprintf("%s:%s", id, st.ID), fmt.Sprintf("%s:%s", id, st.ID), local, shadow)
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
	// The reconciler produces the team's merged change, so it is always gated:
	// the step's own gate wins, else the operator's default (F4).
	reconcilerGate := step.Gate
	if reconcilerGate == nil {
		reconcilerGate = opGate
	}
	rstep, err := r.roleStep(reconciler, config.Step{Type: "agent", Agent: reconciler, Prompt: b.String(),
		Checkout: step.Checkout, WorkDir: step.WorkDir, Env: step.Env, Gate: reconcilerGate})
	if err != nil {
		return outputs, "", err
	}
	recOut, raw, err := r.execAgent(ctx, t, rstep, id+":reconcile", id+":reconcile", data, shadow)
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

// teamBranchSuffixes assigns each worker a git-safe, collision-free branch
// suffix. The subtask id is the readable default, but ids are slugified for the
// ref: a non-ASCII or punctuation-only id sanitizes to "" and distinct ids can
// collapse to the same slug — either would put two workers on one branch. When
// the sanitized id is empty or already taken, fall back to the worker index
// ("w<i>"), which is itself sanitized and de-duplicated so it can never clash
// with a literal "w0"-style id (#36 §146 F6).
func teamBranchSuffixes(subs []teamSubtask) []string {
	out := make([]string, len(subs))
	seen := map[string]bool{}
	for i, st := range subs {
		slug := dispatch.SanitizeBranchSuffix(st.ID)
		if slug == "" || seen[slug] {
			slug = fmt.Sprintf("w%d", i)
			for n := 0; slug == "" || seen[slug]; n++ {
				slug = dispatch.SanitizeBranchSuffix(fmt.Sprintf("w%d-%d", i, n))
			}
		}
		seen[slug] = true
		out[i] = slug
	}
	return out
}

// teamWorkerGate composes each worker's gate: the team's explicit checks
// plus, when a critic is set, the implicit critic check (a §16 agent check
// with a mandatory pass verdict) injected via the ctx check registry.
func (r *Runner) teamWorkerGate(spec *config.TeamSpec) (*config.GateSpec, map[string]config.Step, error) {
	var run []string
	var maxRev *int
	if spec.Gate != nil {
		run = append(run, spec.Gate.Run...)
		maxRev = spec.Gate.MaxRevisions
	}
	extra := map[string]config.Step{}
	if spec.Critic != "" {
		critic, err := r.roleStep(spec.Critic, config.Step{Type: "agent", Agent: spec.Critic,
			Prompt: "You are the CRITIC of an agent team. Review the worker's proposed change in {{.gate.workdir}} " +
				"for its subtask:\n\n{{.team.subtask.prompt}}\n\nJudge correctness, scope discipline, and quality. " +
				`Output JSON: {"pass": true|false, "reason": "…"}`,
			OutputSchema: map[string]any{"type": "object", "required": []any{"pass"},
				"properties": map[string]any{"pass": map[string]any{"type": "boolean"}}}})
		if err != nil {
			return nil, nil, err
		}
		extra[teamCriticCheck] = critic
		run = append(run, teamCriticCheck)
	}
	if len(run) == 0 {
		return nil, nil, nil
	}
	return &config.GateSpec{Run: run, MaxRevisions: maxRev}, extra, nil
}

// roleStep fills a synthesized role step in from the workflow step the team
// references (`<workflow>/<step-id>`, or `<workflow>[<n>]` for a step with
// no id). The fields set here win; the rest — model, workspace, skill,
// memory, guidance — come from the referenced step.
//
// IDENTITY. The role takes the referenced step's identity: its `name:` if
// it pins one, else the reference itself, which is that step's structural
// identity written out. Either way every team pointing at the same step
// shares one memory namespace, session pool, and track record — the point
// of pointing at a step rather than inlining one.
//
// A reference that resolves to nothing is a load-time error in both
// validateTeam and guardPlan, so reaching that here means something built a
// team spec that went through neither. Fail rather than silently dispatch a
// bare agent.
func (r *Runner) roleStep(role string, s config.Step) (config.Step, error) {
	role = strings.TrimSpace(role)
	if r.Cfg == nil || role == "" {
		return s, nil
	}
	base, err := r.Cfg.FindStepRef(role)
	if err != nil {
		return s, err
	}
	pinned := strings.TrimSpace(s.Name)
	config.MergeStepInto(&s, *base)
	switch {
	case pinned != "":
		s.Name = pinned
	case strings.TrimSpace(base.Name) != "":
		s.Name = base.Name
	default:
		s.Name = role
	}
	// The role runs as the team's planner/worker/critic, not in the
	// workflow the base step sits in — its own id would be misleading.
	s.ID = ""
	return s, nil
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
