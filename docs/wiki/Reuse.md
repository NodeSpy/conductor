# Configuration reuse: `extends:` and layered guidance

Real configs repeat themselves: agent profiles share a provider/model/runtime, and
near-identical triggers repeat the same `repos:`/`steps:`/`filters:` per team. Two mechanisms
remove that duplication — `extends:` inheritance between named entries, and guidance that
*stacks* across scopes instead of being retyped. YAML anchors (`<<: *base`) don't cover this: they
don't cross imported `conf.d/*.yaml` files and can't append to a scalar.

## `extends:` — inherit from another entry

A named entry declares `extends: <name>` to inherit from another entry **in the same section**.
Supported on **`steps:`, `runtimes:`, `workflows:`, `handoffs:`, and `triggers:`** — and from a
trigger/workflow step onto a `steps:` template. Resolution runs
once at load, after `imports:` merge and before validation, so everything downstream sees
fully-resolved entries.

```yaml
steps:
  base:
    type: agent
    workspace: worktree
    labels: { team: autopilot }
    guidance: "House style: terse, one thought per sentence."
  fixer:
    extends: base            # inherits workspace/labels
    model: claude-opus-5     # scalars: the child wins
    labels: { role: ci }     # maps deep-merge -> {team: autopilot, role: ci}
    guidance:
      - "You fix failing CI: reproduce, fix, verify locally, push."   # stacks under base's tone
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
target** is a load error. One documented limitation: a plain `bool` field (e.g. an agent's
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
2. **The agent profile's own `guidance`** (plus anything it inherits through `extends:`) stacks on
   top of the baseline.

`config.example.yaml` ships a reasonable house tone under `policy.guidance` you can adopt or
change — it is an example, not a default conductor imposes.

`guidance:` accepts three forms, at both the policy scope and the agent profile:

```yaml
guidance: "one block"            # a single part
guidance: [ "first", "second" ]  # several parts, in order
guidance: { replace: "only me" } # reset: drop everything below this level, use only this
```

- `guidance: ""` or `[]` at a level contributes nothing but does **not** suppress the levels below.
- `guidance: { replace: "" }` disables guidance entirely for that agent.
- An `extends:` child inherits its parent's guidance underneath its own, unless the child resets with
  `{ replace }`.

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

steps:
  reviewer:
    type: agent
    guidance: "Flag only what a thoughtful senior would bother raising."     # stacks on top
```

A `reviewer` dispatched from a `gh` trigger sees all three blocks; the same profile on a Slack
trigger sees only the global tone plus its own.

## See also

- [[Steps]] — agent profiles and the `guidance`/`extends` fields
- [[Policy]] — the cascade `policy.guidance` rides on
- [[Runtimes]], [[Workflows]] — other sections that support `extends:`
- [[Configuration]] — the full trigger grammar
