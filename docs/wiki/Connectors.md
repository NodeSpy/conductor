# Connectors

A connector is one connection to an external service, with two faces:

- **Sources** — events you trigger on: `on: <connector>.<event>`
- **Verbs** — actions you call from steps and hooks: `uses: <connector>.<verb>`

The same name addresses both directions. Slack is configured once and is both
"a mention arrived" and "post this message".

```yaml
connectors:
  gh:
    type: github
    app: { app_id: 123456, private_key_path: ~/.config/conductor/github-app.pem, webhook_secret: ${GH_WEBHOOK_SECRET} }
    webhook: { smee_url: ${GH_SMEE_URL} }
    me: { logins: [your-login] }
    repos: ["your-org/*"]
    options: { as: me }            # default verb options — every call merges over these
    policy:                        # connector-scoped policy (see [[Policy]])
      ignore: { users: ["dependabot[bot]"] }
      rate_limits: { per_minute: 60 }
  slack-ops:
    type: slack
    app_token: ${SLACK_APP_TOKEN}
    bot_token: ${SLACK_BOT_TOKEN}
    options: { channel: C0123456789 }
```

## The contract

Every connector type is self-describing. It declares:

1. **Events** — the kinds valid after `on: <conn>.`, each with a **filter
   schema** (the legal `filters:` keys and how they evaluate) and a **context
   schema** (the facts the event publishes into templates).
2. **Verbs** — the actions valid after `uses: <conn>.`, each with an **option
   schema** and, for request-response verbs, an **output schema**.
3. **Connection** — credentials, identity (`me:`), default match (`repos:`),
   default `options:`, an `enabled:` toggle, and a per-connector `policy:`.

`conductor connectors ls` lists every configured connector's state, events,
and verbs; `conductor schema <conn>` prints the full schemas. `conductor
validate` checks every `on:` kind, `filters:` key, `uses:` verb, option, and
template reference against these declarations at load time.

## Enable / disable, and failure posture

- `enabled: false` on a connector opens no sources and rejects its verbs; the
  config stays intact and still validates.
- A connector whose credentials or secret references fail to resolve is
  **disabled with the reason recorded** — the daemon boots and runs the rest
  (`connectors ls` and `secrets check` show why). A bad connector never
  crash-loops the box.

## Option merging and identity

A connector's `options:` are defaults for every verb call; each call's
`options:` merges over them, the call winning (nested maps merge key-wise).
Identity is an option like any other: `as: me` (default — acts as you) or
`as: bot` (the GitHub App's bot user), settable per connector or per call.
A connector-wide default that a particular verb does not declare is ignored
for that verb.

## Types

| type | events | verbs | notes |
|---|---|---|---|
| `github` | `merge_conflict`, `pr_behind`, `failing_checks`, `changes_requested`, `new_comment`, `review_requested`, `self_review`, `merge_ready`, `issue_matched`, `release`, `deployment_status`, `dependabot_alert`, `secret_scanning_alert`, `stuck_checks` | `comment`, `reply`, `rerequest_review`, `submit_review`, `add_labels` | creds: app → token → gh (see [[Connector-GitHub|Integration-GitHub]]) |
| `slack` | `app_mention`, `reaction_added`, `slash_command` | `post`, `react`, `ask` | Socket Mode in, Web API out |
| `discord` | — | `post`, `ask` | bot token; gateway captures ask replies |
| `web` | — | `ask` | approve/revise/discard page on the inbound listener; [[Hand-offs]] tunnels |
| `cron` | one per declared schedule | — | `schedules:` on the connection |
| `webhook` | one per declared source | `post` (generic outbound HTTP) | `sources:` with signing/match/title/dedup |
| `sentry` | `alert` | — | filters: projects/levels/environments |
| `pagerduty` | `incident` | — | filters: event_types/services/urgencies/priorities |
| `rss` | one per declared feed | — | per-trigger `match:` regex filter |
| `command` | — | `run` | commands local or over SSH via `host:`/`ssh:`; outputs `stdout`/`stderr`/`exit_code` |
| `rest` | user-declared polled `events:` | user-declared `verbs:` | any HTTP API from config: `base_url` + shared `auth:` (incl. oauth2 w/ refresh rotation) — see [[Configuration]] |
| `graphql` | — | user-declared `verbs:` | one endpoint; verbs are queries/mutations with typed `variables:`; `errors` fails even on 200 — see [[Configuration]] |
| `kv` | — | `get`, `set`, `setnx`, `merge`, `delete`, `incr`, `append`, `remove`, `contains`, `first`, `last`, `index`, `slice`, `len`, `pop`, `list` | the data verbs over the `stores:` section (boltdb/redis/http); every call requires `store:` naming a defined store — see [[Configuration]] |

Trigger matching is uniform across every connector: triggers are
**independent** — every trigger whose filters match an event fires. (Legacy
sentry/pagerduty rules were first-match-wins; the migration reproduces that
winner exactly by generating `exclude:` filters on later triggers, so nothing
double-fires after a migration.)

Related: [[Verbs]] · [[Configuration]] · [[Grouping]] · [[Policy]] · [[Migration]]
