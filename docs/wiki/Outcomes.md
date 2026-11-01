# The outcome-learning loop

Conductor closes the loop on agent work (#36 §18): every agent action's
**outcome** — merged / closed-unmerged / reverted / approved / rejected /
CI-failed / gate result — is captured and fed back, so over time the system
prefers the workflows and agents that produce **merged-and-not-reverted**
changes.

## How outcomes are detected

Every agent dispatch on a PR/issue records an **engagement** (agent,
workflow, saved-workflow attribution, run id, spend), persisted beside the
state file and pruned by age. Signals then resolve them:

| Signal | Source | Outcome |
|---|---|---|
| PR closed, merged | the github connector's `_closed` signal (emitted unconditionally — no trigger config needed) | `merged` (terminal — consumes the engagements) |
| PR closed, unmerged | same | `closed` (terminal) |
| a merged PR titled `Revert "…"` whose body carries GitHub's own `Reverts owner/repo#N` back-reference (same repo only) | same | `reverted` for PR N |
| a configured `failing_checks` trigger fires on an engaged PR | github events | `ci_failed` — **once per head** (per push, so a fail-fast matrix's cancelled-check fan-out counts once), non-terminal (the PR lives on) |
| a review hand-off resolves | approve / discard on the [[Hand-offs]] channel | `approved` / `rejected` |
| a gate resolves (§16) | `event: gate` audit rows | pass / escalated rates in the report |

Each resolution writes an `outcome` audit row (agent, workflow, run, cost) —
the durable record everything below reads. A bare `#N` mention is never
treated as a revert; only GitHub's explicit back-reference on a
revert-titled merged PR counts — and because a PR's title and body are
editable text, the claim is additionally **corroborated against the revert
PR's own commit messages** (git's `This reverts commit <sha>` trailer, one
REST read). An uncorroborated claim writes a `reverted_unconfirmed` audit
row for the operator's eyes and does nothing else: no agent counter, no
engagement consumed, no workflow rot.

## Where it feeds back

- **Memory (§9)** — each terminal outcome writes a note
  (`outcome: o/r#12 merged (step fixer, workflow gh.pr/nightly)`, tags
  `outcome`, `<outcome>`) under the scope **`outcome:<owner/repo>`**. Only
  when a `memory:` section is configured.

  The scope is deliberately *not* the bare repo key. The repo key is in
  every opted-in step's default recall set, recall is newest-first under a
  limit, and a busy repo merges a PR a day: its outcome notes buried every
  learned entry for that repo, and the shared set with it. Nothing in
  conductor reads these notes back from memory (reports and health use the
  audit rows), so they sit beside the repo's memory and are recalled only by a
  step that asks — `memory: { extra_scopes: ["outcome:${repo}"] }`.

  Notes written by earlier versions are still in the repo scope. Move them
  with the operator command, dry run first:

  ```sh
  conductor memory rescope --tag outcome --to 'outcome:{scope}'          # prints what would move
  conductor memory rescope --tag outcome --to 'outcome:{scope}' --apply  # moves them
  ```

  See [[Memory#operator-commands]].
- **Workflow-promotion health (§11)** — an engagement inside a **saved**
  workflow feeds its delivery counters: merged bumps `deliveries`, a revert
  bumps `reverts`, and a workflow with ≥3 deliveries and more than half
  reverted is **flagged as rotting** (deprioritized in the Choose catalog)
  even when its runs "succeed". `conductor workflows` shows
  `N merged/M reverted` beside the run health.
- **Per-profile guidance tuning (optional)** — a profile with
  `outcome_feedback: true` gets a one-line track record appended to its
  guidance ("of your last N delivered changes, X merged … Y were later
  REVERTED — bias toward smaller, well-tested changes"). Counters persist
  across restarts; the line is omitted while there is no history.

```yaml
x-templates:
  fixer: &fixer
    type: agent
    outcome_feedback: true      # append THIS step's track record to its prompt
    # outcome_key: reviewers    # …or pool several steps onto one record
```

The record is keyed by the step's stable **identity** (or an explicit
`outcome_key`), not by an agent name — see [[Steps]]. Because the identity
survives restarts and prompt edits, the feedback still matches a step to its
own past outcomes.

## The agent-quality view

`conductor report` gains a quality section built from the outcome + gate
rows in the window:

```
agent quality (outcomes):
  agent    merged closed revert reject gate✓/✗  accept  revert%  $/merged
  fixer         8      2      1      1   12/1      73%      13%    $0.412
```

- **accept rate** — merged / (merged + closed + rejected);
- **revert rate** — reverted / merged;
- **$/merged** — the §14 cost of merged engagements per merged change
  (cost-per-merged-change).

Related: [[Gates]] · [[Cost-Accounting]] · [[Memory]] · [[Workflows]] · [[Runs]]
