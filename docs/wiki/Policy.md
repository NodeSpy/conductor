# Policy

`policy:` is one block that can appear at three scopes — **global**, on a
**connector**, and on a **trigger** — with the most specific setting winning
per key: trigger → connector → global. Quieting a single workflow is just a
trigger-level `policy:`.

```yaml
policy:                                   # global defaults
  quiet_hours: { tz: America/Denver, from: "22:00", to: "07:00", hold: true }
  concurrency: { max_agents: 8 }

connectors:
  gh:
    type: github
    policy:
      ignore: { users: ["dependabot[bot]", your-login] }
      pause_label: "conductor:hold"
      rate_limits: { per_minute: 60 }
      backoff: { base: 10s, max: 30m }

triggers:
  - on: gh.review_requested
    policy: { quiet_hours: { hold: false }, pause_label: "review:hold" }
    steps: [ … ]
```

## Keys

| key | meaning | scopes |
|---|---|---|
| `quiet_hours` | a defer (`hold: true`, default — re-queued when the window ends) or drop (`hold: false`) window; `from`/`to` are local clock times in `tz`, windows may span midnight; overrides merge field-wise so a trigger can set just `hold: false` | any |
| `concurrency.max_agents` | the global cap on concurrently running agents (per-target serialization is [[Grouping]], not this) | global |
| `concurrency.max_agents_per_hour` | rolling-hour dispatch cap (runaway guard) | global |
| `ignore.users` | authors whose activity never triggers work | connector (global default) |
| `rate_limits.per_minute` | that connector's outbound verb cap | connector |
| `backoff.base` / `backoff.max` | retry cadence past the soft attempt threshold | connector |
| `pause_label` | a github label that parks a target; a trigger-level value gives that workflow its own hold label | connector, trigger |
| `shadow` | preview instead of dispatching | any |
| `max_attempts_per_head` | soft attempt threshold before backoff | any |

## Enable / disable

Any connector or trigger turns off in place with `enabled: false` (default
`true`): a disabled connector opens no sources and exposes no verbs; a
disabled trigger never fires. The config stays intact and `validate` still
checks it.

There is no policy-level `enabled` — the global kill switch is the runtime
`conductor pause` / `resume`, not a config field. (Migration refuses a legacy
`control.enabled: false` for the same reason, naming the fix.)

Unparsable quiet-hours values fail **open** (never quiet) — a typo must not
silently hold all work.

Related: [[Configuration]] · [[Connectors]] · [[Grouping]]
