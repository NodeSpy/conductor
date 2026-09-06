# conductor

Event-driven agent orchestration for your Paseo daemon: connect to services
once, declare where agents run, and wire events to work — agents, commands,
inline code, and any connector's verbs, crossing service boundaries freely.

```yaml
connectors:
  gh:        { type: github, app: {…}, me: { logins: [your-login] }, repos: ["your-org/*"] }
  slack-ops: { type: slack, app_token: ${SLACK_APP_TOKEN}, bot_token: ${SLACK_BOT_TOKEN} }

runtimes:
  paseo: { type: paseo, default: true }

agents:
  fixer: { provider: claude, workspace: worktree }

triggers:
  - on: gh.merge_conflict
    steps:
      - { id: fix, type: agent, agent: fixer, prompt: "Resolve the conflict on {{.repo}}#{{.pr}}." }
    hooks:
      - { at: done, uses: slack-ops.post, options: { text: "resolved {{.repo}}#{{.pr}}" } }
```

## The model

- **[[Connectors]]** — external services, each with events (`on:`) and verbs
  (`uses:`), self-describing schemas, and per-connector policy. Anything
  without a built-in type is a `rest` / `graphql` connector declared in
  config.
- **[[Runtimes]] + [[Agents]]** — where agents run and who they are; an
  agent profile's `session:` binds one live agent per key ([[Agents]]).
- **[[Workflows]]** — the trigger grammar: `on` / `filters` / `steps` /
  `hooks`, position-scoped context, control flow, reusable workflows, and
  agent-authored plans.
- **[[Verbs]]** — the shared action unit, option merging, `as:` identity,
  and request-response `ask` ([[Hand-offs]]).
- **[[Code-Steps]]** — sandboxed in-process engines (`js`, `go-embed`,
  `risor`, `lua`) and host interpreters; **[[Hosts]]** for SSH remote
  execution.
- **Stores & [[Memory]]** — named `stores:` (KV + SQL) behind the always-on
  `kv.*` / `sql.*` verbs ([[Configuration]]), and shared agent memory with
  provenance and scope.
- **[[Grouping]]** — debounce batching, one run per key.
- **[[Policy]]** — quiet hours, concurrency, ignores, rate limits, pause
  labels, `reply_to_bots`, and the `agent_authored` guardrails; global /
  connector / trigger, most specific wins.
- **[[Secrets]]** — `${ENV}` as the baseline plus named `vaults:`
  (conductor / onepassword / pass / file / hashicorp), one
  `{{ vault … }}` reference syntax, tainting/redaction, and the
  non-interactive unlock model.

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
4. **Agents** — [[Runtimes]], [[Agents]] (profiles, checkout, guidance),
   [[Hand-offs]] (`ask` verbs), and [[Notifications]] (the `conductor.*`
   lifecycle source).
5. **Composition** — reusable workflows with inputs/outputs ([[Workflows]]),
   [[Grouping]], [[Code-Steps]], [[Hosts]], and file splitting
   (`imports:`, [[Configuration]]).
6. **State** — `stores:` + the `kv.*`/`sql.*` verbs ([[Configuration]]),
   [[Memory]], and session affinity ([[Agents]]).
7. **Hardening** — [[Secrets]] (vaults, OAuth2 logins, unlock), [[Policy]]
   (quiet hours, rate limits, bots), and dry-run/replay ([[Commands]]).
8. **Advanced** — agent-driven workflows and `policy.agent_authored`
   ([[Workflows]] + [[Policy]]), self-update as a workflow
   ([[Configuration]]), remote fleets over SSH ([[Hosts]]).
9. **Coming from the legacy schema** — [[Migration]] (automatic, total,
   fail-safe).

## Pages

Setup: [[Installation]] · [[Quickstart]] · [[GitHub-App-Setup]] ·
[[Configuration]] · [[Commands]] · [[Examples]]

The model: [[Connectors]] · [[Workflows]] · [[Verbs]] · [[Code-Steps]] ·
[[Runtimes]] · [[Agents]] · [[Grouping]] · [[Memory]] · [[Policy]] ·
[[Secrets]] · [[Hosts]]

Connector references: [[Integration-GitHub]] · [[Integration-Slack]] ·
[[Integration-Cron]] · [[Integration-Webhook]] · [[Integration-Sentry]] ·
[[Integration-PagerDuty]] · [[Integration-RSS]]

Operations: [[Notifications]] · [[Hand-offs]] · [[Migration]] (the legacy
schema still loads and auto-migrates)
