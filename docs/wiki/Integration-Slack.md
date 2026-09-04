# Slack connector

One Slack connection carries all three roles the legacy config split across
`integrations:`, `notify:`, and `handoffs:`: events in (Socket Mode), posts
out, and interactive asks.

```yaml
connectors:
  slack-ops:
    type: slack
    app_token: ${SLACK_APP_TOKEN}   # xapp-… Socket Mode (needed for events + ask replies)
    bot_token: ${SLACK_BOT_TOKEN}   # xoxb-… posting
    options: { channel: C0123456789 }
```

App setup is unchanged: a Slack app with Socket Mode on, an app-level token
(`connections:write`), a bot token with `app_mentions:read`, `chat:write`,
`reactions:write` (+ `im:write`/`im:history` for DMs and `commands` for slash
commands), and the relevant event subscriptions.

## Events (`on: slack-ops.<event>`)

| event | filters | context |
|---|---|---|
| `app_mention` | `channel`, `users` | `slack.channel`, `slack.user`, `slack.text`, `slack.ts`, `slack.thread_ts` |
| `reaction_added` | `reaction`, `channel`, `users` | + `slack.reaction` |
| `slash_command` | `command`, `channel`, `users` | + `slack.command` |

## Verbs (`uses: slack-ops.<verb>`)

| verb | options | outputs |
|---|---|---|
| `post` | `text`*, `channel` or `user` (DM), `thread_ts`, `ephemeral` (needs channel+user) | `ts`, `channel` |
| `react` | `channel`*, `ts`*, `emoji`* (no colons) | `ok` |
| `ask` | `prompt`*, `to`* (dm\|thread), `user`/`channel`, `draft`, `title`, `timeout` | `action`, `text`, `ref` |

The legacy per-rule `ack:`/`on_done:`/`on_fail:` feedback is now ordinary
hooks — [[Migration]] converts them:

```yaml
triggers:
  - on: slack-ops.app_mention
    steps: [{ id: work, type: agent, agent: fixer, prompt: "{{.slack.text}}" }]
    hooks:
      - { at: start, uses: slack-ops.react, options: { channel: "{{.slack.channel}}", ts: "{{.slack.ts}}", emoji: eyes } }
      - { at: done,  uses: slack-ops.react, options: { channel: "{{.slack.channel}}", ts: "{{.slack.ts}}", emoji: white_check_mark } }
```

Related: [[Connectors]] · [[Hand-offs]] · [[Verbs]] · [[Migration]]
