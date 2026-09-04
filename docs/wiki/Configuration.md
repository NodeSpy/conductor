# Configuration

`~/.config/conductor/config.yaml`, secrets in the sibling chmod-600
`conductor.env` (`${VAR}` expands at load; a referenced-but-unset variable is
a load error naming it). `conductor validate` checks everything below —
including every template reference against the scope at its position — before
the daemon runs. The full annotated example ships as `config.example.yaml`.

The LEGACY schema (`integrations:`/`notify:`/`handoffs:`/`controllers:`/
`control:`/`paseo_bin`) still loads and runs unchanged, and auto-migrates on
boot — see [[Migration]] and `config.example.legacy.yaml`.

## Top level

| key | what | reference |
|---|---|---|
| `connectors:` | named service connections: type, credentials, `me:`, default `repos:`, default `options:`, `enabled:`, per-connector `policy:` | [[Connectors]] |
| `triggers:` | the workflows: `on` / `filters` / `steps` / `hooks` (+ `group`, `policy`, `name`, `enabled`, `options`, `repo`, `shadow`) | [[Workflows]], [[Grouping]] |
| `runtimes:` | where agents run: `type`/`agent`, `transport`, `bin`, `host`, `default` | [[Runtimes]] |
| `agents:` | named profiles: `provider`, `model`, `thinking`, `mode`, `runtime`, `workspace`, `wait_timeout`, `archive_when_done`, `labels`, `guidance`, `host` | [[Agents]] |
| `hosts:` | named SSH targets: `host`, `user`, `port`, `key`, `known_hosts`, `cwd`, `env` | [[Hosts]] |
| `stores:` | named data stores — KV (`boltdb`/`redis`/`http`) served by `kv.*`, SQL (`postgres`/`mysql`/`sqlite`) served by `sql.*`; addressed by the required `store:` selector | below |
| `workflows:` | reusable step lists with `inputs:` / `outputs:` | [[Workflows]] |
| `policy:` | global controls; also valid on connectors and triggers (most specific wins) | [[Policy]] |
| `vaults:` | named secret stores (`conductor`/`onepassword`/`pass`/`file`/`hashicorp`), read as `{{ vault "<name>" "<key>" }}` with per-vault read/write verbs | [[Secrets]] |
| `imports:` | split the config across files (globs, deep-merged) | below |
| `store:` | `state_file`, `audit_log`, `state_ttl`, `max_tracked_prs`, `audit_max_size` | |
| `update:` | `auto`, `interval`, `apply` — self-update; migration runs on the new binary's first boot | |
| `dry_run:` | stub every dispatch and verb | |
| `agent_guidance:` | house prompt guidance appended to every agent (per-profile `guidance:` overrides) | [[Agents]] |
| `adopt_open_workspaces:` | route PR feedback to a workspace already on the branch | |

## The trigger grammar in brief

```yaml
triggers:
  - on: <connector>.<event>       # what fires it — one source, a list, or `manual`
    filters: { … }                # whether it fires (event-schema keys, AND-ed)
    group: { key: …, window: 15s }# optional burst batching
    steps: [ … ]                  # agent | command | run: code | uses: verb | use: workflow
    hooks: [ {at: start|done|fail, uses: <conn>.<verb>, options: {…}} ]
    policy: { … }                 # trigger-scoped overrides
```

Steps address the trigger context (`{{.repo}}`, event facts), prior step
outputs (`{{.<id>.<field>}}`), vault reads (`{{ vault "house" "gh" }}` /
`{{.vaults.house.gh}}` — tainted, redacted from logs/audit), and the batch
(`{{.group.*}}`). `if:` conditions use comparison, `&&`/`||`/`!`,
`contains()`, `exists()`, `default()`, and `coalesce()`; templates may also
call `default`/`coalesce` (`{{.sev | default "low"}}`). See [[Workflows]].

### Multiple sources, per-source filters, and manual runs

`on:` takes one event **or a list** — a trigger fans in from several sources
into the same `steps:`. Each list item is a bare `conn.event`, or a one-key
map `conn.event: { … }` whose value is a per-source block scoped to events
from that source. The block takes `filters:`, `policy:`, and `hooks:` —
nothing else (`steps:` stay trigger-level, shared):

```yaml
connectors:
  timer: { type: cron, schedules: { nightly: { cron: "0 2 * * *" } } }

triggers:
  - name: clone-invoice              # names the trigger (required for `conductor run`)
    on:
      - timer.nightly                # a cron schedule — no filter
      - manual                       # `conductor run clone-invoice`
      - gh.issue_matched:              # a per-source block
          filters: { labels_any: [billing] }
          policy:  { reply_to_bots: off }
          hooks:
            - { at: start, uses: gh.react, options: { emoji: eyes } }
    steps:
      - { workflow: clone-latest-invoice,
          with: { contact_id: '{{ .issue.number | default .inputs.contact_id }}' } }
```

- **Per-source `filters:`** validate against **that** source's schema only —
  no lowest-common-denominator restriction across sources. An optional
  top-level `filters:` is a shared base applied to every listed source, so
  each of its keys must be one every source accepts (the intersection); a
  per-source key **overrides** the base for that source.
- **Per-source `policy:`** is the innermost policy scope: per-source →
  trigger → connector → global, most specific wins.
- **Per-source `hooks:`** append after the trigger's shared `hooks:` (shared
  first, per-source second) and fire only for events from that source.
- The trigger fires **once per matching event** from any listed source;
  `steps:` and `group:` are shared configuration (grouping batches per
  source). Sources are heterogeneous — reference a field one source
  publishes and another doesn't defensively: `{{ .issue.author | default "" }}`.
  Step references validate against the union of the listed sources'
  contexts.
- **`manual` is a built-in source** (no connector; the name is reserved). A
  trigger whose `on:` includes it runs on demand through the same
  validation, policy, quiet-hours, and audit as any firing:

  ```sh
  conductor run clone-invoice --input contact_id=abc-123
  conductor run clone-invoice --json '{"contact_id":"abc-123","adjustments":{"Quantity":2}}'
  ```

  CLI values land in the trigger context — under `{{.inputs.*}}` and as
  top-level keys — and flow to workflow `inputs:` via `with:`. `--input k=v`
  entries are strings and overlay `--json`. `manual` accepts no `filters:`.
- **`name:`** is optional for ordinary triggers, **required and unique** for
  any trigger reachable by `conductor run` (a load error otherwise).

## Bot-authored comments (github)

The github comment/review events (`new_comment`, `changes_requested`)
publish `author` and `author_is_bot` in their context — true when the
webhook's actor account type is `Bot` or the login ends in `[bot]`
(dependabot[bot], cursor[bot]). The matching `author_bot` filter gates a
trigger on it: `filters: { author_bot: false }` fires only for humans,
`true` only for bots, absent for either.

`policy.reply_to_bots` (github connector `policy:`, trigger-overridable,
global default allowed) gates the conversational reply BACK to a bot author.
Fixes, thread resolution, and labels always run — only the reply is gated:

| mode | behavior |
|---|---|
| `decline_only` (default) | the agent is instructed to skip thanks/acknowledgements and reply only to state a concrete reason for not applying a suggestion |
| `off` | the flow runner skips `comment`/`reply` verbs on github connectors for that run (logged and audited) |
| `full` | no gating |

## Generic REST & GraphQL connectors

Any HTTP API becomes a connector without new Go code: `type: rest` and
`type: graphql` take their verbs (and, for rest, polled events) from the
config itself. Declared verbs and events flow through the same machinery as
built-in types — `conductor schema <name>` prints them, `validate` checks
step references against the declared `output:` keys, and calls are audited
and rate-limited like any other verb.

### `type: rest`

```yaml
connectors:
  xero:
    type: rest
    base_url: https://api.xero.com/api.xro/2.0
    auth: { … }                        # shared auth block, below
    headers: { Accept: application/json }   # defaults, templated
    verbs:
      list_invoices:
        method: GET
        path: /Invoices                 # templated; joined onto base_url
        query: { where: 'Contact.ContactID==Guid("{{.options.contact}}")' }
        expect: [200]                   # success statuses; default any 2xx
        output: { invoices: "{{.response.body.Invoices}}" }
      create_invoice:
        method: POST
        path: /Invoices
        body: "{{ .options.invoice | json }}"   # json encodes a structured option
        output: { id: "{{ (index .response.body.Invoices 0).InvoiceID }}" }
    events:                             # optional polled sources
      new_invoice:
        poll: 10m                       # default 5m
        request: { method: GET, path: /Invoices, query: { order: "UpdatedDateUTC DESC" } }
        list: "{{.response.body.Invoices}}"   # names the response array
        id: "{{.item.InvoiceID}}"             # dedup key per item
        context: { title: "invoice {{.item.InvoiceNumber}}", total: "{{.item.Total}}" }
```

Verb templates see `{{.options.*}}` (the step's rendered options) and
`{{ vault … }}` reads; `output:` templates add `{{.response.status}}`,
`{{.response.body.*}}` (parsed JSON; non-JSON arrives as `.body.raw`), and
`{{.response.headers.*}}`. An output that is a sole `{{.path}}` reference
keeps the underlying type — an array stays an array for `for_each:`. A
status outside `expect:` fails the verb with the status and body.

Polled events fetch `request:` every `poll:`, extract the `list:` array, and
fire one trigger per item whose rendered `id:` has not been seen (the first
poll seeds silently — no replay storm on boot). Each `context:` field and the
raw `{{.item}}` are published to the trigger scope.

### `type: graphql`

```yaml
connectors:
  shop:
    type: graphql
    endpoint: https://myshop.myshopify.com/admin/api/2025-01/graphql.json
    auth: { type: header, name: X-Shopify-Access-Token, value: '{{ vault "house" "shopify-token" }}' }
    verbs:
      create_order:
        query: |
          mutation($id: ID!, $lines: [OrderLineInput!]!) {
            orderCreate(customerId: $id, lines: $lines) { order { id name } }
          }
        variables: { id: "{{.options.customer}}", lines: "{{.options.lines}}" }
        output: { order_id: "{{.response.data.orderCreate.order.id}}" }
```

One `endpoint:`; each verb is a named query/mutation with templated
`variables:` (type-preserving — a sole `{{.path}}` binds a list/map/number,
not its string form). The request is `POST {query, variables}`. A non-empty
`errors` array in the response **fails the verb even on HTTP 200**;
`output:` templates read `{{.response.data.*}}`.

### The shared `auth:` block

| type | fields | sent as |
|---|---|---|
| `none` (default) | — | — |
| `bearer` | `token` | `Authorization: Bearer …` |
| `basic` | `username`, `password` | HTTP basic auth |
| `header` | `name`, `value` | the named header |
| `oauth2` | `grant`, `token_url`, `client_id`, `client_secret`, `token_vault`, `refresh_token` (seed), `scopes`, `auth_url`, `device_auth_url`, `redirect_uri` | `Authorization: Bearer <fetched>` |

Every credential field takes a literal, `${ENV}` / `env:VAR`, or a vault
reference (`{{ vault "<name>" "<key>" }}` — see [[Secrets]]).

`oauth2` grants: `client_credentials` (machine-to-machine — tokens fetch on
demand, nothing to seed), `refresh_token`, `authorization_code`, and
`device`. Access tokens are cached per connector in memory, refreshed ahead
of expiry and once more on a 401, and never logged.

**`token_vault:`** names the `vaults:` entry conductor stores the captured
tokens in — keys `oauth/<connector>/access_token`, `…/refresh_token`,
`…/expiry`. It must be a writable vault; the interactive grants
(`authorization_code`, `device`) require it. When the provider **rotates the
refresh token on use** (Xero does), the new token is written back there, so
it survives the daemon's own restarts. A `refresh_token:` config value is
only the SEED — used until the vault holds a captured/rotated token.

`conductor connector auth <name>` is the one-time interactive login:
`authorization_code` prints the consent URL (built from `auth_url`, scopes,
and `redirect_uri` — default `http://localhost:8400/callback`), captures the
provider's redirect on that localhost port, and exchanges the code at
`token_url`; `device` requests a user code from `device_auth_url`, prints
where to enter it, and polls `token_url` until approved. Both store the
access + refresh tokens (and expiry) in `token_vault`. Restarts never prompt
— the daemon path only ever uses the vault. `conductor connector auth ls`
shows each connector's login state and access-token expiry;
`auth <name> --revoke` clears the stored tokens.

### Worked example — Xero: clone yesterday's invoice

```yaml
triggers:
  - on: xero.new_invoice
    steps:
      - id: fetch
        uses: xero.list_invoices
        options: { contact: "{{.item.Contact.ContactID}}" }
      - id: clone
        run: js
        code: |
          const src = ctx.fetch.invoices[0];
          return { invoice: { Type: src.Type, Contact: src.Contact,
                              LineItems: src.LineItems, Status: "DRAFT" } };
      - uses: xero.create_invoice
        options: { invoice: "{{.clone.invoice}}" }
```

The list arrives typed from `fetch`, the code step reshapes it, and the
mutation posts it back — three steps, no custom Go.

## Stores (`stores:`) and the data verbs

`stores:` is a named map (like `hosts:`/`agents:`) of data stores in two
families: **KV** types (`boltdb`/`redis`/`http`) served by the `kv.*` verbs,
and **SQL** types (`postgres`/`mysql`/`sqlite`) served by `sql.query` /
`sql.exec`. Every store is explicit — nothing is implicit and there is no
default: a data verb reaches a store only through its required `store:`
selector, and a config with no `stores:` section has no stores. The selector
is family-checked at load: a `kv.*` verb on a SQL store, or a `sql.*` verb
on a KV store, fails `conductor validate` naming the store and its type.

```yaml
stores:
  scratch:   { type: boltdb }                       # file <data dir>/scratch.db
  archive:   { type: boltdb, path: /mnt/big/archive.db }
  cache:     { type: redis,  url: "redis://10.0.0.5:6379/0", password: '{{ vault "house" "redis_pw" }}' }
  shared:    { type: http,   base_url: https://kv.example.com/kv, auth: { type: bearer, token: '{{ vault "house" "kv" }}' } }
  analytics: { type: postgres, url: "postgres://conductor@db/analytics", password: '{{ vault "house" "pg" }}' }
  billing:   { type: mysql,    dsn: "conductor:@tcp(db:3306)/billing", password: '{{ vault "house" "mysql" }}' }
  local:     { type: sqlite }                       # file <data dir>/local.sqlite
```

Every KV backend implements one `KVBackend` interface with identical
semantics; a `stores:` entry that names an unknown type, misses its
connection fields, or (boltdb/sqlite) can't open its file is a **load error
naming the store**. A backend that can't serve an op is capability-checked
rather than silently degrading. Native hosted-KV SDKs (firestore, dynamodb)
are the documented extension point: implement `KVBackend`, add a
store-builder entry.

| type | connection | notes |
|---|---|---|
| `boltdb` | `path?` | one file per store: `path:`, else `<data dir>/<store-name>.db` (beside the state file). Pure Go, ACID, fsync on commit — committed state survives a crash and the daemon's auto-update restart |
| `redis` | `url` (`redis://host:port/db`), `password?` (secret schemes) | native ops (`SET`/`GET`/`SETNX`, `RPUSH`, `LPOP`/`RPOP`, `LRANGE`, `LREM`, `PEXPIRE` for ttl); multi-step read-modify-writes are Lua scripts (merge: `WATCH`/`MULTI`), keeping single-transaction atomicity |
| `http` | `base_url`, `auth?` (the REST connector's block: bearer/basic/header/oauth2) | a generic REST shim — protocol below; **the remote shim owns atomicity for read-modify-write ops** |

### Verbs (`uses: kv.*`)

`store:` is required on every verb and must be a **literal** name of a
defined store — a missing, templated, or undefined `store:` fails
`conductor validate`, not the run.

| verb | options (beyond `store`) | output |
|---|---|---|
| `kv.get` | `key`, `namespace?`, `default?` | `{ value, found }` (found=false → value is `default`, else null) |
| `kv.set` | `key`, `value`, `namespace?`, `ttl?` | `{}` |
| `kv.setnx` | `key`, `value`, `namespace?`, `ttl?` | `{ value, created }` — set only if absent; created=false returns the existing value |
| `kv.merge` | `key`, `value` (object), `namespace?` | `{ value }` — shallow-merge into the object at key (upsert) |
| `kv.delete` | `key`, `namespace?` | `{}` |
| `kv.incr` | `key`, `by?` (default 1), `namespace?` | `{ value }` |
| `kv.append` | `key`, `item \| items`, `unique?`, `namespace?` | `{ value, len }` — append to the list at key (created as `[]`); `unique` skips present values |
| `kv.remove` | `key`, `item \| items`, `namespace?` | `{ value, len }` — remove all occurrences (absent = no-op) |
| `kv.contains` | `key`, `item`, `namespace?` | `{ contains }` (false when absent) |
| `kv.first` / `kv.last` | `key`, `namespace?` | `{ value, found }` |
| `kv.index` | `key`, `index`, `namespace?` | `{ value, found }` — negative counts from the end; out of range → found=false |
| `kv.slice` | `key`, `start?`, `end?` (exclusive), `namespace?` | `{ value, len }` — Python-style, negatives allowed, bounds clamp |
| `kv.len` | `key`, `namespace?` | `{ len }` (0 when absent) |
| `kv.pop` | `key`, `from?` (front\|back, default back), `namespace?` | `{ value, found, len }` — remove and return an end element; empty/absent → found=false, no error |
| `kv.list` | `namespace?`, `prefix?` | `{ keys, entries }` |

- **Namespaces** isolate keyspaces (boltdb buckets / redis key prefixes),
  auto-created on write; `namespace:` defaults to `default`. Values are
  JSON — any serializable value round-trips.
- **Atomicity.** Every read-modify-write verb — `incr`, `setnx`, `merge`,
  `append`, `remove`, `pop` — is one backend transaction, so concurrent and
  grouped steps hitting the same key stay correct (parallel pops each take a
  distinct element). `first`/`last`/`index`/`slice`/`len`/`contains` are
  read-only.
- **TTL.** `ttl:` on `set`/`setnx` expires the key: an expired key reads as
  absent and is skipped by `list` (boltdb sweeps in the background; redis
  expiry is native). Mutating a live entry keeps its expiry.
- **Type errors** are step errors naming the key and the actual type
  (`merge` on a non-object; the list verbs on a non-list).

### Three access paths

1. **Verbs** — `uses: kv.*` in steps and hooks (the table above), audited
   like any verb call.
2. **Templates** — read-only, store first:
   `{{ kv "cache" "runs" (print .pr) | default 0 }}` and
   `{{ kvContains "cache" "pd" "seen" .incident.id }}`. The template surface
   never mutates.
3. **`ctx.store("<name>")` in `run:` code** — the in-process engines resolve
   a defined store to a handle with the full method set
   (`get/set/setnx/merge/delete/incr/append/remove/contains/list/first/
   last/index/slice/len/pop`, namespace first, absent reads null/nil):
   `ctx.store("cache").get(ns, key)` in **js** and **lua**, a top-level
   `store("cache")` builtin in **risor**, and `import "conductor/store"` +
   `store.Use("cache")` returning a typed handle in **go-embed**.
   Host-interpreter steps (`run: sh/node/python/…`) run in a separate
   process — use the `kv.*` verbs from those.

```yaml
- run: js
  code: |
    const kv = ctx.store("cache");
    const key = "last-invoice-" + ctx.inputs.contact_id;
    const prev = kv.get("billing", key);
    kv.set("billing", key, ctx.recent.invoices[0].InvoiceID);
    return { first_time: !prev };
```

### The http store protocol

An `http` store POSTs every operation to `base_url` as one JSON object and
reads the verb's result object back:

```
POST <base_url>
{ "op": "get|set|setnx|merge|delete|incr|append|remove|contains|
         first|last|index|slice|len|pop|list",
  "namespace": "…", "key": "…",
  "value": …,                      set/setnx (any JSON), merge (object)
  "items": […], "item": …,         append/remove; contains
  "unique": bool, "by": int,       append; incr
  "ttl_ms": int,                   set/setnx
  "index": int,                    index
  "start": int, "end": int, "end_set": bool,   slice
  "front": bool,                   pop
  "prefix": "…" }                  list

200 → the verb's result JSON ({value, found}, {value, created},
      {value, len}, {contains}, {len}, {value, found, len}, {keys, entries})
non-2xx → the op fails; the body's {"error": "…"} becomes the message
```

Conductor sends exactly one request per operation and never composes
multi-request transactions — **the shim must apply each read-modify-write op
transactionally on its side** to keep the atomicity contract.

### Worked example — act once per incident id, durably across runs

```yaml
stores:
  state: { type: boltdb }
triggers:
  - name: new-incidents
    on: [ pd.incident ]
    steps:
      - { id: gate, uses: kv.get, options: { store: state, namespace: pagerduty, key: last-seen, default: "" } }
      - if: "{{ .incident.id }} != {{ .gate.value }}"
        uses: slack-ops.post
        options: { channel: "#outages", text: "New incident {{ .incident.id }}" }
      - { uses: kv.set, options: { store: state, namespace: pagerduty, key: last-seen, value: "{{ .incident.id }}" } }
```

### SQL stores and the `sql.*` verbs

SQL store types run statements against your existing schema — conductor does
not create tables or manage migrations. The drivers are pure Go (pgx,
go-sql-driver/mysql, modernc.org/sqlite), keeping the static-binary
invariant.

| type | connection | notes |
|---|---|---|
| `postgres` | `url` (`postgres://user@host/db`), `password?` (secret schemes; overrides the URL's) | pgx; URL validated at load, dialed lazily; placeholders `$1`, `$2`, … |
| `mysql` | `dsn` (`user:pass@tcp(host:3306)/db`), `password?` | DSN validated at load, dialed lazily; placeholders `?` |
| `sqlite` | `path?` | one file per store: `path:`, else `<data dir>/<store-name>.sqlite`; `:memory:` works; opened at load like boltdb; placeholders `?` |

Two verbs; `store:` is required and must name a SQL-type store:

| verb | options | output |
|---|---|---|
| `sql.query` | `store`, `sql`, `args?` | `{ rows, count }` — `rows` is one `{column: value}` object per row |
| `sql.exec` | `store`, `sql`, `args?` | `{ rows_affected, last_insert_id? }` — `last_insert_id` is absent on postgres (use `RETURNING` with `sql.query`) |

**Parameterized, never interpolated.** The `sql:` text is fixed config;
event data goes in `args:`, which bind to the driver's placeholders in
order. A value containing quotes or `'; DROP TABLE …; --` is stored and
returned as that literal string — it is never parsed as SQL. Query results
are JSON-shaped: byte columns decode to strings, timestamps to RFC 3339,
`NULL` to null.

```yaml
stores:
  analytics: { type: postgres, url: "postgres://conductor@db/analytics", password: '{{ vault "house" "pg" }}' }
triggers:
  - name: record-incidents
    on: [ pd.incident ]
    steps:
      - id: record
        uses: sql.exec
        options:
          store: analytics
          sql: "INSERT INTO incidents (id, urgency, title) VALUES ($1, $2, $3)"
          args: [ "{{.incident.id}}", "{{.incident.urgency}}", "{{.title}}" ]
      - id: recent
        uses: sql.query
        options:
          store: analytics
          sql: "SELECT id, title FROM incidents WHERE urgency = $1 ORDER BY created_at DESC LIMIT 5"
          args: [ high ]
      - uses: slack-ops.post
        options: { channel: "#outages", text: "{{.recent.count}} recent high-urgency incidents" }
```

**From `run:` code** — `ctx.sql("<name>")` resolves a defined SQL store to a
handle with `query(sql, args?)` (returns the row list) and `exec(sql,
args?)` (returns `{rows_affected, last_insert_id?}`): `ctx.sql("analytics")`
in **js** and **lua**, a top-level `sql("analytics")` builtin in **risor**,
and `import "conductor/sql"` + `sql.Use("analytics")` in **go-embed**.
Host-interpreter steps use the `sql.*` verbs.

```yaml
- run: js
  code: |
    const db = ctx.sql("analytics");
    db.exec("INSERT INTO events (kind, body) VALUES ($1, $2)", ["comment", ctx.comment.body]);
    const rows = db.query("SELECT COUNT(*) AS n FROM events WHERE kind = $1", ["comment"]);
    return { total: rows[0].n };
```

## Conductor itself (`conductor.*`) — events and verbs

Conductor is a built-in connector (always available; the name is reserved).
Its lifecycle events are a source — alerting is an ordinary trigger, and the
retired `notify:` block auto-migrates onto it (see [[Notifications]] for the
event list, context, and examples):

```yaml
triggers:
  - on: [ conductor.escalate, conductor.needs_input ]      # the "act now" events
    steps: [ { uses: slack-ops.post, options: { text: "conductor {{.message}}" } } ]
```

Events emitted by a conductor-lifecycle trigger's own run are never re-fed
(the loop guard — no notify storms); `escalate`/`needs_input`/`complete`
stay audit-logged regardless, so `status`/`report` work with no trigger
configured.

Conductor also exposes **verbs** on itself, usable in `steps:`/`hooks:`
like any verb — and since hooks nest on steps, work runs before and after
each one:

| verb | options | output |
|---|---|---|
| `conductor.update` | — | `{ updated, version }` — download the latest release, apply, restart into it (the step checkpoints first; the workflow resumes past it on the new process) |
| `conductor.pause` / `conductor.resume` | — | `{}` — the runtime dispatch switch (the pause control file) |
| `conductor.restart` | — | `{}` — restart the daemon |
| `conductor.reload` | — | `{}` — re-read the config (a restart into the same binary; config loads at boot) |
| `conductor.run` | `name`, `inputs?` | `{ message }` — fire a named `on: manual` trigger |
| `gh.sweep` (github connectors) | — | `{ nudged }` — run the catch-up sweep now (`conductor sweep --now`, verb-shaped) |

### Self-update as a workflow

The default stays unattended: `update: { auto: true }` installs and restarts
into each release (`apply: false` stages instead). To gate or wrap it, flip
detection to **emit** rather than self-apply:

```yaml
update: { auto: true, apply: workflow }    # emit conductor.update_available; install nothing

triggers:
  - name: gated-update
    on: conductor.update_available          # context carries {{.version}}
    steps:
      - { uses: app.drain }                                    # before
      - uses: conductor.update                                 # download + apply + restart
        hooks:
          - { at: start, uses: slack-ops.post, options: { text: "updating conductor → {{.version}}" } }
          - { at: fail,  uses: pager.notify,   options: { message: "conductor update failed: {{.error}}" } }
      - { uses: app.smoketest }                                # after (resumes post-restart)
```

`conductor.updated` fires on the first boot of the new release — announce
completed updates by triggering on it.

## Splitting the config across files (`imports:`)

Imports live under each section. A map section — `connectors:`, `runtimes:`,
`hosts:`, `agents:`, `workflows:` — takes an `imports:` key listing files or
globs (relative to the importing file) whose entries join that section,
alongside inline entries. The `triggers:` list takes imports as list items.

One vocabulary: **`imports:`** (plural) is a list of file globs, used by
every section — including the `triggers:` list, as a `- imports: [...]` item.
**`import:`** (singular) is exactly one file, only as a named-entry body or a
workflow step ref.

```yaml
connectors:
  imports: [conf.d/connectors/*.yaml]        # entries from these files join the section
  gh: { type: github, … }                    # inline entries mix in
  pd: { import: ./conf.d/pagerduty.yaml }    # a named entry's BODY from its own file
workflows:
  imports: [workflows/*.yaml]
triggers:
  - imports: [triggers/*.yaml]               # spliced at this position
  - on: gh.review_requested                  # inline triggers mix in
    steps: [ { workflow: review-flow } ]
```

An imported section file holds bare entries (`timer: { type: cron, … }`) or
the section-wrapped form (`connectors: { timer: … }`); an entry-body file
holds the body directly; a trigger file holds a bare list or a `triggers:`
block. **Merge, not last-wins:** a name defined in two files (or a file and
inline) fails the load naming the key and both sources. A glob matching no
files is an error, never a silent no-op. Validation runs over the merged
config, so cross-file `{{…}}`/`workflow:`/`uses:` references are checked at load.

A workflow can also be pulled in per step, without a section import — see
`workflow:`/`import:` in [[Workflows]].

The legacy TOP-level `imports:` (whole-document deep merge: maps merge
recursively, lists concatenate, the importing file's keys win) is unchanged,
and auto-migration still walks it, transforming each legacy file with its own
backup.

## Validation and fleet safety

- `conductor validate` (and boot) resolve every `on:` kind, `filters:` key,
  `uses:` verb, option map, workflow input/output, and `{{…}}`/`if:`
  reference against the connectors' published schemas AND the scope at that
  position. A config that validates cannot reference a value that will not
  exist when the step runs.
- A connector whose credentials or secrets fail to resolve is disabled with
  the reason recorded — never a crash loop; the daemon boots and runs the
  rest.
- Introspection: `conductor connectors ls`, `conductor schema <conn>`,
  `conductor secrets check`; dry-run: `conductor replay <event.json>`.

Related: [[Connectors]] · [[Workflows]] · [[Policy]] · [[Secrets]] · [[Migration]] · [[Commands]]
