# Code-step engines (3/N): the plugin wire protocol for out-of-process engines

Increment 3 of "code-step engines": the shared plugin protocol grows a step-engine
kind, a `plugin.run` method, and a NEW direction — `host.kv`/`host.sql`/`host.memory`
requests the plugin issues and the daemon answers — plus a reference engine plugin
proving the whole path end to end.

Branch `feat/engines-plugin-protocol`, not pushed.

**Implementation commit:** `51e5f2ab5dfcbffd713d77e0e9bd67fc69063737` — `Code-step engines (3/N): the plugin wire
protocol for out-of-process engines`. This report is the commit directly on top of
it (a report cannot name its own sha).

The prime directive was that existing connector and runtime plugins keep loading and
invoking byte-for-byte unchanged. Everything below serves that, and §5 is the proof.

---

## 1. Verification

### gofmt / build / vet

```
=== gofmt -l (repo) ===
(no output above = clean)

=== go build ./... ===
build: clean

=== go vet ./... ===
vet: clean
```

(`gofmt -l cmd internal pkg test` printed nothing; `CGO_ENABLED=0 go build ./...` and
`go vet ./...` both exited 0.)

### `go test ./...` — exit 0, 0 FAIL

```
ok  	github.com/NodeSpy/conductor/internal/secrets	0.731s
ok  	github.com/NodeSpy/conductor/internal/skill	0.006s
ok  	github.com/NodeSpy/conductor/internal/sqlstore	0.023s
ok  	github.com/NodeSpy/conductor/internal/store	0.094s
ok  	github.com/NodeSpy/conductor/internal/vaults	0.049s
ok  	github.com/NodeSpy/conductor/pkg/githubkit	0.005s
ok  	github.com/NodeSpy/conductor/pkg/plugin	0.012s
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	0.003s
?   	github.com/NodeSpy/conductor/test/plugins/acme-echo	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-engine	[no test files]
EXIT=0   (grep -c '^FAIL' = 0)
```

### `CGO_ENABLED=1 go test -race ./...` — exit 0, 0 FAIL, 0 DATA RACE

Exact tail:

```
ok  	github.com/NodeSpy/conductor/internal/models	(cached)
ok  	github.com/NodeSpy/conductor/internal/netguard	(cached)
ok  	github.com/NodeSpy/conductor/internal/notify	(cached)
ok  	github.com/NodeSpy/conductor/internal/plugin	5.127s
ok  	github.com/NodeSpy/conductor/internal/sandbox	(cached)
ok  	github.com/NodeSpy/conductor/internal/secrets	(cached)
ok  	github.com/NodeSpy/conductor/internal/skill	(cached)
ok  	github.com/NodeSpy/conductor/internal/sqlstore	(cached)
ok  	github.com/NodeSpy/conductor/internal/store	(cached)
ok  	github.com/NodeSpy/conductor/internal/vaults	(cached)
ok  	github.com/NodeSpy/conductor/pkg/githubkit	(cached)
ok  	github.com/NodeSpy/conductor/pkg/plugin	(cached)
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	(cached)
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeacp	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeagentdeck	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakecli	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeopencode	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakepaseo	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fixer	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/mockgithub	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/sinkcatcher	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-echo	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-engine	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-runtime	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-ticker	[no test files]
EXIT=0   (grep -c '^FAIL' = 0 · grep -c 'DATA RACE' = 0)
```

The packages this change touches, under `-race`:

```
ok  	github.com/NodeSpy/conductor/cmd/conductor	11.129s
ok  	github.com/NodeSpy/conductor/internal/code	16.144s
ok  	github.com/NodeSpy/conductor/internal/config	5.331s
ok  	github.com/NodeSpy/conductor/internal/flow	16.255s
ok  	github.com/NodeSpy/conductor/internal/plugin	5.127s
ok  	github.com/NodeSpy/conductor/pkg/plugin	(cached)
```

### docker e2e — PASS=123, FAIL=0

`MODE=stub bash test/e2e/run.sh`, exit 0. Baseline before this change was 119; the four
new rows are Group V. Every pre-existing row stayed green, including the `cli` engine's
socket data plane (K9/K9ctx), which is the closest neighbour to what changed.

```
K      K9-cli      PASS   K9 cli engine bridged ctx-on-stdin and stdout outputs
K      K9-ctx      PASS   K9ctx cli engine round-tripped ctx data over the per-run socket, guards enforced host-side
V      V-run       PASS   V a use: <plugin> step ran OUT OF PROCESS and its outputs flowed back
V      V-inputs    PASS   V the rendered ctx document reached the engine subprocess as inputs
V      V-guard     PASS   V the engine's ctx.store callback is HOST-authorized — a gated store was denied by the daemon
V      V-code      PASS   V the step's code: body crossed the plugin wire intact
====================================================
PASS=123  FAIL=0  (SKIP = genuinely N/A for this stack; see the row note)
E2E_EXIT=0
```

---

## 2. Files changed, by area

**Wire protocol (public SDK)**
- `pkg/plugin/wire.go` — `KindStep`, `Decl.ABI`, `EngineABI`, `plugin.run` +
  `host.kv`/`host.sql`/`host.memory` method names, `RunRequest`/`RunResult`,
  `HostRequest`/`HostResult`, `HostMethodFor`/`HostKindFor`.
- `pkg/plugin/serve.go` — bidirectional Serve (call table, response delivery),
  per-message stdin cap, `EngineHandler`/`EngineFunc`, the `plugin.run` dispatch arm.
- `pkg/plugin/host.go` *(new)* — the typed `*Host` client stub (`KV`/`SQL`/`Memory`),
  the `*Refusal` error type and `IsRefused`.

**Daemon side**
- `internal/plugin/plugin.go` — kind/method/type aliases, `EngineABI`, `Spec.Key()`
  gains `engines/`.
- `internal/plugin/client.go` — `Client.Run` (mints the per-run token, registers it for
  the call's duration), `handleRequest`/`runFor` (constant-time token check → the
  caller's `RunHost`), `pluginHandler.onRequest`, the ABI check in `Describe`, the
  `call`/`callFor` split so a run carries no 30s cap.
- `internal/plugin/manager.go` — `EngineSpecs()`.
- `internal/plugin/spawn.go` — deny-by-default egress for `KindStep`.

**Data plane reuse**
- `internal/code/engineplugin.go` *(new)* — `PluginEngine`/`EngineLookup` seam,
  `CtxHandler.InvokeHost` (the plugin-wire face of the existing handler),
  `execPluginEngine`, the local-only refusal.
- `internal/code/code.go` — `Spec.Plugin`, `Executor.Engines`, dispatch.

**Resolution & dispatch**
- `internal/config/engines.go` — `EnginePlugin` is a legal selection; the workflow /
  unparseable-reference errors kept and sharpened.
- `internal/config/plugins.go` — `PluginRefs()` derives engine refs by walking steps;
  `validatePluginRefs` covers them; `PluginKindEngine`.
- `internal/flow/flow.go` — carries the config layer's classification into `code.Spec`.
- `internal/flow/plan.go` — an agent-authored plan may name only an engine the
  operator's config already references.
- `cmd/conductor/plugins.go` — `loadEnginePlugins` (start → describe → kind+ABI check →
  lookup), engine-aware `plugin add --engine` / `plugin show` disclosure.
- `cmd/conductor/connectors.go` — wires the lookup into `code.Executor`.

**Reference plugin + tests + docs**
- `test/plugins/acme-engine/main.go` *(new)* — SDK-only reference engine.
- `pkg/plugin/engine_test.go`, `internal/plugin/engine_test.go`,
  `internal/code/engineplugin_test.go` *(new)*; `internal/config/engines_test.go`
  updated for the now-legal plugin reference.
- `test/e2e/` — Dockerfile builds the engine, `entrypoint-conn.sh` seeds install state,
  `connectors.e2e.yaml` gains the V trigger, `docker-compose.yml` a repo,
  `fixtures/conn_engine_comment.json`, `run.sh` group V.
- `docs/wiki/Plugins.md`, `docs/wiki/Code-Steps.md`.

---

## 3. The wire additions, exactly

### `Decl.ABI` — the negotiation, and why `ProtocolVersion` did not move

`internal/plugin/client.go`'s `Describe` compares `ProtocolVersion` for **exact
equality**. Bumping it to 2 would refuse every plugin already installed in the field,
including ones a new daemon understands perfectly. So it stays at 1 and the engine
surface negotiates through a new field:

```go
type Decl struct {
	ProtocolVersion int  `json:"protocol_version"`
	Kind            Kind `json:"kind,omitempty"`
	ABI             int  `json:"abi,omitempty"`   // NEW
	...
}
```

- `omitempty`, so a plugin that does not set it emits **nothing**.
- Zero means "a plugin from before this field existed".
- The daemon reads it **only when `decl.Kind == KindStep`**. A connector's or a
  runtime's ABI is never consulted; setting one is describing something nobody asks
  about.
- `EngineABI = 1` is the revision this daemon drives. A `KindStep` plugin reporting a
  different one is refused with a message that says *step-engine ABI*, explicitly not
  "protocol version", so an operator is not sent to the wrong place.

`KindStep`'s **wire value is `"engine"`** — the same word as `config.UseKindEngine` and
the official repo's `engines/` directory — because the daemon cross-checks a plugin's
declared kind against where it was referenced from, and those two strings have to be the
same string to compare. The Go name says what it serves; the value says where it is
declared.

### `plugin.run` (daemon → plugin)

```jsonc
--> {"id":1,"method":"plugin.run","params":{
      "instance":"wasmtime","run_id":"9f3c…","code":"…",
      "args":["a","b"],"env":{"K":"V"},"inputs":{"repo":"o/r"}}}
<-- {"id":1,"result":{"outputs":{"attempts":3}}}
```

`inputs` is the rendered ctx document (what `ctx` is in-process, and what arrives as
JSON on stdin for `use: cli`); `outputs` becomes the step's outputs through the same
`{{.steps.<id>.outputs.*}}` path every other engine uses. `run_id` is the capability for
the callbacks below, not an identifier.

### `host.kv` / `host.sql` / `host.memory` (plugin → daemon — the new direction)

```jsonc
<-- {"id":"h1","method":"host.kv","params":{
      "run_id":"9f3c…","kind":"kv","op":"get",
      "resource":"cache","args":["run","attempts"]}}
--> {"id":"h1","result":{"ok":true,"value":3}}
```

`HostRequest`/`HostResult` mirror `internal/code`'s `CtxRequest`/`CtxResponse` field for
field — `run_id` here is what `token` is there — so the daemon converts by **copying**,
not translating. One ABI in front of one handler.

| field | meaning |
|---|---|
| `run_id` | the per-run capability; wrong/absent/expired → refused before any policy runs |
| `kind` | `kv` · `sql` · `memory`; must equal the kind the method names |
| `op`, `resource`, `args` | identical to the ctx socket, positional args and all |
| `ok`/`value` | success and its JSON result |
| `error`/`refused` | `refused` marks a **policy** denial, not a malfunction |

Error ranges follow `serve.go`'s existing JSON-RPC codes and split them the way the
socket does:

- **In-band** (`{"ok":false,...}`): a policy refusal, a store-level gate, a bad op — the
  daemon answering "no" is the protocol working, exactly as `CtxResponse` treats it.
- **JSON-RPC error**: the transport's own problems — `CodeMethodNotFound` for a method
  that is not a host callback, `CodeInvalidParams` for params that will not decode or a
  body whose `kind` contradicts its method.

---

## 4. How the bidirectional Serve loop stays backward-compatible

The SDK's read loop previously serviced `method+id` and **ignored everything else**
(`if m.Method == "" || m.ID == nil { continue }`). The change splits that ignore arm:

```go
if m.Method == "" {
	if m.ID != nil { calls.deliver(m) }   // a response to a host.* call WE issued
	continue
}
if m.ID == nil { continue }               // notification — unchanged
// method + id: serve it — unchanged
```

A message with no method and an id is something **only a plugin that called out can
receive**, and a plugin that never calls out has no pending call to match, so `deliver`
is a no-op. Nothing that used to be serviced changed meaning.

- **Writes are unchanged.** `calls` is empty for any plugin that never issues a host
  call; every byte a connector puts on the wire comes from the same `write(resp)` path
  it always did. `TestConnectorWireIsUnchangedByTheEngineAdditions` asserts the exact
  bytes.
- **No deadlock, by construction.** Every incoming request is already dispatched on its
  own goroutine (that predates this change — it was added so one slow verb could not
  block the read loop). A handler blocked on a host response therefore never blocks the
  read loop, which stays free to deliver that response *and* to keep servicing the
  daemon's other requests. `TestInterleavedDaemonRequestsAndHostCallsDoNotDeadlock`
  parks one run on an unanswered host call, then drives a describe, an invoke, and 8
  concurrent runs through it; it runs under `-race`.
- **Ids do not collide.** The daemon numbers its requests with integers from 0; the
  plugin's host calls use string ids `"h1"`, `"h2"`, …. Two independent id spaces on one
  stream are unambiguous either way (direction discriminates them), but making them
  *look* different keeps a trace readable and a half-correct implementation honest.
- **Shutdown unblocks callers.** When the read loop ends, `callTable.shutdown()` fails
  every waiting call, so a handler parked on a host response returns instead of hanging
  Serve's in-flight drain.

**The 32 MiB cap is now per-message (changed — noted as instructed).** It was
`io.LimitReader(in, 32<<20)` over the whole of stdin, i.e. **cumulative**: after 32 MiB
of lifetime traffic the decoder saw EOF and `Serve` returned `nil` — a clean shutdown,
reported as one. A connector never reached it; a long-lived step engine, which sees
every step's inputs, would, and the daemon would have attributed the exit to a crash.
It is now a `msgLimitReader` whose window resets at each message boundary. The bound is
what it was always meant to be — no single frame becomes unbounded memory — and it is
strictly *more* permissive than before for stream totals, never less for one message.
`TestStdinCapIsPerMessageNotCumulative` pins it.

---

## 5. PROOF: existing connector/runtime plugins are unaffected

### `client.go`'s version gate still accepts them

The `ProtocolVersion != ProtocolVersion` exact-equality check is untouched, and
`ProtocolVersion` is still `1`. ABI is read only inside `if decl.Kind == KindStep`.
`TestExistingPluginsLoadUnchanged` drives `Describe` with the three shapes that exist in
the field and asserts each loads and that no ABI is invented:

```
=== RUN   TestExistingPluginsLoadUnchanged
=== RUN   TestExistingPluginsLoadUnchanged/connector_with_no_kind_and_no_abi_(pre-#54_shape)
=== RUN   TestExistingPluginsLoadUnchanged/connector_that_declares_its_kind_but_no_abi
=== RUN   TestExistingPluginsLoadUnchanged/runtime_with_no_abi
--- PASS: TestExistingPluginsLoadUnchanged (0.00s)
```

### The SDK reference CONNECTOR is wire-identical

`TestConnectorWireIsUnchangedByTheEngineAdditions` runs a describe and an invoke through
the real `serve` loop and compares the **exact result bytes**, and additionally fails if
the plugin writes any message with no id or any message carrying a method (i.e. if a
connector ever issued a request):

```
want["0"] = {"protocol_version":1,"type":"acme","verbs":[{"name":"echo"}],"capabilities":{}}
want["1"] = {"outputs":{"message":"hi"}}
```

No `"abi"`, no `"kind"`, no envelope additions. `TestDeclWithoutABIStaysAbsentOnTheWire`
pins the marshal/unmarshal side of the same property.

### The real reference connector still describes / loads / invokes

The pre-existing integration tests drive the **actual `test/plugins/acme-echo`
subprocess** over the real transport and are unchanged:

```
--- PASS: TestAConnectorPluginCannotUseTheDataPlane (0.00s)
--- PASS: TestExistingPluginsLoadUnchanged (0.00s)
--- PASS: TestExamplePluginRoundTrip (0.26s)
--- PASS: TestExamplePluginShaMismatchRefused (0.26s)
--- PASS: TestExamplePluginHangTimeoutAndRecovery (0.52s)
--- PASS: TestExamplePluginOversizeRejected (0.61s)
--- PASS: TestExamplePluginStderrRedaction (0.23s)
ok  	github.com/NodeSpy/conductor/internal/plugin	1.887s
```

`TestAConnectorPluginCannotUseTheDataPlane` is the property from the other side: a
connector is never given a `plugin.run`, so it holds no token, so every `host.*` call it
could make is refused — the new direction grants an existing plugin nothing.

### And the e2e fake plugins

Every pre-existing e2e row stayed green (PASS 119 → 123, all four new rows are V), which
includes the connector plugins and runtime plugins the harness drives.

---

## 6. How `host.*` reuses `CtxHandler` (one policy path, not two)

Increment 2 built `CtxHandler` deliberately transport-free. This increment adds a second
transport in front of it and **no second decision**:

```
use: cli   → ctxServer.answer   → CtxHandler.Invoke     → kv/sql/memInvoke → DataGuard
use: <plg> → Client.handleRequest → CtxHandler.InvokeHost → Invoke → …     → DataGuard
```

`CtxHandler.InvokeHost` (`internal/code/engineplugin.go`) is 8 lines: copy
`HostRequest{Kind,Op,Resource,Args}` into a `CtxRequest`, call `Invoke`, copy
`CtxResponse{OK,Value,Error,Refused}` back. No check, no default, no special case.

Authentication and authorization stay split exactly as `ctxsock.go` splits them:

- **The transport authenticates.** `internal/plugin` mints 32 random bytes per run
  (`crypto/rand`), registers them for the duration of that `plugin.run` and deletes them
  on every exit path (`defer`), and compares a presented token with
  `subtle.ConstantTimeCompare` over the registered runs — the same discipline
  `ctxServer.answer` uses. It looks at nothing else.
- **The handler authorizes.** `execPluginEngine` builds `CtxHandler{Guard:
  spec.DataGuard}` per step, so a plugin engine's reach into kv/sql/memory is, op for
  op, the reach a `run: js` step has: the plan write barrier plus the agent-authored
  resource allowlist, then the store's own gates.

The engine holds no store handle, no connection string and no capability beyond the
ability to ask while its run is in flight.

---

## 7. The reference engine plugin

`test/plugins/acme-engine/` — SDK-only (no internal imports), the engine-side twin of
`acme-echo`. It implements `plugin.run`: reads `inputs`, round-trips one value through
`host.kv` (and, for a relational deployment, `host.sql`), deliberately touches a store it
must not reach, and returns outputs. It reports the two denials distinctly — `refused`
for a **policy** refusal (the DataGuard), `denied` for any not-allowed outcome including
a store-level gate like `code_access: none` — because conflating them would send an
operator to the wrong file.

**Go integration (`internal/code`, real subprocess, real stores, real guard):**
`TestReferenceEnginePluginEndToEnd` builds the binary, runs it through
`internal/plugin.Client` with verify-before-execute, drives one step, and asserts the
value landed in a **real boltdb kv store** (the engine has no store to write with, so the
only way it is there is that conductor performed the op), that the ctx document arrived
with its values, that `code:` crossed intact, and that the out-of-allowlist store came
back **refused by the guard**. `TestReferenceEnginePluginWithoutADataPlane` covers the
no-token posture.

**e2e (Group V, `test/e2e/`):** `use: acme-engine` on a trigger in the connectors
daemon. The engine is recorded in install state by `entrypoint-conn.sh` — the e2e has no
network, so the *fetch* is the only part skipped; resolution, verify-before-execute,
spawn, describe, kind+ABI check and dispatch are all the real ones. Four assertions, all
PASS (§1).

One honest note on what V proves: an e2e trigger is **config-authored**, so its
`DataGuard` is nil and the host-side denial V asserts is a store-level gate
(`k9locked`, `code_access: none`) — the same bar K9ctx sets for the `cli` engine. The
**DataGuard** refusal is proven in Go, against the real binary, in
`TestReferenceEnginePluginEndToEnd`.

---

## 8. Mutation results

Both security-critical points were broken on purpose and the tests caught both.

### Mutation 1 — the plugin `host.*` path bypasses the step's guard

`internal/code/engineplugin.go`: `CtxHandler{Guard: spec.DataGuard}` → `CtxHandler{}`.

```
--- FAIL: TestPluginEngineHostCallbackIsGuarded (0.00s)
    engineplugin_test.go:89: an out-of-allowlist store was NOT refused host-side: {OK:true Value:<nil> Error: Refused:false}
--- FAIL: TestReferenceEnginePluginEndToEnd (0.23s)
    engineplugin_test.go:211: the out-of-allowlist store was not refused: refused=false refusal=<nil>
FAIL	github.com/NodeSpy/conductor/internal/code	0.244s
```

Caught by both the unit test and the real-subprocess integration test. Reverted.

### Mutation 2 — the `run_id` capability check is neutralized

`internal/plugin/client.go`: `runFor`'s `ConstantTimeCompare(id, token)` → a comparison
that always matches, so any presented token resolves to a registered run.

```
--- FAIL: TestHostCallbackWithoutAValidRunIDIsRefused (0.00s)
    engine_test.go:145: bad run_id was ACCEPTED: map[ok:true value:v]
    engine_test.go:148: bad run_id: want a refusal naming run_id, got map[ok:true value:v]
    engine_test.go:155: the data-plane handler was reached 2 times — only the authenticated call may reach it
--- FAIL: TestConcurrentRunsGetDistinctTokens (0.00s)
    engine_test.go:292: run 15 got another run's host handler: map[token:d58d4d88… who:d]
    engine_test.go:292: run 7 got another run's host handler:  map[token:b9379428… who:d]
    engine_test.go:292: run 0 got another run's host handler:  map[token:d934efaa… who:l]
    …
FAIL	github.com/NodeSpy/conductor/internal/plugin	0.006s
```

Caught twice over: the direct refusal test *and* the cross-run isolation test, which
shows the concrete consequence — one run's callback reaching another run's data plane.
Reverted.

---

## 9. Decisions made (and the ambiguities behind them)

1. **`KindStep`'s wire value is `"engine"`, not `"step"`.** The daemon compares a
   plugin's declared kind with the kind derived from where it was referenced
   (`config.UseKindEngine == "engine"`). Two different strings would have needed a
   translation table, and a translation table is where a kind check goes wrong. The Go
   identifier says what it serves; the value says where it is declared.

2. **An engine MUST declare `kind: engine`; connectors/runtimes still need not.** An
   absent kind means "an older plugin, trust it to its block" — that leniency exists for
   plugins in the field. There are no engine plugins in the field, so an engine gets the
   strict rule from day one and the leniency never widens.

3. **`plugin.run` carries no per-call timeout.** A code step is the operator's own work
   and may legitimately take minutes; `DefaultCallTimeout` (30s) would break it. It is
   bounded by the caller's ctx (the step's timeout, the run's cancellation, daemon
   shutdown) — the same choice `StartSource` already makes. A transport failure still
   tears the subprocess down, and the crash-loop guard is unchanged.

4. **Refusals are in-band; JSON-RPC errors are for transport problems.** This mirrors
   `CtxResponse` exactly and is what keeps "one ABI" true. It also means an engine can
   distinguish "conductor will not let this step do that" from "the store is down"
   without matching strings — the SDK exposes it as a typed `*Refusal` / `IsRefused`.

5. **The method is the kind.** A `host.kv` body claiming `kind: "sql"` is rejected
   (`CodeInvalidParams`) rather than reconciled: a method name that lies makes every log
   line and audit row about it wrong.

6. **A plugin engine does not require `code:`.** Every builtin non-`cli` engine does,
   but an engine plugin declares its own contract — one may take `code:` as a script,
   another may be driven entirely by `args:`/`env:`. The loader cannot tell which it is
   looking at, so it does not guess; the engine says so itself, at run time. `command:`
   **is** still rejected (that is the `cli` engine's argv).

7. **A plugin engine is local-only.** `use: <plugin>` + `host:` is refused rather than
   silently run locally: the engine is a subprocess of *this* daemon holding the
   transport its ctx callbacks come back on, so shipping it over ssh preserves neither
   half. Same posture the in-process engines already have, same error shape.

8. **Agent-authored plans may only name an engine the operator's config already
   references.** Increment 1 refused plugin engines in plans outright; simply lifting
   that would let a plan name any engine in the official repo (or any `owner/repo` it
   invented) and have conductor fetch and execute it. That is a code-supply-chain
   decision, and it belongs to a human. The check is `cfg.PluginRefs()` containing the
   reference's install key.

9. **Deny-by-default egress for `KindStep` only.** A connector that declares no egress
   gets no allowlist, because connectors predating the manifest declare nothing and call
   the service they exist to call — an empty allowlist there would break them. An engine
   declaring nothing gets an **empty** allowlist (deny-all). There is no engine field to
   break, and "I also reach the internet" is worth saying out loud. Honest about the
   mechanism: the proxy arrives as `HTTP(S)_PROXY`, so it confines a cooperating client
   — the same manifest-level confinement every non-`isolation:` plugin gets. An
   `isolation:` block is what makes it a wall.

10. **`RunHost` is a type ALIAS, not a defined type.** `internal/code` declares the same
    seam in its own words (`code.PluginEngine`) and cannot import `internal/plugin`, so
    the two must be the *identical* type for `*Client` to satisfy that interface. A
    defined type is a near-miss the compiler reports as "wrong type for method Run".

11. **`Spec.Plugin` carries the config layer's classification rather than re-deriving
    it.** `internal/code` taking the answer means the validator, the dispatcher and
    `conductor validate` cannot disagree about whether a name is a plugin.

12. **The e2e seeds install state instead of fetching.** The harness is hermetic (no
    network), and referencing the binary by path would have made it a *host interpreter*
    — increment 1 already decided that a path in `use:` is a program on the box, and
    changing that would break `use: ./venv/bin/python`. Seeding the record
    `conductor init` would have written exercises the real installed-plugin path with
    only the download skipped.

13. **CLI surface follows.** `plugin add --engine`, and `plugin show` now discloses what
    an engine actually is (it executes your code steps and can ask conductor to touch
    your stores) rather than reusing the runtime wording. The "add it to your config"
    snippet prints a `steps:` example for an engine, because `engines:` is not a block —
    an engine's reference lives on the step that runs it.
