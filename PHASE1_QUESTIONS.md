# Phase 1 — open questions on `docs/design/unified-filter.md`

Everything below was implementable under a stated assumption, so nothing
blocked. Each item says what I assumed and why; each is a one-line change if
you'd rather have the literal reading.

---

## 1. The doc's own worked example needs parentheses, which `internal/expr` did not have

§Worked example is:

```
!is_draft && !( (head_branch == 'staging' || head_branch == 'prod') && contains(title, 'Release ') )
```

but `internal/expr` documented itself as "boolean combinators (**no
parentheses**)" and `splitTop` was a plain `strings.Split`. On the old engine
that condition splits naively on `&&`, the `!` binds to the first term only,
and the filter silently becomes something quite different — the exact class of
bug the design exists to kill.

§Non-goals says "no new expression language … if the design needs
`startswith`/`in`, add them as small, additive, separately-tested helpers". I
read grouping as the same class of gap and added it the same way: quote- and
paren-aware splitting/scanning, a term wrapped in one pair of parens evaluating
as a sub-condition, depth-bounded like the existing negation fold, with its own
test file. I also added `startswith`/`endswith` (§github Match predicates
points new configs at them). **I did not add `in`** — `contains(labels, 'x')`
already covers membership and nothing in the doc uses `in`.

**Question:** confirm grouping belongs in `expr` permanently (it now applies to
step `if:` too, which is a widening of that surface — a welcome one, I think,
but it is a behaviour change outside `filter:`).

---

## 2. "A trigger may set at most one of `filter:`/`filters:`" makes `filter:` unusable as written

`repos:` and `exclude_repos:` live inside `filters:`, but they are not
predicates over the event — they route the trigger to repositories. Under the
literal rule, any trigger using `filter:` loses repo scoping, which is nearly
every real trigger (and the required e2e scenario could not be written at all).

**Assumed:** `filter:` conflicts with the legacy **predicate** keys only;
`repos`/`exclude_repos` stay legal alongside it. Implemented as
`filterRoutingKeys` in `internal/config/connectors.go`, tested both ways.

**Question:** confirm — or, if the literal rule is wanted, phase 2 should give
the grammar a `repo` fact so `filter:` can express routing itself.

---

## 3. The legacy block lowers *per site*, not as one whole-block filter

§Legacy lowering says the whole block lowers to
`And([ Not(excludeOr), gates…, matches… ])` and that the keep-conditions call
one IR evaluator built from it. Taken literally that is **not**
behaviour-identical, because the existing sites each evaluate a different
subset:

- `readyReviewTriggers` checks `exclude` but deliberately **not** the
  `not_draft` gate — the PR has just left draft, and re-applying the gate there
  is precisely what the ready transition exists to undo.
- the sweep's missed-comment recovery checks `from_users`/`ignore_users` but
  **not** `author_bot` — its comment listing carries no account type.
- `not_draft` is read by *two different* gate readers: opt-**in**
  (`gateEnabled`, absent = not enforced) for review_requested, opt-**out**
  (`mergeGateOn`, absent = enforced) for merge_ready. One lowering cannot serve
  both.
- `labels_any` on a review_requested variant was never evaluated there at all;
  folding it in would start gating a trigger that previously ignored it.

**Assumed:** parity wins (§Legacy lowering: "non-negotiable"). Each site lowers
exactly the conjuncts it evaluated, via `lowerReviewRequested` /
`lowerReadyReview` / `lowerIssueMatch` / `lowerMergeReady` / `lowerComment` /
`lowerChangesRequested`. The IR, the facts and the Match predicates are shared;
only which conjuncts a site contributes differs. A hand-written `filter:`
replaces all of them and applies uniformly.

---

## 4. `reviewer` and `assignee` are in the legacy block but not in the Match table

§github Match predicates lists no `reviewer` or `assignee` key, yet
§Legacy lowering names `reviewer` among the keys that lower. Both resolve
against the connector's `me` identity rather than a published fact, so neither
has a fact to match on.

**Assumed:** they stay **outside** the IR as separate conjuncts at their call
sites (`prReviewerMatches`, `reviewerInList`, `issueAssigneeMatch`), exactly as
before. Consequence: a trigger using `filter:` cannot set `reviewer:`, so its
reviewer defaults to `me` — which is the right default, but worth knowing.

`reviewer` **is** published as a *fact* on `changes_requested` (the review
author's login, per §Facts), so an expr can read it there.

---

## 5. `merge_ready` is not in the doc's list of call sites, but its keys are in the table

§Legacy lowering names `sweep.go:485` and `events.go:543/847/1111`. It does not
name `events.go:594` (`mergeGatePasses`) — yet `merge_state`,
`review_decision`, `non_author_approval`, `threads_resolved` and
`require_label` are all in the Match table and have nowhere else to live.

**Assumed:** merge_ready is wired too. Without it those five keys are dead and
`filter:` on a merge_ready trigger would be silently ignored. Same for
`changes_requested` and the two `new_comment` sites, which are the only homes
of `author_bot` / `from_users` / `ignore_users`.

---

## 6. An empty list value means different things in the two dialects

In the legacy block an unset `labels_any` means "no constraint"; as a
predicate, `labels_any: []` reads naturally as "has any of nothing" = false.
The lowering sidesteps this by emitting **no node** for an empty legacy list,
so parity holds. But a hand-written `filter: { labels_any: [] }` evaluates
false (never fires), and `from_users: []` likewise.

**Assumed:** that is the correct predicate reading and not worth special-casing.
If you'd rather it were a load error, it is a few lines in
`connector.ValidateFilter`.

---

## 7. §Facts lists facts the runtime map must carry but a `filter:` should not read

A legacy `exclude.branches` on `issue_matched` ran its globs against an **empty**
head branch (`config.Exclude.Matches("", title, labels)`), including the
`path.Match("*", "")` = true edge. The lowered Match has to see the same empty
`head_branch` to stay bit-identical.

**Assumed:** the runtime facts map is a **superset** of the declared surface.
`issue_matched` carries `head_branch: ""` but does not *declare* it, so
validation refuses a `filter:` that reads a value which is always empty.
Documented at `filterFacts` in `internal/integrations/github/filter.go`.

---

## 8. Not done, deliberately

- **Legacy `actions:`-style configs** (`config.Action.Filter` written directly
  in a pre-connectors config) decode and evaluate, but are **not** load-validated
  — `flow.Validate` only walks `triggers:`. Those configs are auto-migrated on
  boot anyway (group L), so this seemed the wrong place to spend the budget.
  Say the word if you want it.
- **`conductor config migrate` rewriting `filters:` → `filter:`** is phase 2 per
  §Scope.
- **`lower()`** would make the substring `title:` key expressible in `expr`
  (`contains()` is case-sensitive). Not needed by anything in the doc; the
  recommended replacement is `startswith()`.
