# Phase 2 of the unified `filter:` — implementation report

Branch `feat/unified-filter-phase2`, based on `main` = v0.9.10 (`f8e7ed1`).

Implementation commit: **`b28816a2c0562b23c2b019086dc0a780b8baf4e6`**
(72 files changed, 2982 insertions, 981 deletions). This report is committed on
top of it.

Nothing was pushed, tagged, or opened as a PR. `~/.config/conductor/` was not
touched. (Note: `conductor schema` run without `--config` picks up the live
config, and that config still carries `filters:` — it now fails to load with
the migration error. The operator will need to migrate it; that is theirs to
do, and I left it alone.)

---

## 1. Verification

### `gofmt -l` on every touched file

```
$ gofmt -l $(git status --porcelain | awk '{print $NF}' | grep '\.go$')
$
```

Empty — clean.

### `go build ./...`

```
$ go build ./...
$ echo rc=$?
rc=0
```

No output, exit 0.

### `go vet ./...`

```
$ go vet ./...
$ echo rc=$?
rc=0
```

No output, exit 0.

### `go test ./...`

All 41 packages with tests pass. Exit 0, no `FAIL` lines.

### `go test -race ./...`

Exit 0. No races, no leaks, no `FAIL` lines (`grep -cE "^(FAIL|---)"` → `0`).
Full tail:

```
ok  	github.com/NodeSpy/conductor/cmd/conductor	(cached)
ok  	github.com/NodeSpy/conductor/internal/acp	(cached)
ok  	github.com/NodeSpy/conductor/internal/blob	(cached)
ok  	github.com/NodeSpy/conductor/internal/callable	(cached)
ok  	github.com/NodeSpy/conductor/internal/code	(cached)
ok  	github.com/NodeSpy/conductor/internal/config	(cached)
ok  	github.com/NodeSpy/conductor/internal/connector	(cached)
ok  	github.com/NodeSpy/conductor/internal/controller	(cached)
ok  	github.com/NodeSpy/conductor/internal/core	2.093s
ok  	github.com/NodeSpy/conductor/internal/cost	(cached)
ok  	github.com/NodeSpy/conductor/internal/dispatch	(cached)
ok  	github.com/NodeSpy/conductor/internal/engine	(cached)
ok  	github.com/NodeSpy/conductor/internal/expr	(cached)
ok  	github.com/NodeSpy/conductor/internal/flow	(cached)
ok  	github.com/NodeSpy/conductor/internal/gitdiff	(cached)
ok  	github.com/NodeSpy/conductor/internal/gitwt	(cached)
ok  	github.com/NodeSpy/conductor/internal/handoff	(cached)
ok  	github.com/NodeSpy/conductor/internal/hosts	(cached)
ok  	github.com/NodeSpy/conductor/internal/inbound	(cached)
ok  	github.com/NodeSpy/conductor/internal/integrations/cron	(cached)
ok  	github.com/NodeSpy/conductor/internal/integrations/github	(cached)
ok  	github.com/NodeSpy/conductor/internal/integrations/rss	(cached)
ok  	github.com/NodeSpy/conductor/internal/integrations/slack	(cached)
ok  	github.com/NodeSpy/conductor/internal/integrations/webhook	(cached)
ok  	github.com/NodeSpy/conductor/internal/kv	(cached)
ok  	github.com/NodeSpy/conductor/internal/memory	2.291s
ok  	github.com/NodeSpy/conductor/internal/migrate	(cached)
ok  	github.com/NodeSpy/conductor/internal/models	(cached)
ok  	github.com/NodeSpy/conductor/internal/netguard	(cached)
ok  	github.com/NodeSpy/conductor/internal/notify	(cached)
ok  	github.com/NodeSpy/conductor/internal/plugin	(cached)
ok  	github.com/NodeSpy/conductor/internal/sandbox	(cached)
ok  	github.com/NodeSpy/conductor/internal/secrets	(cached)
ok  	github.com/NodeSpy/conductor/internal/skill	(cached)
ok  	github.com/NodeSpy/conductor/internal/sqlstore	(cached)
ok  	github.com/NodeSpy/conductor/internal/store	(cached)
ok  	github.com/NodeSpy/conductor/internal/vaults	(cached)
ok  	github.com/NodeSpy/conductor/pkg/githubkit	(cached)
ok  	github.com/NodeSpy/conductor/pkg/plugin	(cached)
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	(cached)
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeacp	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeagentdeck	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakecli	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeopencode	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakepaseo	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fixer	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/mockgithub	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-echo	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-runtime	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-ticker	[no test files]
```

(The first `-race` run was uncached and equally green — 21.9s for
`internal/flow`, 12.8s for `internal/integrations/github`, 8.5s for
`internal/dispatch`.)

### Config validation (the crashloop check)

Every in-repo config was loaded with the freshly built binary. The e2e configs
were copied with `/data/` and `/etc/conductor/` rewritten to a writable temp
dir, since they otherwise fail on a blob-dir `mkdir` **after** config
validation:

```
conductor.yaml             OK
controllers.yaml           OK
controllers.live.yaml      OK
connectors.e2e.yaml        OK
```

`config.example.yaml`, `config.starter.yaml` and `config.example.legacy.yaml`
are covered by `cmd/conductor`'s own tests, which pass:

```
--- PASS: TestExampleConfigValidates
--- PASS: TestLegacyExampleConfigStillLoads
--- PASS: TestStarterConfigValidates
--- PASS: TestStarterConfigFreshSeedLayout
```

`test/e2e/config/bad-filter.yaml` still fails as designed, now naming the
declared facts:

```
error: triggers[0] (on: gh.review_requested): filter: gh.review_requested
publishes no fact "is_drafft" (facts: author, base_branch, head_branch,
is_draft, labels, title)
```

`conductor config migrate --dry-run` on `test/e2e/config/legacy-migrate.yaml`
emits `filter: {repo: [migr/*]}`, and on a hand-built legacy config covering
every retired key it emits the full mapping (verified by hand, and by
`internal/migrate`'s tests).

---

## 2. What was not done / is partial

Stated plainly rather than implied:

- **No docker e2e run.** `test/e2e/run.sh` drives a docker-compose stack; I
  changed its fixtures and one comment but did not execute it. The four
  configs it mounts were each loaded and validated with the real binary, and
  the Group U scenario's new filter form was reasoned through and covered by
  unit tests (`TestPhase2RepoRoutingKey`, `TestIssue5590`), but the container
  suite itself is unrun.
- **`gates.no_branch` / `gates.project` lost their surface.** They read a
  GraphQL enrichment rather than a published fact, so they have no `filter:`
  spelling and no connectors-model config can set them any more. `conductor
  config migrate` says so per occurrence rather than dropping them silently.
  They were already unreachable from a connectors-model config before this
  change (the `filters.gates` map reached `issueGatePasses`, but nothing in
  the new schema documented them); this makes it explicit.
- **One rough edge, by design.** On an event with no keep-condition
  (`failing_checks`, `merge_conflict`, `pr_behind`, `stuck_checks`), a NON-flat
  routing filter — `filter: [{repo: [a]}, {repo: [b]}]` — is routed by the
  hoisted union and its `Or` structure is never evaluated, because there is no
  keep-condition to evaluate it in. The union is a superset, so it fires for
  `a` and `b` as written; a `not_repo` under an `Or` arm on such an event would
  not be applied. Validation cannot catch this (the filter is well-formed) and
  the flat `{repo: [a, b]}` is equivalent, so I documented it rather than
  adding a special case.

---

## 3. Files changed, by area

### The config-layer IR

- `internal/config/filter.go` — `FilterNotPrefix` and the universal `not_`
  decode; the `x_filter_*` internal marshal form and `decodeStructural`;
  `FilterFromValue`; the structural queries `MatchUnion`, `TopLevelNegated`,
  `OnlyKeys`, `FlatConjunctionOf`; `FilterValueStrings`.
- `internal/config/connectors.go` — deleted `TriggerSpec.Filters`,
  `OnSource.Filters`, `filterRoutingKeys`, `validateTriggerFilters`,
  `mergeFilterMaps`; added `legacyFiltersKey`, `errLegacyFiltersKey`,
  `rejectLegacyFilters` and hooked them into `TriggerSpec.UnmarshalYAML` and
  `OnSource.UnmarshalYAML`.
- `internal/config/config.go` — dropped the `validateTriggerFilters` call;
  rewrote the `Action.Filter` / `Action.Exclude` doc comments.

### Packs

- `internal/config/pack.go` — `TriggerArm.Filters` → `TriggerArm.Filter`;
  `TriggerArm.UnmarshalYAML` rejecting `filters:`; `mergeArm` replace-semantics
  for `Filter`.
- `internal/config/pack_instantiate.go` — `applyTriggerArm` composes the AND;
  `andFilters` (with And-splicing); `clearTriggerRepos` and
  `triggerScopesRepos` rewritten against the filter; new exported
  `TriggerRepoScope`.

### The github filter half

- `internal/integrations/github/filter.go` — `FilterRepoKey`; the renamed
  `matchKeyFacts`; `FilterMatchKeys` always advertising `repo`; the `repo` and
  `draft` matcher cases; `filterPasses` taking `kind, repo` and injecting the
  `repo` fact; `notDraftIf`; `lowerExclude` / `lowerReviewRequested` /
  `lowerIssueMatch` / `lowerMergeReady` / `lowerComment` on the new key names.
- `internal/integrations/github/events.go` — `filterPasses` call sites;
  exported `MergeGateKeys` / `MergeGateOn` for migrate.
- `internal/integrations/github/sweep.go` — `filterPasses` call sites.
- `internal/integrations/github/force.go` — one comment.

### The connector layer

- `internal/connector/github.go` — deleted `baseGithubFilters`; `githubEvent`
  no longer takes or sets a `Filters` schema; `ignore_checks` / `reviewer` /
  `assignee` / `include_prereleases` moved into the option schemas;
  `lowerTrigger` rewritten (routing hoist + option reads).
- `internal/connector/connector.go` — `EventDecl.FilterKeys` /
  `FilterFacts` / `hasUnifiedSurface`; doc comments for `Filters` and
  `TypeDecl.Filter`.
- `internal/connector/filter.go` — `ValidateFilter` against
  `FilterKeys`/`FilterFacts`, `not_<key>` in the error text, `orNone`;
  new `GenericFilterMatcher`.
- `internal/connector/pluginsource.go` — evaluates `t.Spec.Filter` through
  `pluginFilterMatch`.

### The flow runner

- `internal/flow/flow.go` — `FilterMatch` evaluates `spec.Filter` via
  `GenericFilterMatcher`.
- `internal/flow/validate.go` — dropped the `spec.Filters` schema check;
  `validateManualTrigger` rejects a `filter:` instead.

### `conductor config migrate`

- `internal/migrate/actions.go` — `actionFilters` → `actionFilter(kind, …)`
  emitting the unified object; `gateTruthy`; `noteDroppedGates`;
  `actionOptions` absorbing the four non-predicates.
- `internal/migrate/github.go` — builds the trigger's `filter:` (repo +
  not_repo + predicate) via `config.FilterFromValue`.
- `internal/migrate/sources.go` — slack/rss emit `filter:`; `filterOf`.

### CLI

- `cmd/conductor/packs.go` — reads the repo consent via
  `config.TriggerRepoScope`.
- `cmd/conductor/connectors.go` — `conductor schema` prints "filter match
  keys" and "filter facts" (they had no printer after `Filters` went away).

### Tests

New: `internal/config/filters_retired_test.go`,
`internal/connector/github_routing_test.go`,
`internal/integrations/github/filter_phase2_test.go`.

Updated: `internal/config/{filter,extends,onlist,pack_armall,
pack_instance_overlay,pack_instances,pack_overlay,pack,triggers}_test.go`,
`internal/connector/{github,pluginsource,authoring_example,discord}_test.go`,
`internal/flow/{filter_validate,onlist_validate,coverage_more,
validate_group}_test.go`,
`internal/integrations/github/filter_parity_test.go`,
`internal/migrate/{breadth,migrate,coverage_top}_test.go`.

### Fixtures, examples, docs

Listed in §7 below.

---

## 4. The final match-key table

github. No key carries a baked polarity — negation is the grammar's `not_`
prefix. Every key listed is also legal as `not_<key>`; only the base key is
declared or implemented.

| key | value | fact read | true when | was |
|---|---|---|---|---|
| `repo` | list of globs | *(routing; `repo` injected)* | `matchRepo(globs, repo)` | `filters.repos` |
| `branch` | list of globs | `head_branch` | `config.Exclude{Branches}.Matches(head_branch, "", nil)` | `branches` |
| `base_branch` | list of globs | `base_branch` | same, against `base_branch` | `base_branches` |
| `title` | list of strings | `title` | case-insensitive **substring** for any entry | `title` |
| `label_any` | list | `labels` | `anyFold(labels, want)` | `labels_any` |
| `label_all` | list | `labels` | `allFold(labels, want)` | `labels_all` |
| `require_label` | string | `labels` | `containsFold(labels, label)` | `require_label` |
| `author` | list of logins | `author` | `containsFold(want, author)` | `authors` |
| `comment_author` | list of logins | `comment_author` | `loginMatch(want, comment_author)` | `from_users`; `not_comment_author` was `ignore_users` |
| `author_bot` | bool | `author_is_bot` | `authorBotMatch(&want, author_is_bot)` | `author_bot` |
| `draft` | bool | `is_draft` | `is_draft == want` | `not_draft` (baked polarity) |
| `sole_assignee` | bool | `sole_assignee` | `!want \|\| sole_assignee` | `sole_assignee` |
| `merge_state` | bool | `merge_state` | `!want \|\| merge_state == "CLEAN"` | `gates.merge_state` |
| `review_decision` | bool | `review_decision` | `!want \|\| review_decision == "APPROVED"` | `gates.review_decision` |
| `non_author_approval` | bool | `non_author_approval` | `!want \|\| non_author_approval` | `gates.non_author_approval` |
| `threads_resolved` | bool | `threads_resolved` | `!want \|\| threads_resolved` | `gates.threads_resolved` |

Per-event legality is derived, not restated: a key is legal for an event
exactly when the event publishes the fact it reads (`matchKeyFacts` × the
event's `filterFacts`) — except `repo`, which every github event accepts.
`TestPhase2MatchKeysAreDeclaredForEveryEvent` fails the build if a declared key
is not implemented, if an implemented key is declared by no event, if an event
omits `repo`, or if any retired key is still declared or evaluable.

Moved to `options:` (never predicates over the event): `ignore_checks`
(failing_checks), `reviewer` (review_requested), `assignee` (issue_matched),
`include_prereleases` (release). Gone with no replacement:
`gates.no_branch`, `gates.project`.

---

## 5. How merge_ready and ready_for_review defaults were preserved

The `lowerX` functions and `filterPasses`'s `if f == nil { f = <default> }`
fallback are kept exactly as phase 1 left them; only the KEY NAMES the
lowerings emit changed. With the `filters:` surface deleted, Action's legacy
predicate fields are only ever zero in the connectors model, so each lowering
now yields its event's intrinsic default:

- **merge_ready.** `lowerMergeReady` walks `mergeGateKeys` and reads
  `mergeGateOn(act.Gates, k)`. `mergeGateOn` returns **true for an absent
  key** (the gates are opt-OUT), so a zero `Gates` map yields all five
  conjuncts enforced. The `not_draft` entry is the one that changed shape: the
  key is gone, so it lowers through `notDraftIf(on)` to
  `Not(Match("draft", true))` when on and to **nothing** when off — and
  `Match("not_draft", false)` was vacuously true before, which `FilterAnd`
  dropping a nil is also. Bit-preserved.

  `TestPhase2MergeReadyDefaultsSurvive` asserts the lowered default contains
  all five conjuncts, that an all-green PR passes, that breaking each gate one
  at a time suppresses it, and that the decision equals
  `mergeGatePasses(&gate, nil)` — the shipped legacy oracle.

- **ready_for_review.** `lowerReadyReview` lowers only `lowerExclude`, and
  deliberately never the draft gate: the PR just left draft, and re-applying
  the gate is what the transition exists to undo. With a zero `Exclude` it
  returns `nil`. `TestPhase2ReadyForReviewSkipsTheDraftGate` asserts it returns
  nil even with `gates: {not_draft: true}` set, that a draft PR still fires
  through it, and that `lowerReviewRequested` on the SAME action does suppress
  — so the asymmetry is pinned, not incidental.

- **review_requested** uses `gateEnabled` (opt-IN, absent → false), so its
  default is "always keep". **new_comment**, **issue_matched** and
  **changes_requested** lower to nothing.

The pre-existing `filter_parity_test.go` remains the bit-preservation proof: it
calls the shipped legacy predicates (`draftGate`, `mergeGatePasses`,
`commentAuthorAllowed`, `config.Exclude.Matches`, `authorBotMatch`,
`anyFold`/`allFold`/`containsFold`) as its oracle for every representative
legacy action × every PR case, and it passes unchanged apart from the
`filterPasses` signature.

The new `TestPhase2MigrationParityPRPredicates` and
`TestPhase2MigrationParityComment` add the *migration* oracle the spec asked
for: for each retired key, the legacy config (as `config.Action` fields — what
`filters:` used to decode into) and the `filter:` an operator writes instead
are both run through `filterPasses` against every PR in `prCases` and must
agree. 12 PR-shaped cases + 4 comment cases.

`TestPhase2NotDraftRequiresNotDraft` verifies the specific requirement that
`not_draft: true` fires only when the PR is not a draft, that it decodes to
`and(not(match(draft,true)))` (there is no `not_draft` matcher case), and that
`draft: true/false` and `not_draft: false` all behave as plain
equality-under-negation.

A related decision is what makes the defaults survive migration at all: a
filter that is nothing but a **flat conjunction** of `repo`/`not_repo` states
no predicate, so `Action.Filter` stays nil and the default applies. Without it,
rewriting `filters: {repos: […]}` to `filter: {repo: […]}` on a merge_ready
trigger would have silently dropped all five gates.
`TestGithubRoutingOnlyFilterKeepsEventDefaults` pins this.

---

## 6. How `repo` extraction feeds both routing and sweep

`internal/connector/github.go:lowerTrigger` is the single place it happens:

```go
f := t.Spec.Filter
if !f.FlatConjunctionOf(gh.FilterRepoKey) {
    act.Filter = f                              // a predicate was stated
}
act.Repos = f.MatchUnion(gh.FilterRepoKey)      // union, skipping negated branches
if len(act.Repos) == 0 {
    act.Repos = g.conn.Repos                    // the connector's default scope
}
act.ExcludeRepos = f.TopLevelNegated(gh.FilterRepoKey)
```

- **`Action.Repos`** is the UNION of every `repo` value anywhere in the filter,
  in first-seen order, skipping branches under a `Not` (a `not_repo` is an
  exclusion, never a scope). For the common top-level case the union IS the
  top-level value, so this is the extraction the spec described; for an `Or` of
  repo sets it is a superset, which only widens what is CONSIDERED.
- **`Action.ExcludeRepos`** takes only TOP-LEVEL `not_repo` — a
  `Not(Match(repo, …))` that is a direct conjunct of the root. A conjunct of
  the root holds for every event the filter admits, so hoisting it is sound;
  the same key under an `Or` arm is conditional and hoisting it would suppress
  events the filter says should fire, so it stays inside the filter.

Both consumers were left untouched:

- **Routing** — `events.go:emit()` still gates on
  `len(act.Repos) > 0 && !matchRepo(act.Repos, repo)` and
  `len(act.ExcludeRepos) > 0 && matchRepo(act.ExcludeRepos, repo)`, before any
  keep-condition runs. This is the only mechanism on the four events that
  evaluate no keep-condition.
- **Sweep scope** — `sweep.go:stuckRepos()` still prefers an enabled
  `stuck_checks` action's own `a.Repos` over the rule's `"*/*"` catch-all
  (reading `Match.Repos` there would send the poller into an installation
  lookup for a wildcard owner), and `Sweep.Repos` still falls back to the
  connector's `repos:`.

Precision is not lost where it matters: `repo` is also a real match key
(`matchRepo` against a `repo` fact injected by `filterPasses`), so a nested
`repo` — the case the hoist can only approximate — is decided exactly at the
keep-condition. `TestPhase2RepoRoutingKey` covers an `Or` of
`{repo, predicate}` arms across five repo/draft/label combinations;
`TestGithubRepoRoutingHoist` covers six hoist shapes;
`TestGithubRoutingReachesTheSweepScope` builds a real lowered integration for
`stuck_checks` with an `Or` of two repo sets and asserts the poller's scope is
their union; `TestGithubNoRepoFilterFallsBackToTheConnector` covers the
fallback for three filter shapes including one that names only `not_repo`.

---

## 7. Every fixture and doc migrated

### e2e fixtures (these gate CI)

| file | change |
|---|---|
| `test/e2e/config/conductor.yaml` | 10 `filters:` blocks → `filter:` (`repos`→`repo`, `exclude_repos`→`not_repo`); 2 × `ignore_checks` → `options:` |
| `test/e2e/config/controllers.yaml` | 13 `filters:` blocks → `filter:` |
| `test/e2e/config/controllers.live.yaml` | 5 `filters:` blocks → `filter:` |
| `test/e2e/config/connectors.e2e.yaml` | 17 × `filters: {repos: […]}` → `filter: {repo: […]}`; Group U's trigger now carries `repo:` and `expr:` as conjuncts of ONE filter (it previously had `filters:` *and* `filter:` side by side — the phase-1 shape being removed) |
| `test/e2e/run.sh` | the Group U header comment now says the legacy spelling is retired and that one filter carries both scope and condition |

`bad-filter.yaml`, `legacy-migrate.yaml` and `unmappable.yaml` needed no
change: the first already used `filter:`, the other two are legacy-schema
inputs for the migration scenarios.

### Examples

| file | change |
|---|---|
| `config.example.yaml` | 11 sites: `labels_any`→`label_any`, `ignore_users`→`not_comment_author`, `repos`/`labels` in the instance-array and `extends:` examples, the pack `on:`/`triggers:` overlays, the per-source block prose; `gh.review_requested`'s `filters: {reviewer, exclude}` → `filter: {not_branch, not_label_any}` + `options: {reviewer}`; `gh.release`'s `include_prereleases` → `options:` |
| `config.example.legacy.yaml` | unchanged — it is the LEGACY-schema example, and its one "filters" mention is prose about legacy action fields |
| `config.starter.yaml` | no `filters:` to migrate |
| `examples/packs/review-kit/` | no `filters:` to migrate |
| `README.md` | 2 prose mentions |

### Design docs

| file | change |
|---|---|
| `docs/design/unified-filter.md` | rewritten: phase 2 IS the current design. The "`filter:` and `filters:` coexist" language is gone; the grammar section documents `not_`; the match-key table points at the phase-2 doc; a new "Intrinsic defaults" section; the worked example gains the structured spelling; Scope now reads shipped-phase-1 / shipped-phase-2 / later |
| `docs/design/unified-filter-phase2.md` | **new** — the phase-2 contract: why, the two grammar additions, routing-only semantics, the full key table, what moved to `options:`, behavior preservation, packs, the internal marshal form, other connectors, validation, migration, and an explicit "Choices made beyond the spec" section |
| `docs/design/config-surface-refinements.md` | pack instance-array example → `filter:`, with a note that the key was renamed after it was written |
| `docs/design/runtimes-models-packs.md` | 4 sites in the pack overlay / instance examples and prose |

### Wiki

| file | change |
|---|---|
| `docs/wiki/Configuration.md` | the `filter:` section rewritten: "the one filter key", the `not_` prefix, `repo:`/`not_repo:` routing and the routing-only rule, the full github match-key list, what lives in `options:`, a "Migrating from `filters:`" block including the merge_ready gates warning; the per-source block, `extends:`, `manual`, bot-author and validate sections updated |
| `docs/wiki/Workflows.md` | "a trigger is four keys" now names `filter:`; per-source block; instance-array example and identity prose |
| `docs/wiki/Reuse.md` | intro; merge-rules table gains a `filter` row (replaces, not merges); the trigger `extends:` example rewritten to show replace semantics |
| `docs/wiki/Packs.md` | 4 sites: the `on:` overlay, the `"*"` arming defaults (now noting named-arm replace), the instance array, the mirrored overlay |
| `docs/wiki/Integration-GitHub.md` | the events table rebuilt: routing keys, per-event `filter:` match keys (with `(routing only)` where that is all there is), the four options, the merge_ready gate note; the Legacy section describes the `not_repo` output |
| `docs/wiki/Examples.md` | 6 examples |
| `docs/wiki/Connectors.md` | the contract's event bullet, the validate sentence, the sentry/pagerduty table rows, the trigger-matching paragraph |
| `docs/wiki/Authoring-Connectors.md` | the `EventDecl.Filters` comment, the `TypeDecl.Filter` signature comment (now noting it is called one key at a time), the validate sentence |
| `docs/wiki/Migration.md` | the three github/slack mapping rows now describe the `filter:` output |
| `docs/wiki/{Teams,Quickstart,Home,Integration-Cron,Integration-RSS,Integration-Sentry,Integration-PagerDuty,Integration-Slack}.md` | one site each |

`grep -rn "filters:"` across `docs/`, `examples/`, `test/`, `config.example*`,
`config.starter.yaml` and `README.md` now returns only: the four prose mentions
in `Configuration.md` that describe the retired key, the retired-key note in
`config-surface-refinements.md`, the legacy-example prose, and the two
deliberately-retained "this is what the legacy spelling looked like" comments in
`connectors.e2e.yaml` / `run.sh` (one marked `# retired`).

---

## 8. Tests added

`internal/config/filter_test.go`
- 6 new decode cases for the universal `not_` prefix: `not_<matchkey>`,
  `not_expr`, `not_repo`, a key + its twin in one object, `not_` under an `Or`
  arm, and a bare `not_` staying a match key.
- `TestFilterRoutingQueries` — 9 shapes × `MatchUnion` / `TopLevelNegated` /
  `OnlyKeys` / `FlatConjunctionOf`, plus the nil-filter case.
- `TestFilterFromValue` — object/string/list forms, and the marshal round trip.
- `TestFilterMarshalProgrammaticNode` — rewritten from "must error" to "must
  round-trip through the internal form", twice, so the form is itself stable.
- existing cases renamed to the phase-2 keys.

`internal/config/filters_retired_test.go` (new)
- `TestLegacyFiltersKeyIsRejectedEverywhere` — `filters:` refused on a trigger,
  in an `on:`-list per-source block, in a pack trigger arm, and in a pack `on:`
  overlay, each error containing both "`filters:` was removed" and "`filter:`".
- `TestRetiredFiltersKeysHaveUnifiedSpellings` — the 8 replacement spellings the
  error text recommends all parse.

`internal/config/onlist_test.go`
- a per-source `filters:` rejection case.

`internal/connector/github_routing_test.go` (new)
- `TestGithubRepoRoutingHoist` — 6 shapes → `Action.Repos`,
  `Action.ExcludeRepos`, and whether a predicate survives.
- `TestGithubRoutingOnlyFilterKeepsEventDefaults` — routing-only leaves the
  default; a match key or an `expr` takes it over.
- `TestGithubNoRepoFilterFallsBackToTheConnector`.
- `TestGithubRoutingReachesTheSweepScope` — a real lowered integration.

`internal/connector/github_test.go`
- `TestGithubSourceLowersTriggerFilter` rewritten: the filter is now given as
  the YAML an operator writes and decoded through the real grammar, and the
  test asserts the routing hoist, that the filter rides through whole, and that
  `reviewer` comes from `options:`.

`internal/integrations/github/filter_phase2_test.go` (new, 8 tests)
- `TestPhase2MigrationParityPRPredicates` (12 sub-cases × 8 PRs).
- `TestPhase2MigrationParityComment` (4 sub-cases × 5 commenters).
- `TestPhase2NotDraftRequiresNotDraft`.
- `TestPhase2NotKeysCanAND` — reproduces #5590 under the OR-of-denials
  spelling and fixes it by negating the conjunction.
- `TestPhase2RepoRoutingKey`.
- `TestPhase2MergeReadyDefaultsSurvive`.
- `TestPhase2ReadyForReviewSkipsTheDraftGate`.
- `TestPhase2MatchKeysAreDeclaredForEveryEvent` and
  `TestPhase2FilterFailsClosedOnARetiredKey`.

`internal/flow/filter_validate_test.go`
- 3 new accept cases (routing + predicate in one filter; every `not_` twin
  validating undeclared; `not_expr`), 3 new reject cases (a `not_` twin refused
  by its BASE name; a retired key refused by name; the bool-type message naming
  `draft`).
- `TestFilterUnsupportedEvent` → `TestFilterOnAPredicatelessEvent`: a predicate
  on `gh.merge_conflict` is refused, routing on it is legal.
- `TestFilterAndLegacyFiltersConflict` → `TestFilterIsTheOnlyFilterKey`.

`internal/flow/coverage_more_test.go`
- `TestFilterMatch` gains `not_only` and an `Or` of match keys, proving the
  generic connectors get the whole grammar over keys they never restated.

`internal/migrate/{breadth,migrate}_test.go`
- assert the migrated `filter:` by key (`label_any`, `author`,
  `comment_author`/`not_comment_author`, `not_draft`, `not_branch`,
  `TopLevelNegated("repo")`) and that `reviewer`/`assignee`/`ignore_checks`
  land in `options:`.

---

## 9. Design choices I made (spec did not cover)

Each is also recorded in `docs/design/unified-filter-phase2.md §Choices made
beyond the spec`, so the reasoning lives with the code rather than only here.

1. **A routing-only `filter:` states no predicate.** The spec says a filter
   replaces the intrinsic default and that `repo`/`not_repo` are structural
   routing "gated before `matchFilterKey`". Taken literally together, migrating
   `filters: {repos: […]}` → `filter: {repo: […]}` on a `merge_ready` trigger
   would have dropped its five gates — a silent unlock, on ~40 migrated
   fixtures. So a filter that is a FLAT CONJUNCTION of `repo`/`not_repo` leaves
   `Action.Filter` nil. The moment it names anything else, the operator owns the
   predicate. "Flat" rather than merely "only those keys" because an `Or` of
   repo sets is more than the structural pre-gate can carry, so that shape keeps
   its filter and is evaluated.

2. **`filter:` generalised to the generic source connectors.**
   `TriggerSpec.Filters` was connector-agnostic, so deleting it would have
   deleted slack's `channel`/`users`, rss's `match` and every plugin source's
   own filter keys along with github's — none of which the spec mentions.
   Instead `TypeDecl.Filter` is now called one match key at a time, and an event
   that declares neither `Facts` nor `MatchKeys` has its `filters:` schema read
   as its match-key set and its `context:` as its facts. Those connectors keep
   their filtering and gain AND/OR nesting, `not_` and `expr:` for free. No
   in-repo fixture exercised them, so either choice kept CI green; removing a
   capability silently seemed worse than extending the grammar to it.

3. **`reviewer` / `assignee` / `include_prereleases` moved to `options:`**,
   following the `ignore_checks` precedent the spec set explicitly. All three
   are read by code that still runs (`act.Reviewer` in `reviewerFor`,
   `act.Assignee` in `issueAssigneeMatch`, `act.IncludePrereleases` in the
   release keep-closure) and none is a predicate over a published fact, so
   leaving them surface-less would have been a silent capability loss. Per the
   spec's rule I deleted no `Action` field — grep shows every one still read.

4. **An internal `x_filter_*` marshal form.** Pack arming has to AND the arm's
   repo scope with the shipped trigger's filter, and "AND of two arbitrary
   filters" has no surface spelling (an object ANDs keys, a list ORs), while
   `cloneTriggerSpec` and `connector.buildIntegration` both re-serialise a
   config. `MarshalYAML` therefore emits the operator's `raw` when it has one
   (an authored filter round-trips byte-identically) and falls back to the
   `x_`-prefixed structural form otherwise — the same convention `Action`'s
   `x_repos`/`x_flow_ref` already use. `andFilters` also SPLICES `And` operands
   rather than nesting them, so every conjunct of an armed trigger's filter is a
   conjunct of its root and the pack consent check can still see the repo scope.

5. **`conductor schema` prints the filter surface.** Removing github's
   `Filters` schema left the printer with nothing to show for `filter:`, so it
   now prints "filter match keys (each also legal as `not_<key>`)" and, where a
   connector declares its own, "filter facts". Not asked for, but the schema
   command going quiet about the only filter key would have been a real
   regression in discoverability.

6. **Smaller calls.** `mergeArm`: a named arm's `filter:` REPLACES the `"*"`
   wildcard's (matching `Repos`, so an operator can loosen as well as tighten).
   `clearTriggerRepos` strips only a TOP-LEVEL `repo` scope from a shipped pack
   trigger; a `repo` under an `Or` arm is part of the pack's predicate, and the
   consent check then refuses to arm it until the consumer names their own repos
   — the fail-closed direction. `Action.Repos` preserves first-seen ORDER rather
   than sorting, so a migrated config's repo list reads as written.
