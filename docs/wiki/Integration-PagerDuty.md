# PagerDuty connector

PagerDuty V3 webhooks in; one event, `incident`.

```yaml
connectors:
  oncall:
    type: pagerduty
    listen: ":8097"                        # and/or smee_url:
    # path: /pagerduty                     # default /pagerduty
    signing_secret: ${PAGERDUTY_SIGNING_SECRET}

triggers:
  - on: oncall.incident
    filters: { event_types: [incident.triggered], urgencies: [high] }
    steps:
      - { id: triage, type: agent, agent: fixer, checkout: none,
          prompt: "Research incident {{.pagerduty.title}} ({{.pagerduty.url}}) and summarize likely causes." }
      - { id: page, uses: slack-ops.post, options: { channel: "#outage", text: "{{.triage.text}}" } }
```

Filters: `event_types`, `services` (summary or id), `urgencies`, `priorities`
(case-insensitive lists; empty = any). Context: `pagerduty.event_type`,
`.status`, `.title`, `.urgency`, `.priority`, `.service`, `.service_id`,
`.number`, `.id`, `.url`, plus `url`. Signature verification handles key
rotation (any `v1=` entry may match).

**Ordering:** first matching trigger wins, in config order (legacy-rule
semantics — see [[Connectors]]).

Related: [[Connectors]] · [[Migration]]
