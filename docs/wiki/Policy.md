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
    use: github
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
| `guidance` | the scoped house-tone baseline (layer 0) appended to every agent this scope governs — a string, a list of blocks, or `{ replace: … }`. Scopes **stack** (a trigger's guidance adds under the global tone); `{ replace }` resets the cascade from that scope down. An agent profile's own `guidance` stacks on top. The top-level `agent_guidance` folds into the global scope. See [[Reuse]] | any |
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
    allow_scopes:                             # RESOURCE allowlists, by the dimension a connector declares
      secret:  [ house/deploy_key ]           #   (deny-by-default; the dispatch's own is always in scope)
      store:   [ cache ]
      repo:    [ your-org/* ]
      channel: [ "#code-reviews" ]
    # allow_secrets / allow_stores / allow_targets  # legacy spellings of secret / store / repo — still work
    # trust: full                             # deliberate opt-out: lift allow/approve/host + the three allowlists (limits still bind)
```

| key | meaning |
|---|---|
| `allow` | step classes emitted freely: step forms (`code`, `cli`/`command`, `agent`, `workflow`) and verb patterns (`gh.comment`, `kv.*`, `*.read`). **Step `hooks:` are classified exactly the same way** (a hook's `uses:` is a verb call — it can't smuggle anything the steps couldn't). **`conductor.*` daemon-control verbs (update/pause/resume/restart/reload/run) are EXACT-MATCH ONLY**: neither `"*"` nor a written `"conductor.*"` glob admits them — deliberate hardening; name each one you mean to hand an agent (`conductor.restart`). Anything matching neither list rejects the whole plan pre-run, naming the class |
| `approve` | classes admitted only after a full dry-run preview (every verb stubbed, audited) + an approval on `approve_via` (an ask-capable connector). No `approve_via` → dry-run, `needs_input` escalation, reject. Approve wins over allow |
| `host` | the sandbox `hosts:` entry agent `code`/`cli` steps are FORCED onto (an agent-chosen `host:` is overridden). Unset → those steps are rejected outright |
| `identity` | set as `as:` on emitted verbs that declare the option — the plan posts as the bot, never as you; the call can't override it back |
| `limits` | `max_steps` (declared, incl. branches/compensations/hooks — and CUMULATIVE executed units across nested plans), `max_fan_out` (parallel branches + runtime `for_each` size), `max_sub_agents` (declared + cumulative runtime units across nested plans), `timeout` (wall clock; a nested plan can't extend its parent's), `tokens` (approximate cumulative sub-agent budget, chars/4). Defaults 50/20/5/30m/200k. A sub-agent whose output is itself a plan shares the PARENT's budget — never a fresh one — and plan nesting is depth-capped (4). Exceed → halt + escalate, never spin |
| `max_revisions` | supervision rounds before the plan compensates and escalates to a human (default 3). A revision is fully re-guarded; the classes the run's ORIGINAL approval granted stay usable, but new approval-gated work rejects mid-run |
| `no_secret_egress` | default true, two layers. Static: a plan that reads secret material (a vault verb, `{{ vault … }}`, `.secrets`/`.vaults` refs — including the `{{index . "secrets" …}}` forms) AND either touches the outside world (any non-builtin verb, code, cli) or **writes durable shared state** (`kv.set/setnx/merge/append`, `memory.remember`, `sql.exec` — parking a secret where a later, individually-innocent plan could read it back out) is approval-gated. Runtime, for unapproved plans: an internal write (verb OR a code step's `ctx.store`/`ctx.sql`/`ctx.memory`) carrying a tracked secret is refused; an EXTERNAL verb whose rendered options carry one is refused (the read-and-relay path); and once any step's outputs carried tracked secret material the plan is tainted — every later outside-touching step refuses, even when the value was transformed in between |
| `allow_scopes` | the RESOURCE allowlists, keyed by the SCOPE DIMENSION a connector declares on a verb option (`repo`, `channel`, `store`, `secret`, `path`, and whatever the next connector invents). Applies to agent-authored workflows and skill grants only — config-authored `uses:` steps are untouched, since the operator wrote them with their own credential. **Deny by default**: an unset dimension means an agent-authored step may not name a resource in it at all. Entries are exact names or globs (`house/deploy_key`, `house/*`, `owner/*`, `#ops`); `"*"` grants a whole dimension. **Whatever the DISPATCH itself points at is implicitly allowed** — the repo it fired for, the channel its event came from, the connector's configured default option value — so the list only constrains the ADDITIONAL resources an agent picks. WHICH options are gated is the connector's own declaration (`Scope` on the option schema), never a name written into the enforcement path: one check walks the called verb's scoped options, so the plan surface and the skill surface can't disagree. Enforced statically at plan admission (literal references, hooks/branches/compensations included) plus a runtime belt: a rendered verb option outside the lists is refused and audited (`barrier: resource_allowlist`), as is a code step's `ctx.store`/`ctx.sql` name |
| `allow_secrets` / `allow_stores` / `allow_targets` | LEGACY spellings of `allow_scopes.secret` / `.store` / `.repo`. Kept working; unioned with `allow_scopes` when both are present. New configs should use `allow_scopes`, which is the only form that reaches `channel`, `path`, and future dimensions |
| `trust: full` | lift allow/approve/host AND every resource allowlist for this scope — for AGENT-AUTHORED PLANS. It does NOT lift a `skill.verbs` per-verb resource constraint or the skill surface's deny-by-default: that grant is the operator's own sentence about one agent, and plan latitude is not permission to ignore it (`allow_scopes` still widens the skill surface under `trust: full`) — a deliberate operator opt-in; limits and revision caps still bind, and the gate is audited as `trust` |

Every plan's admission is audited — the gate (`allow`/`approve`/`trust`),
the rejection reason, per-step outcomes, compensations, and the
deterministic-vs-hybrid classification.

The same guard covers every agent-authored surface: plans, the live
`run_step` tool, `workflow.run { steps }`, and **saved workflows** — a
promoted workflow must pass the guard at `workflow.save` AND is re-guarded
under the then-current policy on every run (review is a human trust signal,
never a policy bypass; tighten the policy and the already-reviewed workflow
obeys immediately).

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

## Spend budgets (`budget:`)

`policy.budget` is the hard `$`/token cap layer (#36 §14): a global cap
here, a per-workflow cap on a trigger's `policy:`, and a per-profile cap on
`agents.<name>.budget`. Over-cap dispatches shed (recorded + retried when
the rolling window frees) and notify. Full details, capture semantics, and
the pricing table: [[Cost-Accounting]].

Related: [[Configuration]] · [[Connectors]] · [[Grouping]] · [[Cost-Accounting]]
