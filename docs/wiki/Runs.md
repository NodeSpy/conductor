# Execution history, live watch & retry-from-step

Every connectors-model run is fully inspectable (#36 §20/§17) — live while
it runs and after the fact: per-step **inputs**, **outputs**, **status**,
**timing**, **cost**, and the agent's **proposed diff**, plus the pinned
trigger a user-driven retry re-runs from. This extends the crash-resume
checkpoint machinery into a deliberate operation, and it is the data layer a
future run-inspector UI would render — usable from the CLI now.

## Watching live

```
conductor watch                 # every run: steps, gates, outcomes as they happen
conductor watch <run-id>        # one run (a history id, or a workflow-run id)
conductor watch --json          # raw JSONL for tooling
```

`watch` tails the daemon's run event stream over the control socket:
`run_started`, `step_started` / `step_done` (status + duration), `gate`
rounds (pass / fail / revise / escalated), `run_done`. Events piggyback on
the history recorder, so the live view shows exactly what the run record
persists; shadow/dry runs emit nothing, and a slow watcher loses events
rather than stalling runs (the record stays lossless).

## The proposed diff

A foreground agent step with a local worktree captures its **proposed
change** when it finishes — `git diff HEAD` (uncommitted) plus committed-but-
unpushed work — secret-scrubbed and clipped (64 KiB):

- it lands in the step's outputs as `{{.<id>.diff}}` (with `{{.<id>.workdir}}`
  beside it), so a later step can
  present it for approval before anything applies it:

  ```yaml
  steps:
    - { id: fix, type: agent, agent: fixer, prompt: "…" }
    - { id: ok, uses: slack-ops.ask, options: { to: dm, user: U0123ABCD, prompt: "Apply?\n{{.fix.diff}}" } }
    - { id: push, if: "{{.ok.action}} == approve", type: command,
        command: ["git", "-C", "{{.fix.workdir}}", "push"] }
  ```

- the run record persists it with the step (timeline + diff + cost);
- an interactive review hand-off presents it live: every presentation of the
  draft carries the agent's CURRENT worktree diff — the draft is a real
  diff, not just prose (see [[Hand-offs]]).

Remote runtimes and `checkout: none` runs have no local worktree — no diff
is captured there.

## Inspecting

```
conductor runs                    # newest first: id, status, trigger, target, steps, cost
conductor runs <id>               # one run: step table, errors, per-step in/out, spend
conductor run <id>                # same detail (a configured trigger name always wins)
```

List/detail read the history directory straight off disk — no daemon needed.
Each record carries:

- run status (`running` / `ok` / `failed`), start/finish, total spend (§14);
- per top-level step: status (`ok` / `failed` / `skipped`), start + duration,
  the **rendered** verb options (or the agent's prompt source), the step's
  outputs in checkpoint form, error text, and agent-step token/$ usage;
- the pinned trigger + action (tokens stripped — they're re-minted on retry);
- `retry_of` backlinks on records a retry produced.

**Scrubbing.** Step I/O persists secret-scrubbed exactly like the crash-resume
checkpoints: tracked secret values are redacted out of inputs and outputs,
and a vault-read step's outputs persist as a re-resolve marker, never
cleartext. Agent prompts are recorded as their template source (the runtime
renders them at dispatch) and clipped.

## Retry

```
conductor runs retry <id>                 # resume from the recorded FAILED step
conductor runs retry <id> --from <step>   # resume from a chosen step
conductor runs retry <id> --from <step> --force-replay   # …even one that already succeeded
```

A `--from` target the record says already **succeeded** (or any step before
the recorded failure) is refused by default — re-running it replays its
committed side effects (posted comments, pushes). `--force-replay` does it
deliberately, and the audit row records the flag.

Records are HMAC-signed at write (key: `.hmac-key` beside the history files,
generated on first use, mode 0600). Retry verifies the signature before
trusting a record's pinned outputs — a record edited on disk, or one whose
signature is missing (including records written before signing existed), is
refused. List/detail views read unverified; they're display, not a trust
boundary.

Retry goes through the running daemon (it needs tokens, slots, and policy).
The recorded outputs of every successful step **before** the chosen one are
pinned into scope exactly as recorded — those steps do not re-run — and
execution resumes at the chosen step through the ordinary flow runner:
checkpoints, budgets, policy, audit, and a fresh history record (backlinked
via `retry_of`) all apply. Retrying a failed step in place is the default
(`--from` omitted → the recorded failed step).

Notes:

- Only connectors-model (flow) runs are retryable; the trigger must still
  exist in the config.
- The pinned trigger context is a snapshot — a PR's head may have moved
  since. The re-run acts on the recorded state deliberately; use
  `conductor force` for a fresh derivation instead.
- Tokens are re-minted at retry (never persisted).

## Retention

```yaml
store:
  history_retention: 14d     # age bound (default 14d)
  history_max_runs: 500      # count bound (default 500)
```

Records live one JSON file per run under `history/` beside the state file;
pruning runs lazily as new records land. Shadow/dry-run executions are not
recorded.

Related: [[Commands]] · [[Cost-Accounting]] · [[Workflows]]
