# Connectors

A connector is one connection to an external service, with two faces:

- **Sources** — events you trigger on: `on: <connector>.<event>`
- **Verbs** — actions you call from steps and hooks: `uses: <connector>.<verb>`

The same name addresses both directions. Slack is configured once and is both
"a mention arrived" and "post this message".

```yaml
connectors:
  gh:
    use: github                    # WHAT implements it — see [[Plugins]] for the
                                   # full resolution path (builtin → official
                                   # plugin repo → an explicit repo → a local binary)
    app: { app_id: 123456, private_key_path: ~/.config/conductor/github-app.pem }
    webhook: { smee_url: ${GH_SMEE_URL}, secret: ${GH_WEBHOOK_SECRET} }
    me: { logins: [your-login] }
    repos: ["your-org/*"]
    options: { as: me }            # default verb options — every call merges over these
    policy:                        # connector-scoped policy (see [[Policy]])
      ignore: { users: ["dependabot[bot]"] }
      rate_limits: { per_minute: 60 }
  slack-ops:
    use: slack
    app_token: ${SLACK_APP_TOKEN}
    bot_token: ${SLACK_BOT_TOKEN}
    options: { channel: C0123456789 }
```

## Where connectors come from

`use:` names what implements a connector, and conductor resolves it in one
order — **first match wins** (full rules in [[Plugins]]):

| `use:` value | resolves to |
|---|---|
| `use: cron` | a **built-in** — compiled into the daemon (see [Built-in connector types](#built-in-connector-types)) |
| `use: github` | not built-in → the **official plugin** `connectors/github` |
| `use: sonarr` | not built-in → the **official plugin repo** `NodeSpy/conductor-plugins`, at `connectors/sonarr` |
| `use: acme/plugins/jira` | an explicit **GitHub** repo (`github.com` implied) |
| `use: git.corp.example/team/p//jira` | an explicit **non-GitHub** host (`//` separates the repo from the component) |
| `use: ./bin/conductor-jira` | a **local** binary, for developing one |

**Built-in beats official** — `use: cron` is always the in-binary connector,
never the plugin repo. Built-ins are vendor-neutral; every vendor connector
(GitHub, Slack, Discord, …) is a plugin. A plugin stays current by default; pin an exact build
with `use: sonarr@v1.2.3` (or a range, `@^1.2`). Built-ins and local binaries
have no version to pin. See [[Plugins]] for versioning, the trust/allowlist
model, and the `conductor plugin` commands.

### The plugin catalog

The official plugins — **69 connectors** (the Servarr apps, the Google
Workspace set, proxmox, unifi, grafana, home-assistant, and many more), plus
code engines and agent runtimes — live in
**[conductor-plugins](https://github.com/NodeSpy/conductor-plugins)**. Browse
them, with a reference **and setup walkthrough** for each (how to get the
service's token / OAuth app, the exact settings-page path, and a minimal config
block), under
**[docs/connectors](https://github.com/NodeSpy/conductor-plugins/tree/main/docs/connectors)**
— start at the
**[catalog index](https://github.com/NodeSpy/conductor-plugins/blob/main/docs/README.md)**.
You don't clone the repo: name one in `connectors:` and run `conductor init`,
and conductor downloads it, verifies the checksum, and runs it as a sandboxed
subprocess. For GitHub App setup specifically, see [GitHub App setup](https://github.com/NodeSpy/conductor-plugins/blob/main/docs/connectors/github.md#setup).

### github is a plugin

The github connector is the official plugin (`conductor-plugins`
`connectors/github`); `use: github` names it. The daemon fetches and verifies
it at boot before any connector starts, and a connector whose plugin is not
installed yet runs disabled with the reason while the daemon retries — nothing
else is held up.

Like every installed plugin, its events carry the semantics its declaration
gives them (a `new_comment` dedupes on its comment cursor, a `_closed` ends
the PR's runs, …) and its targets are taken as the platform assigned them:
trust in a plugin is decided once, at install (`plugin_trust:`), and after
that all plugins are equal. `api_base:` is how the plugin reaches GitHub
Enterprise Server or a test double — a plugin's environment is scrubbed, so
it reads no API base from the environment.

## The contract

Every connector type is self-describing. It declares:

1. **Events** — the kinds valid after `on: <conn>.`, each with a **filter
   schema** (the match keys legal inside a trigger's `filter:` and how they
   evaluate) and a **context schema** (the facts the event publishes into
   templates, which its `filter:` expr strings also read).
2. **Verbs** — the actions valid after `uses: <conn>.`, each with an **option
   schema** and, for request-response verbs, an **output schema**.
3. **Connection** — credentials, identity (`me:`), default match (`repos:`),
   default `options:`, an `enabled:` toggle, and a per-connector `policy:`.

`conductor connectors ls` lists every configured connector's state, events,
and verbs; `conductor schema <conn>` prints the full schemas. `conductor
validate` checks every `on:` kind, `filter:` key, `uses:` verb, option, and
template reference against these declarations at load time.

## Enable / disable, and failure posture

- `enabled: false` on a connector opens no sources and rejects its verbs; the
  config stays intact and still validates.
- A connector whose credentials or secret references fail to resolve is
  **disabled with the reason recorded** — the daemon boots and runs the rest
  (`connectors ls` and `secrets check` show why). A bad connector never
  crash-loops the box.
- `conductor validate` keeps exiting 0 even when a connector ends up
  disabled this way (degrade-not-fail is deliberate — the same posture the
  daemon's own boot takes), but its summary says so honestly: `ok: 2
  connector(s) (1 disabled: slack — see above)` names every one, pointing
  back at the reason already logged above it. Pass `--strict` to make
  validate fail instead when any connector is disabled — useful in CI or
  before a deploy, where "it loads" isn't the same question as "it's
  actually going to work". `--strict` never flags a connector turned off on
  purpose with `enabled: false`; only a credential/build/fetch failure
  counts. `--require-plugins` is the narrower, existing check self-update
  itself uses (a referenced plugin must be installed or fetchable) and is
  independent of `--strict`.

## Option merging and identity

A connector's `options:` are defaults for every verb call; each call's
`options:` merges over them, the call winning (nested maps merge key-wise).
Identity is an option like any other: `as: me` (default — acts as you) or
`as: bot` (the GitHub App's bot user), settable per connector or per call.
A connector-wide default that a particular verb does not declare is ignored
for that verb.

## Built-in connector types

These ship **compiled into the daemon** — no download, always available by name.
(The [plugin catalog](#the-plugin-catalog) adds ~69 more as sandboxed
subprocesses — every vendor connector is one: `github`, `slack`, `discord`,
`ntfy`, `pushover`, `notifiarr`, and the tunnel services `cloudflared`,
`ngrok`, `localxpose`, `sshtunnel`, `tailscale` and `smee`.)

| type | events | verbs | notes |
|---|---|---|---|
| `web` | — | `ask` | approve/revise/discard page on the inbound listener; [[Hand-offs]] tunnels |
| `cron` | one per declared schedule | — | `schedules:` on the connection |
| `webhook` | one per declared source | `post` (generic outbound HTTP) | `sources:` with signing/match/title/dedup |
| `rss` | one per declared feed | — | per-trigger `match:` regex filter |
| `command` | — | `run` | commands local or over SSH via `host:`/`ssh:`; outputs `stdout`/`stderr`/`exit_code` |
| `rest` | user-declared polled `events:` | user-declared `verbs:` | any HTTP API from config: `base_url` + shared `auth:` (incl. oauth2 w/ refresh rotation) — see [[Configuration]] |
| `graphql` | — | user-declared `verbs:` | one endpoint; verbs are queries/mutations with typed `variables:`; `errors` fails even on 200 — see [[Configuration]] |
| `kv` | — | `get`, `set`, `setnx`, `merge`, `delete`, `incr`, `append`, `remove`, `contains`, `first`, `last`, `index`, `slice`, `len`, `pop`, `list` | the data verbs over the `stores:` section's KV types (boltdb/redis/http); every call requires `store:` naming a defined store — see [[Configuration]] |
| `sql` | — | `query`, `exec` | parameterized SQL over the `stores:` section's SQL types (postgres/mysql/sqlite, pure-Go drivers); `store:` required, values bind through `args:` to driver placeholders — see [[Configuration]] |
| `memory` | — | `remember`, `recall`, `forget`, `list` | shared agent memory over the `memory:` section; always available, load-checked against it — see [[Memory]] |
| `workflow` | — | `list`, `run`, `save` | the workflow catalog, run-by-name / inline plans (guarded by `policy.agent_authored`), and agent promotion — see [[Workflows]] |
| `conductor` | `dispatch`, `escalate`, `needs_input`, `complete`, `failed`, `updated`, `update_available` | `update`, `pause`, `resume`, `restart`, `reload`, `run` | conductor itself — lifecycle events as a source (alerting is an ordinary trigger; loop-guarded), daemon operations as verbs; always available, name reserved — see [[Notifications]] |

`webhook`'s `dedup:` is replay protection, not a default suppression: a
source with no `dedup:` template fires on **every** delivery, full stop — no
hash of the body is checked behind your back. A sender that legitimately
re-POSTs an identical payload (a heartbeat/status webhook) is never silently
dropped. Declare `dedup:` (a template over `{{.body...}}`) only when a
retried/re-delivered body should collapse to one event.

Every `vaults:` entry also surfaces under its own name with `read` (all
types) and `write` (writable types) verbs — values read there are tainted
sensitive and redacted from logs/audit. See [[Secrets]].

Trigger matching is uniform across every connector: triggers are
**independent** — every trigger whose `filter:` matches an event fires. (Legacy
sentry/pagerduty rules were first-match-wins; the migration reproduces that
winner exactly by generating negated keys on later triggers, so nothing
double-fires after a migration.)

## Adding a connector type

The library accretes: `rest`/`graphql` cover anything not yet typed, and a
new typed connector is one file + one registration. See
[[Authoring-Connectors]] and the executable template in
`internal/connector/authoring_example_test.go`.

Related: [[Verbs]] · [[Configuration]] · [[Grouping]] · [[Policy]] · [[Migration]] · [[Authoring-Connectors]] · [[Plugins]]
