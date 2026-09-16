# Engine slim-down (Stage B): removing the in-binary interpreters

Branch `feat/engines-slim-core`. Stage A shipped the four scripting engines as
plugins to `conductor-plugins` (`engines/{js,go-embed,risor,lua}`, released
`engines/<name>/v1.0.0`). This change removes them from conductor itself, so
`use: js` resolves to the official engine plugin and the interpreters leave
the binary.

**Commit:** `71c037b2f5e4261b8c873dc116ec80205dd3a061`

---

## 1. Result in one line

`cli` is now the ONLY builtin engine. The binary went from **47,194,274 →
30,888,098 bytes** (−15.6 MiB, **−34.6 %**) and yaegi, Risor, QuickJS/qjs and
wazero are gone from `go.mod`. Every `use: js` / `run: js` config still
selects the js ENGINE — it now resolves to
`github.com/NodeSpy/conductor-plugins//engines/js` and dispatches through
`plugin.run`.

## 2. Binary size and dependencies

```
$ CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /tmp/cond-slim ./cmd/conductor
$ stat -c '%s %n' /tmp/cond-baseline /tmp/cond-slim
47194274 /tmp/cond-baseline      # main, before
30888098 /tmp/cond-slim          # this branch
```

| | bytes | human |
|---|---|---|
| before | 47,194,274 | 45.0 MiB |
| after | 30,888,098 | 29.5 MiB |
| **delta** | **−16,306,176** | **−15.6 MiB (−34.6 %)** |

Direct dependencies removed from `go.mod`:

- `github.com/traefik/yaegi` (go-embed)
- `github.com/risor-io/risor` (risor)
- `github.com/fastschema/qjs` (js)
- `github.com/yuin/gopher-lua` (lua) — **demoted to indirect**, see below
- `github.com/tetratelabs/wazero` — dropped entirely (it was qjs's WASM runtime)

```
$ grep -E 'yaegi|risor|gopher-lua|fastschema/qjs|wazero' go.mod
	github.com/yuin/gopher-lua v1.1.2 // indirect
```

**The one survivor, and why it is not a miss.** `gopher-lua` is still in the
module graph as an *indirect* dependency — but not ours:

```
$ go mod why github.com/yuin/gopher-lua
github.com/NodeSpy/conductor/internal/kv
github.com/NodeSpy/conductor/internal/kv.test     <-- a TEST binary
github.com/alicebob/miniredis/v2
github.com/yuin/gopher-lua
```

`miniredis` (the fake Redis used by `internal/kv/redis_test.go`) embeds Lua for
`EVAL`. It reaches only the test binary, which is why it does not appear in the
shipped one:

```
$ go list -deps ./cmd/conductor | grep -E 'yaegi|risor|gopher-lua|qjs|wazero'
(no output — none of them are linked)
```

This is pre-existing and unrelated to code steps. Removing it would mean
replacing miniredis, which is out of scope.

## 3. Verification

```
$ gofmt -l .
(clean)

$ go build ./...
BUILD OK

$ go vet ./...
VET OK

$ go test ./...
(all packages pass)

$ CGO_ENABLED=1 go test -race ./...
race exit=0
40 packages ok, no data races
```

## 4. What was deleted vs kept

### Deleted outright

| file | what |
|---|---|
| `internal/code/js.go` | QuickJS engine + `jsKVShim`/`jsSQLShim` |
| `internal/code/goembed.go` | yaegi engine + stdlib allowlist |
| `internal/code/risor.go` | Risor engine + global allowlist |
| `internal/code/lua.go` | gopher-lua engine + `luaToGo`/`goToLua` |
| `internal/code/engines_test.go` | risor/lua engine tests |
| `internal/code/timeout_test.go` | the four engines' interpreter-loop timeout tests |

Also removed: the `case "js"/"go-embed"/"risor"/"lua"` arms in
`Executor.Exec`, the matching rejection list in `hostinterp.go`'s
`execRemote`, and `errRemoteInProcessEngine` (now unreachable — the plugin
branch's `errRemotePluginEngine` covers it).

### The shared ctx core — KEPT, untouched in behaviour

This is the part that matters, because the `cli` engine and the plugin
`host.*` path both depend on it:

| kept | used by |
|---|---|
| `kvInvoke` + `kvValueWrites` (kvbind.go) | `CtxHandler` → cli socket, plugin `host.kv` |
| `sqlInvoke` (sqlbind.go) | `CtxHandler` → cli socket, plugin `host.sql` |
| `memInvoke` + `memScopeOf`/`memScopeList`/`asAnyList` (membind.go) | `CtxHandler` → cli socket, plugin `host.memory` |
| `kvOps` / `sqlOps` / `memOps` (the op tables) | see note below |
| `ctxhost.go` — `CtxHandler`, `CtxRequest`, `CtxResponse`, `Invoke` | both out-of-process engines |
| `ctxsock.go`, `ctxclient.go` (`CtxClientMain`) | the `cli` engine's data plane |
| `engineplugin.go` — `InvokeHost`, `execPluginEngine` | the plugin engine path |
| `ParseOutputs` (outputs.go) | cli + host interpreters |

### Per-engine shims removed from the binding files

Only the four interpreters' faces went; the dispatcher under each stayed.

- **kvbind.go** — removed `kvInvokeJSON` (js bridge), `KVHandle` + all its
  methods and `kvGoEmbedExports` (go-embed), `kvRisorStoreFn` (risor),
  `luaStoreFn` (lua). Kept `kvInvoke`, `kvValueWrites`, `kvOps`.
- **sqlbind.go** — removed `sqlInvokeJSON`, `SQLHandle` + `sqlGoEmbedExports`,
  `sqlRisorFn`, `luaSQLFn`. Kept `sqlInvoke`, `sqlOps`.
- **membind.go** — removed `memInvokeJSON`, `jsMemShim`, `MemHandle` +
  `memGoEmbedExports`, `memRisorFn`, `luaMemFn`. Kept `memInvoke`, `memOps`,
  `asAnyList`, `memScopeOf`, `memScopeList`.

### Two judgement calls, flagged

**(a) `wrapValue` was deleted, not kept.** The brief listed it under "KEEP …
they are used by the `cli` engine and the plugin `host.*` path". It is not:
`cli` returns `ParseOutputs(stdout)` and the plugin path returns the engine's
`RunResult` map directly. Its only four callers were js/go-embed/risor/lua.
Keeping it would have left a private function nothing calls, whose doc comment
("the in-process (js/go-embed) half of the output contract") described two
engines that no longer exist. I removed it and am flagging it here rather than
silently. Nothing else changed about the output contract.

**(b) The op tables were kept and given a live caller.** `memOps` was already
used by the ctx client's usage error; `kvOps`/`sqlOps` would have been dead
after the shims went. Rather than leave two unreferenced tables, I used them
the same way `memOps` already was — `conductor ctx kv <store>` with a missing
op now names the ops, symmetric with `ctx memory`. This is the only
behavioural change outside the engines themselves, and it is error text.

## 5. How `use: js` resolves and dispatches now

**Resolution** (`internal/config`): `builtinEngines` in `use.go` is now
`{"cli": true}`. `js` therefore fails `builtinFor`, and `ParseUse(UseKindEngine,
"js")` falls through to the official-repo rule:

```
$ /tmp/cond-slim plugin list --config <a config with use: js>
NAME   KIND     ORIGIN    VERSION   STATUS
cli    engine   builtin   dev       bundled
js     engine   official  -         not installed — run `conductor init`
```

i.e. origin `official`, component `engines/js`, source
`github.com/NodeSpy/conductor-plugins//engines/js`.

**One thing this needed that was not obvious.** `run:` is deliberately
permissive — any name it does not recognise is a host interpreter, no
allowlist. With the builtins slimmed, a deployed `run: js` would have stopped
being an engine selection at all and silently become a **PATH lookup for a
program called `js`**. So `Step.StepEngine` now consults an explicit
`retiredInProcessEngines` set `{js, go-embed, risor, lua}` *before* the
`run:`/`use:` split, pinning both spellings to `EnginePlugin`. `use:` would
have reached the same answer on its own; the set is written once and consulted
for both so they cannot drift.

**Dispatch** (`internal/flow` → `internal/code`): unchanged. `execCode` passes
`Plugin: class == config.EnginePlugin` into `code.Spec`, and `Executor.Exec`
takes the plugin branch → `execPluginEngine` → `plugin.run`, with
`CtxHandler{Guard: spec.DataGuard}.InvokeHost` as the `host.*` callback. This
is exactly the path `acme-engine` already took.

**`host:` is still refused at load.** Two existing tests asserted that
`run: js` + `host:` fails at config load. That check lived on the
now-deleted `EngineInProcess` class, so I moved it to the `EnginePlugin`
branch of `validateStepEngine` — a plugin engine is a subprocess of *this*
daemon holding a ctx channel back to it, so it is local-only for the same
practical reason. Same configs, same refusal, new wording. (`internal/code`
guards it again at runtime via `errRemotePluginEngine`.)

**`EngineInProcess` was removed** from `config.EngineClass`: with no in-binary
interpreters it is unreachable. Nothing outside `internal/config` referenced it.

## 6. Tests

Per-engine unit tests were deleted. Tests covering the **shared core** were
ported to exercise it where it now lives rather than being dropped:

| was | now |
|---|---|
| `kv_ctx_test.go` — 4 near-identical tests, one per engine binding | one `TestCtxKVOpSurface` driving the full op set through `CtxHandler`, plus `TestKVInvokeArity` |
| `sql_ctx_test.go` — 4 per-engine faces | `TestCtxSQLOpSurface` through `CtxHandler` (keeps the SQL-injection/bind-literal assertion) |
| `mem_ctx_test.go` — 4 per-engine faces | `TestCtxMemoryOpSurface` through `CtxHandler` |
| `dataguard_test.go` — write barrier via js/go-embed/risor/lua | via the **`cli` engine with a real subprocess** over the real socket, plus `CtxHandler` for sql/memory |
| `scope_guard_test.go` — reserved "global" scope via js | via `CtxHandler` (+ a new assertion that it is an error, not a `Refused` policy denial) |
| `sql_caps_test.go` — ATTACH/`code_access` via js | via `CtxHandler` |
| `spawnenv_test.go` — go-embed GOPATH sandbox test | deleted with the engine; the env-allowlist regression test stays |

`internal/flow` needed a new harness. Its data-plane tests wrote `run: js` and
called `ctx.store(…)` directly, which only worked because js held an in-process
binding. They now use `use: cli` and speak the real protocol: a new
`internal/flow/ctxhelper_test.go` re-execs the test binary as
`code.CtxClientMain` (the same trick `internal/code/ctxcli_test.go` uses), so
the step goes step → socket → `CtxHandler` → guard. Converted this way:
`TestKVStepsAndCodeShareOneStore`, `TestSQLVerbSteps`,
`TestResourceAllowlistCodeStores`, `TestCodeStepReachesItsOwnScopeWithNoAllowlist`,
`TestCodeStepOwnTargetStoreStillNeedsListing`, `TestGuardCodeBindingWriteBarrier`.

Two flow changes worth naming:

- `TestExecCodeRoutesByEngine` lost its js arm; a new
  `TestExecCodeRoutesRetiredEnginesToThePluginPath` asserts that `js`, `lua`,
  `risor` and `go-embed` under **both** `use:` and `run:` reach the plugin path
  and never a PATH lookup. This is the regression test for the `run:` trap above.
- `TestGuardSandboxHost` had an agent plan emit `run: js`. The plan validator
  now (correctly) refuses an agent-named engine plugin the operator's config
  does not reference, which masked what the test was about, so its plan step is
  `run: sh` — still class `code`, still needs the sandbox host.

## 7. e2e

**Approach taken: convert the glue to `cli`; rely on group V for the
out-of-process engine path.** Seeding an installed js engine was not available:
the e2e image's build context is this repo, the js engine plugin lives in the
separate `conductor-plugins` repo, and the stack is hermetic (no network), so
there is nothing to build it from without vendoring an external repo.

The four `run: js` sites in `test/e2e/config/connectors.e2e.yaml` were all
**glue, not subject** — none tested the js engine:

| site | its actual subject | now |
|---|---|---|
| K2 `shape` | code step → verb chain | `use: cli, command: [sh]`, `env:` templated |
| Q `prep` (history) | per-step history recording | `use: cli, command: [sh, -c, …]` |
| S `echo` (callable) | inputs arriving over the invoke surface | `use: cli, command: [sh]`, `env:` templated |
| §16 `always-pass`/`always-fail` checks | the gate machinery | `use: cli, command: [sh, -c, …]` constants |

Every scenario keeps its assertions and its sink text, so `run.sh` is
unchanged. **Group V (`acme-engine`) is untouched** and remains the proof of
the out-of-process engine path — `plugin.run` out, `host.*` callbacks back,
host-side authorization of a gated store — which is now the path `js` takes
too.

```
$ MODE=stub bash test/e2e/run.sh
====================================================
PASS=123  FAIL=0
e2e exit=0
```

Group V specifically:

```
V  V-run     PASS  V a use: <plugin> step ran OUT OF PROCESS and its outputs flowed back
V  V-inputs  PASS  V the rendered ctx document reached the engine subprocess as inputs
V  V-guard   PASS  V the engine's ctx.store callback is HOST-authorized — a gated store was denied by the daemon
V  V-code    PASS  V the step's code: body crossed the plugin wire intact
```

## 8. Back-compat

```
$ /tmp/cond-slim validate --config /tmp/bc/backcompat.yaml
ok: 2 connector(s), 2 trigger(s), 0 workflow(s)
```

covering `use: cli` + `command: [bash]`, `run: bash`, `type: command`, and
`uses: <verb>` — all load and dispatch.

A `use: js` / `run: js` config **loads** (resolution succeeds → plugin) and
fails with the ordinary not-installed message, not a crash:

```
$ /tmp/cond-slim validate --config /tmp/bc/js.yaml     # use: js
error: engine plugin js: plugin js (js) is referenced by your config but not
installed — run `conductor init` (or `conductor plugin update js`) to fetch it

$ /tmp/cond-slim validate --config /tmp/bc/js.yaml     # run: js — identical
error: engine plugin js: plugin js (js) is referenced by your config but not
installed — run `conductor init` (or `conductor plugin update js`) to fetch it
```

Shipped configs:

```
config.example.yaml          ok: 5 connector(s), 10 trigger(s), 1 workflow(s)
config.starter.yaml          ok: 1 connector(s), 3 trigger(s), 0 workflow(s)
config.example.legacy.yaml   line 409: field agents not found in type config.Config
```

`config.example.legacy.yaml` fails **identically on the pre-change binary**
(verified against `/tmp/cond-baseline`) — pre-existing, and it is the
migrate-me fixture, not a config meant to validate as-is.

`config.example.yaml` had the one `use: js` step in the tree. Since a shipped
example should work on a fresh install with nothing fetched, its inline-code
demo is now `use: python3` (a host interpreter), with the comment block
rewritten to spell out the three engine kinds and note that `use: js` needs
`conductor init` first.

The LIVE-shaped config (github connector + agent/command/verb steps, no code
steps) is `config.example.yaml`'s shape and loads unchanged.

## 9. Fleet safety

The plugin protocol was **not touched**:

```
$ git diff --stat -- pkg/plugin internal/plugin
(no output)

$ go test ./pkg/plugin/... ./internal/plugin/...
ok  github.com/NodeSpy/conductor/pkg/plugin
ok  github.com/NodeSpy/conductor/internal/plugin
```

The builtin-engine registry lives in `internal/config/use.go`, not in either
plugin package, so no protocol surface moved. Existing connector, runtime and
engine plugins are unaffected.

## 10. Lingering references

```
$ grep -rn 'go-embed|risor|gopher-lua|yaegi|qjs|wazero' --include='*.go' .
internal/code/code.go:18        # package doc: "used to be linked in"
internal/config/engines.go:54   # EnginePlugin doc
internal/config/engines.go:95   # retiredInProcessEngines — the routing map
internal/config/engines.go:176  # host: refusal doc
internal/config/connectors.go:853  # Step.Use doc
```

All five are intentional: the routing map that makes `run: js` work, and doc
comments describing the migration. No code treats any of the four as builtin or
in-process.

## 11. Docs

`docs/wiki/` updated to travel with the change: **Code-Steps** (rewritten —
`cli` is the only builtin, the four are official engine plugins fetched by
`conductor init`, the ctx bindings section reframed around the data plane,
trust/timeout/host sections corrected now that nothing shares the daemon's
process), **Quickstart** (its first code-step example moved to `use: cli` so it
runs on a fresh install), plus **Home, Steps, Hosts, Stores, Memory, Plugins,
Examples, Integration-Webhook**.

`docs/design/code-step-engines.md` is the design record for this work and was
left as written.
