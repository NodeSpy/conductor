# Unified `filter:` — one composable trigger filter

Status: design / phase 1 in progress. This is the implementation contract.

## Why

A trigger's `filters:` is a grab-bag with three inconsistent shapes of the same
idea (a predicate over the event/PR):

- `exclude` — a denylist, **OR** across `branches`/`labels`/`title`, negated.
- `gates` — readiness requirements, **AND** across `not_draft`/`merge_state`/….
- a dozen per-kind match keys — `labels_any`, `labels_all`, `authors`,
  `from_users`, `ignore_users`, `author_bot`, `sole_assignee`, `require_label`,
  `reviewer`, … — each with its own baked-in combination rule.

You cannot say "skip a review only when it is a release PR **and** on a release
branch" without the operator reverse-engineering which block ANDs and which ORs.
This bit us live: `exclude.title: ['Release ']` is a case-insensitive
**substring** match, so it silently skipped RosterStream#5590 ("changelog:
publish each **release** entry…").

The fix is not another key. It is **one** key, `filter:`, whose *shape* is its
composition, evaluated on the `expr` engine that already powers step `if:`
(`internal/expr`). `exclude`, `gates`, `when` all dissolve into it.

## The grammar

`filter:` is polymorphic. The YAML shape IS the boolean structure:

| shape | meaning |
|---|---|
| **string** | an `expr` boolean over the connector's facts |
| **object (map)** | its keys **AND**-ed together |
| **array (list)** | its entries **OR**-ed together |

Nest freely: an array of objects (OR of ANDs), an object with an array-valued
key, a string anywhere a leaf is allowed.

```yaml
# string — full boolean, the escape hatch
filter: "!is_draft && !contains(title, 'Release')"

# object — AND of structured keys
filter: { not_draft: true, authors: [dependabot] }

# array — OR of branches
filter:
  - { authors: [dependabot], not_draft: true }     # a ready bot PR …
  - { labels_any: [urgent, security] }             # … OR anything urgent/security …
  - "!is_draft && !contains(title, 'Release')"     # … OR the general case minus releases
```

### The object `expr:` escape

An object AND-s its keys. To keep negation ("skip if…") from needing a separate
`exclude` concept, an object may carry a reserved **`expr:`** string key,
AND-ed with its siblings:

```yaml
filter: { not_draft: true, expr: "!contains(title, 'Release')" }
```

`expr:` is the only reserved key; every other key is a connector match key.

## Facts

Facts are a flat `map[string]any` the connector computes for the event being
filtered. The **string** form (`expr`) reads them by name; the **object** match
keys are evaluated against the same values. Facts are per-connector — the
grammar is universal, the fact *names* are not.

**github facts (phase 1):** `head_branch`, `base_branch`, `title`,
`labels` (`[]string`), `is_draft` (bool), `merge_state` (string, e.g. `CLEAN`),
`review_decision` (string, e.g. `APPROVED`), `non_author_approval` (bool),
`threads_resolved` (bool), `author` (login). Event-specific where present:
`comment_author`, `comment_body`, `reviewer`. These are exactly the values
`draftGate`/`mergeGatePasses`/the sweep already compute in
`internal/integrations/github` — this design only *exposes* them.

## The Filter IR

Decode `filter:` into a small tree, then evaluate against a facts map. Keep the
IR connector-agnostic; only `Match` and the fact map are connector-aware.

```
Filter =
  | And([]Filter)          // object, and array-of-… when combined
  | Or([]Filter)           // array
  | Not(Filter)            // used by legacy exclude lowering; also `!` inside expr
  | Expr(string)           // string form / object `expr:` key → expr.Eval
  | Match(key, value)      // one structured object key (labels_any, not_draft, …)
```

**Decode:**
- string → `Expr(s)`
- array `[e1, e2, …]` → `Or([decode(e1), decode(e2), …])`
- object `{k1: v1, …}` → `And([...])` where each non-`expr` key → `Match(k, v)`
  and an `expr:` key → `Expr(v)`. Key order does not matter (AND is commutative);
  decode deterministically (sort keys) so errors and any serialization are stable.

**Evaluate `(f Filter, facts map[string]any) (bool, error)`:**
- `And` → all children true (short-circuit false)
- `Or` → any child true (short-circuit true)
- `Not` → negate child
- `Expr(s)` → `expr.Eval(s, facts)`
- `Match(k, v)` → the github match predicate for `k` (below)

An empty/absent `filter:` is `true` (fires).

### github `Match` predicates (phase 1)

Port the existing semantics exactly (so lowering is behavior-identical):

| key | value | true when |
|---|---|---|
| `branches` | `[]glob` | `path.Match(glob, head_branch)` for any glob |
| `base_branches` | `[]glob` | same against `base_branch` |
| `title` | `[]string` | `head_branch`… no — `contains(lower(title), lower(s))` for any `s` (substring — the legacy behavior; document the footgun and point new configs at `expr` + `startswith`/`contains`) |
| `labels_any` | `[]string` | PR has ANY (case-insensitive) |
| `labels_all` | `[]string` | PR has ALL |
| `authors` | `[]login` | `author` ∈ set |
| `from_users` | `[]login` | comment/review author ∈ set |
| `ignore_users` | `[]login` | comment/review author ∉ set |
| `author_bot` | bool | author-is-bot == value |
| `sole_assignee` | bool | you are the only assignee |
| `require_label` | string | PR has that label |
| `not_draft` | bool | value ? `!is_draft` : true |
| `merge_state` | bool | value ? `merge_state == CLEAN` : true |
| `review_decision` | bool | value ? `review_decision == APPROVED` : true |
| `non_author_approval` | bool | value ? `non_author_approval` : true |
| `threads_resolved` | bool | value ? `threads_resolved` : true |

Reuse the existing helpers (`config.Exclude.Matches`, `draftGate`,
`mergeGatePasses`, `prReviewerMatches`, the labels/authors matchers) as the
bodies of these predicates rather than reimplementing them.

## Legacy lowering (back-compat — non-negotiable)

The current `filters: { … }` block **lowers into a Filter IR** so existing
configs are bit-identical. The block is an implicit AND of:

- `exclude: {branches, labels, title}` → `Not(Or([Match(branches,…), Match(labels_any,…)?, Match(title,…)]))`
  (exclude is an OR-denylist; "not excluded" is `Not(Or(...))`). Honor the
  opt-in `exclude.match: all` too if it exists — but that field is superseded by
  the new grammar and need not be added if not already present.
- `gates: {not_draft, merge_state, …}` → `And([Match(not_draft,true), …])`
- match keys (`labels_any`, `authors`, `from_users`, `ignore_users`,
  `author_bot`, `reviewer`, …) → `And([Match(k, v), …])`

i.e. the whole legacy block → `And([ Not(excludeOr), gates…, matches… ])`.

Wire it so the github keep-conditions (`sweep.go:485`, `events.go:543/847/1111`)
call **one** IR evaluator built from either the new `filter:` or the lowered
legacy block — the old `Exclude.Matches`/`draftGate`/`mergeGatePasses` call sites
are replaced by the single IR eval, but their *logic* is reused inside `Match`.

**Parity is the acceptance test:** a table of representative legacy configs must
evaluate identically before and after (same keep/skip for the same PR facts).

## Validation

At load time, reject a `filter:` that references something the connector does
not provide:

- Every fact path a string/`expr:` references must be a declared github fact.
- Every object key must be a declared github match key (or `expr:`).
- Type-check values (`labels_any` is a list, `not_draft` is a bool, …).

Surface the connector's fact/key set from its schema (`internal/connector/github.go`),
next to the existing filter-schema entries.

## Worked example — the #5590 fix

Legacy (false-excluded #5590 because `title` is a substring OR):
```yaml
filters:
  exclude: { branches: [staging, prod], title: ['Release '] }
  gates: { not_draft: true }
```
Unified — says exactly what was meant, AND across the two:
```yaml
filter: "!is_draft && !( (head_branch == 'staging' || head_branch == 'prod') && contains(title, 'Release ') )"
```
`codex-changelog` ≠ staging/prod → the inner AND is false → not excluded → #5590
reviews. A real `Release 2.4.0` PR on `staging` → inner AND true → skipped.

## Scope

**Phase 1 (this work):** the grammar + IR + evaluator; github facts + `Match`
predicates; the legacy-block lowering (behavior-identical); load-time
validation; unit tests (grammar, IR eval, **legacy parity**, the #5590 case);
one docker e2e scenario using `filter:`; user docs. `filter:` and `filters:`
coexist — a trigger may set at most one.

**Phase 2 (later):** other connectors' fact models (slack/sentry/pagerduty);
`conductor config migrate` rewriting `filters:` → `filter:`; deprecation of the
`filters:` block once configs have migrated.

## Non-goals

- No new expression language — reuse `internal/expr` (`&&`, `||`, `!`, `==`,
  `contains`, comparisons). If the design needs `startswith`/`in`, add them to
  `expr` as small, additive, separately-tested helpers.
- No behavior change for any existing config — lowering must be a no-op in
  observable behavior.
