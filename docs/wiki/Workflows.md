# Workflows: triggers, steps, hooks

A trigger is four keys: `on:` (what fires it), `filters:` (whether it fires —
keys from the event's schema, all AND-ed), `steps:` (the workflow), and
`hooks:` (lifecycle actions). Plus optional `group:` ([[Grouping]]),
`policy:` ([[Policy]]), `name:` (a variant label for dedup state; required
for `conductor run`), and `enabled:`.

`on:` takes one `<connector>.<event>`, the built-in `manual` source
(`conductor run <name>` fires it on demand), or a **list** of sources fanning
into the same steps — each item a bare `conn.event` or a one-key map
`conn.event: { filters, policy, hooks }` scoped to that source. See
[[Configuration]] for the list grammar and merge semantics.

```yaml
triggers:
  - on: gh.merge_conflict
    steps:
      - { id: fix, type: agent, agent: fixer, prompt: "Resolve the conflict on {{.repo}}#{{.pr}}." }
    hooks:
      - { at: start, uses: slack-ops.post, options: { text: "on it: {{.repo}}#{{.pr}}" } }
      - { at: done,  uses: slack-ops.post, options: { text: "resolved {{.repo}}#{{.pr}}" } }
      - { at: fail,  uses: slack-ops.post, options: { text: "failed: {{.error}}" } }
```

## Step forms

A step is one of five forms (all share `id` and `if`):

- `type: agent` — run an agent profile: `agent`, `prompt`, `checkout`,
  `output_schema`, `background` (+ `handoff`, see [[Hand-offs]]),
  `rerequest_review`, `workdir`, `env`.
- `type: command` — a host command (POSIX sh semantics; argv list). With
  `host:` it runs over SSH and outputs `{stdout, stderr, exit_code}`.
- `run:` — an inline code step ([[Code-Steps]]).
- `uses: <conn>.<verb>` — a service verb ([[Verbs]]). This includes the
  always-on data and memory verbs: `kv.*`/`sql.*` over `stores:` and
  `memory.*` over the `memory:` section ([[Memory]]).
- `workflow: <name>` — a reusable workflow call (below).

Agent steps have two extra memory hooks (when a `memory:` section is
configured): a `remember:` block in the agent's final output persists
post-run with the run's provenance (the output contract), and an opted-in
profile (`memory: true`) gets the scoped memories injected into its prompt —
see [[Memory]].

An agent step whose profile carries a `session:` block participates in
**session affinity**: events rendering the same key reach one live agent as
follow-up prompts instead of fresh spawns, across every trigger using that
agent — see [[Agents]]. A follow-up returns `{ agent_id, ... }` like any
agent step; on paseo its output is empty (the prompt is queued to the live
agent), so steps that read the agent's structured output should not assume a
keyed session.

## Context and scope

Templates and `if:` conditions address:

- **Trigger context** — the event's published facts (`{{.repo}}`,
  `{{.comment_body}}`, `{{.slack.channel}}` — see `conductor schema <conn>`).
- **Step outputs** — `{{.<stepid>.<field>}}` from any PRIOR step (the legacy
  `{{.steps.<id>.outputs.<field>}}` spelling also resolves).
- **Secrets** — `{{.secrets.<name>}}` from the named block.
- **The batch** — `{{.group.*}}` when the trigger groups.

Scope is positional: a step sees the trigger context plus every prior step's
outputs. Hooks see the same, scoped to when they fire — `at: start` the
trigger context only; `at: done` everything; `at: fail` everything completed
plus `{{.error}}` and `{{.failed_step}}`. A step-level hook is scoped to its
step (its `at: done` adds that step's own output). `conductor validate`
resolves every reference against the scope at its position — a start hook
reading a step output fails at load, not at 3am.

`if:` uses the pinned expression set (comparison, `&&`/`||`/`!`,
`contains()`, `exists()`, `default(x, fallback)`, `coalesce(a, b, …)`), with
paths written bare or as `{{.path}}`. `default`/`coalesce` yield the first
present, non-empty argument (nil and `""` are empty; `0` and `false` are real
values) and work bare or as a comparison's left side:
`default(sev, "low") == "high"`. Templates get the same two as functions —
`{{.sev | default "low"}}`, `{{coalesce .a .b "z"}}`.

## Hooks

`hooks:` entries are verb action units `{at, uses, options, if, id}` at
`start` (on match, before steps, synchronous), `done` (steps succeeded), or
`fail` (steps failed); multiple per phase run in order. **Hooks nest on steps
too** — the same unit under a step's own `hooks:` fires around that step, so
a step can announce itself, post its result the moment it finishes, or handle
its own failure. A failing step fires its own `at: fail` hooks, then (unless
it sets `continue_on_error`) the workflow's. Hook verbs are best-effort:
logged and audited, never fatal.

## Control flow

- `if:` — skip the step when false (skips are audited).
- `for_each: <ref>` — run the step once per element; `{{.item}}` and
  `{{.index}}` in scope; `parallel: true` fans iterations out concurrently
  (bounded). Outputs land under `{{.<id>.items}}` / `{{.<id>.count}}`.
- `parallel: [ [steps…], [steps…] ]` — concurrent branches, joined before the
  next step; branch step outputs merge into the parent scope (ids must not
  collide).
- `retry: { max, backoff }` — re-run on error; `retry: {
  while_output_matches, interval, timeout }` — re-run while the output still
  says "not ready" (the legacy defer-retry).
- `timeout:` — bound the step.
- `continue_on_error: true` — record `{error, failed: true}` as the step's
  outputs and keep going.

## Failure and resume

A step error stops the workflow (fail hooks fire, the failure is audited with
the step named, `report` shows where it stopped). Runs checkpoint each
completed top-level step in `runs.json`: a daemon restart resumes AFTER the
last completed step, so a `slack.post` that already ran never re-fires; the
interrupted step re-runs (at-least-once).

## Reusable workflows

`workflows:` holds named, parameterized step lists:

```yaml
workflows:
  assess-and-post:
    inputs:
      repo:    { type: string,  required: true }
      pr:      { type: integer, required: true }
      channel: { type: string,  default: "#reviews" }
    outputs:
      decision: "{{.triage.decision}}"
    steps:
      - { id: triage, type: agent, agent: planner, checkout: none,
          output_schema: { type: object, required: [decision], properties: { decision: { enum: [auto, manual] } } },
          prompt: "Assess {{.inputs.repo}}#{{.inputs.pr}}." }
      - { id: ping, if: "{{.triage.decision}} == manual",
          uses: slack-ops.post, options: { channel: "{{.inputs.channel}}", text: "needs a human" } }

triggers:
  - on: gh.review_requested
    steps:
      - { id: a, workflow: assess-and-post, with: { repo: "{{.repo}}", pr: "{{.pr}}" } }
      - { id: auto, if: "{{.a.decision}} == auto", uses: gh.submit_review, options: { … } }
```

Inside, inputs read as `{{.inputs.<name>}}`; the workflow also sees the
trigger context and its own steps — but NOT the caller's other step outputs
(pass those via `with:`). The caller reads declared `outputs:` off the call
step's id. A workflow may call another workflow; `validate` rejects cycles,
unknown/missing inputs, type mismatches, and outputs referencing steps that
do not exist.

A workflow may carry a `description:` — with its declared inputs/outputs it
is self-describing (`conductor schema`, `workflow.list`, and a choosing
agent all read it). `workflow:` may also be a **templated name**
(`workflow: "{{.pick}}"`) resolved at runtime against the workflow set
(config + saved): the static checks don't apply to a dynamic name, so it's
guarded by a runtime depth cap (8) and a clear unknown-name error naming the
set.

## Agent-driven workflows

An agent can *program* conductor (#36 §11): given a goal it emits a plan of
ordinary steps, conductor runs them deterministically (no further tokens on
the happy path), and the agent re-engages only on failures or to promote a
recurring pattern. Everything below runs under
[[Policy]]'s `agent_authored` block — **no block, no plans**.

**Plan (emit), two ways.** Every runtime can end its output with a `plan:`
block — a fenced ` ```plan ` YAML step list, or a `plan:` key in a JSON
(`output_schema`) output. Conductor validates it against the connector
schemas, admits it through the guard, and runs it in a child scope that has
NO named secrets or preloaded vault values. Runtimes with live tools (ACP)
additionally get `run_step` — author and run ONE step mid-run, same guard —
and `workflow_list`, over the same conductor MCP server the memory tools
ride (see [[Memory]]); paseo and remote sessions use the output contract.

**Choose (don't always author).** `workflow.list` is the catalog: every
config + saved workflow's name, description, inputs, and health.
`workflow.run { name, with, reason }` runs the pick — the rationale is
audited (`workflow_choice`) — and `workflow.run { steps }` runs an inline
plan under the full guard. Memory (§9) informs the pick; repetition
collapses to recognize → pick → run.

```yaml
# Recognize → pick → run: the agent consults the catalog and runs the fit.
triggers:
  - on: gh.issue_matched
    steps:
      - id: triage
        type: agent
        agent: planner
        prompt: |
          Goal: handle "{{.title}}". Consult the workflow catalog and prior
          memory; if a workflow fits, run it and say why. Only author fresh
          steps for genuine novelty. Reply with a ```plan block, e.g.
          - uses: workflow.run
            options: { name: assess-and-post, with: { repo: "{{.repo}}", pr: "{{.pr}}" }, reason: "..." }
```

**Supervise.** A failing plan step routes back to the authoring agent's
**session (§10)** as a follow-up with STRUCTURED context — the failed step,
the error, executed steps and their outputs, the remaining steps. The agent
replies with a revised plan; conductor re-validates, re-guards (a revision
may not smuggle in approval-gated work), splices it in, and **resumes from
the failed step** — committed side-effecting steps never re-run. After
`max_revisions` rounds the run compensates and escalates `needs_input`. A
step marked `escalate_to: agent` checks in on success too. Supervision
needs the authoring agent to keep sessions (`session:` on its profile);
without one, failures go straight to compensate + escalate.

**Compensate.** A plan step may declare `compensate:` (a nested step — its
undo). On terminal failure the committed steps' compensations run in
REVERSE order, best-effort, audited (`plan_compensate`).

**Promote.** `workflow.save { name, description, steps }` persists a
durable, versioned reusable workflow with provenance — a catalog candidate
for Choose next time. Trust is earned: a saved (or newly revised) workflow
is UNREVIEWED — it dry-runs freely but refuses a real run until `conductor
workflows review <name>` (or `trust: full`). Conductor tracks each saved
workflow's success rate; a rotting one (≥3 runs, <50% success) is flagged
and deprioritized in the catalog. `conductor workflows ls` shows it all.

```yaml
# Plan → recover → promote: fix now, and keep the pattern.
triggers:
  - on: gh.failing_checks
    steps:
      - id: fixer
        type: agent
        agent: planner              # session: on the profile → supervised revisions
        prompt: |
          CI failed on {{.repo}}#{{.pr}}. Emit a ```plan that diagnoses and
          fixes it (allowed verbs only; compensate: where a step has an undo).
          If this pattern recurs, also include a workflow.save step promoting
          it — future runs will pick it from the catalog with zero planning.
```

**Audit & honesty.** Every plan run is audited: the admission gate
(`allow`/`approve`/`trust`), rejections with reasons, per-step outcomes,
revisions, compensations, the choice rationale — and the
**deterministic-vs-hybrid** classification (a plan with no sub-agent steps
is truly token-free; one with them is hybrid, with its sub-agent count and
approximate token spend bounded by `limits.tokens`).

## File-based references

A `workflow:` resolves three ways; all are checked at load:

```yaml
- { workflow: review-flow }                                     # by name — defined inline or in any imported file
- { workflow: review-flow, import: ./workflows/review.yaml }    # name + the file it lives in (no section import needed)
- { workflow: ./workflows/review.yaml }                         # a bare file path, when the file defines ONE workflow
```

A referenced file holds a `workflows:` block or bare name→definition
entries; relative paths resolve against the config file's directory. The
bare-path form errors on a multi-workflow file (name one with `workflow:` +
`import:`). A workflow can also keep its name in the config with its body in
its own file — `workflows: { review-flow: { import: ./workflows/review.yaml } }`
— and section-level splitting (`workflows: { imports: [...] }`) is
[[Configuration]].

Steps can also act on conductor itself — `uses: conductor.update / pause /
resume / restart / reload / run` (and `gh.sweep`) — and react to it:
triggers on the `conductor.*` lifecycle events replace the old notify:
block. Hooks nest on those steps like any other, so a gated self-update
runs drain → update (announced at start/fail) → smoke-test; see
[[Configuration]] and [[Notifications]].

Related: [[Connectors]] · [[Verbs]] · [[Code-Steps]] · [[Grouping]] · [[Hand-offs]]
