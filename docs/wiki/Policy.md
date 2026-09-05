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
| `reply_to_bots` | gate the conversational reply back to a bot author: `decline_only` (default — the agent replies only to decline a suggestion), `off` (comment/reply verbs to the bot are skipped), `full` (ungated). Fixes always run. See [[Configuration]] | any |
| `shadow` | preview instead of dispatching | any |
| `max_attempts_per_head` | soft attempt threshold before backoff | any |

## Agent-authored plans (`agent_authored`)

Governs the steps an agent emits at runtime (a `plan:` output block, the
live `run_step` tool, `workflow.run { steps }` — see [[Workflows]]). **Safe
by default:** with no `agent_authored` block anywhere, agent-authored plans
are rejected entirely; the block is the opt-in, and every rule is enforced
STRUCTURALLY before anything runs — never on the agent's honor.

```yaml
policy:
  agent_authored:
    allow:   [ code, kv.*, sql.query, gh.comment, workflow, "workflow.*" ]
    approve: [ cli, gh.merge, "*.write" ]     # dry-run + hand-off approval first
    approve_via: slack-ops                    # ask-capable connector for that approval
    host: sandbox                             # a hosts: entry — agent code/cli NEVER on the main box
    identity: bot                             # injected as `as:` on verbs that take it
    limits: { max_steps: 50, max_fan_out: 20, max_sub_agents: 5, timeout: 30m, tokens: 200k }
    max_revisions: 3                          # supervise-loop cap → then needs_input
    no_secret_egress: true                    # secret-read + external-write in one plan needs approval
    # trust: full                             # deliberate opt-out: lift allow/approve/host (limits still bind)
```

| key | meaning |
|---|---|
| `allow` | step classes emitted freely: step forms (`code`, `cli`/`command`, `agent`, `workflow`) and verb patterns (`gh.comment`, `kv.*`, `*.read`). Anything matching neither list rejects the whole plan pre-run, naming the class |
| `approve` | classes admitted only after a full dry-run preview (every verb stubbed, audited) + an approval on `approve_via` (an ask-capable connector). No `approve_via` → dry-run, `needs_input` escalation, reject. Approve wins over allow |
| `host` | the sandbox `hosts:` entry agent `code`/`cli` steps are FORCED onto (an agent-chosen `host:` is overridden). Unset → those steps are rejected outright |
| `identity` | set as `as:` on emitted verbs that declare the option — the plan posts as the bot, never as you; the call can't override it back |
| `limits` | `max_steps` (declared, incl. branches/compensations), `max_fan_out` (parallel branches + runtime `for_each` size), `max_sub_agents` (declared + runtime units), `timeout` (wall clock), `tokens` (approximate sub-agent budget, chars/4). Defaults 50/20/5/30m/200k. Exceed → halt + escalate, never spin |
| `max_revisions` | supervision rounds before the plan compensates and escalates to a human (default 3) |
| `no_secret_egress` | default true: a plan that both reads secret material (a vault verb, `{{ vault … }}`, `.secrets`/`.vaults` refs) and touches the outside world (any non-builtin verb, code, cli) is approval-gated — the exfiltration combo the allowlist alone misses |
| `trust: full` | lift allow/approve/host for this scope — a deliberate operator opt-in; limits and revision caps still bind, and the gate is audited as `trust` |

Every plan's admission is audited — the gate (`allow`/`approve`/`trust`),
the rejection reason, per-step outcomes, compensations, and the
deterministic-vs-hybrid classification.

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
