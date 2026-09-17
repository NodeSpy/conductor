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
| `connectors:` | named service connections: `use:` (what implements it), credentials, `network:` (declared egress), `me:`, default `repos:`, default `options:`, `enabled:`, per-connector `policy:`, optional `isolation:` | [[Connectors]], [[Plugins]] |
| `triggers:` | the workflows: `on` / `filter` / `steps` / `hooks` (+ `group`, `policy`, `gate`, `name`, `enabled`, `options`, `repo`, `shadow`) | [[Workflows]], [[Grouping]], [[Gates]] |
| `runtimes:` | where agents run: `use:` (what implements it), `agent` (with `use: acp`), `transport`, `bin`, `host`, `isolation`, `default` | [[Runtimes]], [[Isolation]] |
| `plugin_trust:` | where remote plugins may come from: `allow:` source globs. The official plugin repo is trusted by default; anything else remote needs an entry | [[Plugins]] |
| `steps:` | named, reusable STEP TEMPLATES (this replaced `agents:`): `name`, `model`, `runtime`, `thinking`, `mode`, `workspace`, `wait_timeout`, `archive_when_done`, `labels`, `guidance`, `host`, `memory`, `session`, `skill`, `isolation`, `outcome_feedback`, `outcome_key` | [[Steps]], [[Agent-Skill]], [[Isolation]], [[Outcomes]] |
| `models:` | named FLEETS — ranked acceptable-model lists `{ any, required }` a step's `model:` can name | [[Model-Selection]] |
| `hosts:` | named SSH targets: `host`, `user`, `port`, `key`, `known_hosts`, `cwd`, `env`, `isolation` | [[Hosts]], [[Isolation]] |
| `stores:` | named data stores — KV (`boltdb`/`redis`/`http`) served by `kv.*`, SQL (`postgres`/`mysql`/`sqlite`) served by `sql.*`; addressed by the required `store:` selector | below |
| `memory:` | shared agent memory: `store:` (a KV `stores:` entry) \| `dir:` (Markdown files) \| `type: memory` (ephemeral) — served by `memory.*` | [[Memory]] |
| `workflows:` | reusable step lists with `inputs:` / `outputs:` (+ a default `gate:` for their agent steps) | [[Workflows]], [[Gates]] |
| `policy:` | global controls incl. the `budget:` spend cap; also valid on connectors and triggers (most specific wins) | [[Policy]], [[Cost-Accounting]] |
| `vaults:` | named secret stores (`conductor`/`onepassword`/`pass`/`file`/`hashicorp`), read as `{{ vault "<name>" "<key>" }}` with per-vault read/write verbs | [[Secrets]] |
| `checks:` | named quality-gate checks (command / code / verb / critic agent) that `gate: run:` lists reference | [[Gates]] |
| `pricing:` | model→$ overrides for cost estimation (`models:` glob patterns, `default:`) | [[Cost-Accounting]] |
| `imports:` | split the config across files (globs, deep-merged) | below |
| `store:` | `state_file`, `audit_log`, `state_ttl`, `max_tracked_prs`, `audit_max_size`, `history_retention`, `history_max_runs` | [[Runs]] |
| `update:` | `auto`, `interval`, `apply` — self-update; migration runs on the new binary's first boot | |
| `dry_run:` | stub every dispatch and verb | |
| `agent_guidance:` | house prompt guidance appended to every agent (per-profile `guidance:` overrides) | [[Steps]] |
| `adopt_open_workspaces:` | route PR feedback to a workspace already on the branch | |

## The trigger grammar in brief

```yaml
triggers:
  - on: <connector>.<event>       # what fires it — one source, a list, or `manual`
    filter: …                     # whether it fires, and where — the one filter key
    group: { key: …, window: 15s }# optional burst batching
    steps: [ … ]                  # agent | command | use: engine | uses: verb | call: workflow | team: | helper (sleep/log/set/assert/fail/wait_for)
    hooks: [ {at: start|done|fail, uses: <conn>.<verb>, options: {…}} ]
    policy: { … }                 # trigger-scoped overrides
```

Steps address the trigger context (`{{.repo}}`, event facts), prior step
outputs (`{{.<id>.<field>}}`), vault reads (`{{ vault "house" "gh" }}` /
`{{.vaults.house.gh}}` — tainted, redacted from logs/audit), and the batch
(`{{.group.*}}`). `if:` conditions use comparison, `&&`/`||`/`!`,
`contains()`, `exists()`, `default()`, and `coalesce()`; templates may also
call `default`/`coalesce` (`{{.sev | default "low"}}`). See [[Workflows]].

### `filter:` — the one filter key

`filter:` states in one key whether a trigger fires and which repos it applies
to. It is the **only** filter key: the older `filters:` block is gone from the
schema, and a config still carrying it is a load error naming `filter:`. Its
**YAML shape IS its boolean structure**:

| shape | meaning |
|---|---|
| **string** | a condition over the event's facts (the same syntax as a step `if:`) |
| **map** | its keys **AND**-ed together |
| **list** | its entries **OR**-ed together |

```yaml
# string — the full boolean, and the escape hatch
filter: "!is_draft && !contains(title, 'Release')"

# map — AND of structured keys
filter: { not_draft: true, author: [dependabot] }

# list — OR of alternatives; nest freely (a list of maps is an OR of ANDs)
filter:
  - { author: [dependabot], not_draft: true }    # a ready bot PR …
  - { label_any: [urgent, security] }            # … OR anything urgent …
  - "!is_draft && !contains(title, 'Release')"   # … OR the general case
```

A map may carry the reserved key **`expr:`** — a condition string AND-ed with
its siblings — and **any** key may take a `not_` prefix to negate it, so a
denylist needs no separate `exclude:` concept:

```yaml
filter: { not_draft: true, expr: "!contains(title, 'Release')" }
filter: { not_branch: [staging, prod], not_label_any: [wip] }
filter: { not_expr: "contains(title, 'Release')" }
```

`not_<key>` is the same key negated — nothing a connector declares separately —
so `comment_author:` / `not_comment_author:` replace what used to be two
unrelated keys (`from_users` / `ignore_users`). Writing both in one map is
legal and they AND.

**Repo scope lives in the filter too.** `repo:` / `not_repo:` route a github
trigger to repositories (globs; default: the connector's `repos:`). They are
legal on **every** github event, including those that publish no facts, and a
filter that is nothing but `repo:`/`not_repo:` leaves the event's own default
behaviour alone — scoping a trigger is not stating an opinion about the event:

```yaml
filter: { repo: ["org/*"], not_repo: [org/legacy] }        # routing only
filter: { repo: ["org/*"], not_draft: true }               # routing + predicate
```

**Facts are per connector.** github publishes `head_branch`, `base_branch`,
`title`, `labels`, `is_draft`, `author` and, where the event has them,
`merge_state`, `review_decision`, `non_author_approval`, `threads_resolved`,
`sole_assignee`, `comment_author`, `comment_body`, `reviewer`, `author_is_bot`.
Five github events publish predicate facts (`review_requested`,
`changes_requested`, `new_comment`, `issue_matched`, `merge_ready`); the rest
take routing only. `conductor validate` checks every fact and key a `filter:`
names against the event that will evaluate it, so a typo is a load error rather
than a filter that quietly never matches — `conductor schema gh` lists what each
event publishes.

The github match keys: `repo`, `branch`, `base_branch`, `title`, `label_any`,
`label_all`, `require_label`, `author`, `comment_author`, `author_bot`,
`draft`, `sole_assignee`, `merge_state`, `review_decision`,
`non_author_approval`, `threads_resolved` — each also legal as `not_<key>`.
A few former `filters:` keys are NOT predicates over the event and live in
`options:` instead: `ignore_checks` (failing_checks), `reviewer`
(review_requested), `assignee` (issue_matched), `include_prereleases`
(release).

**Why one key.** The retired `filters:` block mixed three inconsistent
combination rules for the same idea: `exclude:` was an OR denylist, `gates:` an
AND of requirements, and each match key baked in its own rule. You could not
say "skip a review only when it is a release PR **and** on a release branch"
without reverse-engineering which block ORed and which ANDed — and
`exclude.title` is a case-insensitive **substring**, so `['Release ']` also
skipped a PR titled "changelog: publish each **release** entry…". With
`filter:` you write the sentence:

```yaml
filter: "!is_draft && !( (head_branch == 'staging' || head_branch == 'prod') && contains(title, 'Release ') )"
```

Prefer `startswith(title, 'Release ')` to the substring `title:` match key when
you mean a prefix.

**Migrating from `filters:`.** `conductor config migrate` rewrites a legacy
config; by hand, the mapping is `repos`→`repo`, `exclude_repos`→`not_repo`,
`exclude: {branches, labels, title}`→`not_branch`/`not_label_any`/`not_title`,
`gates: {not_draft: true}`→`not_draft: true`, `labels_any`→`label_any`,
`labels_all`→`label_all`, `authors`→`author`, `from_users`→`comment_author`,
`ignore_users`→`not_comment_author`, and `ignore_checks`/`reviewer`/`assignee`/
`include_prereleases` into `options:`. A gate that was set to `false` (waived)
is simply a conjunct you do not write.

One thing to know when migrating a `merge_ready` trigger: its five gates
(`not_draft`, `merge_state`, `review_decision`, `non_author_approval`,
`threads_resolved`) are enforced **by default**, and a `filter:` that states any
predicate replaces that default. A routing-only `filter: {repo: […]}` leaves it
in place; anything more, and spell the gates you still want.

### Multiple sources, per-source filter, and manual runs

`on:` takes one event **or a list** — a trigger fans in from several sources
into the same `steps:`. Each list item is a bare `conn.event`, or a one-key
map `conn.event: { … }` whose value is a per-source block scoped to events
from that source. The block takes `filter:`, `policy:`, and `hooks:` — nothing
else (`steps:` stay trigger-level, shared):

```yaml
connectors:
  timer: { use: cron, schedules: { nightly: { cron: "0 2 * * *" } } }

triggers:
  - name: clone-invoice              # names the trigger (required for `conductor run`)
    on:
      - timer.nightly                # a cron schedule — no filter
      - manual                       # `conductor run clone-invoice`
      - gh.issue_matched:              # a per-source block
          filter: { label_any: [billing] }
          policy:  { reply_to_bots: off }
          hooks:
            - { at: start, uses: gh.react, options: { emoji: eyes } }
    steps:
      - { call: clone-latest-invoice,
          with: { contact_id: '{{ .issue.number | default .inputs.contact_id }}' } }
```

- **Per-source `filter:`** validates against **that** source's facts only —
  no lowest-common-denominator restriction across sources. It **replaces** the
  trigger's shared `filter:` outright rather than merging: the shape is the
  boolean structure, so there is nothing coherent to merge key-by-key. An
  optional top-level `filter:` is a shared base applied to every listed source,
  so every key it names must be one every source accepts (the intersection).
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
  entries are strings and overlay `--json`. `manual` accepts no `filter:`.
- **`name:`** is optional for ordinary triggers, **required and unique** for
  any trigger reachable by `conductor run` (a load error otherwise).

### Sharing config across triggers (`extends:` / `abstract:`)

Near-identical triggers (the same `steps:`/`filter:` repeated per repo or org) can share a base.
A trigger `extends: <name>` inherits another trigger's config — `options:` deep-merge,
`filter:`/`steps:`/`hooks:` replace when set, `policy:`/`gate:` fill if unset. A base marked
`abstract: true` never fires and is stripped after resolution, so it needs no `on:`:

```yaml
triggers:
  - name: review-base
    abstract: true
    steps:
      - { id: r, type: agent, name: reviewer, prompt: "Review {{.repo}}#{{.pr}}." }
  - { on: gh.review_requested, extends: review-base, filter: { repo: [org/api] } }
  - { on: gh.review_requested, extends: review-base, filter: { repo: [org/web] } }
```

Full semantics (chains, cycles, the merge rules, and layered guidance) are in [[Reuse]].

## Bot-authored comments (github)

The github comment/review events (`new_comment`, `changes_requested`)
publish `author` and `author_is_bot` in their context — true when the
webhook's actor account type is `Bot` or the login ends in `[bot]`
(dependabot[bot], cursor[bot]). The matching `author_bot` filter gates a
trigger on it: `filter: { author_bot: false }` fires only for humans,
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
    use: rest
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

**Interpolated values are escaped by default** — option values come from
event data (webhook/PR/issue text), so they are treated as data, not
syntax. In `path:` templates every interpolated value is percent-escaped
into a single path segment (no traversal, no spliced `?query`; a rendered
dot-segment is refused outright). In `body:` templates values are
JSON-encoded — a title containing `","role":"admin` stays inside its
string. Two explicit opt-outs: `{{ .x | json }}` emits a full JSON fragment
(as before), and `{{ .x | raw }}` splices verbatim (for non-JSON bodies,
e.g. form-encoded). Literal template text is never touched.

Polled events fetch `request:` every `poll:`, extract the `list:` array, and
fire one trigger per item whose rendered `id:` has not been seen (the first
poll seeds silently — no replay storm on boot). Each `context:` field and the
raw `{{.item}}` are published to the trigger scope.

### `type: graphql`

```yaml
connectors:
  shop:
    use: graphql
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
of expiry and once more on a 401, and never logged. The interactive
code-exchange bootstrap always uses PKCE (S256) and its localhost callback
answers a state-mismatched request with a 400 while continuing to wait for
the real redirect.

**`token_vault:`** names the `vaults:` entry conductor stores the captured
tokens in. Keys are per-connector (`oauth/<connector>/…`), but a vault is a
shared namespace: EVERY connector (and every `<vault>.read` verb) configured
against the same vault can read every other connector's stored tokens — give
each oauth2 connector its own token vault when that blast radius matters.
Keys are `oauth/<connector>/access_token`, `…/refresh_token`,
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

`stores:` is a named map of durable data stores in two families — **KV**
(`boltdb`/`redis`/`http`, served by the `kv.*` verbs) and **SQL**
(`postgres`/`mysql`/`sqlite`, served by `sql.query`/`sql.exec`). Every store is
explicit: a data verb reaches one only through its required `store:` selector,
family-checked at load.

```yaml
stores:
  state:     { type: boltdb }                       # file <data dir>/state.db
  cache:     { type: redis,  url: "redis://10.0.0.5:6379/0" }
  analytics: { type: postgres, url: "postgres://conductor@db/analytics", password: '{{ vault "house" "pg" }}' }
```

Full reference — every backend type, the `kv.*`/`sql.*` verb tables, namespaces,
atomicity, TTL, the http store protocol, `code_access`, and worked examples — is
in **[[Stores]]**. Reaching stores from `run:` code (`ctx.store`/`ctx.sql`) is in
[[Code-Steps]].


## Agent memory (`memory:`)

A durable memory agents share across runs, layered over the same storage
model as the data verbs. Entries carry **provenance** (which agent/run/
trigger/repo wrote them) and a **scope** (`global` / `repo:<owner/repo>` /
`agent:<name>`). One backend is picked explicitly — no default:

```yaml
memory:
  store: state                          # (a) a durable stores: KV entry (redis/http → fleet-shared)
  # or  dir: ~/.config/conductor/memory # (b) one Markdown-with-frontmatter file per memory
  # or  type: memory                    # (c) ephemeral in-process (gone on restart)
```

The always-on `memory.*` verbs (`remember` / `recall` / `forget` / `list`)
work in steps and hooks and are audited like `kv.*`; agents write back via a
`remember:` block in their final output (every runtime) or a live
remember/recall MCP tool (runtimes with live-tool injection — ACP); and an
agent profile opts into prompt injection with `memory: true` (or a
`{ scopes, tags, limit }` filter) — non-opted profiles pay no tokens. Code
steps get `ctx.memory`, templates get `{{ memory "<scope>" <limit> }}`.
Recall is tags + scope + substring + recency. Full model, verb tables, and
the write paths: [[Memory]].

## Session affinity (an agent's `session:`)

A `session:` block — on a **runtime** (the overall pool) or on a **step**
(its own) — is session affinity. Dispatches bind one live session per
`(runtime, model, rendered key)`, so a comment, a check failure, and a
review-change on the same PR all reach the SAME agent as follow-up prompts
with full prior context.

```yaml
x-templates:
  reviewer: &reviewer
    type: agent
    session:
      key: "{{.repo}}#{{.pr}}"
      idle_ttl: 12h
      max_lifetime: 7d
      end_on: [ gh._closed ]     # github's close-or-merge signal
```

Same-key prompts serialize (one in flight; bursts queue), the key→session
map persists in conductor's own state and resumes across restarts/
auto-updates, and sessions evict on idle/age/end_on. Needs a
session-persistent runtime (paseo/ACP); one-shot runtimes stay
fresh-per-event and lean on [[Memory]]. Full behavior and the worked
one-agent-per-PR example: [[Steps]].

## Agent-driven workflows (`workflow.*`, `policy.agent_authored`)

An agent can emit a plan of ordinary steps (a ` ```plan ` block in its final
output, the live `run_step` tool on ACP runtimes, or `workflow.run
{ steps }`), choose an existing workflow from the `workflow.list` catalog
(`workflow.run { name, with, reason }`), and promote a recurring pattern
into a durable saved workflow (`workflow.save` — versioned, provenance-
stamped, unreviewed until `conductor workflows review <name>`). All of it is
governed by `policy.agent_authored` — safe by default (no block, no plans),
allowlist + approve-gated + sandboxed + bounded, enforced structurally
before anything runs. The full model: [[Workflows]]; the guardrails:
[[Policy]].

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
  gh: { use: github, … }                    # inline entries mix in
  pd: { import: ./conf.d/pagerduty.yaml }    # a named entry's BODY from its own file
workflows:
  imports: [workflows/*.yaml]
triggers:
  - imports: [triggers/*.yaml]               # spliced at this position
  - on: gh.review_requested                  # inline triggers mix in
    steps: [ { call: review-flow } ]
```

An imported section file holds bare entries (`timer: { type: cron, … }`) or
the section-wrapped form (`connectors: { timer: … }`); an entry-body file
holds the body directly; a trigger file holds a bare list or a `triggers:`
block. **Merge, not last-wins:** a name defined in two files (or a file and
inline) fails the load naming the key and both sources. An unmatched **glob**
is a no-op — the seeded `conf.d/` folders start empty and fill over time —
but a missing **literal** path is a load error (a typo'd filename must not
vanish silently). `**` globs are refused by name —
`filepath` globs match one directory level, so a `conf.d/**/*.yaml` would
quietly skip nested files; list each level instead. Workflow files may
reference each other (even mutually) — each (file, workflow) pair resolves
once. Validation runs over the merged config, so cross-file
`{{…}}`/`workflow:`/`uses:` references are checked at load.

A workflow can also be pulled in per step, without a section import — see
`workflow:`/`import:` in [[Workflows]].

The legacy TOP-level `imports:` (whole-document deep merge: maps merge
recursively, lists concatenate, the importing file's keys win) is unchanged,
and auto-migration still walks it, transforming each legacy file with its own
backup.

## Validation and fleet safety

- **Unknown keys are load errors.** The whole document (and every imported
  file) decodes strictly: a typo'd key (`known_hostss:`, `filtres:`, a
  misplaced `approve:`) fails the load naming the key and line, instead of
  silently not applying. Type-specific connection/store bodies keep their
  own builder-side validation.
- `conductor validate` (and boot) resolve every `on:` kind, `filter:` key,
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
