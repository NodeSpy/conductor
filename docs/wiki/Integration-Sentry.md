# Sentry connector

Sentry Integration-Platform webhooks (issue / error / metric alerts) in; one
event, `alert`.

```yaml
connectors:
  errors:
    type: sentry
    listen: ":8098"                       # and/or smee_url:
    # path: /sentry                       # default /sentry
    client_secret: ${SENTRY_CLIENT_SECRET} # Sentry-Hook-Signature HMAC

triggers:
  - on: errors.alert
    filters: { projects: [backend], levels: [error, fatal], environments: [production] }
    repo: acme/backend                    # optional checkout target
    steps:
      - { id: dig, type: agent, agent: fixer, prompt: "Investigate {{.sentry.title}} ({{.sentry.url}})." }
```

Filters: `projects`, `levels`, `environments` (case-insensitive lists; empty
= any). Context: `sentry.resource`, `sentry.action`, `sentry.title`,
`sentry.level`, `sentry.environment`, `sentry.culprit`, `sentry.short_id`,
`sentry.project`, `sentry.url`, plus `url`.

**Ordering:** sentry triggers are evaluated in config order and the first
match handles an event — mirroring the legacy rules exactly (see
[[Connectors]]).

Related: [[Connectors]] · [[Migration]]
