# Configuration reuse: `<<:`, `extends:`, and layered guidance

Real configs repeat themselves: steps share a model and a tone, and near-identical triggers repeat
the same `repos:`/`steps:`/`filters:` per team.

A step has **two** ways to reuse a chunk of config, and they differ in one thing — *when* they
happen:

| | what it does | when |
| --- | --- | --- |
| **`<<: *base`** | dumb override: the step's own key wins outright, scalars and **lists** alike | at parse, before conductor sees the file |
| **`extends: *base`** | field-aware merge: scalars override, **lists append**, maps deep-merge, **`guidance:` stacks** | at load, with both layers in hand |

That is the whole distinction. `<<:` is finished by the time conductor reads the document, so there
is no base left to append to; `extends:` hands conductor both layers, so it can be smarter. Reach
for `<<:` when you want a copy of a base, and `extends:` when you want to *add to* one.

Two more mechanisms sit alongside them: **`extends:` on a map section or a trigger** (a different
feature that shares the word — see below), and **layered `guidance:`** through the policy cascade.

## Where the base lives: an `x-` anchor

Both forms take the same thing on the right: a YAML alias to an anchor, or an inline map. Anchors
must be defined somewhere, so park them under any top-level **`x-`** key — the loader ignores
`x-`-prefixed sections (the docker-compose extension-field convention), and they exist purely to
hold anchors.

```yaml
x-templates:
  reviewer: &reviewer
    type: agent
    model: heavy
    guidance: "Terse and human."
    skill: { verbs: [github.comment] }
```

This works the same in a [[Packs|pack manifest]] — a pack author gets both forms with no extra
machinery, and the anchors stay inside the pack's own file.

## `<<: *anchor` — a copy of the base

```yaml
triggers:
  - on: gh.review_requested
    steps:
      - <<: *reviewer                     # merge the anchor…
        id: fix                           # …then this step's own fields, which win
        model: light                      # OVERRIDES
        skill: { verbs: [github.submit_review] }   # REPLACES — the whole map, not just verbs
        prompt: "Fix the failing checks on {{.repo}}#{{.pr}}."
```

Plain YAML semantics throughout: a key the step sets replaces the merged one whole. A list is not
appended, a map is not deep-merged, and a `guidance:` is not stacked.

## `extends: *anchor` — build on the base

```yaml
workflows:
  review:
    steps:
      - extends: *reviewer
        id: fix
        model: light                      # OVERRIDES (scalar)
        guidance: "Also cite file:line."  # APPENDS  → "Terse and human." then this
        skill: { verbs: [linear.create] } # APPENDS  → [github.comment, linear.create]
        prompt: "Fix the failing checks on {{.repo}}#{{.pr}}."
```

- **scalars** (`model`, `workspace`, `mode`, …) — the step overrides.
- **lists** (`skill.verbs`, `network`, …) — append, base items first.
- **maps** (`labels`, `env`, `skill`, …) — deep-merge, recursively.
- **`guidance:`** — always appends, base tone underneath. There is no opt-in: a base that set the
  house voice should still be heard under a step that adds to it.

Chains work, resolved base-first, so the outermost layer speaks last:

```yaml
x-templates:
  house: &house { type: agent, guidance: "House voice." }
  team:  &team  { extends: *house, guidance: "Team voice." }
# a step with `extends: *team` and its own guidance gets all three, in that order
```

An `extends:` chain is depth-bounded. An alias cannot loop (YAML requires the anchor first), but a
hand-written chain of inline maps could, and that is a load error rather than a hang.

### Escape hatches: `!override` and `!reset`

Always-appending is only safe if there is a way out. Both are YAML **tags** on the step's own
value, read by conductor during the merge:

```yaml
      - extends: *reviewer
        guidance: !override "Only this."   # replace instead of appending
      - extends: *reviewer
        guidance: !reset                   # drop the inherited value entirely
        skill: { verbs: !reset }           # works on any inherited list, map, or scalar
```

`!reset` drops both layers for that key; `!override` takes the step's value verbatim. A tag on a
field nothing was inherited for means the same thing either way, so it is simply consumed.

> The older `guidance: { replace: … }` form means exactly `guidance: !override …` and keeps
> working. The tag is the spelling to reach for — it works on every field, not just guidance.

### Both on one step

Unusual, but defined: `<<:` resolves first (it is YAML-level and already finished), then `extends:`
merges field-aware on top of that result. So a `guidance:` that arrived through `<<:` counts as the
step's own, and the `extends:` base stacks underneath it.

## What `extends:` does NOT do

- **It does not touch identity.** A step's identity is still `name:` → structural
  (`<workflow>/<id-or-index>`) → fingerprint. Extending a base contributes nothing to it; if the
  base happens to set `name:`, that merges like any other field and its consequences are the
  ordinary ones (see [[Steps]]).
- **It is not a reference.** Both forms copy config at load; neither leaves anything to point at.
  A `team:` role and a pack overlay address a step where it lives — `<workflow>/<step-id>`, or
  `<workflow>[<n>]` for a step with no `id:`.
- **Neither crosses `imports:`.** An anchor defined in `config.yaml` is invisible to an imported
  `conf.d/*.yaml` — that is YAML, not a conductor limit. For cross-file reuse of a whole section,
  use the map-section `extends:` below.

## Section `extends:` — inherit from another named entry

A different feature that shares the word. A named entry declares `extends: <name>` to inherit from
another entry **in the same section**. Supported on **`runtimes:`, `workflows:`, `handoffs:`, and
`triggers:`** — never on a step, whose `extends:` takes an anchor rather than a sibling key.
Resolution runs once at load, **after `imports:` merge** and before validation, which is what makes
it work across files where an anchor cannot.

```yaml
runtimes:
  remote:
    use: cli
    host: build-box
    isolation: { mode: namespace }
  remote-codex:
    extends: remote          # inherits host/isolation
    command: [codex]         # slices: the child replaces
```

### Merge rules

| Field kind | Rule |
| --- | --- |
| scalars (`provider`, `model`, `host`, …) | child wins when set; otherwise inherited |
| pointers / blocks (`isolation`, `session`, `policy`, …) | child wins when present; otherwise inherited |
| maps (`labels`, `env`, `inputs`, trigger `filters`/`options`) | deep-merged per key — child keys override, the parent's other keys are kept |
| slices (`command`, trigger `steps`/`hooks`) | child **replaces** when it sets a non-empty value; otherwise inherited |
| `guidance` | **stacks** (parent parts, then child parts) — see below |

Chains are allowed (`c → b → a`, resolved root→leaf). A **cycle** or an **unknown `extends:`
target** is a load error. One documented limitation: a plain `bool` field (e.g. a step's
`archive_when_done`, a runtime's `default`) is inherited only when the child leaves it `false` — a
child can't force a parent's `true` back to `false`. The optional fields that matter are pointers or
strings, so this rarely bites.

> **Not yet on `connectors:`, `stores:`, `vaults:`.** These decode through a retained raw YAML node
> (their type-specific sub-blocks aren't plain struct fields), which needs a different merge, and they
> rarely duplicate in practice. `extends:` there may come later.

## `extends:` on triggers, and `abstract:` bases

Triggers reference a parent by its `name:`. Filters and options deep-merge; steps and hooks replace;
`policy`/`gate`/`group` fill if unset. A base that exists only to be extended is marked
`abstract: true`: it never fires and is stripped after resolution, so it needs no `on:`.

```yaml
triggers:
  - name: review-base            # a base, not a live trigger
    abstract: true
    filters:
      gates: { not_draft: true }
    steps:
      - { id: r, type: agent, agent: reviewer, prompt: "Review {{.repo}}#{{.pr}}." }

  - on: gh.review_requested
    extends: review-base
    filters: { repos: [org/api] }   # merges with the base's gates

  - on: gh.review_requested
    extends: review-base
    filters: { repos: [org/web] }
```

Both children inherit the base's `steps` and `not_draft` gate; each adds its own `repos`. The base is
gone from the running config. Trigger `extends:` resolves **before** the multi-source `on:` list
expansion, so a child may also inherit a base's `on:`. An `abstract: true` base cannot be
`on: manual` (it is never a `conductor run` target).

## Layered guidance

Guidance (house tone/format appended to an agent's prompt) is **additive** — a stack, not a value
that the most specific level overwrites. It is also entirely **config-driven**: conductor ships no
tone of its own. If you configure nothing, nothing is injected. From the bottom up:

1. **Layer 0 — the scoped baseline: [[Policy|`policy.guidance`]].** Because policy cascades
   **global → connector → trigger**, the baseline is scopable. Scopes **stack** by default (a
   trigger's guidance adds under the global tone); a scope that uses the `{ replace: … }` form
   **resets** the cascade from that scope down.
2. **The step's own `guidance`** — including everything an `extends:` chain stacked underneath it
   — sits on top of the baseline.

`config.example.yaml` ships a reasonable house tone under `policy.guidance` you can adopt or
change — it is an example, not a default conductor imposes.

`guidance:` accepts three forms, at both the policy scope and the step:

```yaml
guidance: "one block"            # a single part
guidance: [ "first", "second" ]  # several parts, in order
guidance: !override "only me"    # reset: drop everything below this level, use only this
guidance: { replace: "only me" } # the older spelling of the same thing
```

- `guidance: ""` or `[]` at a level contributes nothing but does **not** suppress the levels below.
- `guidance: { replace: "" }` disables guidance entirely for that agent.
- A step's `extends:`, a section `extends:` child, and a `team:` role filled in from the step it
  references all inherit the base's guidance underneath their own, unless they escape with
  `!override` (or the older `{ replace }`) or drop it with `!reset`. A `<<:` anchor does **not**
  stack: a merged `guidance:` is replaced wholesale by the step's own, because that is what a YAML
  merge does.

`agent_guidance:` (the old top-level field) still works — it is folded into the **global**
`policy.guidance` for back-compat, so connector/trigger-scoped guidance stacks on top of it. If both
are set, `policy.guidance` wins. `conductor config migrate` (and the boot auto-migration) rewrites a
top-level `agent_guidance:` to `policy.guidance` so configs converge on the canonical form; the alias
stays accepted, so migrating is optional.

> **Behavior change (v0.7.4):** a per-agent `guidance:` now *appends* to the baseline instead of
> replacing it. To restore the old replace-the-global behavior, write `guidance: { replace: … }`.

### Example: one house tone, a stricter tone for one connector, one agent that adds to it

```yaml
policy:
  guidance: "Write like a busy engineer: a sentence or two, plain and direct."

connectors:
  gh:
    use: github
    policy:
      guidance: "On PRs, lead with the point and propose a concrete fix."   # stacks under global

workflows:
  review:
    steps:
      - id: reviewer
        type: agent
        guidance: "Flag only what a thoughtful senior would bother raising."  # stacks on top
        prompt: "Review {{.repo}}#{{.pr}}."
```

The `reviewer` step reached from a `gh` trigger sees all three blocks; the same step on a Slack
trigger sees only the global tone plus its own.

## See also

- [[Steps]] — step behavior, step references, and step identity
- [[Policy]] — the cascade `policy.guidance` rides on
- [[Runtimes]], [[Workflows]] — the sections that support the map-section `extends:`
- [[Configuration]] — the full trigger grammar
