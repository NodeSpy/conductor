# Stores

`stores:` is a named map (like `hosts:`/`agents:`) of data stores a workflow reads and writes —
durable state that outlives a single run: a "seen this incident id" gate, a rolling counter, a list
of pending items, a row in your analytics DB. Two families:

- **KV** types — `boltdb`, `redis`, `http` — served by the `kv.*` verbs.
- **SQL** types — `postgres`, `mysql`, `sqlite` — served by `sql.query` / `sql.exec`.

Everything is **explicit**: there is no default store, and a data verb reaches a store only through
its required `store:` selector. A config with no `stores:` section has no stores. The selector is
**family-checked at load** — a `kv.*` verb pointed at a SQL store (or a `sql.*` verb at a KV store)
fails `conductor validate`, naming the store and its type.

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

A `stores:` entry that names an unknown type, misses its connection fields, or (boltdb/sqlite) can't
open its file is a **load error naming the store**. Secrets in connection fields use the usual
`{{ vault … }}` / `${ENV}` schemes ([[Secrets]]).

---

## KV stores

### Types

| type | connection | notes |
|---|---|---|
| `boltdb` | `path?` | one file per store: `path:`, else `<data dir>/<store-name>.db` (beside the state file). Pure Go, ACID, fsync on commit — committed state survives a crash and the daemon's auto-update restart. The zero-config default for local durable state |
| `redis` | `url` (`redis://host:port/db`), `password?` | native ops (`SET`/`GET`/`SETNX`, `RPUSH`, `LPOP`/`RPOP`, `LRANGE`, `LREM`, `PEXPIRE` for ttl); multi-step read-modify-writes run as Lua scripts (`WATCH`/`MULTI`) to keep single-transaction atomicity. Use when several conductors (or other apps) share state |
| `http` | `base_url`, `auth?` (bearer/basic/header/oauth2) | a generic REST shim — [protocol below](#the-http-store-protocol); **the remote shim owns atomicity** for read-modify-write ops |

Every KV backend implements one `KVBackend` interface with identical semantics; a backend that can't
serve an op is capability-checked rather than silently degrading. Native hosted-KV SDKs (firestore,
dynamodb) are the documented extension point: implement `KVBackend`, add a store-builder entry.

### Verbs (`uses: kv.*`)

`store:` is required on every verb and must be a **literal** name of a defined store — a missing,
templated, or undefined `store:` fails `conductor validate`, not the run.

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

- **Namespaces** isolate keyspaces (boltdb buckets / redis key prefixes), auto-created on write;
  `namespace:` defaults to `default`. Values are JSON — any serializable value round-trips.
- **Atomicity.** Every read-modify-write verb (`incr`, `setnx`, `merge`, `append`, `remove`, `pop`)
  is one backend transaction, so concurrent and grouped steps hitting the same key stay correct
  (parallel pops each take a distinct element). `get`/`first`/`last`/`index`/`slice`/`len`/`contains`
  are read-only.
- **TTL.** `ttl:` on `set`/`setnx` expires the key: an expired key reads as absent and is skipped by
  `list` (boltdb sweeps in the background; redis expiry is native). Mutating a live entry keeps its
  expiry.
- **Type errors** are step errors naming the key and the actual type (`merge` on a non-object; the
  list verbs on a non-list).

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

### The http store protocol

An `http` store POSTs every operation to `base_url` as one JSON object and reads the verb's result
object back:

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

200 → the verb's result JSON ({value, found}, {value, created}, {value, len},
      {contains}, {len}, {value, found, len}, {keys, entries})
non-2xx → the op fails; the body's {"error": "…"} becomes the message
```

Conductor sends exactly one request per operation and never composes multi-request transactions —
**the shim must apply each read-modify-write op transactionally on its side** to keep the atomicity
contract.

---

## SQL stores

SQL store types run statements against **your existing schema** — conductor does not create tables or
manage migrations. The drivers are pure Go (pgx, go-sql-driver/mysql, modernc.org/sqlite), keeping
the static-binary invariant.

### Types

| type | connection | notes |
|---|---|---|
| `postgres` | `url` (`postgres://user@host/db`), `password?` (overrides the URL's) | pgx; URL validated at load, dialed lazily; placeholders `$1`, `$2`, … |
| `mysql` | `dsn` (`user:pass@tcp(host:3306)/db`), `password?` | DSN validated at load, dialed lazily; placeholders `?` |
| `sqlite` | `path?` | one file per store: `path:`, else `<data dir>/<store-name>.sqlite`; `:memory:` works; opened at load like boltdb; placeholders `?` |

SQL stores also take `code_access: none | read | write` — what in-process [[Code-Steps|code steps]]
may do through `ctx.sql`: `read` (query-only, the default), `write` opts a store into exec from code,
`none` cuts code steps off. The `sql.*` **verbs** are config-authored and not gated. sqlite stores
refuse `ATTACH`/`DETACH`/`PRAGMA`/`VACUUM` for every caller (they reach the host filesystem/engine,
not your schema).

### Verbs (`uses: sql.*`)

`store:` is required and must name a SQL-type store.

| verb | options | output |
|---|---|---|
| `sql.query` | `store`, `sql`, `args?` | `{ rows, count }` — `rows` is one `{column: value}` object per row |
| `sql.exec` | `store`, `sql`, `args?` | `{ rows_affected, last_insert_id? }` — `last_insert_id` is absent on postgres (use `RETURNING` with `sql.query`) |

**Parameterized, never interpolated.** The `sql:` text is fixed config; event data goes in `args:`,
which bind to the driver's placeholders in order. A value containing quotes or `'; DROP TABLE …; --`
is stored and returned as that literal string — never parsed as SQL. Results are JSON-shaped: byte
columns decode to strings, timestamps to RFC 3339, `NULL` to null.

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

---

## Using stores from code and templates

Three access paths reach the same stores — see [[Code-Steps]] for the full `ctx` surface:

1. **Verbs** — `uses: kv.*` / `sql.*` in steps and hooks (tables above), audited like any verb call.
2. **Templates** (read-only, store first): `{{ kv "cache" "runs" (print .pr) | default 0 }}` and
   `{{ kvContains "cache" "pd" "seen" .incident.id }}`. The template surface never mutates.
3. **`ctx.store("<name>")` / `ctx.sql("<name>")` in `run:` code** — the in-process engines (js, lua,
   risor, go-embed) resolve a defined store to a handle with the full method set. Host-interpreter
   steps (`run: sh/node/python/…`) run in a separate process — they use the `kv.*`/`sql.*` verbs.

```yaml
- run: js
  code: |
    const kv = ctx.store("cache");
    const key = "last-invoice-" + ctx.inputs.contact_id;
    const prev = kv.get("billing", key);          // (namespace, key) — absent reads null
    kv.set("billing", key, ctx.recent.invoices[0].InvoiceID);
    return { first_time: !prev };
```

## See also

- [[Code-Steps]] — the `ctx` surface (`ctx.store`, `ctx.sql`, `ctx.memory`) in each engine
- [[Verbs]] — how `uses:` verbs and their options work
- [[Memory]] — the agent-facing durable store (`memory.*`), distinct from these data stores
- [[Secrets]] — `{{ vault … }}` for store connection credentials
- [[Configuration]] — the full trigger/step grammar
