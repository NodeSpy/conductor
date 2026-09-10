# PagerDuty connector (plugin)

PagerDuty V3 webhooks in; one event, `incident`.

**PagerDuty is not bundled.** It is a plugin from the official repo
([conductor-plugins](https://github.com/NodeSpy/conductor-plugins), at
`connectors/pagerduty`). Naming it in `use:` is the whole installation:
`conductor init` fetches, checksum-verifies, and installs it. See [[Plugins]].

```yaml
connectors:
  oncall:
    use: pagerduty                         # bare name → the official plugin repo
    listen: ":9098"
    # path: /pagerduty                     # default /pagerduty
    signing_secret: ${PAGERDUTY_SIGNING_SECRET}

triggers:
  - on: oncall.incident
    filters: { event_types: [incident.triggered], urgencies: [high] }
    steps:
      - { id: triage, type: agent, agent: fixer, checkout: none,
          prompt: "Research incident {{.title}} ({{.url}}) and summarize likely causes." }
      - { id: page, uses: slack-ops.post, options: { channel: "#outage", text: "{{.triage.text}}" } }
```

**Filters:** `event_types`, `services` (summary or id), `urgencies`,
`priorities` — case-insensitive lists; empty = any.

**Context.** Template `{{.title}}` and `{{.url}}`. The plugin also emits
`pagerduty.event_type`, `.status`, `.title`, `.urgency`, `.priority`,
`.service`, `.service_id`, `.number`, `.id`, `.url` — but as **literal dotted
keys in a flat map**, not a nested `pagerduty` object. Conductor's template
resolver walks a dotted path through *nested* maps, so `{{.pagerduty.title}}`
does not resolve. Use the flat `{{.title}}` / `{{.url}}`.

Signature verification handles key rotation: any `v1=` entry in a multi-value
`X-PagerDuty-Signature` may match.

**Permissions.** The plugin declares an empty manifest: it *listens*, it never
dials out, and it spawns nothing. `conductor plugin show pagerduty` prints this.

> **Migrating from the bundled connector?** `conductor config migrate`
> deliberately does **not** transform a legacy `integrations: - type: pagerduty`
> block, and says so loudly. The context shape differs, and `exclude:` is not
> evaluated by the generic filter evaluator plugin sources go through — so an
> automatic transform would emit a config that looks migrated but whose triggers
> never fire. Write the connector and triggers by hand against the contract
> above.

Related: [[Plugins]] · [[Connectors]] · [[Migration]]
