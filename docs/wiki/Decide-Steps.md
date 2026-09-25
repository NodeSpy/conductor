# Decide steps

A `decide:` step asks typed questions — yes/no, pick-one, score — and gets back
answers with probabilities, in the `system_one/v1` contract (TypeSafe's
published System One API, taken as-is). Use it where a workflow needs a
classification rather than code or prose: refute-verifying a review finding,
triaging a comment, deciding whether a failing check is a flake.

```yaml
- id: verify
  model: light                    # the tier, exactly as on an agent step
  decide:
    state: |
      FINDING at {{.item.path}}:{{.item.line}}: {{.item.body}}
      DIFF:
      {{.diff.diff}}
    questions:
      refuted:
        type: noul
        instructions: The diff or description POSITIVELY contradicts this finding.
        criteria:
          true: Evidence contradicts it
          false: Plausible, or merely unprovable from the diff
    default: { refuted: { noul: 0 } }      # only if every candidate fails
```

A downstream step reads the answer by question name:

```js
// a code step: refute only at >= 0.8
const kept = ctx.flatten.value.filter((f, i) => ctx.verify.items[i].refuted.noul < 0.8);
```

## Questions and answers

| type | question fields | answer |
|---|---|---|
| `noul` | `instructions`, optional `criteria: { true, false }` | `{ type: noul, noul }` — the probability of yes |
| `choice` | `instructions`, `criteria: { <label>: <description>, … }` (2+ labels, in order) | `{ type: choice, choice, confidence, probabilities: { <label>: p } }` |
| `score` | `instructions`, `criteria: [<level 0>, <level 1>, …]` (2+ levels, lowest first) | `{ type: score, score, confidence, legend, probabilities: { "0": p, … } }` — `score` is the probability-weighted average level |

`instructions` and each criterion may be text or any structured value. A
question name is an identifier (letters, digits, `_`), and must not start with
`_`.

A decide step's outputs are the answers, one per question, plus:

- `_by` — the `<runtime>/<model>` that answered, or `default`.
- `_escalated_from` — present when an escalation replaced an earlier answer
  (see below).

Every answer, from every backend, is validated against the questions before a
workflow can read it: an answer of the wrong type, a label that wasn't asked,
or a probability outside 0..1 is refused, and the next candidate is asked.

## Who answers

The step's `model:` resolves exactly as an agent step's does ([Model
selection](Model-Selection)) — but to a ranked list of `(runtime, model)`
candidates rather than one winner:

1. **Decision runtimes** first — runtime plugins that speak the protocol
   natively (see [below](#decision-runtimes)). They are only reached when the
   fleet names their models.
2. **Agent runtimes** next — paseo, `cli`, `acp`, `opencode`. Conductor renders
   the questions into a prompt and output schema (a port of TypeSafe's
   MIT-licensed system-one-adapter: its prompt, per-question schema,
   probability normalization and confidence metrics) and runs **one restricted
   session**: no checkout, one turn, the prompt and schema only. The reply is
   converted back into v1 answers. No model API key is involved — the runtime's
   own credentials answer.

Each group is ranked as model selection ranks a fleet (fleet order, overlaid by
`prefer:`). Conductor asks the first candidate; **a successful answer is
final**, whatever its confidence. A failure — an error, a timeout, an invalid
answer — moves to the next candidate. When every candidate failed, `default:`
answers; with no `default:`, the step fails.

A decide step takes only `state` as its input. Agent-behavior fields —
`prompt`, `checkout`, `output_schema`, `mode`, `thinking`, `workspace`,
`guidance`, `memory`, `session`, `skill`, `isolation`, `gate`, `team`,
`background`, `handoff`, `command`, `env`, `host` — are a load error on a
decide step. A decision that needs to explore the repository is an agent step.

## `default:`

The answers used when no candidate answered. Each is a full v1 answer or just
its headline value; the rest is filled in with confidence 0 and a uniform
distribution:

```yaml
default:
  refuted: { noul: 0 }
  risk:    { choice: high }
  tests:   { score: 0 }
```

A default must answer every question and name only labels the question offers.

## `escalate:` — a second opinion on uncertain answers

Off unless declared. When `when:` holds over a successful answer, conductor asks
the next candidate and uses its answer instead:

```yaml
decide:
  # …
  escalate:
    when: "refuted.noul >= 0.5 && refuted.noul < 0.8"   # uncertain near this step's 0.8 boundary
    # to: heavy     # optional: a tier or inline fleet; default is the next candidate in the step's list
    # max: 1        # optional: hops, 1..5, default 1
```

- `when:` is an expression ([[Workflows]] condition grammar) over **this step's
  answers only** — its roots are the question names. Anything else is a load
  error. It is an expression rather than one confidence number because the
  uncertain zone depends on where the workflow's own decision boundary sits.
- A pair that already answered is never asked again. With nobody left to ask,
  the original answer stands.
- The newer answer replaces the older. The replaced one is kept:

  ```json
  {
    "refuted": { "type": "noul", "noul": 0.91 },
    "_by": "paseo/claude-sonnet-5",
    "_escalated_from": { "_by": "jev/jev-1", "answers": { "refuted": { "type": "noul", "noul": 0.66 } } }
  }
  ```

  On a chain (`max` > 1) each `_escalated_from` nests its own predecessor.
- Each escalation is audited (`decide_escalated`).

Probabilities from an agent runtime are self-reported by an LLM, not
calibrated; a threshold means something different on each backend. `observe:`
records both answers so the difference can be measured.

## `observe:` — record every decision

```yaml
decide:
  # …
  observe: review_log      # a SQL stores: entry (sqlite / postgres / mysql)
```

Every answer the step produced — the initial one and each escalation — is
written to a `conductor_decisions` table (created on first use), one row per
question: `recorded_at`, `run_id`, `repo`, `number`, `step` (the step identity),
`step_id`, `question`, `hop` (0 = the initial answer), `final` (1 = the answer
the workflow used), `answered_by`, and `answer` (the answer as JSON). Join on
`run_id` against run history and [[Outcomes]] to calibrate thresholds.

Recording is best-effort: a store that is down or misconfigured is logged and
audited (`decide_observe_failed`), and the step still succeeds.

## `protocol:`

Optional. Omitted is `system_one/v1`, the only version today.

## Decision runtimes

A decision runtime is a runtime plugin that declares the decision protocols it
answers natively (`Decl.Protocols`) and serves the `decide` and `models` verbs
([[Plugins]]). It serves **decide steps only**:

- agent resolution never considers it, so an agent step never lands on it,
  even when the step's fleet lists its models;
- it gets no agent controller;
- an agent step that pins it with `runtime:` is refused at dispatch.

Its `connection:` block carries what the plugin needs on every call — a key, an
endpoint. Values may be secret references (`env:NAME`, a vault reference),
resolved at boot and redacted like any secret:

```yaml
runtimes:
  paseo: { default: true }
  jev:
    connection:
      api_key: env:JEV_API_KEY

packs:
  review:
    source: github.com/NodeSpy/conductor-packs//pr-review-team
    models:
      light: ["jev-*", "claude-sonnet-*"]    # opt this pack's light tier in
```

Installing a decision runtime changes nothing on its own: a decide step reaches
it only when its tier lists the runtime's models. A decide step sends its
`state` to that runtime — for a hosted one, to a third-party API.

## Packs

A pack's decide steps take two instance settings, applied to every decide step
the pack ships:

```yaml
packs:
  review:
    decide:
      observe: review_log      # one of YOUR SQL stores
      escalate: false          # turn off the pack's escalation
```

`observe` fills a step that names no store of its own; `escalate: false`
removes every `escalate:` the pack declares. A pack's own fleets named in
`escalate.to` are namespaced with the pack, as `model:` is.
