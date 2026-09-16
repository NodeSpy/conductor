# Code-step engines as plugins

Status: **proposal** (for review). Owner-driven. No code yet.

## Problem

`run:` code steps currently support three embedded-language engines — `js`
(QuickJS), `go-embed` (the yaegi Go interpreter + the entire Go stdlib symbol
table), and `lua` (gopher-lua) — all compiled into the conductor binary
(`internal/code/`). Adding a language is a core change, and every build carries
every interpreter whether or not a config uses one (yaegi + `stdlib.Symbols`
alone is heavy).

The deeper problem is **who decides which languages exist**. Today it is the
maintainer. We want the opposite: the set of languages a code step can be
written in should be **open and community-managed**, the same way connectors and
runtimes already are — you publish an engine, anyone can `use:` it, no core
change, no maintainer gatekeeping.

## The cut

> **conductor owns the contract; the community owns the languages.**

conductor owns and versions the *engine ABI* — how a snippet receives its
inputs, how it returns outputs, and the `ctx` data-plane it may reach. The
community owns the *engines* — the actual interpreters/runtimes (Ruby, WASM,
Python-embedded, a stricter JS, whatever), fetched, trusted, and sandboxed
through the existing `use:` machinery.

A direct consequence: the built-in engines stop being privileged. `js` /
`go-embed` / `lua` become *official* engine plugins (in `conductor-plugins`),
resolved the same way as a third-party engine — not a hardcoded `switch` in the
binary. They can stay bundled for zero-config/offline UX, but the model treats
them as replaceable. The binary can eventually shed the interpreters entirely;
that slimming falls out of this design rather than being a separate project.

## What already exists (and is reused)

- **`use:` resolution** (`internal/config/use.go`): one kind-aware search path —
  builtin → official repo → `owner/repo[/component]` → host → local path. Adding
  an engine kind is additive.
- **Plugin protocol + SDK** (`pkg/plugin/wire.go`, `internal/plugin`):
  newline-delimited JSON-RPC 2.0 over stdio, `ProtocolVersion = 1`. A plugin
  `Decl` advertises `Verbs` with `Options`/`Outputs` `Schema`s and a
  `Capabilities` permission manifest (egress / commands / fs / spawns). Verbs
  run out-of-process today.
- **Resource scoping** (`Field.Scope`): a verb option tagged with a scope
  (`store`, `repo`, `channel`, …) has its *value* gated to what the dispatch's
  own trigger points at plus the operator allow-list. This is exactly the gate a
  code step's store access needs.
- **Trust + sandbox**: `plugin_trust` allow-list (official repo is default-
  trusted), plus the process sandbox (namespaces, deny-by-default egress,
  `ScrubEnv`, PATH confined to declared `Commands`).
- **The `ctx` data-plane + guards** (`internal/code`): `ctx.store`/`ctx.kv`
  (`get set setnx merge delete incr append remove contains list`), `ctx.sql`
  (`query exec`), `ctx.memory` (`remember recall`), each vetted by `DataGuard`
  (the plan write-barrier) and the `code_access` resource allow-list.
- **`ParseOutputs`** (`internal/code/outputs.go`): the stdout→outputs contract
  shared across engines, feeding `{{.steps.<id>.outputs.*}}`.

The gap is small and specific: the protocol has daemon→plugin *invoke* and one-
way plugin→daemon *events*, but **no plugin→daemon request/response** — which is
precisely what a running snippet needs to reach `ctx.kv/sql/memory`.

## Selection surface

`run:` stays the field; its value resolves through the `use:` search path:

```yaml
steps:
  - run: js                       # official engine plugin (conductor-plugins)
    code: "ctx.outputs = {n: ctx.pr}"
  - run: acme/conductor-engines/wasm@^1   # third-party engine, owner/repo/component
    code: "..."
  - run: ./bin/my-engine          # local dev engine
    code: "..."
```

- Bare name → builtin (if bundled) else the official engine repo.
- `owner/repo[/component]` / host / local path → exactly as connectors resolve.
- **Shell interpreters stay as-is.** `sh`, `bash`, `node`, `python`, a bare `go`,
  or an interpreter path are NOT engines — they already shell out to a real
  binary on PATH (local or `host:`), need no embedded runtime, and carry no ABI.
  The plugin model is only for *embedded-language* engines, the ones that would
  otherwise bloat the binary. (Open question O5 revisits whether shell
  interpreters could also be expressed as engines for uniformity — not proposed
  for phase 1.)

Back-compat: `run: js` / `go-embed` / `lua` / `bash` / `python` / `<path>` keep
working unchanged.

## The engine ABI (what conductor owns and versions)

An engine plugin declares `Kind: "step"` and one entry point. A run carries four
things:

1. **Inputs** — the step's rendered template context, handed to the engine as a
   single JSON document (the `ctx` the snippet reads). Same context the in-
   process engines expose today as a JS global / Go map / Lua table.
2. **Code** — the snippet source, plus optional `args`/`env` (as today).
3. **Outputs** — the engine returns an outputs map (JSON), normalized through
   the existing `ParseOutputs` semantics → `{{.steps.<id>.outputs.*}}`.
4. **`ctx` data-plane** — the snippet's calls to `ctx.kv` / `ctx.sql` /
   `ctx.memory`, delivered as **host callbacks** (see below). The engine never
   touches a store directly; it asks the daemon, which authorizes and executes.

The ABI is versioned independently of the app (its own `abi_version`), because
every community engine implements it and we must be able to evolve it without
breaking the ecosystem.

### Protocol additions (`pkg/plugin/wire.go`)

- `Kind = "step"`.
- **`plugin.run`** (daemon→plugin request):
  `{ instance, run_id, code, args?, env?, inputs }` → `{ outputs }`. A dedicated
  method rather than a `Verb`, because an engine has no fixed option/output
  schema — it runs arbitrary code against the `inputs` document.
- **Host-callback channel** (plugin→daemon request/response — NEW direction):
  the engine issues `host.kv` / `host.sql` / `host.memory` requests, correlated
  by `run_id` so the daemon applies the *right* step's `DataGuard`, `code_access`
  allow-list, and `Scope` gate before executing. JSON-RPC is already
  bidirectional over the one stdio pipe; this is a new method set, not a new
  transport.
  - `host.kv`    → `{ run_id, store, op, args }` — op ∈ the `kvOps` set.
  - `host.sql`   → `{ run_id, store, op: query|exec, args }`.
  - `host.memory`→ `{ run_id, op: remember|recall, args }`.
  - The daemon runs the guard, executes against the resolved store, returns the
    result or a refusal. **Enforcement is entirely host-side** — an untrusted
    engine cannot bypass `DataGuard` by construction, because it has no store
    handle, only the callback.

### The `Capabilities` story for an engine

An engine declares its own manifest, surfaced to the operator at install:

- A pure data-shaping engine (js/go-embed/lua analog) declares **no egress, no
  spawns** — it can only reach the world through the host-mediated `ctx`. This
  reproduces today's yaegi-allowlist sandbox as a *process* sandbox.
- An engine that legitimately needs the network (say a `fetch`-style engine)
  declares `egress:` and the operator consents when adding it — the same trust
  prompt any connector plugin gets.

Two trust layers stack: (a) the engine binary is trust-gated by `plugin_trust`
(official = default-trusted; third-party = explicit allow); (b) the *snippet* is
the operator's own code, confined by the engine's sandbox and reachable outside
world limited to the declared capabilities. Note the snippet is generally
*operator-authored config*, not agent-authored — but the `DataGuard` plan write-
barrier still applies for the agent-authored-plan case, unchanged.

## Execution model

- The engine runs **out-of-process**, sandboxed by the existing plugin sandbox.
  This is stricter isolation than today's in-process interpreters (which share
  the daemon's fate); a snippet that hangs or OOMs takes down its engine
  subprocess, not conductor.
- **Latency**: subprocess spawn + per-callback RPC vs an in-process call. Fine
  for data-shaping and occasional store touches; worse for a tight loop over
  `ctx.kv`. Mitigation (O2): a warm/pooled engine process reused across steps of
  a run, or across a debounced burst.
- **`host:`** (O3): out-of-process engines *could* run on a `hosts:` target,
  which the in-process engines cannot. Not proposed for phase 1 — the host-
  callback channel to the daemon over an SSH hop needs its own thought.

## Migration of the built-in engines

Phase-by-phase so nothing breaks and the ABI is proven before we depend on it:

- **Phase 1 — prove the ABI.** Add `Kind: step`, `plugin.run`, and the host-
  callback channel. Ship ONE reference engine as a plugin (candidate: a WASM
  engine, or re-implement `lua`) to exercise inputs/outputs/`ctx` end-to-end.
  The bundled js/go-embed/lua stay in-process and unchanged. `run:` learns to
  resolve a non-builtin name through `use:`.
- **Phase 2 — de-privilege the built-ins.** Re-implement `js` / `go-embed` /
  `lua` as official engine plugins. Keep them bundled (builtin resolution short-
  circuits, like builtin connectors) so `run: js` still works offline with zero
  config. Optionally gate the in-binary interpreters behind a build tag so a
  slim build drops them and fetches on first use.
- **Phase 3 — ecosystem.** SDK example + docs for authoring an engine; the
  official-engines repo; a couple of community engines (Ruby, Python-embedded).

Back-compat is preserved throughout: op set, `DataGuard`/`code_access`
semantics, and `ParseOutputs` are identical — only the *transport* of `ctx`
changes (in-process for bundled engines, host-callback for plugin engines), and
only for engines that opt into the plugin path.

## Open questions

- **O1 — selection spelling.** `run: <use-ref>` (proposed) vs a separate `use:`
  key on the step. `run:` reads naturally and reuses one resolver; the risk is
  overloading a field that also names shell interpreters. Leaning `run:`.
- **O2 — process reuse.** Spawn-per-step (simple, slower) vs a pooled warm
  engine keyed by (engine, run) or (engine, group). Affects the callback
  correlation model.
- **O3 — `host:` engines.** Allow an out-of-process engine on a remote host in
  phase 1, or defer until the callback-over-SSH story is designed?
- **O4 — ABI v1 surface.** Ship the full `ctx` op set on day one, or a minimal
  `kv get/set` + outputs and grow? Every op added to v1 is one we must keep.
- **O5 — shell interpreters.** Leave `sh/bash/node/python` as the host-
  interpreter path (proposed), or eventually express them as trivial engines for
  one uniform model?
- **O6 — inputs size.** The `inputs` document over JSON-RPC — cap size / chunk
  large contexts, or document a practical limit?
- **O7 — determinism/replay.** A plugin engine's output must remain replayable
  for `conductor runs retry`; confirm the recorded-inputs model still holds when
  the engine is external.

## Non-goals

- Replacing the shell-out interpreters. They already give you any language on
  PATH, locally or on a host, with no ABI — that stays.
- A general FFI. The `ctx` data-plane is deliberately the kv/sql/memory surface
  plus inputs/outputs, nothing more; an engine that wants more reaches the world
  through declared, operator-consented `Capabilities`, not through a wider `ctx`.
