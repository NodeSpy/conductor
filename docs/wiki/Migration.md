# Migration (legacy schema → connectors)

The legacy schema (`integrations:` / `notify:` / `handoffs:` / `controllers:`
/ `control:` / `paseo_bin`) still loads and runs unchanged — both schemas
coexist. Migration converts a legacy file to the connectors model, and it is
**automatic, total, and fail-safe**.

## Automatic on boot

Deployed boxes auto-update; a schema change requiring a manual edit would
crash-loop them. So on boot the daemon:

1. detects legacy constructs (per file — `imports:` are walked too),
2. transforms each legacy file,
3. backs the original up alongside it (`<file>.pre-connectors`, first backup
   wins),
4. swaps the file in and **re-validates the whole config** — on any failure
   the original is restored, the daemon keeps running on it, and a
   `config needs manual migration: …` notification names what failed,
5. logs a mapping summary.

Idempotent: a migrated file has no legacy constructs, so the next boot is a
no-op. Because both schemas coexist, every intermediate state (some files
migrated, some not) still loads.

Manual: `conductor config migrate` (same flow), `--dry-run` prints the
transformed YAML and the mapping summary without writing.

## Totality — no silent drops

The transform maps **every** legacy construct; anything it cannot map is a
hard error that names it and refuses to commit. Fields the legacy engine
never read (documented inert: rule `workspace:`, action `project:` /
`method:`, `match.project`/`match.status`) are dropped **with a summary
note** — stated, never silent. `${VAR}` references survive verbatim (the
transform masks them around parsing; secrets are never inlined). Carried
blocks (`agents:`, `notify:`, `store:`, `update:`, …) keep their original
YAML, comments included.

## What maps where

| legacy | connectors model |
|---|---|
| `integrations: - type: github` (app/webhook/sweep/identity/retry/project_map/project_rewrite/me) | a `connectors:` entry, fields carried |
| github `rules:`/`defaults:` (most-specific repo wins) | per-trigger `filters.repos` + computed `filters.exclude_repos`, the same winner per repo; the defaults merge is flattened into each trigger |
| every github kind + its action filters (`labels_any/labels_all/authors/assignee/sole_assignee/reviewer/from_users/ignore_users/ignore_checks/require_label/include_prereleases/gates/exclude`) and variants | `on: <conn>.<kind>` triggers, `name:` = variant, filters mapped 1:1 |
| `flaky_rerun` / `stuck_after` / `poll_interval` / `max_attempts_per_head` | trigger `options:` |
| action `steps:` (id/if/type/agent/prompt/checkout/workdir/env/output_schema/background/handoff/rerequest_review/retry/backend) | `steps:` carried field-for-field |
| slack `triggers:` (on/reaction/command) | `on: <conn>.<event>` + filters; a multi-variant rule merges into ONE trigger whose step is parallel branches (ids variant-prefixed, intra-variant references rewritten), so the feedback aggregation point is the join |
| slack `ack` / `on_done` / `on_fail` | hooks `at: start/done/fail` using `<conn>.react` / `<conn>.post` — `on_done` fires once after ALL variants complete and `on_fail` once when any failed, identical to the legacy aggregation |
| cron `schedules:` | connection `schedules:` + one trigger per schedule |
| webhook `sources:` (path/sign/match/title/dedup/repo) | connection `sources:` + one trigger per source (`repo:` on the trigger) |
| sentry / pagerduty `rules:` (match, repo) | one trigger per rule; later triggers carry `exclude:` maps of every earlier rule's match, so the legacy first-match winner is preserved under independent triggers (a rule behind a catch-all was unreachable and is skipped with a note) |
| rss `feeds:` (url/interval/match/repo) | connection `feeds:` + one trigger per feed (`match` as its filter) |
| `handoffs:` (web + tunnels, slack/discord dm/thread) | ask-capable connectors; the default entry's name is stamped onto background steps that named none |
| `controllers:` | `runtimes:` (same fields; agent `controller:` refs stay valid) |
| `paseo_bin` | the paseo runtime's `bin:` |
| `control:` (shadow/pause_label/max_concurrent_agents/max_agents_per_hour) | the global `policy:`; an explicit `enabled: false` refuses to migrate (the kill switch is now only the runtime `conductor pause`) |
| `notify:` sinks (slack/discord webhooks, ntfy, pushover, notifiarr) | generated connectors (`notify-slack`, `notify-ntfy`, …) + `notify.via:` routes with byte-identical wire payloads; `on:`/`push`/`digest` stay on the block |
| `agents:`, `agent_guidance`, `store:`, `update:`, `imports:`, `dry_run`, `adopt_open_workspaces` | carried through unchanged |

## Proof

Behavioral-equivalence golden tests feed identical webhook payloads through
the legacy integration and the migrated-then-lowered one and assert the same
triggers fire the same work (kind, variant, dedup signature, step content).
The shipped legacy example transforms and passes full semantic validation;
the e2e suite boots a daemon on a legacy config and asserts the backup, the
swap, and that the same fixture still produces the same commit — plus the
fail-safe refusal on an unmappable file.

Legacy removal is a later release, after deployed boxes have auto-migrated.

Related: [[Configuration]] · [[Connectors]] · [[Commands]]
