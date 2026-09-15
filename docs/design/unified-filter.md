# Unified `filter:` — one composable trigger filter

Status: implemented. This describes the grammar as shipped. The decisions that
removed the legacy `filters:` block are recorded in
[unified-filter-phase2.md](unified-filter-phase2.md).

## Why

A trigger's `filters:` was a grab-bag with three inconsistent shapes of the
same idea (a predicate over the event/PR):

- `exclude` — a denylist, **OR** across `branches`/`labels`/`title`, negated.
- `gates` — readiness requirements, **AND** across `not_draft`/`merge_state`/….
- a dozen per-kind match keys — `labels_any`, `labels_all`, `authors`,
  `from_users`, `ignore_users`, `author_bot`, `sole_assignee`, `require_label`,
  `reviewer`, … — each with its own baked-in combination rule.

You could not say "skip a review only when it is a release PR **and** on a
release branch" without reverse-engineering which block ANDs and which ORs.
This bit us live: `exclude.title: ['Release ']` is a case-insensitive
**substring** match, so it silently skipped RosterStream#5590 ("changelog:
publish each **release** entry…").

The fix is not another key. It is **one** key, `filter:`, whose *shape* is its
composition, evaluated on the `expr` engine that already powers step `if:`
(`internal/expr`). `exclude`, `gates`, `when` all dissolve into it — and so
does the repo routing, which is why `filters:` could be deleted outright rather
than deprecated.

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
filter: { not_draft: true, author: [dependabot] }

# array — OR of branches
filter:
  - { author: [dependabot], not_draft: true }      # a ready bot PR …
  - { label_any: [urgent, security] }              # … OR anything urgent/security …
  - "!is_draft && !contains(title, 'Release')"     # … OR the general case minus releases
```

### The object `expr:` escape, and the `not_` prefix

An object ANDs its keys. Two of them are the grammar's own rather than a
connector's:

- **`expr:`** — a condition string, AND-ed with its siblings.
- **`not_<anything>`** — negation. `not_expr:` negates a condition;
  `not_<matchkey>:` negates that match key. The prefix produces a `Not` around
  exactly what the un-prefixed key would have produced, so a connector declares
  one key and gets both spellings; its matcher only ever sees BASE keys.

```yaml
filter: { not_draft: true, not_expr: "contains(title, 'Release')" }
```

A key and its `not_` twin are distinct keys: both in one object is legal and
they AND. That is how `from_users` + `ignore_users` collapsed into
`comment_author` + `not_comment_author` — one key, and the relationship between
the two spellings is visible in their names.

## Facts

Facts are a flat `map[string]any` the connector computes for the event being
filtered. The **string** form (`expr`) reads them by name; the **object** match
keys are evaluated against the same values. Facts are per-connector — the
grammar is universal, the fact *names* are not.

**github facts:** `head_branch`, `base_branch`, `title`, `labels` (`[]string`),
`is_draft` (bool), `merge_state` (string, e.g. `CLEAN`), `review_decision`
(string, e.g. `APPROVED`), `non_author_approval` (bool), `threads_resolved`
(bool), `author` (login). Event-specific where present: `comment_author`,
`comment_body`, `reviewer`, `sole_assignee`, `author_is_bot`. These are exactly
the values `draftGate`/`mergeGatePasses`/the sweep already compute in
`internal/integrations/github` — this design only *exposes* them.

Five github events publish predicate facts (`review_requested`,
`changes_requested`, `new_comment`, `issue_matched`, `merge_ready`); the rest
publish none and accept only the routing keys.

## The Filter IR

Decode `filter:` into a small tree, then evaluate against a facts map. The IR
is connector-agnostic; only `Match` and the fact map are connector-aware.

```
Filter =
  | And([]Filter)          // object, and array-of-… when combined
  | Or([]Filter)           // array
  | Not(Filter)            // the `not_` prefix; also the exclude lowering
  | Expr(string)           // string form / object `expr:` key → expr.Eval
  | Match(key, value)      // one structured object key (label_any, draft, …)
```

**Decode:**
- string → `Expr(s)`
- array `[e1, e2, …]` → `Or([decode(e1), decode(e2), …])`
- object `{k1: v1, …}` → `And([...])` where each key → `Match(k, v)`, an `expr:`
  key → `Expr(v)`, and a `not_`-prefixed key → `Not(…)` of either. Key order
  does not matter (AND is commutative); decode deterministically (sort keys) so
  errors and any serialization are stable.

**Evaluate `(f Filter, facts map[string]any) (bool, error)`:**
- `And` → all children true (short-circuit false)
- `Or` → any child true (short-circuit true)
- `Not` → negate child
- `Expr(s)` → `expr.Eval(s, facts)`
- `Match(k, v)` → the connector's match predicate for `k`

An empty/absent `filter:` is `true` (fires). An evaluation error fails CLOSED.

### github `Match` predicates

No key carries a baked polarity — negation is the grammar's. The full table,
with what each reads and what it means, is in
[unified-filter-phase2.md §The github match keys](unified-filter-phase2.md).
Each predicate's body is the existing helper (`config.Exclude.Matches`,
`draftGate`/`mergeGatePasses`'s readers, `matchRepo`, the labels/authors
matchers) rather than a reimplementation.

`repo` / `not_repo` are special: they are **routing**, hoisted out of the
filter into the structural repo gate and the sweep's scope, and legal on every
github event because they need no fact. See phase 2 §2.

## Intrinsic defaults

Every keep-condition site evaluates exactly ONE filter: the trigger's own when
it states a predicate, otherwise the event's **intrinsic default**, lowered into
the same IR by `lowerX` in `internal/integrations/github/filter.go`. The
defaults that matter:

- `merge_ready` keeps its five opt-out gates enforced.
- `ready_for_review` deliberately skips the draft gate — the PR just left
  draft.
- everything else defaults to "fire".

A trigger that states a predicate takes that decision over, which is what
waiving a gate (`merge_state: false`) has always meant.

## Validation

At load time, reject a `filter:` that references something the connector does
not provide:

- Every fact path a string/`expr:` references must be a declared fact for that
  event.
- Every object key must be a declared match key (or `expr:`); `not_<key>` is
  checked as `<key>`.
- Type-check values (`label_any` is a list, `draft` is a bool, …).
- An event with no facts and no match keys refuses `filter:` outright.

The surface comes from the connector's own schema
(`internal/connector/github.go` reading `gh.FilterFacts`/`gh.FilterMatchKeys`),
so a fact cannot be declared without being published.

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
…or in structured form, negating the conjunction rather than each arm:
```yaml
filter:
  not_draft: true
  not_expr: "(head_branch == 'staging' || head_branch == 'prod') && contains(title, 'Release ')"
```
`codex-changelog` ≠ staging/prod → the inner AND is false → not excluded →
#5590 reviews. A real `Release 2.4.0` PR on `staging` → inner AND true →
skipped.

## Non-goals

- No new expression language — reuse `internal/expr` (`&&`, `||`, `!`, `==`,
  `contains`, `startswith`, comparisons).
- No behavior change for a trigger that states no predicate — the intrinsic
  defaults are the pre-filter keep-conditions, and the parity tests
  (`filter_parity_test.go`) call the shipped legacy predicates as their oracle.

## Scope

**Shipped (phase 1):** the grammar + IR + evaluator; github facts and `Match`
predicates; load-time validation; unit tests including legacy parity and the
#5590 case; a docker e2e scenario.

**Shipped (phase 2):** the universal `not_` prefix; `repo`/`not_repo` routing;
polarity-free match-key names; `ignore_checks`/`reviewer`/`assignee`/
`include_prereleases` moved to `options:`; `filters:` deleted from the schema;
`conductor config migrate` emitting `filter:`; the grammar generalised to the
generic source connectors. See
[unified-filter-phase2.md](unified-filter-phase2.md).

**Later:** richer fact models for slack/sentry/pagerduty (they currently filter
over their declared context keys).
