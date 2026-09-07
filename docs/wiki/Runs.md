# Execution history & retry-from-step

Every connectors-model run is fully inspectable after the fact (#36 §20) —
not just the audit summary: per-step **inputs**, **outputs**, **status**,
**timing**, and **cost**, plus the pinned trigger a user-driven retry
re-runs from. This extends the crash-resume checkpoint machinery into a
deliberate operation, and it is the data layer a future run-inspector UI
would render — usable from the CLI now.

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
conductor runs retry <id> --from <step>   # resume from a chosen step ('' = the top)
```

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
