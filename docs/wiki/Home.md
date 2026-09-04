# conductor

Event-driven agent orchestration for your Paseo daemon: connect to services
once, declare where agents run, and wire events to work — agents, commands,
inline code, and any connector's verbs, crossing service boundaries freely.

```yaml
connectors:
  gh:        { type: github, app: {…}, me: { logins: [you] }, repos: ["org/*"] }
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
  (`uses:`), self-describing schemas, and per-connector policy.
- **[[Runtimes]] + [[Agents]]** — where agents run and who they are.
- **[[Workflows]]** — the trigger grammar: `on` / `filters` / `steps` /
  `hooks`, position-scoped context, control flow, reusable workflows.
- **[[Verbs]]** — the shared action unit, option merging, `as:` identity,
  and request-response `ask` ([[Hand-offs]]).
- **[[Code-Steps]]** — `run: js` (WASM-sandboxed), `run: go-embed` (yaegi),
  host interpreters; **[[Hosts]]** for SSH remote execution.
- **[[Grouping]]** — debounce batching, one run per key.
- **[[Policy]]** — quiet hours, concurrency, ignores, rate limits, pause
  labels; global / connector / trigger, most specific wins.
- **[[Secrets]]** — `${ENV}` plus `op://` / `pass:` / `vault:` / `file:`
  references, the named block, and the non-interactive unlock model.

## Pages

Setup: [[Installation]] · [[GitHub-App-Setup]] · [[Configuration]] ·
[[Commands]] · [[Examples]]

Connector references: [[Integration-GitHub]] · [[Integration-Slack]] ·
[[Integration-Cron]] · [[Integration-Webhook]] · [[Integration-Sentry]] ·
[[Integration-PagerDuty]] · [[Integration-RSS]]

Operations: [[Notifications]] · [[Hand-offs]] · [[Migration]] (the legacy
schema still loads and auto-migrates)
