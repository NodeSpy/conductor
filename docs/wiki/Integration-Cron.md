# Cron connector

Schedules are declared on the connection; each schedule name is an event.

```yaml
connectors:
  timer:
    type: cron
    schedules:
      nightly-tidy: { cron: "0 4 * * *" }            # standard 5-field cron, or @daily etc.
      rate-check:   { every: 6h, run_on_start: true } # interval form

triggers:
  - on: timer.nightly-tidy
    steps:
      - { id: tidy, type: command, command: [make, tidy], workdir: ~/src/infra }
```

Context: `schedule` (the name), `kind`, `title`. No filters, no verbs. One
trigger per schedule (a second trigger on the same schedule is a validation
error — declare another schedule). Scheduled work usually wants
`checkout: none` on agent steps, or an explicit `workdir:`.

Legacy `integrations: - type: cron` still loads; [[Migration]] moves each
schedule onto the connection and emits its trigger.

Related: [[Connectors]] · [[Workflows]]
