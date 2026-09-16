# Code steps

Inline code is the glue between agent outputs and service verbs — reshape
JSON, compute a condition, run a build — without a whole agent. A code step
is **`use: <engine>`** plus the body that engine takes: `code:` for the
script engines, `command:` for `cli`.

```yaml
steps:
  - { id: build,  use: cli,      command: [make, -C, ./svc, release] } # the builtin
  - { id: shape,  use: js,       code: "return { sev: ctx.body.detail.severity }" }
  - { id: calc,   use: go-embed, code: "…" }                          # engine plugin
  - { id: score,  use: risor,    code: '{"sev": ctx["level"]}' }      # engine plugin
  - { id: pick,   use: lua,      code: "return { sev = ctx.level }" } # engine plugin
  - { id: heavy,  use: go,       code: "…" }                          # host go run
  - { id: deploy, use: sh,   host: build-box, code: "make deploy" }   # sh on build01
  - { id: enrich, use: ruby, host: build-box, code: "…" }             # build01's ruby
```

> **The scripting engines are plugins.** `js`, `lua`, `risor` and `go-embed`
> used to be compiled into conductor. They now live in
> [conductor-plugins](https://github.com/NodeSpy/conductor-plugins) under
> `engines/<name>` and are **fetched on `conductor init`**, like any other
> official component. Every existing `use: js` / `run: js` config keeps
> working and keeps meaning the js engine — but the daemon will not start a
> config that names one until it is installed, and it tells you to run
> `conductor init`. Only **`cli`** is built in.

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

**Built in (the only one):**

- `use: cli` — run the step's own **`command:`** argv as a subprocess. See
  [`cli`](#the-cli-engine) below. Nothing to install, works local or over
  `host:`.

**Official engine plugins (fetched on `conductor init`, out-of-process):**

Each is a verified, sandboxed subprocess conductor spawns and drives over the
plugin wire — see [Engine plugins](#engine-plugins). They need no interpreter
on the box (the engine binary carries its own), which is what makes them the
drop-in for the old in-binary engines.

- `use: js` — QuickJS compiled to WASM, executed in wazero (pure Go, no CGo).
  A true WASM sandbox, identical on every OS. The code body is a function
  body: `return` its result.
- `use: go-embed` — yaegi, a Go interpreter written in Go. No toolchain
  needed; sandboxed by a stdlib import allowlist (strings, strconv, fmt,
  encoding/json, time, math, regexp, sort, …; no os, os/exec, net, syscall,
  unsafe). The code must define `func run(ctx map[string]any) (any, error)`
  (or `… any`).
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

**Third-party engine plugins:**

- `use: <any other name>` / `use: owner/repo/engine` / `use: ./path/to/binary`
  — the same mechanism, from somewhere other than the official repo.

**Host interpreters (bring your own):**

- `use: go` — the host `go run`: full fidelity (generics, cgo, third-party
  modules). The code is a complete program reading the ctx JSON on stdin and
  printing its result JSON on stdout. `go` resolves via PATH; a clear error
  says so when the toolchain is absent.
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
- A **local** cli step reaches `ctx.store`/`ctx.sql`/`ctx.memory` over a
  per-run socket — see [the ctx data plane](#the-ctx-data-plane-cli) below.
  A **remote** (`host:`) one does not.

`type: command` is the older, agent-dispatch-flavored way to run a program
and still works unchanged. `use: cli, command: […]` is the code-step
equivalent: it goes through the code path, so it gets ctx on stdin and its
stdout becomes structured outputs.

## What's in `ctx`

Everything a code step gets arrives as `ctx` — a global in js, risor, and lua; the `run(ctx)`
argument in go-embed; JSON on stdin for `cli` and host interpreters. It carries the same scope your templates
see, **plus** access to stores and memory in every engine that has a data plane.

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

### Bindings (engine plugins — js, go-embed, risor, lua — and `cli`)

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

Every engine here is a **subprocess**, so none of them holds a store handle:
each op is a request back to conductor, which authorizes it. The engines
present it as a binding; the mechanism underneath is the same one `cli` uses
([the ctx data plane](#the-ctx-data-plane-cli)), and so are the guards.

**Host interpreters** (`use: sh/node/python3/…`) get no `ctx` faces at all —
conductor hands them a script and a stdin document, with no channel back. Use
the `kv.*` / `sql.*` / `memory.*` **verbs** in surrounding steps instead, or
`use: cli`, which does have the channel.

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

### The ctx data plane (`cli`)

A subprocess cannot hold a Go binding, so a **local `use: cli`** step gets the
same three faces over a **per-run unix socket**: it *asks* conductor to
perform each op, and conductor decides. The step never receives a store
handle, a connection string, or a credential — **every op is authorized
host-side**, through the identical guards an engine plugin's `ctx.store(…)`
call goes through (the store/scope allowlist for agent-authored steps, the
`no_secret_egress` write barrier, `code_access:` on SQL stores, reserved
memory buckets).

It is **opt-in**: a command that ignores the environment below is the
inputs-and-outputs step it always was.

#### The helper

conductor's own binary is the reference client, exported to the command as
`$CONDUCTOR_CTX_HELPER`:

```yaml
- id: bump
  use: cli
  command: [bash]
  code: |
    seen=$("$CONDUCTOR_CTX_HELPER" ctx kv state contains pd incidents "$(jq -r .incident.id)")
    n=$("$CONDUCTOR_CTX_HELPER" ctx kv state incr pd attempts 1)
    "$CONDUCTOR_CTX_HELPER" ctx sql analytics query 'SELECT count(*) AS c FROM incidents WHERE day = ?' '[3]'
    "$CONDUCTOR_CTX_HELPER" ctx memory remember "retried twice" '["ci"]' 'repo:acme/api'
    printf '{"seen": %s, "attempts": %s}' "$seen" "$n"
```

```
conductor ctx kv     <store> <op> [arg...]     # ns/key positional, as ctx.store(…)
conductor ctx sql    <store> <op> <sql> [args] # args is a JSON list of bind values
conductor ctx memory <op> [arg...]             # remember | recall | forget | list
```

Each argument is read as **JSON when it parses** and as a plain string when it
does not, so `3` is a number, `'{"a":1}'` an object, and `hello` the string.
The result value is printed as **JSON on stdout** — capture it with `$(…)`;
feeding it straight back to `set` round-trips the value with its type intact.
Exit codes: **0** ok · **1** error · **2** usage · **3** refused by policy, so
a step can branch on "conductor will not let me do this" without parsing text.

#### The wire protocol

Nothing about the helper is privileged — a step that would rather speak the
protocol itself (Python's `json` + `socket`, Go's `net.Dial`) gets exactly the
same treatment. The socket is **JSON Lines** in both directions: one request
object per line, one response per line, in order; a connection may carry many.

```
CONDUCTOR_CTX_SOCK     the unix socket path
CONDUCTOR_CTX_TOKEN    a 256-bit random token, required on EVERY request
CONDUCTOR_CTX_HELPER   conductor's binary — the client above
```

```jsonc
--> {"token":"…","kind":"kv","op":"set","resource":"cache","args":["ns","k",{"v":1}]}
<-- {"ok":true}
--> {"token":"…","kind":"kv","op":"get","resource":"cache","args":["ns","k"]}
<-- {"ok":true,"value":{"v":1}}
--> {"token":"…","kind":"kv","op":"set","resource":"secrets-parking","args":["ns","k","…"]}
<-- {"ok":false,"refused":true,"error":"no_secret_egress: refusing to write secret material…"}
```

| field | meaning |
|---|---|
| `token` | the per-run token; a wrong or absent one is refused before any guard or store is consulted |
| `kind` | `kv` · `sql` · `memory` |
| `op` | the operation, spelled as in the tables above |
| `resource` | the **defined** store (`kv`/`sql`); omitted for `memory`, which has no store dimension |
| `args` | positional, exactly as an engine's `ctx.store(ns, key, …)` call takes them |
| `ok` / `value` | success and its JSON result (an absent read is `null`, not an error) |
| `error` / `refused` | the failure; `refused` marks a **policy** denial rather than a malfunction |

**Auth and isolation.** The socket lives in a fresh `0700` directory with a
random name, and the token is minted **per run**. Another user on the box
cannot reach the socket; another *run* has a different socket and a different
token, so one step's capability can never name another's data plane. There is
no daemon-wide credential. The socket, its directory and the token die with
the step — on success, on failure, on timeout and on cancellation alike — so a
backgrounded grandchild that kept the environment finds a path that no longer
exists.

**Local only.** A `host:`-remote cli step gets **no** data plane: the socket is
on the daemon's box and the callback cannot cross the ssh hop. The variables
are simply absent there (the step keeps its inputs and outputs), and the
helper fails with *"no ctx data plane in this environment"* rather than
silently reading a different store. A remote step that needs durable state
uses the `kv.*` / `sql.*` / `memory.*` verbs in surrounding steps.

### The ctx data plane (engine plugins)

An engine plugin is also a subprocess, and gets the same three faces the same
way — by asking. The only thing that differs is the pipe: instead of a unix
socket it uses the **plugin wire it is already on**, as `host.kv` / `host.sql` /
`host.memory` requests issued back at the daemon *during* its `plugin.run`.

```jsonc
--> {"id":1,"method":"plugin.run","params":{"run_id":"9f3c…","code":"…","inputs":{…}}}
<-- {"id":"h1","method":"host.kv","params":{"run_id":"9f3c…","kind":"kv","op":"get",
                                            "resource":"cache","args":["run","attempts"]}}
--> {"id":"h1","result":{"ok":true,"value":3}}
<-- {"id":1,"result":{"outputs":{"attempts":3}}}
```

`run_id` is the plugin wire's `CONDUCTOR_CTX_TOKEN`: 32 random bytes minted per
run, valid only while that run is in flight, checked in constant time, revoked
the instant the step ends. The request and response shapes are the socket's
`{kind, op, resource, args}` / `{ok, value, error, refused}` field for field,
and they reach the **same handler with the same guard** — a plugin engine's
reach into kv/sql/memory is, op for op, the reach a `use: js` step has.

An engine author writes ops, not JSON-RPC:

```go
v, err := host.KV().Get(ctx, "cache", "run", "attempts")
if plugin.IsRefused(err) { /* conductor's policy said no */ }
```

See [Plugins → Authoring an engine plugin](Plugins#authoring-an-engine-plugin).

### Engine plugins

A step's `use:` that names neither a builtin nor a program on the box is an
**engine plugin** — the same `use:` grammar connectors and runtimes use,
resolved into `engines/<name>` of the plugin repo (or an explicit
`owner/repo/engine`):

```yaml
steps:
  - id: build
    use: wasmtime            # engines/wasmtime from the official plugin repo
    code: |
      (module (func (export "run") …))
    args: [--opt, "2"]
    env:
      TARGET: wasm32-wasi
```

What the engine receives is the code step, whole: `code`, `args`, `env`, and the
rendered ctx document as `inputs`. What it returns becomes the step's outputs.
Beyond that, the engine defines its own contract — conductor does not require a
`code:` body for one, because an engine driven entirely by `args:`/`env:` is a
legitimate engine and the loader cannot tell which kind it is looking at.

Notes:

- **Local-only.** An engine plugin is a subprocess of *this* daemon holding a
  transport back to it, so `host:` is refused rather than silently run locally —
  its ctx callbacks could not cross the ssh hop. Use `use: cli` or a host
  interpreter for remote code.
- **Installed before it runs.** The reference must be in your config so
  `conductor init` fetches and records it; a step naming an engine that is not
  loaded fails with that, not with a PATH lookup.
- **Agent-authored plans may only use engines your config already references.**
  Otherwise a plan could name any engine in the plugin repo and have conductor
  fetch and execute it — a supply-chain decision that belongs to a human.
- **Deny-by-default egress.** An engine that declares no `egress` in its
  capabilities gets *none*.

### Outputs

The return value / stdout becomes the step's outputs: a JSON **object** as-is (referenced as
`{{.step.field}}` / `ctx.step.field` downstream), any other JSON under `value:`, plain text under
`text:`.

## Where it runs

A code step runs where conductor runs. `host: <name>` (a [[Hosts]] entry) or
an inline `ssh: {…}` runs a **`cli` or host-interpreter** step on that box —
the code travels as a base64 frame, the ctx JSON on stdin, and a missing
program is a distinct clear error. An **engine plugin** (`js`, `go-embed`,
`risor`, `lua`, or a third-party one) is a subprocess of *this* daemon
holding a channel back to it, so it is **local-only**; `conductor validate`
rejects `host:` on one and names the alternatives (run `node` there, or run a
conductor on that box).

### `ctx.sql` capabilities

Code steps are **query-only** against SQL stores by default. A store opts
into writes with `code_access: write` on its `stores:` entry; `code_access:
none` cuts code steps off entirely. The `sql.query`/`sql.exec` workflow
verbs (config-authored, parameterized) are not gated. Independently, sqlite
stores refuse `ATTACH`/`DETACH`/`PRAGMA`/`VACUUM` for every caller — those
statements reach the host filesystem/engine, not your schema.

## Timeouts

A step's `timeout:` binds actual execution in every engine: `cli` and host
interpreters are killed with the subprocess, and an engine plugin is a
subprocess too — conductor cancels the run and tears the process down. A
`while(1)` costs you the step, not the daemon.

## Trust boundary

Nothing conductor runs for a code step shares the daemon's process any more:
`cli`, host interpreters, and engine plugins are all subprocesses, so a
runaway or a crash costs the step rather than the daemon. Each engine keeps
its own internal sandbox on top of that (js is memory-isolated WASM; go-embed,
Risor and Lua run behind import/global allowlists) — appropriate for
operator-authored config, not untrusted input.
`cli` and host interpreters have full host power (that is their point) — but they
inherit an allowlisted base environment (PATH/HOME/locale/GO*) plus the
step's own `env:`, never the daemon's full environment; pass an ambient
variable explicitly if a step needs it. A cli step's [ctx data
plane](#the-ctx-data-plane-cli) does not widen that: it hands over no store
handle, only the ability to ask, and conductor applies the same guards it
applies to an engine plugin's callbacks. The socket address and token are appended
**after** the step's `env:`, so a step cannot point its own data plane
somewhere else.

An **engine plugin** is third-party code the daemon executes, so it carries the
whole plugin security model (verify-before-execute, scrubbed environment,
declared-permission manifest, supervision — see [[Plugins]]) *plus* the same
data-plane posture: no store handle, only the ability to ask, authorized
host-side against this step's guard, on a token that is minted per run and dies
with the step. Its process environment is the scrubbed minimal one; a step's
`env:` reaches it as call data over the transport, never as process env, so an
engine cannot be pointed at another run's data plane by anything a step writes.

Related: [[Hosts]] · [[Workflows]] · [[Connectors]]
