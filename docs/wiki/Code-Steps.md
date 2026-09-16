# Code steps

Inline code is the glue between agent outputs and service verbs — reshape
JSON, compute a condition, run a build — without a whole agent. A code step
is **`use: <engine>`** plus the body that engine takes: `code:` for the
script engines, `command:` for `cli`.

```yaml
steps:
  - { id: shape,  use: js,       code: "return { sev: ctx.body.detail.severity }" }
  - { id: calc,   use: go-embed, code: "…" }                          # yaegi, install-free
  - { id: score,  use: risor,    code: '{"sev": ctx["level"]}' }      # Risor, install-free
  - { id: pick,   use: lua,      code: "return { sev = ctx.level }" } # Lua 5.1, install-free
  - { id: build,  use: cli,      command: [make, -C, ./svc, release] }
  - { id: heavy,  use: go,       code: "…" }                          # host go run
  - { id: deploy, use: sh,   host: build-box, code: "make deploy" }   # sh on build01
  - { id: enrich, use: ruby, host: build-box, code: "…" }             # build01's ruby
```

`use:` resolves exactly like a connector's or a runtime's: a **builtin**
first, then the official plugin repo's `engines/<name>`, then an explicit
`owner/repo`, host, or local path ([[Plugins]]). A **host interpreter**
(`bash`, `python3`, `/opt/venv/bin/python`) is not a plugin — it is a
program on the box, and `use:` takes it by name or by path.

> **`run:` is the same key.** Every engine below can be written `run: js`
> just as before; `run:` and `use:` select one engine and every existing
> config keeps working. The one difference is strictness: `run:` treats any
> name it does not recognize as a host interpreter, while `use:` will not
> guess — an unknown name is an error that names the builtins. Set one or
> the other, never both.
>
> **`use:` is not a workflow call.** That is **`call:`** ([[Workflows]]).
> A step-level `use:` used to mean the call; `conductor config migrate`
> rewrites those to `call:`, and the loader says so if one slips through.

## Engines

**Baked-in, sandboxed (zero-install, local-only):**

- `use: js` — QuickJS compiled to WASM, executed in wazero (pure Go, no CGo).
  A true WASM sandbox, identical on every OS. The code body is a function
  body: `return` its result.
- `use: go-embed` — yaegi, a Go interpreter written in Go, in-process. No
  toolchain needed; sandboxed by a stdlib import allowlist (strings, strconv,
  fmt, encoding/json, time, math, regexp, sort, …; no os, os/exec, net,
  syscall, unsafe). The code must define
  `func run(ctx map[string]any) (any, error)` (or `… any`).
- `use: risor` — [Risor](https://github.com/risor-io/risor), a Go-flavored
  scripting language interpreted in pure Go. The script's final expression is
  its result. Sandboxed by an explicit global allowlist: the core builtins
  plus strings, strconv, math, json, regexp, time, base64, bytes, and errors —
  no os, exec, net, or filesystem modules.
- `use: lua` — Lua 5.1 on gopher-lua, a Lua VM in pure Go. The script
  `return`s its result (a table with string keys becomes the step's outputs).
  Only the base, table, string, and math libraries are opened — no os, io,
  debug, or package — and the file/chunk loaders (`dofile`, `loadfile`,
  `load`, `loadstring`) are removed.

**Baked-in, subprocess:**

- `use: cli` — run the step's own **`command:`** argv as a subprocess. See
  [`cli`](#the-cli-engine) below.

**Host interpreters (bring your own):**

- `use: go` — the host `go run`: full fidelity (generics, cgo, third-party
  modules). The code is a complete program reading the ctx JSON on stdin and
  printing its result JSON on stdout. `go` resolves via PATH; a clear error
  names the `go-embed` fallback when absent.
- `use: ruby | node | python3 | php | perl | sh | bash | /usr/bin/…` —
  resolved by name on PATH or by explicit path. conductor writes `code:` to a
  private temp file and invokes it (`args:` appends extra argv); the ctx JSON
  arrives on stdin. `sh` is the portable default — never assume bash.

## The `cli` engine

`use: cli` runs an argv you wrote, and bridges it onto the same contract
every other engine honors: **ctx as JSON on stdin, outputs from stdout.**

```yaml
steps:
  - { id: build,   use: cli, command: [make, -C, ./svc, release] }
  - { id: oneline, use: cli, command: "gh pr list --json number" }   # split on spaces
  - { id: shape,   use: cli, command: [python3], code: "…" }         # ≡ run: python3
  - { id: remote,  use: cli, command: [make, deploy], host: build-box }
```

- **`command:`** is the argv — a list of words, or one string split on
  whitespace with single/double quotes honored. There is **no shell**: no
  `$VAR` expansion, no globbing, no `;` chaining. Write `command: [sh, -c,
  "…"]` when a shell is what you want.
- **`code:`**, when set, is written to a private temp file and that **path is
  appended to the argv** — the same thing a host-interpreter step does. So
  `use: cli, command: [bash]` + `code:` is exactly `run: bash`, and
  `command: [python3]` + `code:` is exactly `run: python3`. (An inline
  `-c`-style script is the argv form above, with no `code:` at all.)
- **`args:`** is appended last, after the code file.
- **`host:`/`ssh:`** work exactly as they do for a host interpreter: the argv
  is shell-quoted into a generated remote script, `code:` travels base64-
  framed, ctx goes over stdin, and a missing program is a distinct error.
- A cli step runs in a **separate process**, so it has no `ctx.store`/
  `ctx.sql`/`ctx.memory` handles — use the `kv.*`/`sql.*`/`memory.*` verbs in
  surrounding steps, as with any host interpreter.

`type: command` is the older, agent-dispatch-flavored way to run a program
and still works unchanged. `use: cli, command: […]` is the code-step
equivalent: it goes through the code path, so it gets ctx on stdin and its
stdout becomes structured outputs.

## What's in `ctx`

Everything a code step gets arrives as `ctx` — a global in js, risor, and lua; the `run(ctx)`
argument in go-embed; JSON on stdin for `cli` and host interpreters. It carries the same scope your templates
see, **plus** live handles to stores and memory in the in-process engines.

### Data (read-only)

| in `ctx` | what |
|---|---|
| `ctx.inputs` | the workflow/manual-run inputs (`--input`, `with:`) — a map |
| `ctx.<stepId>` | a prior step's outputs, e.g. `ctx.diff.text`, `ctx.assess.decision` |
| trigger fields | `ctx.repo`, `ctx.owner`, `ctx.name`, `ctx.pr`, `ctx.number`, `ctx.head`, `ctx.base`, `ctx.kind`, `ctx.title`, `ctx.url` (+ every connector-specific context key, e.g. `ctx.comment`, `ctx.incident`) |
| `ctx.item` | the current element inside a `for_each` step |
| `ctx.group` | the batched events when the trigger has a `group:` window |

Named `secrets`/vault values are **NOT** in `ctx` — pass one explicitly via a step's `env:` or
`args:` template when code genuinely needs it (see [[Secrets]]).

### Bindings (in-process engines only — js, go-embed, risor, lua)

| binding | shape | notes |
|---|---|---|
| `ctx.store("<name>")` | a KV handle: `get, set, setnx, merge, delete, incr, append, remove, contains, list, first, last, index, slice, len, pop` — **namespace first**, e.g. `.get(ns, key)`, `.set(ns, key, value)` | semantics mirror the `kv.*` verbs ([[Stores]]); absent reads return null/nil |
| `ctx.sql("<name>")` | a SQL handle: `query(sql, args?)` → row list, `exec(sql, args?)` → `{rows_affected, last_insert_id?}` | **query-only by default**; a store needs `code_access: write` to `exec` from code (see below) |
| `ctx.memory` | `remember(text, {tags, scope}?)`, `recall(query?, filter?)`, `forget(id)`, `list(filter?)` | over the configured [[Memory]]; relative scopes need explicit `repo:<owner/repo>` / `agent:<name>` forms in code |

The spelling differs by engine but the surface is identical:

| engine | store | sql | memory |
|---|---|---|---|
| **js**, **lua** | `ctx.store("x")` | `ctx.sql("x")` | `ctx.memory` |
| **risor** | `store("x")` (builtin) | `sql("x")` | `memory` |
| **go-embed** | `import "conductor/store"` → `store.Use("x")` | `import "conductor/sql"` → `sql.Use("x")` | `import "conductor/memory"` |

**`cli` and host interpreters** (`use: cli`, `use: sh/node/python3/…`) run in a separate process —
they have no `ctx` handles; use the `kv.*` / `sql.*` / `memory.*` **verbs** in surrounding steps
instead.

```yaml
- use: js
  code: |
    const kv = ctx.store("state");
    const seen = kv.contains("pd", "incidents", ctx.incident.id);   // (ns, key, item)
    if (!seen) kv.append("pd", "incidents", ctx.incident.id);
    const n = ctx.sql("analytics").query(
      "SELECT count(*) AS c FROM incidents WHERE day = $1", [ctx.inputs.day]);
    return { first_time: !seen, total: n[0].c };
```

### Outputs

The return value / stdout becomes the step's outputs: a JSON **object** as-is (referenced as
`{{.step.field}}` / `ctx.step.field` downstream), any other JSON under `value:`, plain text under
`text:`.

## Where it runs

A code step runs where conductor runs. `host: <name>` (a [[Hosts]] entry) or
an inline `ssh: {…}` runs a **`cli` or host-interpreter** step on that box —
the code travels as a base64 frame, the ctx JSON on
stdin, and a missing program is a distinct clear error. The in-process
engines (`js`, `go-embed`, `risor`, `lua`) execute inside conductor's own
process and are **local-only**; `conductor validate` rejects `host:` on them
and names the alternatives (run `node` there, or run a conductor on that
box).

### `ctx.sql` capabilities

Code steps are **query-only** against SQL stores by default. A store opts
into writes with `code_access: write` on its `stores:` entry; `code_access:
none` cuts code steps off entirely. The `sql.query`/`sql.exec` workflow
verbs (config-authored, parameterized) are not gated. Independently, sqlite
stores refuse `ATTACH`/`DETACH`/`PRAGMA`/`VACUUM` for every caller — those
statements reach the host filesystem/engine, not your schema.

## Timeouts

A step's `timeout:` binds actual execution in every engine: QuickJS halts
the WASM module at the deadline (and its heap is capped at 256MB), yaegi
and Lua interrupt their interpreter loops, risor honors the context
natively, and `cli`/host interpreters are killed with the subprocess. A
`while(1)` costs you the step, not the daemon.

## Trust boundary

WASM (`js`) is memory-isolated. yaegi (`go-embed`), Risor, and Lua are
in-process behind their allowlists — appropriate for operator-authored
config, not untrusted input. go-embed's interpreter never resolves source
imports from the host GOPATH (only the registered stdlib subset exists).
`cli` and host interpreters have full host power (that is their point) — but they
inherit an allowlisted base environment (PATH/HOME/locale/GO*) plus the
step's own `env:`, never the daemon's full environment; pass an ambient
variable explicitly if a step needs it.

Related: [[Hosts]] · [[Workflows]] · [[Connectors]]
