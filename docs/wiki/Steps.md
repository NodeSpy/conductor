# Steps (the retired `agents:` profile)

**There is no `agents:` block.** A named agent profile turned out to be doing
five separate jobs, which is why it could not simply be renamed. Each has a
home now, and none of them is an agent:

| the old job | where it lives |
| --- | --- |
| which model to run (`provider`/`model`) | a **fleet** — `model:` on the step ([[Model-Selection]]) |
| execution cost cap (`budget`) | the **runtime** ([[Cost-Accounting]]) |
| private memory namespace | an **opaque scope key**, defaulting to the step identity ([[Memory]]) |
| live-session pool (`session`) | **`(runtime, model, key)`** ([[Runtimes]], below) |
| self-improvement track record | an **opaque track-record key**, defaulting to the step identity ([[Outcomes]]) |
| behavior (guidance, skill, workspace, timeouts, archive, host, isolation) | **the step**, shared with a **YAML anchor** |

Existing configs are migrated automatically at boot (or by
`conductor config migrate`), and the migration is careful to preserve your
accumulated history — see [Migration](#migration-from-agents) below.

## Sharing step behavior

Behavior lives on the step that dispatches the work. To share it between
steps, park a base under any top-level `x-` key — the loader ignores
`x-`-prefixed sections (the docker-compose extension-field convention), so
they exist purely to hold anchors — and pull it in one of two ways:

- **`<<: *base`** — plain YAML merge. Dumb: the step's own key wins
  outright, scalars and lists alike. Use it for a copy of a base.
- **`extends: *base`** — conductor's own merge. Field-aware: scalars
  override, lists append, maps deep-merge, and `guidance:` stacks. Use it
  to add to a base. `!override` and `!reset` are the escape hatches.

Both work identically in a [[Packs|pack manifest]]. See [[Reuse]] for the
full rules; the example below uses `<<:`.

```yaml
x-templates:
  fixer: &fixer
    type: agent
    model: claude-opus-5              # a fleet name, a model id, a wildcard,
                                      #   or { any: [...], required: bool }
    thinking: ""                      # runtime launch hint (optional)
    mode: ""                          # runtime session mode (optional)
    workspace: worktree               # local | worktree
    wait_timeout: 30m
    archive_when_done: true           # reaper archives the agent once it idles
    labels: { team: autopilot }
    # runtime: paseo                  # pin the backend (default: the default: true runtime)
    # host: build-box                 # a [[Hosts]] SSH target its runtime launches on
    # guidance: |                     # tone/format; STACKS on the scoped baseline
    #   One or two sentences, plain and direct.
    # memory: true                    # shared-memory injection ([[Memory]])
    # outcome_feedback: true          # append this step's track record ([[Outcomes]])
    # outcome_key: reviewers          #   …or pool several steps onto one record
    # session: { key: "{{.repo}}#{{.pr}}" }   # this step's own session pool
    # skill: { verbs: [gh.comment] }  # reach back into conductor ([[Agent-Skill]])
    # isolation: { mode: namespace }  # per-dispatch sandboxing ([[Isolation]])

triggers:
  github.pull_request:
    steps:
      - <<: *fixer                    # merge the anchor…
        id: fix                       # …then this step's own fields, which win
        prompt: "Resolve the conflict on {{.repo}}#{{.pr}}."
```

A step may of course carry these fields inline — an anchor is only how you
avoid retyping them.

Two things to know:

- **Anchors are file-local.** YAML resolves them per document, so an anchor
  defined in `config.yaml` is not visible in an imported `conf.d/*.yaml`.
  For reuse across files, use `extends:` on a map section
  (`runtimes:`/`workflows:`/`handoffs:`) or a named step, below. See
  [[Reuse]].
- **An anchor copies fields, not identity.** Two steps merging one anchor
  are still two identities. `name:` is the separate, deliberate opt-in to
  sharing one — see below.

## Pointing at a step

There is **no top-level `steps:` section**. A step lives in the workflow or
trigger that runs it, and anything that must point at a particular one
addresses it there:

```
review-flow/architect     by the step's own id: (or name:, if it has no id:)
review-flow[2]            by position, for a step that carries neither
```

The container is a `workflows:` entry or a **named** trigger. That is the
same slot the identity ladder uses below — id if present, else index — so
a reference and an identity always name the same step by the same rule:
`review-flow/architect` addresses exactly the step whose structural
identity is `workflow:review-flow/architect`.

Two things use references:

- a [[Teams|`team:`]] block, whose `planner`/`worker`/`critic`/`reconcile`
  roles each name a step. The role takes that step's behavior **and its
  identity**, so every team pointing at it shares one memory namespace,
  session pool, and track record;
- a [[Packs|pack]] overlay, where a consumer overrides one step of a pack
  by reference (`packs.review.steps: { review-flow/review: { … } }`).

```yaml
workflows:
  roles:
    steps:
      - id: architect
        type: agent
        guidance: "Decompose before building. Name the risky part."
        prompt: "…"

triggers:
  - on: gh.issue_matched
    steps:
      - id: feature
        prompt: "Implement {{.url}}"
        team: { planner: roles/architect, worker: roles/architect }
```

The index form is the escape hatch, not the habit: it is positional, so
inserting a step above shifts it. Give a step an `id:` and reference that.

`agent:` still parses, but it is now a free-form **attribution label** that
selects nothing. What identifies a dispatch is its identity, below.

## Step identity — the key everything defaults to

Memory scoping, session affinity, and outcome tracking all default their key
to the step's **identity**. It must be stable across restarts, so it is a
pure function of config — never a per-run value:

1. an explicit **`name:`** — author-pinned, and **shareable**: two steps with
   the same name share one memory namespace, one session pool, and one track
   record — exactly the reuse a shared `agent: fixer` gave you. A `team:`
   role inherits the name of the step it references, for the same reason.
   Merging an **anchor** does not do this: an anchor copies fields and
   leaves identity alone.
2. **structural** — the enclosing qualified trigger/workflow plus the step's
   **slot**: its `id:`, else its `name:`, else its index in the list —
   `github.pull_request/security`, or `github.pull_request/2`. This is the
   default, and it is the same slot a step reference uses.
3. a deterministic **fingerprint** of the step's definition, for a step with
   no enclosing context.

Editing a step's prompt does **not** rotate its identity. Reordering may, for
a step that has neither a `name:` nor an `id:` — give it one to pin it.

> `id:` is not rung 1. It is the run-local handle `steps.<id>.outputs.*`
> addresses and it is near-universal, so treating it as a global identity
> would make two unrelated triggers that both use `id: fix` silently share a
> track record. Instead `id:` supplies the structural **slot**, which is what
> makes structural identity survive reordering.

## Fields

| Field | Meaning |
| --- | --- |
| `name` | Pins the step's identity (see above). Shareable on purpose. |
| `id` | The step's slot: how outputs are addressed (`steps.<id>.outputs.*`), what a step reference points at, and — absent a `name:` — the structural half of its identity. |
| `model` | Which model to run: a fleet name, a model id, a wildcard, an inline list, or `{ any, required }`. Unset → the runtime's `models.default:`, then a bare launch. See [[Model-Selection]]. |
| `runtime` | A `runtimes.<name>` entry to run on (default: the `default: true` runtime, else the built-in paseo). See [[Runtimes]]. |
| `thinking` / `mode` | Runtime launch hints, passed through where the runtime supports them. |
| `workspace` | `local` or `worktree` — the existing checkout, or a fresh git worktree. |
| `wait_timeout` | How long a foreground dispatch waits before giving up. |
| `archive_when_done` | Whether the reaper archives the agent once it idles. Forced off for a `background:` hand-off step. |
| `labels` | Extra `key=value` labels on the dispatched agent. |
| `host` | A [[Hosts]] SSH target this step's runtime launches on, overriding the runtime's own. |
| `guidance` | Tone/format that **stacks on** the scoped baseline ([[Policy\|`policy.guidance`]]) rather than replacing it. A string, a list, or `{ replace: … }`. See [[Reuse]]. |
| `memory` | Opt into shared-memory injection ([[Memory]]). `true` uses the run's context keys; a map `{ scopes, tags, limit }` names arbitrary keys. Also gates writing: only an opted-in step may harvest its output into shared memory. |
| `session` | This step's own session pool: `{ key, idle_ttl, max_lifetime, end_on }`, namespaced to its identity. A step with none joins the runtime's overall pool. See below. |
| `skill` | Verb tools + the secret broker over the daemon socket: `{ verbs, secrets_via, allow_secrets, max_calls }`. See [[Agent-Skill]]. |
| `isolation` | Per-dispatch sandboxing. Wins over the runtime's own; needs a runtime conductor launches itself (not paseo). See [[Isolation]]. |
| `outcome_feedback` | `true` appends this step's own track record to its guidance. See [[Outcomes]]. |
| `outcome_key` | Override the track-record key (default: the identity), so several steps can pool one record. |

Note there is no `budget` here: a budget caps execution cost on a backend, so
it lives on the **runtime** ([[Cost-Accounting]]).

## Behavior

- Every step of a multi-step [[Workflows|workflow]] can differ — a common
  pattern is a cheap `planner` step triaging an issue and handing off to
  a stronger `fixer` only when the triage justifies it
  (`if: "{{.evaluate.has_context}} == true"`).
- `workspace` governs the checkout lifecycle (persistent checkout vs. a fresh
  worktree per dispatch); the step's own `checkout:`
  (`checkout-pr` | `branch-off` | `none`) governs what git state that checkout
  is put into. Independent knobs — a `workspace: worktree` step can still run
  `checkout: none` for triage.
- `archive_when_done: true` steps are still protected from premature cleanup:
  the reaper skips one paused on a permission prompt, and an agent can hold
  itself alive with a `.paseo-hold` marker in its worktree.
- Which models a runtime can actually run is DISCOVERED, per runtime — see
  [[Model-Discovery]]. `conductor validate` reports a `required:` fleet
  nothing can satisfy.

## Session affinity — one agent per (runtime, model, key)

By default each dispatch gets a fresh agent. A `session:` block binds a live
session to the rendered `key`, so later events reach the same agent as a
follow-up with full prior context.

A binding is **`(runtime, resolvedModel, key)`**. Both extra dimensions are
structural, not preferences: a live agent is one model on one runtime, and
you can resume neither a paseo session on codex nor an opus step into a haiku
session. A useful consequence — because a pack assigns a fleet per step, its
model assignments partition affinity for free.

It is declarable at two scopes:

```yaml
runtimes:
  paseo:
    session:                          # the OVERALL pool: one agent per key,
      key: "{{.repo}}#{{.pr}}"        #   shared by every step without its own
      idle_ttl: 12h
      end_on: [ gh._closed ]

x-templates:
  reviewer: &reviewer
    type: agent
    session:                          # this step's OWN pool, namespaced to its
      key: "{{.repo}}#{{.pr}}"        #   identity — so the same key string is
      end_on: [ gh._closed ]          #   still a distinct session
```

Resolution per dispatch: the **step's** `session:` wins, else the
**runtime's**, else a fresh agent.

- **Serialized per key.** At most one prompt in flight per session;
  concurrent same-key events queue — the `group:` one-run-per-key guarantee,
  extended across the session's life.
- **Durable.** The binding persists in conductor's own state
  (`affinity.json`, beside audit/dedup); after a restart the session resumes
  via the runtime's native handle (paseo re-binds the agent id, ACP
  `session/load`). While bound, the agent is held from the reaper.
- **Eviction.** Idle past `idle_ttl`, older than `max_lifetime`, or an
  `end_on` event (matched as `<connector>.<kind>`, a bare `<kind>`, or
  `<type>.<kind>`, rendered against the event's own context). github's PR
  close **and** merge both arrive as the internal `_closed` signal, so
  `end_on: [ gh._closed ]` is the evict-when-done form.
- **`_closed` is eviction-only.** It matches `end_on:` but is not a trigger
  event — `on: gh._closed` fails validation.
- **Runtime support.** Needs a session-persistent runtime — paseo or ACP.
  One-shot `cli` runtimes fall back to a fresh agent per event; pair with
  `memory:` ([[Memory]]) for continuity there.
- **vs `group:` and memory.** `group:` batches a burst into one run; memory
  injects durable facts; session affinity reuses a live conversation. They
  compose.

Follow-up turns return `Queued` run refs: on paseo the prompt is delivered
via `paseo send` (no captured output for later steps); an ACP follow-up
returns the turn's output. A follow-up to a dead session is detected,
unbound, and replaced by a fresh spawn.

> Bindings written before the re-key carry no runtime/model and are ignored
> on restore, so the next event starts a fresh session. Sessions are
> short-lived by design (24h idle default), so nothing durable is lost — a
> track record would have been another matter, which is why that one is
> preserved explicitly.

## Migration from `agents:`

`conductor config migrate` (and the automatic boot migration) decomposes each
profile, and it is careful about one thing above all: **your accumulated
history carries over.** Memory, sessions, and outcomes used to key off the
agent NAME; they now key off the step IDENTITY. So the migration INLINES
each profile's behavior onto every step that referenced it, carrying
**`name: <the old agent name>`** — the identity ladder's top rung — onto
each one.

Where a profile was referenced from several steps IN THE SAME FILE, those
steps share a YAML anchor parked under `x-migrated:` instead of each
getting a copy. Where the steps are in different files, each gets its own:
anchors do not cross `imports:`, and duplicated config that works beats DRY
config that does not parse. Because `agents:` commonly sat in `config.yaml`
while the triggers naming it sat in `conf.d/*.yaml`, the profile table is
gathered from the whole import tree before any file is rewritten.

A profile nothing referenced is dropped with a note — there is no top-level
section left to park it in, and its behavior is in the `.pre-connectors`
backup.

The keys therefore come out identical to what your box already has on disk:
`outcomeStats["fixer"]` stays `outcomeStats["fixer"]`, engagements still
match, and memories written under the old `agent:fixer` scope are still
recalled through a compatibility alias.

| Old | New |
| --- | --- |
| `agents.<n>` | its fields, inlined on each referencing step, with `name: <n>` |
| `provider` + `model` | `model:` (an exact pin — a migration never invents a fleet) |
| `provider` alone | nothing — that named a backend, not a model, so it becomes a bare launch |
| `budget` | `runtimes.<the runtime it ran on>.budget` |
| `controller` | `runtime` |
| behavior fields | the same key on each referencing step |
| `agents.<child>.extends: <parent>` | flattened — the parent's fields are copied into the child before inlining |
| `agent: <n>` on a step | the profile's fields, plus `name: <n>` (or `<<: *n` when shared in-file) |

Anything the decomposition has no home for is dropped **with a note** in the
migration summary, never silently.

## Agent-driven workflows

An agent step's final output may carry a ` ```plan ` block — steps in the
normal grammar that conductor validates, guards (`policy.agent_authored`,
[[Policy]]), and runs deterministically. Failures route back to this
agent's **session** (above) for a bounded revise loop, so a planning agent
should keep sessions; pairing with `memory:` lets it recall what worked.
The full loop — plan, choose from the catalog, supervise, promote — is in
[[Workflows]].

## Explanation

An agent and a runtime answer different questions. The **agent** profile answers "what should
run" — which provider, which model, what tone, what workspace lifecycle. The **runtime** answers
"how is it run" — which process or API actually executes it. A `fixer` profile with
`provider: claude` can run on the built-in `paseo` dispatcher, on `agent-deck`, or through
opencode's HTTP API, unchanged, just by pointing `runtime:` at a different entry — the provider
and model still route through whichever runtime is selected. The one place this decouples is an
**ACP** or **cli** runtime: there, the runtime's own `agent:`/`command:` names the tool directly
(e.g. `gemini` over ACP), so the profile's `provider`/`model` fields have nothing to route and are
ignored. With no `runtimes:` configured at all, every agent profile runs on `paseo`, so this
distinction is invisible until you actually introduce a second runtime. See [[Runtimes]] for the
full resolution order and runtime kinds ([[Controllers]] is the legacy name).
