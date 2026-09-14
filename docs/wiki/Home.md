# conductor

Event-driven agent orchestration for your Paseo daemon: connect to services
once, declare where agents run, and wire events to work — agents, commands,
inline code, and any connector's verbs, crossing service boundaries freely.

```yaml
connectors:
  gh:        { use: github, app: {…}, me: { logins: [your-login] }, repos: ["your-org/*"] }
  slack-ops: { use: slack, app_token: ${SLACK_APP_TOKEN}, bot_token: ${SLACK_BOT_TOKEN} }

runtimes:
  paseo: { use: paseo, default: true }

x-templates:
  fixer: &fixer { type: agent, workspace: worktree }

triggers:
  - on: gh.merge_conflict
    steps:
      - { <<: *fixer, id: fix, prompt: "Resolve the conflict on {{.repo}}#{{.pr}}." }
    hooks:
      - { at: done, uses: slack-ops.post, options: { text: "resolved {{.repo}}#{{.pr}}" } }
```

## What sets it apart

Most workflow tools wire events to API calls. conductor's steps run **autonomous
coding agents** — they check out a repo, edit in a worktree, run commands, and open
a PR — and the platform is built to direct and contain that:

- **Agent-authored workflows** — an agent can plan a multi-step workflow at runtime
  and run it under guardrails: an allow-list of the steps, connectors, and secrets it
  may touch, so the plan can't reach past what you granted ([[Workflows]]).
- **Quality gates on agent output** — tests, lint, or a critic agent must pass before
  a proposed change lands; a failure loops back a revise ([[Gates]]).
- **Governance for autonomous work** — cost/token budgets ([[Cost-Accounting]]),
  OS-enforced isolation with an egress allow-list ([[Isolation]]), and a secret broker
  so agents act *through* conductor without seeing raw secret values ([[Agent-Skill]]).
- **Multi-agent teams** ([[Teams]]), scoped **memory** across runs ([[Memory]]), and an
  **outcome-learning** loop ([[Outcomes]]).

Another orchestrator (n8n and the like) can also call conductor's authenticated
`/invoke` API for the agent work it does ([[Callable-Service]]).

## The model

- **[[Connectors]]** — external services, each with events (`on:`) and verbs
  (`uses:`), self-describing schemas, and per-connector policy. Anything
  without a built-in type is a `rest` / `graphql` connector declared in
  config.
- **[[Runtimes]] + [[Steps]]** — where agents run and who they are; an
  agent profile's `session:` binds one live agent per key ([[Steps]]).
- **[[Workflows]]** — the trigger grammar: `on` / `filters` / `steps` /
  `hooks`, position-scoped context, control flow, reusable workflows, and
  agent-authored plans.
- **[[Verbs]]** — the shared action unit, option merging, `as:` identity,
  and request-response `ask` ([[Hand-offs]]).
- **[[Code-Steps]]** — sandboxed in-process engines (`js`, `go-embed`,
  `risor`, `lua`) and host interpreters; **[[Hosts]]** for SSH remote
  execution.
- **[[Stores]] & [[Memory]]** — named `stores:` (KV + SQL) behind the always-on
  `kv.*` / `sql.*` verbs, and shared agent memory with provenance and scope.
- **[[Grouping]]** — debounce batching, one run per key.
- **[[Policy]]** — quiet hours, concurrency, ignores, rate limits, pause
  labels, `reply_to_bots`, and the `agent_authored` guardrails; global /
  connector / trigger, most specific wins.
- **[[Secrets]]** — `${ENV}` as the baseline plus named `vaults:`
  (conductor / onepassword / pass / file / hashicorp), one
  `{{ vault … }}` reference syntax, tainting/redaction, and the
  non-interactive unlock model. **[[Agent-Skill]]** — how a dispatched agent
  reaches back into conductor (verb tools + the secret broker), gated per
  profile.
- **[[Gates]]** — quality gates on agent output: checks against the
  proposed change, a bounded revise loop, escalation. **[[Teams]]** — one
  task split across planner / parallel workers / critic / reconciler.
- **[[Outcomes]]** — merged / reverted / approved / rejected captured per
  agent action and fed back into memory, workflow health, and guidance;
  **[[Cost-Accounting]]** — per-run token/$ usage and hard budget caps.
- **[[Runs]]** — full execution history, `conductor watch` live streaming,
  proposed-diff preview, and retry-from-step. **[[Isolation]]** —
  per-dispatch sandboxing and the egress allowlist. **[[Binary-Data]]** —
  files as content-addressed blob handles between steps and agents.
- **[[Authoring-Connectors]]** — adding a connector type in-tree (the
  contract, the executable template, build-tag pattern).
- **[[Plugins]]** — external connectors & runtimes: out-of-process plugins the
  daemon runs to add a `type:` or `runtime:` without recompiling, and the
  security model that gates them (verify, sandbox, least-privilege creds).

## Learning path

Work down this list and you go from zero to the most advanced setup:

1. **Install and first trigger** — [[Installation]], then [[Quickstart]]:
   a credential-free config, `conductor validate`, and a `manual` run.
2. **Core concepts** — [[Connectors]] (events vs verbs), [[Workflows]]
   (triggers, steps, hooks, context scope), [[Verbs]] (options + identity).
3. **Connect real services** — [[GitHub-App-Setup]] + [[Integration-GitHub]],
   then [[Integration-Slack]] · [[Integration-Cron]] ·
   [[Integration-Webhook]] · [[Integration-Sentry]] ·
   [[Integration-PagerDuty]] · [[Integration-RSS]]; anything else via the
   `rest`/`graphql` types in [[Configuration]].
4. **Agents** — [[Runtimes]], [[Steps]] (profiles, checkout, guidance),
   [[Hand-offs]] (`ask` verbs), and [[Notifications]] (the `conductor.*`
   lifecycle source).
5. **Composition** — reusable workflows with inputs/outputs ([[Workflows]]),
   YAML anchors, `extends:` inheritance + layered guidance ([[Reuse]]), [[Grouping]],
   [[Code-Steps]], [[Hosts]], and file splitting (`imports:`, [[Configuration]]).
6. **State** — `stores:` + the `kv.*`/`sql.*` verbs ([[Stores]]),
   [[Memory]], and session affinity ([[Steps]]).
7. **Hardening** — [[Secrets]] (vaults, OAuth2 logins, unlock), [[Policy]]
   (quiet hours, rate limits, bots), and dry-run/replay ([[Commands]]).
8. **Advanced** — agent-driven workflows and `policy.agent_authored`
   ([[Workflows]] + [[Policy]]), the agent skill ([[Agent-Skill]]),
   self-update as a workflow ([[Configuration]]), remote fleets over SSH
   ([[Hosts]]).
9. **Agent quality** — [[Gates]] on agent output, [[Teams]] for one big
   task, [[Outcomes]] closing the loop, [[Cost-Accounting]] budgets,
   [[Isolation]] sandboxing, [[Runs]] (history / `watch` / retry),
   [[Binary-Data]] artifacts.
10. **Growing the library** — [[Authoring-Connectors]].
11. **Coming from the legacy schema** — [[Migration]] (automatic, total,
    fail-safe).

## Pages

Setup: [[Installation]] · [[Quickstart]] · [[GitHub-App-Setup]] ·
[[Configuration]] · [[Commands]] · [[Examples]]

The model: [[Connectors]] · [[Workflows]] · [[Reuse]] · [[Settings-and-Templating]] · [[Verbs]] · [[Code-Steps]] · [[Stores]] ·
[[Runtimes]] · [[Steps]] · [[Grouping]] · [[Memory]] · [[Binary-Data]] ·
[[Agent-Skill]] · [[Policy]] · [[Gates]] · [[Teams]] · [[Outcomes]] ·
[[Cost-Accounting]] · [[Secrets]] · [[Trust-and-Isolation]] · [[Hosts]] · [[Isolation]]

Connector references: [[Authoring-Connectors]] · [[Integration-GitHub]] ·
[[Integration-Slack]] · [[Integration-Cron]] · [[Integration-Webhook]] ·
[[Integration-Sentry]] · [[Integration-PagerDuty]] · [[Integration-RSS]]

Operations: [[Runs]] · [[Notifications]] · [[Hand-offs]] · [[Migration]]
(the legacy schema still loads and auto-migrates)
