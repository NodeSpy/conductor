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

- **Memory (§9)** — each terminal outcome writes a repo-scoped note
  (`outcome: o/r#12 merged (agent fixer, workflow gh.pr/nightly)`, tags
  `outcome`, `<outcome>`), so recall and memory-injected prompts see what
  worked. Only when a `memory:` section is configured.
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
agents:
  fixer:
    outcome_feedback: true
```

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
