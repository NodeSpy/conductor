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
x-templates:
  fixer: &fixer { type: agent, workspace: worktree }

triggers:
  - on: gh.merge_conflict
    steps:
      - { <<: *fixer, id: fix, prompt: "Resolve the conflict on {{.repo}}#{{.pr}}." }
    hooks:
      - { at: start, uses: slack-ops.post, options: { text: "on it: {{.repo}}#{{.pr}}" } }
      - { at: done,  uses: slack-ops.post, options: { text: "resolved {{.repo}}#{{.pr}}" } }
      - { at: fail,  uses: slack-ops.post, options: { text: "failed: {{.error}}" } }
```

## Named and qualified triggers

`triggers:` also takes a **map**, where the key is each trigger's stable
address. A key that reads as `<source>.<event>` **implies `on:`**, which
disambiguates across sources for free:

```yaml
triggers:
  github.pull_request:  { steps: [ { id: review, type: agent, prompt: "…" } ] }
  gitlab.merge_request: { steps: [ { id: review, type: agent, prompt: "…" } ] }
  pagerduty.incident:   { steps: [ { id: triage, type: agent, prompt: "…" } ] }
```

A bare event name is not an identity — two connectors can publish the same
event, and a pack needs each of its triggers addressable so you can override
exactly one. The key is that address.

For **two triggers on the same `source.event`**, give them free names and set
`on:` explicitly:

```yaml
triggers:
  review:    { on: github.pull_request, steps: [ … ] }
  autolabel: { on: github.pull_request, steps: [ … ] }
```

A free-named key with no `on:` is an error: only a `source.event` key implies
the event.

### Instances: array instead of object

A trigger's value is polymorphic. An **object** is one trigger; an **array** is
several **instances** that each fire independently:

```yaml
triggers:
  review:
    - { on: github.pull_request, filters: { repos: [me/app], labels: [ready] } }
    - { on: github.pull_request, filters: { repos: [me/api] } }
```

There is no `instances:` keyword and no per-instance name. An instance's
identity is its **content** — its `repos:`/`filters:` are what make it distinct
— so its internal handle is derived from that content. Reordering the array, or
reordering keys within an entry, does not move an instance's dedup or attempt
state. Two byte-identical entries are an error rather than one trigger silently
written twice.

## Step forms

A step is one of six forms (all share `id` and `if`):

- `type: agent` — dispatch an agent: `prompt`, `checkout`,
  `output_schema`, `background` (+ `handoff`, see [[Hand-offs]]),
  `rerequest_review`, `workdir`, `env`, and an optional `gate:` on the
  agent's proposed change ([[Gates]]). A foreground agent step with a local
  worktree also outputs its proposed `diff` and `workdir` ([[Runs]]).
  It may also carry `model:` (a fleet, a model id, a wildcard, or an inline
  `{ any, required }`) and `runtime:` (a `runtimes:` entry to pin it to) —
  see [Model selection](Model-Selection.md).
- `type: command` — a host command (POSIX sh semantics; argv list). With
  `host:` it runs over SSH and outputs `{stdout, stderr, exit_code}`.
- `run:` — an inline code step ([[Code-Steps]]).
- `uses: <conn>.<verb>` — a service verb ([[Verbs]]). This includes the
  always-on data, memory, and artifact verbs: `kv.*`/`sql.*` over `stores:`,
  `memory.*` over the `memory:` section ([[Memory]]), and `blob.*`
  ([[Binary-Data]]).
- `workflow: <name>` — a reusable workflow call (below).
- `team:` — one task split across a planner, parallel workers in isolated
  worktrees, an optional critic, and a reconciler ([[Teams]]).

An agent step carries its own BEHAVIOR — guidance, skill, memory opt-in,
workspace, timeouts, isolation, model, runtime — and shares it with other
steps through a YAML anchor (`<<: *base`, parked under a top-level `x-`
key). Where the NAME is the point rather than the fields — a `team:` role,
a pack role a consumer rebinds — the step plays a named entry of the
top-level `steps:` registry with `step: <name>`. There is no `agents:`
block; see [[Steps]] and [[Reuse]].

Agent steps have two extra memory hooks (when a `memory:` section is
configured): a `remember:` block in the agent's final output persists
post-run with the run's provenance (the output contract), and an opted-in
step (`memory: true`) gets the scoped memories injected into its prompt. The
opt-in gates BOTH directions — a step that did not ask for memory cannot
write to it either. See [[Memory]].

An agent step carrying a `session:` block (or running on a runtime that has
one) participates in **session affinity**: events rendering the same key
reach one live agent as follow-up prompts instead of fresh spawns — see
[[Steps]]. A follow-up returns `{ agent_id, ... }` like any agent step; on
paseo its output is empty (the prompt is queued to the live agent), so steps
that read the agent's structured output should not assume a keyed session.

## Context and scope

Templates and `if:` conditions address:

- **Trigger context** — the event's published facts (`{{.repo}}`,
  `{{.comment_body}}`, `{{.slack.channel}}` — see `conductor schema <conn>`).
- **Step outputs** — `{{.<stepid>.<field>}}` from any PRIOR step (the legacy
  `{{.steps.<id>.outputs.<field>}}` spelling also resolves).
- **Secrets** — `${ENV}` values and vault reads: `{{ vault "house" "gh" }}`
  or `{{.vaults.house.gh}}` (tainted, redacted — see [[Secrets]]; the legacy
  `{{.secrets.<name>}}` block was retired and auto-migrates).
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
  (bounded). At runtime the collected outputs land under `{{.<id>.items}}` /
  `{{.<id>.count}}` (on a for_each **verb** step, `validate` checks later
  references against the verb's own output schema — read the source list
  instead; see [[Examples]]).
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

A trigger (or a reusable workflow) may also set a default `gate:` for every
agent step it contains — the step's own `gate:` wins ([[Gates]]).

## Reusable workflows

`workflows:` holds named, parameterized step lists. A workflow may also `extends:` another
workflow to inherit its `inputs`/`outputs`/`gate` (steps replace when set) — see [[Reuse]].

```yaml
workflows:
  assess-and-post:

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

**Resume.** An agent-step plan checkpoints its committed progress in
conductor's own state (`plans.json`): a daemon crash or auto-update mid-plan
resumes AFTER the last committed step — the agent is not re-dispatched and
committed side effects never re-run (the restored plan is re-guarded under
the current policy; vault-read outputs re-resolve rather than persist). The
`workflow.run { steps }` and live `run_step` surfaces stay at-least-once,
as do nested workflow calls and parallel branches.

**Supervise.** A failing plan step routes back to the authoring agent's
**session (§10)** as a follow-up with STRUCTURED context — the failed step,
the error, executed steps and their outputs, the remaining steps. The agent
replies with a revised plan; conductor re-validates, re-guards (the classes
the run's original approval granted stay usable; NEW approval-gated work
rejects), splices it in, and **resumes from the failed step** — committed
side-effecting steps never re-run. After
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
