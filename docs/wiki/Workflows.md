# Workflows: triggers, steps, hooks

A trigger is four keys: `on:` (what fires it), `filters:` (whether it fires —
keys from the event's schema, all AND-ed), `steps:` (the workflow), and
`hooks:` (lifecycle actions). Plus optional `group:` ([[Grouping]]),
`policy:` ([[Policy]]), `name:` (a variant label for dedup state), and
`enabled:`.

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
- `uses: <conn>.<verb>` — a service verb ([[Verbs]]).
- `use: <workflow>` — a reusable workflow call (below).

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
`contains()`, `exists()`), with paths written bare or as `{{.path}}`.

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
      - { id: a, use: assess-and-post, with: { repo: "{{.repo}}", pr: "{{.pr}}" } }
      - { id: auto, if: "{{.a.decision}} == auto", uses: gh.submit_review, options: { … } }
```

Inside, inputs read as `{{.inputs.<name>}}`; the workflow also sees the
trigger context and its own steps — but NOT the caller's other step outputs
(pass those via `with:`). The caller reads declared `outputs:` off the call
step's id. Workflows may `use:` other workflows; `validate` rejects cycles,
unknown/missing inputs, type mismatches, and outputs referencing steps that
do not exist.

Related: [[Connectors]] · [[Verbs]] · [[Code-Steps]] · [[Grouping]] · [[Hand-offs]]
