# Code steps

Inline code is the glue between agent outputs and service verbs — reshape
JSON, compute a condition — without a whole agent. A code step is `run:
<engine>` plus `code:`; every executor is a single `run:` value.

```yaml
steps:
  - { id: shape,  run: js,       code: "return { sev: ctx.body.detail.severity }" }
  - { id: calc,   run: go-embed, code: "…" }                          # yaegi, install-free
  - { id: score,  run: risor,    code: '{"sev": ctx["level"]}' }      # Risor, install-free
  - { id: pick,   run: lua,      code: "return { sev = ctx.level }" } # Lua 5.1, install-free
  - { id: heavy,  run: go,       code: "…" }                          # host go run
  - { id: deploy, run: sh,   host: build-box, code: "make deploy" }   # sh on build01
  - { id: enrich, run: ruby, host: build-box, code: "…" }             # build01's ruby
```

## Engines

**Baked-in, sandboxed (zero-install, local-only):**

- `run: js` — QuickJS compiled to WASM, executed in wazero (pure Go, no CGo).
  A true WASM sandbox, identical on every OS. The code body is a function
  body: `return` its result.
- `run: go-embed` — yaegi, a Go interpreter written in Go, in-process. No
  toolchain needed; sandboxed by a stdlib import allowlist (strings, strconv,
  fmt, encoding/json, time, math, regexp, sort, …; no os, os/exec, net,
  syscall, unsafe). The code must define
  `func run(ctx map[string]any) (any, error)` (or `… any`).
- `run: risor` — [Risor](https://github.com/risor-io/risor), a Go-flavored
  scripting language interpreted in pure Go. The script's final expression is
  its result. Sandboxed by an explicit global allowlist: the core builtins
  plus strings, strconv, math, json, regexp, time, base64, bytes, and errors —
  no os, exec, net, or filesystem modules.
- `run: lua` — Lua 5.1 on gopher-lua, a Lua VM in pure Go. The script
  `return`s its result (a table with string keys becomes the step's outputs).
  Only the base, table, string, and math libraries are opened — no os, io,
  debug, or package — and the file/chunk loaders (`dofile`, `loadfile`,
  `load`, `loadstring`) are removed.

**Host interpreters (bring your own):**

- `run: go` — the host `go run`: full fidelity (generics, cgo, third-party
  modules). The code is a complete program reading the ctx JSON on stdin and
  printing its result JSON on stdout. `go` resolves via PATH; a clear error
  names the `go-embed` fallback when absent.
- `run: ruby | node | python | php | perl | sh | bash | /usr/bin/…` — resolved
  by name on PATH or by explicit path. conductor writes `code:` to a private
  temp file and invokes it (`args:` appends extra argv); the ctx JSON arrives
  on stdin. `sh` is the portable default — never assume bash.

## The data contract

The step's template scope — trigger context, prior step outputs, `group`,
`inputs` inside a workflow — is injected as `ctx` (a global in js, risor, and
lua; the `run(ctx)` argument in go-embed; JSON on stdin for host
interpreters). Named `secrets`
are NOT passed into ctx; pass one explicitly via `env:` or `args:` templates
when code genuinely needs it.

The return value / stdout becomes the step's outputs: a JSON object as-is
(`{{.step.field}}`), any other JSON under `value:`, plain text under `text:`.

## Where it runs

A code step runs where conductor runs. `host: <name>` (a [[Hosts]] entry) or
an inline `ssh: {…}` runs a **host-interpreter** step on that box through the
remote's interpreter — the code travels as a base64 frame, the ctx JSON on
stdin, and a missing interpreter is a distinct clear error. The baked-in
engines (`js`, `go-embed`, `risor`, `lua`) execute inside conductor's own
process and are **local-only**; `conductor validate` rejects `host:` on them
and names the alternatives (run `node` there, or run a conductor on that
box).

## Trust boundary

WASM (`js`) is memory-isolated. yaegi (`go-embed`), Risor, and Lua are
in-process behind their allowlists — appropriate for operator-authored
config, not untrusted input. Host interpreters have full host power (that is
their point).

Related: [[Hosts]] · [[Workflows]] · [[Connectors]]
