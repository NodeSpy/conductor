# Phase 1 — unified `filter:` — implementation report

Branch `unified-filter-phase1`, 5 commits on top of `c986d25` (the spec).
Nothing pushed, no PR opened.

Open questions and the assumptions I proceeded under are in
**`PHASE1_QUESTIONS.md`** — items 1, 2, 3 and 5 are the ones worth reading
before review, because each changed what I built.

---

## What is implemented

### 1. The Filter IR and its polymorphic decode — `internal/config/filter.go`

`Filter` is `And | Or | Not | Expr | Match`. The YAML shape is the boolean
structure: string → `Expr`, list → `Or`, map → `And` of one `Match` per key,
with the one reserved `expr:` key contributing an `Expr` AND-ed alongside its
siblings. Nesting is free and depth-bounded (32).

Map keys are visited **sorted**, so a decode is deterministic rather than at
the mercy of Go's map iteration order — pinned by
`TestFilterDecodeDeterministic`, which decodes the same document 50 times.

A decoded filter keeps its source YAML, because the connectors lowering
marshals a whole config to YAML and reparses it
(`connector.buildIntegration`) — without that, a user's `filter:` would not
survive reaching the integration. A *programmatically built* node (the legacy
lowering's `Not`/`And` trees, which have no surface syntax) **refuses** to
marshal rather than silently emitting null.

### 2. The evaluator

`(f *Filter) Eval(facts map[string]any, match FilterMatcher) (bool, error)` —
`Expr` nodes go to `internal/expr.Eval`, `Match` nodes to the connector's
matcher, `And`/`Or` short-circuit. A nil filter is true. The IR is
connector-agnostic; only `Match` and the facts map are connector-aware.

### 3. `internal/expr` — grouping, `startswith`/`endswith`, `Refs`

**The doc's own worked example did not evaluate correctly on the old engine.**
`splitTop` was a plain `strings.Split`, so
`!is_draft && !( (head_branch == 'staging' || …) && contains(title, 'Release ') )`
split naively on `&&`, the `!` bound to the first term only, and the filter
quietly meant something else. Splitting and comparator scanning are now quote-
and paren-aware (`scanTop`), a term wrapped in one pair of parens evaluates as
a full sub-condition, and recursion is depth-bounded like the existing negation
fold. `contains(x, y)` is still a call, not a group. Added `startswith` /
`endswith`; **not** `in` (see questions §1).

`Refs(cond)` is the static counterpart of `Eval` — the fact paths a condition
reads, for load-time validation. It deliberately **under**-reports: a
comparison's right side and a `default()`/`coalesce()` argument are
literal-or-path, so reporting them would reject working configs
(`head_branch == staging` must not flag `staging` as an unknown fact).

### 4. github facts, `Match` predicates, and the legacy lowering — `internal/integrations/github/filter.go`

Eight keep-condition sites now evaluate exactly **one** filter — the trigger's
`filter:` when it set one, otherwise its legacy fields lowered into the same
IR:

| site | file | legacy conjuncts lowered |
|---|---|---|
| review_requested (sweep) | `sweep.go` | `not_draft` gate ∧ ¬exclude |
| review_requested (webhook) | `events.go` | `not_draft` gate ∧ ¬exclude |
| ready_for_review | `events.go` | ¬exclude (no draft gate — deliberate) |
| issue_matched (`cheapMatch`) | `events.go` | sole_assignee ∧ labels_any ∧ labels_all ∧ ¬exclude ∧ authors |
| merge_ready | `events.go` | require_label ∧ the five opt-out merge gates |
| new_comment (webhook) | `events.go` | from_users ∧ ignore_users ∧ author_bot |
| new_comment (sweep recovery) | `sweep.go` | from_users ∧ ignore_users (no author_bot — deliberate) |
| changes_requested | `events.go` | author_bot |

Every `Match` predicate's **body is the matcher that site already called** —
`config.Exclude.Matches`, `anyFold`/`allFold`/`containsFold`, `loginMatch`,
`authorBotMatch`, `gateEnabled`, `mergeGateOn` — so a lowered legacy block and
a hand-written `filter:` share one implementation and cannot drift. The only
extraction was `mergeGatePasses`' inline `on()` closure, now the named
`mergeGateOn`, because the lowering needs the same reader.

An unevaluable filter **fails closed** and logs where it came from.

### 5. Declared surface + load-time validation

Facts and match keys are declared once, in the integration next to the code
that computes and evaluates them (`filterFacts`, `matchKeyFacts`);
`internal/connector/github.go` turns them into `EventDecl.Facts` /
`EventDecl.MatchKeys`. **A match key is legal for an event exactly when that
event publishes the fact it reads**, so the two surfaces are derived from one
table and cannot drift.

`connector.ValidateFilter`, called from `flow.validateTrigger`, refuses: a
`filter:` on an event with no surface, a fact the event does not publish, an
unknown match key, and a wrong-typed value. The error names the valid set:

```
error: triggers[0] (on: gh.review_requested): filter: gh.review_requested
publishes no fact "is_drafft" (facts: author, base_branch, head_branch,
is_draft, labels, title)
```

`config.validateTriggerFilters` (in `Load`, after `NormalizeTriggers`) enforces
one predicate per trigger. See questions §2 for the `repos:` carve-out.

---

## Files touched

**New**
```
internal/config/filter.go                              the IR, decode, eval, walkers
internal/config/filter_test.go
internal/connector/filter.go                           ValidateFilter
internal/expr/refs.go                                  static reference extraction
internal/expr/group_test.go
internal/integrations/github/filter.go                 facts, Match, per-site lowering
internal/integrations/github/filter_parity_test.go     THE ACCEPTANCE TEST
internal/flow/filter_validate_test.go
test/e2e/fixtures/func_filter_{fire,skip}.json
test/e2e/canned/pull-func-filter{fire,skip}-1.json
test/e2e/config/bad-filter.yaml
```

**Modified**
```
internal/expr/expr.go                 paren/quote-aware split + scan, group(), startswith/endswith
internal/config/config.go             Action.Filter; validateTriggerFilters in Load
internal/config/connectors.go         TriggerSpec.Filter, OnSource.Filter, the one-filter rule
internal/config/onlist_test.go        one expected error string (the per-source block now takes `filter`)
internal/connector/connector.go       EventDecl.Facts / .MatchKeys
internal/connector/github.go          filterSchema(); Filter through lowerTrigger
internal/flow/validate.go             calls connector.ValidateFilter
internal/integrations/github/events.go  6 sites rewired; mergeGateOn extracted
internal/integrations/github/sweep.go   2 sites rewired
internal/integrations/github/github.go  Filter survives the defaults merge
test/e2e/run.sh                       group_U_filter + registration
test/e2e/config/connectors.e2e.yaml   the group-U trigger
docs/wiki/Configuration.md            the user-facing grammar
```

No existing test was weakened or deleted. The one test edit is a changed
expected **error string** in `onlist_test.go` — the per-source `on:` block
legitimately accepts `filter` now, and the message lists what it accepts.

---

## How the legacy parity test is structured

`internal/integrations/github/filter_parity_test.go`. The point is that its
expected side is **never hand-written**:

```go
want := !draftGate(act, pr.draft) && !act.Exclude.Matches(pr.head, pr.title, pr.labels)
got  := g.filterPasses(act, "test", prFilterFacts(...), lowerReviewRequested(act))
```

`want` **calls the pre-change predicate**. That is why `draftGate`,
`mergeGatePasses`, `commentAuthorAllowed`, `fromUsersMatch` and
`authorBotMatch` survive the rewire even though no keep-condition calls them
any more — they are the oracle, so the usual failure mode ("adjust the
expectation until it passes") is not available: the expectation *is* the
shipped legacy code. Each carries a comment saying so, so a future reader does
not delete them as dead.

The table is `legacyActions()` — 29 representative legacy blocks (each exclude
arm alone and combined, an empty-string title entry, a `*` catch-all glob, the
not_draft gate as `true`/`false`/`"true"`/`"no"`, labels any/all, authors,
sole_assignee, require_label, from/ignore users with an overlap, author_bot
both ways, relaxed merge gates, the #5590 config, and a kitchen sink) — crossed
with 8 PRs, 6 issue states, 9 merge gates and 6 comments, across six per-site
lowerings. Roughly 1,300 keep/skip decisions compared.

**Mutation-checked** — I broke the lowering four ways and confirmed failures,
then restored:

| mutation | parity failures |
|---|---|
| exclude lowering `Or` → `And` | 12 |
| `gateEnabled` → `mergeGateOn` for review_requested's `not_draft` | 23 |
| `mergeGateOn` → `gateEnabled` for the merge gates | 159 |
| `labels_all` → `labels_any` | 2 |

`TestIssue5590` pins the bug both ways: it **asserts the legacy config still
wrongly skips** the changelog PR (so a lowering that "fixed" it would fail here
as a behaviour change), then asserts the unified spelling fires on it, still
skips a real `Release 2.4.0` on staging, fires on a release-titled PR off a
release branch, and still skips a draft.

---

## e2e scenario (authored, not run — no docker here)

`group_U_filter` in `test/e2e/run.sh`, registered in `main`. One trigger in
`connectors.e2e.yaml` carrying the #5590 filter verbatim, two
`review_requested` fixtures:

- `func/filterfire` — head `codex-changelog`, title "changelog: publish each
  release entry…" → expects `U-FILTER fired func/filterfire#1` on the slack sink;
- `func/filterskip` — head `staging`, title "Release 2.4.0" → expects **no**
  capture (with the same time budget the fired one got, so "nothing arrived"
  means skipped rather than slower);
- a third row runs `conductor validate` over `config/bad-filter.yaml`, whose
  filter reads `is_drafft`, and requires a non-zero exit naming the typo.

Both halves of the fire/skip pair are load-bearing: a filter that fails to
parse fails closed so **neither** fires (U-fire catches it), and one that lost
the parenthesis grouping fires **both** (U-skip catches it).

I could not run docker, so I verified what I could locally: `bash -n run.sh`
passes, both fixtures and both canned files parse as JSON, and the exact
trigger shape — `filter:` with the #5590 expression alongside
`filters: { repos: [...] }` — validates through the real loader:

```
$ go run ./cmd/conductor validate --config /tmp/good-filter.yaml
ok: 2 connector(s), 1 trigger(s), 0 workflow(s)

$ go run ./cmd/conductor validate --config test/e2e/config/bad-filter.yaml
error: triggers[0] (on: gh.review_requested): filter: gh.review_requested
publishes no fact "is_drafft" (facts: author, base_branch, head_branch,
is_draft, labels, title)
exit status 1
```

`connectors.e2e.yaml` itself cannot be validated on this host — it fails
earlier at `blob: mkdir /data: permission denied`, which is a container path.

---

## Deviations from the doc

All four are argued in `PHASE1_QUESTIONS.md`; summarised here.

1. **Added parenthesis grouping to `internal/expr`.** Not listed in §Non-goals'
   examples, but the doc's own worked example does not evaluate correctly
   without it. Treated as the same class of additive helper as
   `startswith`/`in`. This widens step `if:` too.
2. **`repos:` / `exclude_repos:` stay legal alongside `filter:`.** The literal
   "at most one of `filter:`/`filters:`" would cost every `filter:` trigger its
   repo scoping.
3. **The legacy block lowers per site, not as one whole-block filter.** The
   literal §Legacy lowering reading is not behaviour-identical, and parity is
   non-negotiable. The sites genuinely differ (ready_for_review has no draft
   gate; the sweep's comment recovery has no account type; `not_draft` is read
   by opt-in *and* opt-out readers).
4. **Wired three sites the doc's call-site list omits** — merge_ready,
   changes_requested, new_comment — because they are the only homes of
   `merge_state`/`review_decision`/`non_author_approval`/`threads_resolved`/
   `require_label`/`author_bot`/`from_users`/`ignore_users` from the Match
   table. Without them those keys are dead and `filter:` is silently ignored
   there.

Also worth flagging: **`reviewer:` and `assignee:` stay outside the IR**
(questions §4) — they resolve against the `me` identity, not a fact, and the
Match table has no key for them. A `filter:` trigger therefore gets the default
reviewer (`me`).

---

## Gates

```
$ gofmt -l internal/ cmd/ test/
$                                        # (no output)

$ go build ./...
$                                        # (no output)

$ go vet ./...
$                                        # (no output)

$ go clean -testcache && go test -race ./internal/config/ ./internal/expr/ \
      ./internal/connector/ ./internal/flow/ ./internal/integrations/github/
ok  	github.com/NodeSpy/conductor/internal/config	4.601s
ok  	github.com/NodeSpy/conductor/internal/expr	1.028s
ok  	github.com/NodeSpy/conductor/internal/connector	2.216s
ok  	github.com/NodeSpy/conductor/internal/flow	15.052s
ok  	github.com/NodeSpy/conductor/internal/integrations/github	11.989s

$ go test -race ./...                    # full tree
...
?   	github.com/NodeSpy/conductor/test/plugins/acme-ticker	[no test files]
EXIT=0                                   # no FAIL lines anywhere
```

## Commits

```
b19d442 e2e + docs: a filter: scenario and the user-facing grammar
ee5e029 test: legacy-lowering parity, the #5590 case, and filter validation
788d343 github: facts, Match predicates, and the legacy lowering behind one evaluator
8c0c994 config: the Filter IR — one composable trigger filter
75c1738 expr: parenthesised grouping, startswith/endswith, and static Refs
```
