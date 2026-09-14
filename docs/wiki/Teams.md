# Multi-agent teams (planner / workers / critic)

A `team:` step (#36 §19) splits **one** unit of work across several agents —
deliberately distinct from `for_each`/`parallel`, which fan *many
events/items* over the same step. A team decomposes a single task at
runtime:

1. **plan** — the planner agent breaks the step's `prompt:` into subtasks
   (a schema-enforced `{"subtasks": [{id, prompt}, …]}` output, bounded by
   `max_workers`; zero, duplicate, or over-cap subtasks fail loudly);
2. **work** — each subtask dispatches the worker profile **in parallel**,
   each in its own isolated worktree (the ordinary dispatch machinery, plus
   whatever [[Isolation]] the worker's profile carries); under checkout
   `branch-off` every worker's branch name carries its subtask id as a
   suffix (`conductor/<kind>-<n>-<subtask>`), so parallel workers off one
   trigger never collide on a branch;
3. **judge** — the critic reviews each worker's proposed change through the
   [[Gates]] machinery: an implicit agent check with a mandatory
   `pass: true|false` verdict, full revise loop and escalation included;
4. **reconcile** — the reconciler (default: the planner) merges, seeing
   every worker's outputs, worktree path, and proposed diff (§17), and
   produces the combined change.

```yaml
runtimes:
  gemini: { agent: gemini }            # isolation applies to runtimes conductor launches itself

# A team's roles are STEP REFERENCES, so the steps they name have to live
# somewhere addressable. A workflow nothing calls is the usual home — the
# roles are dispatched by the team, not by the workflow.
workflows:
  roles:
    steps:
      - { id: architect,   type: agent, model: claude-opus-5, prompt: "…" }
      - { id: implementer, type: agent, runtime: gemini, workspace: worktree, prompt: "…",
          isolation: { mode: namespace, network: { egress: ["api.github.com:443"] } } }
      - { id: reviewer,    type: agent, prompt: "…" }

triggers:
  - on: gh.issue_matched
    filters: { labels_any: [epic] }
    steps:
      - id: feature
        prompt: "Implement the feature described in {{.url}}: {{.title}}"
        team:
          planner: roles/architect
          worker: roles/implementer
          critic: roles/reviewer      # optional judge per worker (gate machinery)
          reconcile: roles/architect  # optional; defaults to the planner
          max_workers: 4              # subtask cap AND parallelism bound (1..16)
          gate: { run: [ test ] }     # optional explicit checks per worker
        gate: { run: [ test, lint ] } # the STEP's gate applies to the reconciler's merged change
      - { id: notify, uses: slack-ops.post,
          options: { text: "feature merged: {{.feature.note}}\n{{.feature.diff}}" } }
```

## Semantics

- **Outputs** — the reconciler's outputs are the team step's face
  (`{{.feature.diff}}`/`{{.feature.workdir}}` are the merged change), with
  the structured detail under `{{.feature.subtasks}}` and
  `{{.feature.workers.<id>}}` (each worker's outputs, diff, workdir).
- **Failure** — any worker failing (including a gate escalation after its
  revise rounds) fails the team step with the failing subtasks named; the
  reconciler never runs on partial work. `continue_on_error`/`retry:` on the
  step apply as usual.
- **Gates compose** — `team.gate` checks + the implicit critic check run on
  every worker (critic revise loops go back to that worker). The critic
  registers under the reserved name `team:critic`; config check names may
  not contain `:` (load-rejected), so no config check can shadow it. The
  step-level `gate:` runs on the reconciler.
- **Everything is per-dispatch** — budgets (§14), usage/cost accounting,
  engagements and outcomes (§18), history and live events (§17/§20) all
  apply to each role, because every role runs through the ordinary agent
  step machinery. Session-affinity profiles (§10) behave as they do
  anywhere else.
- **Agent-authored plans** (§11) may emit `team:` steps only when `"team"`
  is in `policy.agent_authored.allow`; the whole fleet (planner +
  reconciler + `max_workers`) counts against `limits.max_sub_agents`.
- Nested scopes re-run whole on resume/retry (checkpoint parity): a team
  step re-runs from the plan.

Related: [[Gates]] · [[Isolation]] · [[Workflows]] · [[Runs]] · [[Outcomes]]
