# Notifications — the `conductor.*` lifecycle source

Conductor emits its own operational events as a built-in source, so alerting
is an ordinary trigger — the retired `notify:` block auto-migrates to these
(see [[Migration]]). Every attention event is written to the audit log
regardless of configuration (`status` / `report` see escalations with no
trigger at all); a trigger selects which events *also* deliver externally.
Nothing here is ever posted as a PR/issue comment — it is private, addressed
to you alone.

| event | fires when |
|---|---|
| `conductor.dispatch` | work started on a target |
| `conductor.escalate` | a target gave up after retries |
| `conductor.needs_input` | a workflow handed a PR to a live agent and is waiting on you |
| `conductor.complete` | a run finished |
| `conductor.failed` | a run errored |
| `conductor.updated` | conductor self-updated (fires on the first boot of the new release) |
| `conductor.update_available` | a newer release was detected under `update: { apply: workflow }` |

Context: `{{.message}}` (the composed line), `{{.event}}`, `{{.ref}}`
(repo#number), `{{.repo}}`, `{{.number}}`, `{{.origin_kind}}` (the
originating work's kind), `{{.title}}`; the update events add
`{{.version}}`.

```yaml
connectors:
  alerts: { type: ntfy, topic: your-topic }
  pager:  { type: pushover, token: ${PUSHOVER_TOKEN}, user: ${PUSHOVER_USER} }

triggers:
  - name: act-now
    on: [ conductor.escalate, conductor.failed, conductor.needs_input ]
    steps:
      - { uses: alerts.publish, options: { title: conductor, message: "{{.message}}" } }
  - name: page-on-giveup
    on: conductor.escalate
    steps:
      - { uses: pager.notify, options: { message: "conductor {{.message}}" } }
  - name: daily-digest
    on: conductor.complete
    group: { key: '"digest"', window: 24h }
    steps:
      - { uses: alerts.publish, options: { title: conductor, message: "[digest] {{ len .group.events }} completed run(s) today" } }
```

Routing, filtering, per-event fan-out (`on:` lists), quiet hours
(`policy:`), and digests (`group: { window: … }`) all come from the normal
trigger grammar — there is no separate notification subsystem.

**Loop guard.** Events emitted *by* a conductor-lifecycle trigger's own run
are never re-fed into `conductor.*` — a notification workflow's own
dispatch/complete/failed cannot storm. Its escalations still land in the
audit log.

**Acting on conductor.** The same connector also exposes verbs
(`conductor.update` / `pause` / `resume` / `restart` / `reload` /
`run {name, inputs}`, plus `gh.sweep` for the github catch-up) — see
[[Configuration]] for the gated-update workflow
(`update: { apply: workflow }` → a trigger on `conductor.update_available`
drains, updates, and smoke-tests with hooks around each step).

Related: [[Configuration]] · [[Workflows]] · [[Migration]]
