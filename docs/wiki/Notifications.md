# Notifications

conductor posts short messages when something needs your attention. Every
event is written to the journal (the audit log) regardless of configuration;
`notify.on` selects which events *also* deliver externally: `dispatch`,
`complete`, `escalate`, `needs_input` (and the periodic `digest`, opt-in via
`notify.digest`). Nothing notify sends is ever posted as a PR/issue comment —
it is private, addressed to you alone.

## Delivery through verbs (`via:`)

Notifications deliver through connector verbs — the same action layer
workflows use:

```yaml
connectors:
  alerts:  { type: ntfy, topic: your-topic }
  ops:     { type: slack, webhook_url: ${SLACK_WEBHOOK_URL} }  # post-only connection

notify:
  on: [escalate, needs_input]
  via:
    - { uses: alerts.publish, options: { title: conductor, message: "{{.message}}" } }
    - { uses: ops.post, options: { text: "conductor {{.message}}" } }
    - { on: [escalate], uses: pager.notify, options: { message: "{{.message}}" } }  # per-route on:
  digest: 24h
```

Each route is an action unit: options merge over the connector's defaults and
render with `{{.message}}` (the composed notification line) plus `{{.event}}`,
`{{.repo}}`, `{{.number}}`, `{{.kind}}`, `{{.title}}`, and `{{.ref}}`. A route's
own `on:` restricts it to a subset of the block's events. Routes are
best-effort and concurrent, exactly like the sinks they replace; failures log
and never block the daemon. `conductor validate` checks every route's verb,
options, and references.

The sink connector types: `ntfy` (`publish`), `pushover` (`notify`),
`notifiarr` (`notify`), and the `slack`/`discord` connectors' post-only
`webhook_url:` connection mode (an incoming webhook instead of a bot token —
`post` sends the same `{"text": …}` / `{"content": …}` payload the legacy
sinks did; `react`/`ask` still need the bot token).

## The legacy sink fields

`slack_webhook_url`, `discord_webhook_url`, `ntfy:`, `pushover:`, and
`notifiarr:` still work exactly as before on a legacy config. [[Migration]]
maps each onto a generated connector (`notify-slack`, `notify-ntfy`, …) plus a
`via:` route whose wire payload is byte-identical to the legacy poster's —
proven by a golden that runs the same Emit through both paths and compares
bodies.

Workflow-level feedback ("this trigger finished") is better expressed as
[[Workflows|hooks]] calling [[Verbs]] — per-trigger, position-scoped, and
addressed wherever the connector posts.

Related: [[Connectors]] · [[Verbs]] · [[Migration]] · [[Configuration]]
