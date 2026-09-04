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
| `workflows:` | reusable step lists with `inputs:` / `outputs:` | [[Workflows]] |
| `policy:` | global controls; also valid on connectors and triggers (most specific wins) | [[Policy]] |
| `secrets:` | named secret references, read as `{{.secrets.<name>}}` | [[Secrets]] |
| `notify:` | daemon lifecycle notifications (unchanged from legacy) | [[Notifications]] |
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
outputs (`{{.<id>.<field>}}`), named secrets (`{{.secrets.x}}`), and the
batch (`{{.group.*}}`). `if:` conditions use comparison, `&&`/`||`/`!`,
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
`{{.secrets.*}}`; `output:` templates add `{{.response.status}}`,
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
    auth: { type: header, name: X-Shopify-Access-Token, value: vault:shopify-token }
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
| `oauth2` | `grant`, `token_url`, `client_id`, `client_secret`, `refresh_token`, `scopes`, `auth_url`, `redirect_uri` | `Authorization: Bearer <fetched>` |

Every credential field takes a literal, `${ENV}`, or a secret reference
(`vault:…`, `op://…`, `pass:…`, `file:…`, `env:…`).

`oauth2` grants: `client_credentials` (machine-to-machine — tokens fetch on
demand, nothing to seed), `refresh_token`, and `authorization_code`. Access
tokens are cached per connector in memory, refreshed ahead of expiry and once
more on a 401, and never logged. When the provider **rotates the refresh
token on use** (Xero does), the new token is written back to the `vault:`
reference named by `refresh_token:` — which is why that field must be a
`vault:` ref for rotating providers.

`conductor connector auth <name>` is the one-time interactive seeding for the
authorization-code family: it prints the consent URL (built from `auth_url`,
scopes, and `redirect_uri` — default `http://localhost:8400/callback`),
captures the provider's redirect on that localhost port, exchanges the code
at `token_url`, and stores the refresh token in the vault. Restarts never
prompt — the daemon path only ever uses the vault.

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

## The built-in state store (`kv`)

A durable key/value store is always available as the `kv` connector — no
configuration, no credentials (the name is reserved). It backs cross-run
state: values persist to a single bbolt file (`kv.db`, beside the state file
— default `~/.local/state/conductor/kv.db`; pure Go, ACID). Commits fsync,
so committed state survives a crash and the daemon's own auto-update
restart, and is shared across runs — not just across steps within one run.

### Verbs (`uses: kv.*`)

| verb | options | output |
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

- **Namespaces** are the store's buckets, auto-created on write; `namespace:`
  defaults to `default`. Values are JSON — any serializable value
  (string/number/bool/object/array) round-trips.
- **Atomicity.** Every read-modify-write verb — `incr`, `setnx`, `merge`,
  `append`, `remove`, `pop` — runs inside one bbolt transaction, so
  concurrent and grouped steps hitting the same key stay correct (parallel
  pops each take a distinct element). `first`/`last`/`index`/`slice`/`len`/
  `contains` are read-only.
- **TTL.** `ttl:` on `set`/`setnx` expires the key: an expired key reads as
  absent and is skipped by `list`; a background sweep deletes expired
  entries. Mutating a live entry keeps its expiry.
- **Type errors** are step errors naming the key and the actual type
  (`merge` on a non-object; the list verbs on a non-list).

### Three access paths

1. **Verbs** — `uses: kv.*` in steps and hooks (the table above), audited
   like any verb call.
2. **Templates** — read-only, anywhere a value goes:
   `{{ kv "runs" (print .pr) | default 0 }}` (2-arg: namespace, key) or
   `{{ kv "key" }}` (default namespace), and
   `{{ kvContains "namespace" "key" .item }}` for list membership. The
   template surface never mutates.
3. **`ctx.kv` in `run:` code** — the in-process engines get the full method
   set `get/set/setnx/merge/delete/incr/append/remove/contains/list/first/
   last/index/slice/len/pop` (namespace first, absent reads come back
   null/nil): `ctx.kv.get(ns, key)` in **js** and **lua**, a top-level
   `kv` module in **risor** (`kv.get(ns, key)`), and
   `import "conductor/kv"` in **go-embed** (`kv.Get(ns, key)`,
   Go-typed). Host-interpreter steps (`run: sh/node/python/…`) run in a
   separate process — use the `kv.*` verbs from those.

```yaml
- run: js
  code: |
    const key = "last-invoice-" + ctx.inputs.contact_id;
    const prev = ctx.kv.get("billing", key);
    ctx.kv.set("billing", key, ctx.recent.invoices[0].InvoiceID);
    return { first_time: !prev };
```

### Worked example — act once per incident id, durably across runs

```yaml
triggers:
  - name: new-incidents
    on: [ pd.incident ]
    steps:
      - { id: gate, uses: kv.get, options: { namespace: pagerduty, key: last-seen, default: "" } }
      - if: "{{ .incident.id }} != {{ .gate.value }}"
        uses: slack-ops.post
        options: { channel: "#outages", text: "New incident {{ .incident.id }}" }
      - { uses: kv.set, options: { namespace: pagerduty, key: last-seen, value: "{{ .incident.id }}" } }
```

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
