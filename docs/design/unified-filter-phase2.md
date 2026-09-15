# Unified `filter:` — phase 2: the sole filter key

Status: implemented. This is the contract phase 2 was built to; it supersedes
the phase-scoping section of [unified-filter.md](unified-filter.md), which now
describes the grammar as shipped.

## Why phase 2 exists

Phase 1 shipped `filter:` as a **sibling** of the legacy `filters:` block. A
trigger could carry both, with a load-time rule that they must not both hold a
predicate — which meant two keys one letter apart, a rule to remember about
which of them may say what, and `filters:` still required for anything the new
grammar could not express. That is the exact confusion the unification existed
to end, and it was rejected.

Phase 2 makes `filter:` the **only** filter key there is, and deletes `filters:`
from the schema. The strict parser refuses it everywhere it used to appear
(trigger, per-source block in an `on:` list, pack trigger arm, pack `on:`
overlay) and names `filter:` in the error, because a config carrying `filters:`
is not a typo — it is a config from before the rename.

For that to be possible, `filter:` has to express everything the old block did:
both the event **predicate** and the **routing** that decided which repos a
trigger applies to.

## The grammar (unchanged shapes, two additions)

`filter:` stays polymorphic — **string** = expr, **object** = AND of its keys,
**array** = OR of its elements, nested freely. Phase 2 adds:

### 1. A universal `not_` prefix, in the config layer

In a mapping node, any key `not_<X>` decodes to `Not(<whatever X would
produce>)`:

- `not_expr: "…"` → `Not(Expr(…))`
- `not_<matchkey>: v` → `Not(Match(<matchkey>, v))`

`expr` / `not_expr` are the only non-match keys; every other key is a connector
match key and gets its `not_` twin **for free**. The twin is a `Not` wrapping
the base key's node — *not* a separate matcher case. A connector's matcher
therefore only ever sees BASE keys, and no key can carry a polarity that
contradicts its own name.

This lives in `Filter.decode` (`internal/config/filter.go`), so it is
connector-agnostic: slack, rss and plugin sources get `not_` over their own
declared keys without writing a line.

Details: a key and its `not_` twin are **distinct keys** — writing both in one
object is legal and they AND (`{comment_author: [alice, ci-bot],
not_comment_author: [ci-bot]}`). A bare `not_` is not a prefix and stays a
match key. Decode keeps the existing sorted-key visiting, duplicate-key
detection, depth bound, and `raw` preservation for the marshal round trip.

### 2. `repo` / `not_repo` routing keys on every github event

The one-key replacement for `filters: {repos, exclude_repos}`. They are
**structural**, not a predicate: `internal/connector.githubImpl.lowerTrigger`
hoists them out of the decoded filter into `Action.Repos` /
`Action.ExcludeRepos`, which feed two things no keep-condition can serve:

- `emit()`'s per-variant repo gate (`events.go`), which runs before any event
  is evaluated;
- the sweep's repo scope and the stuck-checks poller's watch list
  (`sweep.go`), which iterate a repo LIST with no event in hand at all.

Because the gate is structural, the keys need no per-event fact declaration —
so `filter: {repo: […]}` works on every github event, including the four
(`failing_checks`, `merge_conflict`, `pr_behind`, `stuck_checks`) that publish
no predicate facts and evaluate no keep-condition. That is what made deleting
`filters:` possible at all.

**What is hoisted.** `Action.Repos` is the UNION of every `repo` value anywhere
in the filter, skipping negated branches (`Filter.MatchUnion`). A union is a
SUPERSET of what an Or of repo sets admits, which is the safe direction: it only
widens what is CONSIDERED, and the filter itself still decides precisely.
`Action.ExcludeRepos` takes only TOP-LEVEL `not_repo` (`Filter.TopLevelNegated`)
— a conjunct of the root holds for every event the filter admits, so hoisting
it is sound, while the same key under an Or arm is conditional and hoisting it
would suppress events the filter says should fire.

`repo` is also evaluable as a match key (a glob match against an injected
`repo` fact), so a nested `repo` — the case the hoist can only approximate — is
still decided exactly at the keep-condition.

### 3. Routing-only means "no predicate stated"

`filters: {repos: […]}` never touched an event's keep-condition. `filter:
{repo: […]}` is its replacement, so it must not either — otherwise every
migrated trigger that only scoped its repos would silently drop its event's
intrinsic default, and a `merge_ready` trigger would unlock the merge of an
unreviewed PR.

So: a filter that is nothing but a **flat conjunction** of `repo` / `not_repo`
(`Filter.FlatConjunctionOf`) leaves `Action.Filter` nil, and the event's
intrinsic default keep-condition still applies. Anything else — a predicate key,
an expr, an Or over repo sets, a negation under an Or — is either a real
predicate or a shape the structural pre-gate can only approximate, so the filter
is kept and evaluated.

*This is a decision phase 2 made that the original spec did not cover; see
"Choices made" below.*

## The github match keys

No key carries a baked polarity — negation is the grammar's `not_`. Only base
keys exist; each is also legal as `not_<key>`.

| key | value | reads | true when |
|---|---|---|---|
| `repo` | `[]glob` | *(routing)* | `path.Match(glob, repo)` for any glob. Hoisted to `Action.Repos`; `not_repo` → `Action.ExcludeRepos` |
| `branch` | `[]glob` | `head_branch` | any glob matches (was `branches`) |
| `base_branch` | `[]glob` | `base_branch` | any glob matches (was `base_branches`) |
| `title` | `[]string` | `title` | case-insensitive **substring** for any entry — the legacy footgun, kept; new configs should use `expr` + `startswith()`/`contains()` |
| `label_any` | `[]string` | `labels` | has ANY (case-insensitive) (was `labels_any`) |
| `label_all` | `[]string` | `labels` | has ALL (was `labels_all`) |
| `require_label` | `string` | `labels` | has that label |
| `author` | `[]login` | `author` | author ∈ set (was `authors`) |
| `comment_author` | `[]login` | `comment_author` | commenter ∈ set (was `from_users`; `not_comment_author` is the old `ignore_users`) |
| `author_bot` | `bool` | `author_is_bot` | author-is-bot == value |
| `draft` | `bool` | `is_draft` | `is_draft == value`. `not_draft: true` is "require NOT draft" — the old `gates.not_draft` intent, through the generic negation |
| `sole_assignee` | `bool` | `sole_assignee` | value ? you are the only assignee : true |
| `merge_state` | `bool` | `merge_state` | value ? `== CLEAN` : true |
| `review_decision` | `bool` | `review_decision` | value ? `== APPROVED` : true |
| `non_author_approval` | `bool` | `non_author_approval` | value ? approved by a non-author : true |
| `threads_resolved` | `bool` | `threads_resolved` | value ? all threads resolved : true |

Each matcher's BODY is unchanged from the matcher its call site used
(`config.Exclude.Matches`, `anyFold`/`allFold`, `containsFold`, `loginMatch`,
`authorBotMatch`, `matchRepo`, the gate readers), so semantics are
bit-preserved. A key is legal for an event exactly when the event publishes the
fact it reads — except `repo`, which every event accepts.

The last five stay opt-OUT toggles (`false` waives the check, `true` enforces
it) because that is what `gates:` meant and what `merge_ready`'s default
lowering relies on. `draft` is the exception: it became a plain equality so the
`not_` prefix could carry its polarity.

## What moved to `options:`

Four former `filters:` keys were never predicates over the event, so they are
declared in the failing_checks / review_requested / issue_matched / release
OPTION schemas (`internal/connector/github.go`) and read from `t.Spec.Options`:

| key | event | why it is not a predicate |
|---|---|---|
| `ignore_checks` | `failing_checks` | per-CHECK suppression: it decides which failing check is an event at all, one check at a time, before any trigger is consulted (`events.go` ~453) |
| `reviewer` | `review_requested` | an identity gate resolved against the connector's `me:`, not a published fact |
| `assignee` | `issue_matched` | same |
| `include_prereleases` | `release` | a release-payload switch evaluated outside any filter (`events.go` ~750) |

`gates.no_branch` and `gates.project` have no unified spelling — they read a
GraphQL enrichment rather than a published fact — and no connectors-model
config can set them any more. `conductor config migrate` says so per
occurrence rather than dropping them silently.

## Behavior preservation

The `lowerX` functions and `filterPasses`'s `if f == nil { f = <default> }`
fallback are KEPT. With the `filters:` surface gone, `Action`'s legacy predicate
fields (`Exclude`, `Gates`, `IgnoreUsers`, `FromUsers`, `LabelsAny`, …) are only
ever zero in the connectors model, so the lowerings now yield each event's
**intrinsic default** keep-condition:

- `merge_ready` — `mergeGateOn(nil, k)` is true for all five, so the full
  all-green requirement stays enforced by default. This is the only lowering
  that is not vacuous.
- `review_requested` — `gateEnabled(nil, "not_draft")` is false, so no draft
  gate: "always keep".
- `ready_for_review` — deliberately never lowers a draft gate at all. The PR
  just left draft; re-applying the gate is what the transition exists to undo.
- `new_comment`, `issue_matched`, `changes_requested` — vacuous.

The fields are still READ rather than assumed zero, because a legacy
integration config (`internal/integrations/github`'s own `rules:`/`actions:`
YAML, which `core.Build` decodes straight into `config.Action`) can still set
them — and because "no behavior change when a trigger sets no filter" is only
demonstrable if the same code path produces both.

## Packs

`TriggerArm.Filters` is gone; `TriggerArm.Filter *Filter` replaces it.
`TriggerArm.Repos` STAYS a field of its own (pack.go §7): the repo list is the
security boundary an operator grants a third-party pack, and a boundary you can
only state one way is a boundary you can audit.

Arming composes an AND of three things — the arm's repo scope (as a `repo`
match), the shipped trigger's own `filter:`, and the arm's `filter:` override.
An `And` operand is spliced rather than nested, so every conjunct of the result
is a conjunct of its ROOT and the consent check can still see the repo scope. A
single live operand passes through untouched, so the common case keeps the
author's own YAML verbatim. The array form (one trigger armed for two repo sets)
works unchanged; a named arm's `filter:` REPLACES the `"*"` wildcard's, for the
same reason `Repos` does — an operator writing a filter on one trigger must be
able to loosen what `"*"` set, not only tighten it.

A pack's own shipped top-level `repo` scope is stripped at ship time
(`clearTriggerRepos`), so a pack cannot arrive pre-scoped to its author's
repos. A `repo` inside an Or arm is part of the pack's predicate, not a scope,
and is left alone; the consent check then refuses to arm that trigger until the
consumer names repos of their own — the fail-closed direction.

## An internal marshal form

"AND of two arbitrary filters" has no surface spelling: an object ANDs KEYS and
a list ORs. Pack arming needs exactly that, and several internal round trips
re-serialise a config (`cloneTriggerSpec`, `connector.buildIntegration`), so a
composed filter must survive YAML.

`Filter.MarshalYAML` therefore emits the operator's `raw` when it has one — an
authored filter comes back out of every round trip byte-identical — and falls
back to `x_filter_op` / `x_filter_kids` / `x_filter_key` / `x_filter_val` /
`x_filter_expr` for a node built in code. `decode` reads that form back
losslessly. The keys are `x_`-prefixed like `Action`'s other lowering-only
fields (`x_repos`, `x_flow_ref`), appear in no user-facing config, and are
documented nowhere an author reads.

`config.FilterFromValue` builds a Filter from a plain Go value in the same three
shapes the YAML accepts, going through the real decoder — so the synthesising
producers (`conductor config migrate`, pack arming) cannot invent a shape the
grammar would refuse, and what they build marshals back out.

## Other connectors

`TriggerSpec.Filters` was connector-agnostic, so deleting it would have deleted
slack's `channel`/`users`, rss's `match`, and every plugin source's own filter
keys along with github's. Instead, `filter:` was generalised to them:
`TypeDecl.Filter` is now called ONE MATCH KEY AT A TIME
(`connector.GenericFilterMatcher`, `pluginFilterMatch`), so the grammar owns the
boolean structure and the connector answers "does this key hold". An event that
declares neither `Facts` nor `MatchKeys` has its `filters:` schema read as its
match-key set and its `context:` as its facts (`EventDecl.FilterKeys` /
`FilterFacts`). Those connectors gain AND/OR nesting, `not_`, and `expr:` over
the keys they already declared, without restating any of them.

*This too is beyond the original spec; see below.*

## Validation

Unchanged in shape, tightened in reach (`internal/connector/filter.go`):

- an event with no facts AND no match keys refuses `filter:` outright;
- every fact an expr string reads must be one the event publishes — so a
  predicate on a factless event (`gh.merge_conflict`) is refused, while routing
  on it is fine;
- every object key must be a declared match key or `expr:`;
- values are type-checked against the declared kind.

Only BASE keys are checked: the grammar turns `not_<key>` into a `Not` around
`<key>`, so a connector declares one key and both spellings validate.

An event that declares EITHER `Facts` or `MatchKeys` owns its whole surface and
the generic schemas are not consulted — otherwise a github event with routing
keys but no predicate facts would quietly validate an expr against a context
map nothing evaluates a filter with.

## Migration

`conductor config migrate` emits `filter:` (never `filters:`):
`repos`→`repo`, `exclude_repos`→`not_repo`, `exclude.branches`→`not_branch`,
`exclude.labels`→`not_label_any`, `exclude.title`→`not_title`,
`labels_any`→`label_any`, `labels_all`→`label_all`, `authors`→`author`,
`from_users`→`comment_author`, `ignore_users`→`not_comment_author`; and
`reviewer`/`assignee`/`ignore_checks`/`include_prereleases` into `options:`.

Two translations to note:

- A legacy gate set to **false** waived it. There is nothing to write for that:
  a filter REPLACES the event's intrinsic default, so a waived gate is simply a
  conjunct the filter does not carry.
- `merge_ready`'s gates are the mirror image (absent meant ENFORCED), so every
  on-gate is emitted EXPLICITLY whenever that kind gets a filter. Leaving them
  implicit would silently relax the one event whose default is not "fire".

## Choices made beyond the spec

Three decisions the phase-2 spec did not cover, each taken for "keeps existing
behavior, least surprising", and each called out here because they are the
places a reader might expect something else:

1. **Routing-only filters leave the intrinsic default in place** (§3 above).
   Without this, migrating `filters: {repos: […]}` to `filter: {repo: […]}` on
   a `merge_ready` trigger would drop its five gates. The spec's own framing —
   routing keys "do not gate the EVENT" — is what this preserves. The predicate
   is replaced the moment a filter names anything else.

2. **`filter:` was generalised to the generic connectors** rather than letting
   slack/rss/plugin filtering disappear with `TriggerSpec.Filters` (§Other
   connectors). No in-repo fixture exercised those filters, so either choice
   kept CI green; silently removing a capability seemed worse than extending
   the grammar to it.

3. **`reviewer` / `assignee` / `include_prereleases` moved to `options:`**,
   following the `ignore_checks` precedent the spec set explicitly. All four are
   read by code that still runs (`act.Reviewer`, `act.Assignee`,
   `act.IncludePrereleases`), and none is a predicate over a published fact, so
   leaving them with no surface would have been a silent capability loss.

One known rough edge, stated rather than hidden: on an event with no
keep-condition (`failing_checks`, `merge_conflict`, `pr_behind`,
`stuck_checks`), a NON-flat routing filter — `filter: [{repo: [a]}, {repo:
[b]}]` — is routed by the hoisted union and its Or structure is never
evaluated, because there is no keep-condition to evaluate it in. The union is a
superset, so it fires for `a` and `b` as written; a `not_repo` under an Or arm
on such an event would not be applied. Validation cannot catch this (the filter
is well-formed), and the flat spelling `{repo: [a, b]}` is equivalent and what
anyone would write.
