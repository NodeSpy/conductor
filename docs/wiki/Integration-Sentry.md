# Sentry connector (plugin)

Sentry Integration-Platform webhooks in — issue, error, and metric alerts.

**Sentry is not bundled.** It is a plugin from the official repo
([conductor-plugins](https://github.com/NodeSpy/conductor-plugins), at
`connectors/sentry`). Naming it in `use:` is the whole installation:
`conductor init` fetches, checksum-verifies, and installs it. See [[Plugins]].

```yaml
connectors:
  errors:
    use: sentry                            # bare name → the official plugin repo
    listen: ":9099"
    # path: /sentry                        # default /sentry
    client_secret: ${SENTRY_CLIENT_SECRET} # Sentry-Hook-Signature HMAC

triggers:
  - on: errors.issue_alert
    filters: { projects: [backend], levels: [error, fatal], environments: [production] }
    repo: acme/backend                     # optional checkout target
    steps:
      - { id: dig, type: agent, agent: fixer, prompt: "Investigate {{.title}} ({{.url}})." }
```

**Events:** `issue_alert`, `error_alert`, `event_alert` — one per Sentry alert
resource. (The retired bundled connector had a single `alert` event covering all
three; a config that still says `on: errors.alert` names an event nothing
emits.)

**Filters:** `projects`, `levels`, `environments` (lists), and the singular
`project`, `level`, `environment`.

**Context is FLAT**, not nested under `sentry.`: `resource`, `action`, `title`,
`level`, `environment`, `culprit`, `short_id`, `project`, `url`. A prompt
templating `{{.sentry.title}}` renders empty — use `{{.title}}`.

**Permissions.** The plugin declares an empty manifest: it *listens*, it never
dials out, and it spawns nothing. `conductor plugin show sentry` prints this.

> **Migrating from the bundled connector?** `conductor config migrate`
> deliberately does **not** transform a legacy `integrations: - type: sentry`
> block, and says so loudly. The event names, the context shape, and `exclude:`
> support all differ, so an automatic transform would emit a config that looks
> migrated but whose triggers never fire and whose prompts render empty. Write
> the connector and triggers by hand against the contract above.

Related: [[Plugins]] · [[Connectors]] · [[Migration]]
