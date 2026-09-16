# Code-step engines (2/N): the `cli` engine's ctx data plane

Increment 2 closes the gap increment 1 named: a `use: cli` step got inputs and
outputs but no `ctx.store`/`ctx.sql`/`ctx.memory`. It now has all three, over a
per-run authenticated unix socket, with **every op authorized host-side** by
the same guards an in-process `run: js` step goes through.

Branch `feat/engines-cli-ctx`, based on `main` @ `27f020f`. Commit sha for
this increment: **`__COMMIT_SHA__`** (this file is part of it; the sha is the
commit it lands in — see `git log -1` on the branch).

---

## 1. Files changed

**New — the data plane (internal/code):**

| file | what |
|---|---|
| `internal/code/ctxhost.go` | `CtxRequest`/`CtxResponse`/`CtxHandler` — the single authorization+execution core. Transport-free. |
| `internal/code/ctxsock.go` | `ctxServer` — the per-run 0700 socket + token in front of that handler, and its teardown. |
| `internal/code/ctxclient.go` | `CtxClientMain` — the reference client (the client half of the protocol, in one file). |
| `cmd/conductor/ctxcmd.go` | `conductor ctx …` — ships the reference client as the binary a step can actually run. |

**New — tests:**

| file | what |
|---|---|
| `internal/code/ctxhost_test.go` | the guards: secret-write refusal, out-of-allowlist store, allowed round trip, sql/memory, nil guard, malformed. |
| `internal/code/ctxsock_test.go` | auth, cross-run isolation, 0700 dir, teardown, streaming/concurrency, malformed framing, exported env. |
| `internal/code/ctxcli_test.go` | a real `sh` subprocess driving the real client through the real socket, plus teardown on success/failure/timeout. |

**Modified:**

| file | what |
|---|---|
| `internal/code/cli.go` | `execCLILocal` starts/defers the socket and appends its env; doc comments for the three-half ABI and the remote caveat. |
| `cmd/conductor/main.go` | one `case "ctx"` in the dispatch (next to the existing hidden `sandbox-net`). |
| `docs/wiki/Code-Steps.md` | new "The ctx data plane (`cli`)" section: helper, wire protocol, auth/isolation, local-only caveat; three stale "cli has no ctx handles" claims corrected. |
| `test/e2e/config/connectors.e2e.yaml` | `stores:` + the `ctxrw` step on the existing K9 cli-engine trigger. |
| `test/e2e/run.sh` | the `K9-ctx` assertion. |

Nothing else. `pkg/plugin/wire.go` and `internal/plugin` are untouched (§5).

---

## 2. The wire protocol and auth model

### Transport

A **unix stream socket** speaking **JSON Lines** in both directions: one JSON
request object per line, one JSON response per line, responses in request
order. A connection may carry many pairs; a step may open many connections.
(The server decodes with `encoding/json`'s streaming decoder, so a client that
pretty-prints still works — "one per line" is what a client should *write*, not
a parser restriction it can trip over.)

```
--> {"token":"…","kind":"kv","op":"set","resource":"cache","args":["ns","k",{"v":1}]}
<-- {"ok":true}
--> {"token":"…","kind":"kv","op":"get","resource":"cache","args":["ns","k"]}
<-- {"ok":true,"value":{"v":1}}
--> {"token":"wrong","kind":"kv","op":"get","resource":"cache","args":["ns","k"]}
<-- {"ok":false,"refused":true,"error":"ctx: bad or missing token"}
```

### Request (`code.CtxRequest`)

| field | meaning |
|---|---|
| `token` | the per-run token; required on **every** request |
| `kind` | `kv` \| `sql` \| `memory` |
| `op` | the operation, spelled as the in-process bindings spell it |
| `resource` | the **defined** store (kv/sql); omitted for `memory`, which has no store dimension |
| `args` | **positional, exactly `kvInvoke`'s convention** — `internal/code/kvbind.go:25` `func kvInvoke(guard DataGuard, store, op string, args []any)`, whose doc reads *"Ops take positional JSON-shaped args, ns first"*. So `kv get ns key` → `["ns","key"]`, `kv set ns key v` → `["ns","key",v]`, `kv slice ns key 1 3` → `["ns","key",1,3]`. `sqlInvoke` takes `(sql, args?)`; `memInvoke`'s ops take their own positional args and ignore `resource` (it resolves the scope itself, via `memScopeOf`). |

### Response (`code.CtxResponse`)

| field | meaning |
|---|---|
| `ok` / `value` | success and its JSON result. An absent read is `value: null`, not an error — the same fold `kvbind.go`'s `nullable` does in-process. |
| `error` | the failure text |
| `refused` | **true when this execution's `DataGuard` denied the call** — the `no_secret_egress` write barrier or the agent-authored store/scope allowlist. Also set for a bad token. |

`refused` is a *typed* refusal, not a string match. `CtxHandler.Invoke` wraps
the caller's `DataGuard` in a closure that sets a flag when the inner guard
returns an error; the three invokers return a guard denial verbatim
(`kvInvoke`/`sqlInvoke`/`memInvoke` all `return nil, err`), so the flag
identifies the error that came back. The guard is still called from exactly one
place — inside the invoker — so this adds a **label**, not a second enforcement
point.

Deliberate scope of `refused`: it marks a denial *this execution's policy*
imposed. Denials that are properties of the store's own configuration (an
undefined store, a backend that can't serve the op, `code_access: none`, a
reserved memory bucket) come back as plain `ok:false` + `error`. Documented on
`CtxResponse` and in the wiki.

### Auth model — the `run_id` capability

The capability is the **(socket, token) pair, minted per run**, carried to the
child in its environment:

```
CONDUCTOR_CTX_SOCK     the unix socket path
CONDUCTOR_CTX_TOKEN    a 256-bit random token (hex), required on EVERY request
CONDUCTOR_CTX_HELPER   conductor's own binary — `$HELPER ctx …` is the client
```

Two independent walls, because either alone has a hole:

1. The socket lives in a **fresh `0700` directory with a random name**
   (`os.MkdirTemp`), and the socket inode is chmod'ed `0600`. Another user on
   the box cannot connect at all.
2. The **token** means a process that somehow reaches the socket — a same-uid
   neighbor, a stale descriptor, a sibling step that guessed a path — still
   cannot use it. Compared with `subtle.ConstantTimeCompare`.

Authentication happens in `ctxServer.answer`, **before** `CtxHandler.Invoke`:
an unauthenticated caller has no policy to evaluate it against, so the guard is
never consulted and no store is ever touched. `TestCtxSockToken` asserts exactly
that (a counter in the guard stays false, and the store stays empty).

There is no daemon-wide credential and no shared secret.

**Env ordering.** `cmd.Env = spawnBaseEnv() + spec.Env`, then the ctx variables
are appended **last**. `os/exec` lets later entries win, so a step's own `env:`
cannot shadow the socket address or the token with one it chose
(`TestCLICtxEnvExported` sets `CONDUCTOR_CTX_SOCK=/tmp/mine.sock` and asserts
the child sees the real one).

### The helper (requirement 3)

`conductor ctx` is the reference client, shipped as the binary a step is
guaranteed to be able to run:

```
conductor ctx kv     <store> <op> [arg...]
conductor ctx sql    <store> <op> <sql> [json-args]
conductor ctx memory <op> [arg...]
```

Each argument is read as JSON when it parses and as a plain string when it does
not (`3` a number, `'{"a":1}'` an object, `hello` the string). The value is
printed as JSON on stdout. Exit codes: **0** ok · **1** error · **2** usage ·
**3** refused by policy — so a shell can branch on "conductor will not let me
do this" without parsing text. Nothing about it is privileged: a step that
prefers Python's `socket`+`json` or Go's `net.Dial` gets identical treatment.

(`nc`/`socat` was rejected as specified: platform-divergent, buffers wrong, and
gives a step no way to tell a policy refusal from a broken pipe.)

---

## 3. How the handler reuses the existing guards

`CtxHandler.Invoke` (`internal/code/ctxhost.go`) dispatches to the **same three
functions the in-process engines call**, carrying the **same `Spec.DataGuard`**:

| kind | handler calls | which applies |
|---|---|---|
| `kv` | `internal/code/kvbind.go:25` `kvInvoke` | `DataGuard` → `kv.Use` → `internal/kv/backend.go:75` `kv.CheckCapability` |
| `sql` | `internal/code/sqlbind.go:23` `sqlInvoke` | `DataGuard` → `sqlstore.Use` → `internal/sqlstore/sqlstore.go:62` `(*Store).CheckCodeAccess` |
| `memory` | `internal/code/membind.go:23` `memInvoke` | `memScopeOf` → `memory.CheckOp` → `DataGuard` |

These are literally the functions behind `ctx.store(…)` in js
(`kvbind.go:kvInvokeJSON`), go-embed (`kvbind.go:KVHandle.call`), risor
(`kvbind.go:kvRisorStoreFn`) and lua (`kvbind.go:luaStoreFn`). There is no
second code path and no duplicated policy.

The `DataGuard` itself is unchanged and still installed by the flow layer:
`internal/flow/flow.go:1316` sets `DataGuard: r.planDataGuard(ctx, t)` on the
`code.Spec`, and `internal/flow/plan.go:487` `(*Runner).planDataGuard` is

- the **agent-authored resource allowlist** — `internal/flow/resources.go:307`
  `(*resourcePolicy).storeOK` for kv/sql, `resources.go:319`
  `(*resourcePolicy).memoryScopeOK` for memory scopes;
- the **`no_secret_egress` write barrier** — `plan.go:518`, gated on
  `plan.go:528` `dataValueWrite` (which mirrors `kvbind.go:23`
  `kvValueWrites`).

So a `use: cli` step's reach into kv/sql/memory is, op for op, the reach a
`run: js` step has. Nothing in `internal/flow` needed to change: the guard was
already on the `Spec`, and this increment only gave a subprocess a way to ask.

**The subprocess never gets a handle.** It receives a socket path and a token —
the ability to *ask*, one op at a time. No store handle, no connection string,
no capability object crosses the process boundary.

### Increment 3's reuse

`CtxHandler` is transport-free by construction (it takes a decoded request and
returns a response; it does not look at `Token` — authentication belongs to the
transport, authorization to the handler). The plugin engine's `host.kv`/
`host.sql`/`host.memory` methods put the JSON-RPC wire in front of the same
handler without re-deciding anything.

---

## 4. Teardown on every exit path

`execCLILocal` (`internal/code/cli.go`) starts the server before the child and
`defer`s `Close`:

```go
ctxSrv, err := startCtxServer(CtxHandler{Guard: spec.DataGuard})
if err != nil { return nil, err }
defer ctxSrv.Close()
```

Because it is a `defer` on the function that owns the child, it runs on every
return: clean exit, non-zero exit, `context` timeout, cancellation (both kill
the child via `exec.CommandContext`, so `cmd.Run` returns), and every error
return below it.

`ctxServer.Close` is ordered so the filesystem artifact is gone **before** we
wait on anything:

1. `s.closed = true` under the mutex (idempotent — a second `Close` is a no-op);
2. `ln.Close()` — no new connections;
3. `os.RemoveAll(dir)` — the path a leaked grandchild might still hold is dead;
4. close every open connection — in-flight handlers unblock;
5. `wg.Wait()` — in-flight *ops* finish rather than being abandoned mid-write
   (bounded by the stores they are already inside).

Tested by `TestCLICtxSocketTornDown`, which drives a real subprocess that
records `$CONDUCTOR_CTX_SOCK` to a file before doing its thing, across three
subtests — `success`, `failure` (exit 3), `timeout` (a 300 ms deadline against
`exec sleep 10`) — and asserts both the socket and its directory are gone once
`Exec` returns. `TestCtxSockCloseRemovesEverything` covers idempotence and that
the address no longer accepts.

---

## 5. Plugins untouched

```
$ git diff --stat HEAD -- pkg/plugin internal/plugin
(no output)
```

The plugin JSON-RPC protocol and the out-of-process plugin machinery are
unchanged. This increment adds no `host.*` method, no wire field, and no
dependency from `internal/code` on either package (`ctxhost.go`/`ctxsock.go`/
`ctxclient.go` import only stdlib).

---

## 6. Cross-run isolation proof

`TestCtxSockCrossRunIsolation` (passes under `-race`) starts **two** servers —
two runs — and asserts:

- their socket paths, their directories and their tokens all differ;
- **run B's token presented to run A's socket is refused** (`ok:false`,
  `refused:true`), and vice versa;
- each still works with its own token;
- closing run A leaves run B entirely alone.

There is no shared secret to compare against and no daemon-wide credential:
`s.token` is 32 bytes from `crypto/rand`, per `startCtxServer` call, and
`answer` compares against *that server's* token with
`subtle.ConstantTimeCompare`.

`TestCtxSockDirIsPrivate` asserts the containing directory is exactly `0700`
and the socket inode has no group/other bits.

`TestCtxSockToken` proves the refusal is pre-authorization: an empty token, a
wrong token, a token with an appended byte and a token with a prepended byte
are all refused, the `DataGuard` is never called, and the store is untouched.

---

## 7. Verification

Everything below was run on this branch, in this worktree.

### gofmt / build / vet

```
$ gofmt -l .
(exit 0, empty = clean)

$ CGO_ENABLED=0 go build ./...
(exit 0, no output)

$ CGO_ENABLED=0 go vet ./...
(exit 0, no output)
```

### `go test ./...`

```
$ CGO_ENABLED=0 go test ./... 2>&1 | tail -8
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeopencode	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakepaseo	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fixer	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/mockgithub	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/sinkcatcher	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-echo	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-runtime	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-ticker	[no test files]
```

No `FAIL` lines.

### `go test -race ./...`

`-race` requires cgo, so this one run sets `CGO_ENABLED=1` (builds stay
`CGO_ENABLED=0`).

```
$ CGO_ENABLED=1 go test -race ./... 2>&1 | tail -12
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	1.084s
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeacp	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeagentdeck	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakecli	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakeopencode	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fakepaseo	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/fixer	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/mockgithub	[no test files]
?   	github.com/NodeSpy/conductor/test/e2e/services/sinkcatcher	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-echo	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-runtime	[no test files]
?   	github.com/NodeSpy/conductor/test/plugins/acme-ticker	[no test files]
race-exit=0

$ CGO_ENABLED=1 go test -race ./... 2>&1 | grep -c '^ok'
40
$ CGO_ENABLED=1 go test -race ./... 2>&1 | grep -i 'FAIL\|DATA RACE'
(none)
```

### The new tests, under `-race`

```
$ CGO_ENABLED=1 go test -race ./internal/code/ -run Ctx -v -count=1
--- SKIP: TestCtxClientHelperProcess (0.00s)      # the re-exec'd client, not a test
--- PASS: TestCLICtxStoreRoundTrip (3.17s)        # real subprocess, real socket, real store
--- PASS: TestCLICtxEnvExported (0.00s)
--- PASS: TestCLICtxIsOptIn (0.01s)
--- PASS: TestCtxClientWithoutDataPlane (0.00s)
--- PASS: TestCLIRemoteHasNoCtxSocket (0.01s)
--- PASS: TestCLICtxSocketTornDown (0.31s)        # success / failure / timeout
--- PASS: TestCtxHandlerRoundTrip (0.00s)
--- PASS: TestCtxHandlerRefusesSecretWrite (0.00s)
--- PASS: TestCtxHandlerRefusesOutOfAllowlistStore (0.00s)
--- PASS: TestCtxHandlerGuardsSQLAndMemory (0.02s)
--- PASS: TestCtxHandlerNilGuardStillHitsStoreGates (0.01s)
--- PASS: TestCtxHandlerMalformed (0.00s)
--- PASS: TestCtxSockToken (0.00s)
--- PASS: TestCtxSockCrossRunIsolation (0.00s)
--- PASS: TestCtxSockDirIsPrivate (0.00s)
--- PASS: TestCtxSockCloseRemovesEverything (…)
--- PASS: TestCtxSockStreamAndConcurrency (…)
--- PASS: TestCtxSockMalformedRequest (…)
--- PASS: TestCtxSockEnv (…)
```

`TestCLICtxStoreRoundTrip` is the requirement-3 test: a real `sh` subprocess
runs the **shipped** `CtxClientMain` (the test binary re-execs itself into
client mode, so there is no second client implementation), reads `ns/seed` out
of a real boltdb store through the socket, writes two values back (a string and
a composed object, proving the JSON round trip keeps types), and gets **exit 3**
on both the `no_secret_egress`-shaped write and the out-of-allowlist store —
with the store asserted empty afterwards for both.

### e2e (docker 29.5.1, `MODE=stub`)

```
$ MODE=stub bash test/e2e/run.sh
K      K9-cli    PASS  K9 cli engine bridged ctx-on-stdin and stdout outputs
K      K9-ctx    PASS  K9ctx cli engine round-tripped ctx data over the per-run socket, guards enforced host-side
K      K4-ls     PASS  K4 conductor connectors ls lists the configured connectors
PASS=119  FAIL=0
EXIT=0
```

The increment-1 K9 scenario is extended with a `ctxrw` step on the same trigger:
a `use: cli` `sh` snippet that, through `$CONDUCTOR_CTX_HELPER` over the socket,
creates a table, inserts the comment body, reads it back, and then hits two
things the **daemon** refuses — a `code_access: none` store and an undefined
store. It posts `K9ctx ok` only when all three hold. All previously-green
scenarios stayed green (118 → 119, the +1 being K9-ctx).

---

## 8. Decisions and notes

**The remote `host:` decision — omit the socket, don't refuse.** A
`host:`-remote `cli` step gets **no** data plane. Refusing such a step was the
other option and was rejected for two reasons: it would break every existing
remote `cli` step that never wanted the data plane, and conductor *cannot
detect the case anyway* — nothing in the config declares that a command intends
to use ctx (see the next note). So the variables are simply absent, and the
reference client fails with *"no ctx data plane in this environment
(CONDUCTOR_CTX_SOCK/CONDUCTOR_CTX_TOKEN unset) — available to a LOCAL `use: cli`
step only"* rather than a remote step silently reading a different store. The
step keeps its inputs and outputs; durable state on a remote step goes through
`kv.*`/`sql.*`/`memory.*` verbs in surrounding steps, as before. Documented on
`execCLIRemote` and in the wiki's "Local only" paragraph; asserted by
`TestCLIRemoteHasNoCtxSocket` and `TestCtxClientWithoutDataPlane`.

**Ambiguity, resolved and flagged: "when the step's policy grants any ctx
access".** There is no per-step ctx *declaration* surface in the config today —
no `ctx:` key, no capability list on a code step. The thing that grants or
refuses ctx access is the `DataGuard`, and it decides **per call**, not per
step. The in-process engines reflect this: js/go-embed/risor/lua expose
`ctx.store`/`ctx.sql`/`ctx.memory` **unconditionally** and let the guard decide
each op. So `cli` matches them: the socket is offered to every **local** cli
step, and authorization happens per request. Inventing a new config key would
have been a new surface out of this increment's scope, and gating on "guard is
non-nil" would have made the data plane available to *agent-authored* steps and
unavailable to *config-authored* ones — exactly backwards. This keeps every
existing config working (a command that ignores the env vars is unchanged) and
preserves the host-side guard invariant.

**The e2e store type.** The K9ctx scenario uses `sqlite` stores rather than
`boltdb`. A first run with `boltdb` broke the existing **K4-ls** scenario
(`kv: open /data/k9kv.db: timeout`): boltdb holds an exclusive file lock for the
running daemon's lifetime, and K4 runs `conductor connectors ls` against the
same config *in the same container*, which builds the stores. That is a
pre-existing interaction between boltdb and the introspection command, not
something this increment should change, so the e2e proof moved to sqlite (which
allows concurrent opens) and the comment in `connectors.e2e.yaml` says why. The
`ctx.store`/kv face is covered end-to-end by `TestCLICtxStoreRoundTrip` (a real
subprocess against a real boltdb store, under `-race`) and, in e2e, by the
undefined-store refusal.

**`docs/design/code-step-engines.md` is not in the repo** on this branch (nor is
any file matching `host.kv`/`run_id`-capability language under `docs/design/`).
The design was followed from the brief: the `host.kv`/`sql`/`memory` surface
shape and the `run_id` capability-token model are implemented as described, and
grounded in the real code (`kvbind.go`/`sqlbind.go`/`membind.go`/`plan.go`) for
everything else.

**A note on the same-uid boundary.** The socket is protected against other
*users* and other *runs*. It is not, and cannot be, protected against another
process running as the daemon's own uid — such a process can already read
`/proc/<pid>/environ` of the child, and has far more direct routes to the
daemon's data. The two walls are sized to the threat they can actually address.
