# Code-step engines as plugins

Status: **SHIPPED** as v0.11.0 (PRs #79, #80, #81). The body below is the design
exploration that drove the build and is kept as the rationale record (threat
model §7, alternatives §13). The **As built** section immediately below is the
authoritative account of what actually shipped; where the two disagree, As built
wins. The user-facing reference is the wiki (`Steps` / `Code-Steps` / `Plugins`).

## As built (v0.11.0)

Shipped in three additive increments; the live config loads unchanged on each, so
the box auto-updates with no migration.

- **1 — engine kind + `cli` engine** (#79). A new `UseKindEngine` resolves
  **connector-style** (builtin → `conductor-plugins/engines/<name>` → `owner/repo`
  → local), builtin registry `{cli, js, go-embed, risor, lua}`. A step selects its
  engine with `use: <engine>`; `run:` stays a back-compat alias; `command:` is the
  argv; the built-in **`cli`** engine runs a command with inputs on stdin +
  outputs via `ParseOutputs`.
- **2 — `cli` ctx data-plane** (#80). `internal/code/ctxhost.go` `CtxHandler` is
  the single host-side authorize+execute core (reusing `kvInvoke`/`sqlInvoke`/
  `memInvoke` behind the step's `DataGuard`); `cli` reaches `ctx.store/sql/memory`
  over a per-run, 0700, token-authenticated unix socket, with `conductor ctx` as
  the reference client.
- **3 — plugin engine protocol** (#81). `KindStep`, `plugin.run`, and the
  plugin→daemon `host.kv/sql/memory` callback channel, all routed back into the
  same `CtxHandler`. A reference engine (`test/plugins/acme-engine`) proves the
  out-of-process transport.

**Two premises in the exploration below were overturned by the build:**

1. **Resolution is connector-style, not pack-style.** §1 argues for a reserved
   namespace / no bare-name→official fetch. The owner chose "same as connectors
   and runtimes," so a bare engine name resolves to the official
   `conductor-plugins/engines/<name>` — the accidental-fetch worry is moot because
   `cli` is the blessed zero-plugin default (`use: cli, command: ruby` reaches a
   PATH interpreter).
2. **The surface is additive; there is no breaking `use:`→`call:` rename.**
   Step-level `use:` was never a real key (a stale doc comment claimed it); the
   workflow-call field is `workflow:`, which still works. So `use:` = engine was a
   free slot, and `call:` shipped as an additional alias for `workflow:` — nothing
   broke, no config-first deploy was needed.

**Versioning:** the engine ABI is negotiated by a new `Decl.ABI`, NOT a
`ProtocolVersion` bump — `internal/plugin/client.go` compares `ProtocolVersion`
for exact equality, so a bump would refuse every installed connector/runtime
plugin. `ProtocolVersion` stays 1; existing plugins are unaffected (proved by
`TestExistingPluginsLoadUnchanged` / `TestConnectorWireIsUnchangedByTheEngineAdditions`).

---

Every question the exploration below can answer is answered as a **proposal with
rationale**; the three owner-level choices at §14 were resolved as: selection =
`use:` (additive), `host:` engines deferred, full ctx op surface shipped.

Read `docs/design/use-unification.md` first (especially §D, the honest account
of what the permission manifest does and does not enforce). The resolution
rules, install model, and trust story below are that design's, reused verbatim;
nothing here re-litigates them.

## Problem

`run:` code steps currently support **four** embedded-language engines — `js`
(QuickJS via `github.com/fastschema/qjs`, i.e. wazero), `go-embed` (the yaegi Go
interpreter plus a filtered copy of `stdlib.Symbols`), `risor`
(`github.com/risor-io/risor` plus nine of its modules), and `lua` (gopher-lua) —
all compiled into the conductor binary and dispatched from one switch
(`internal/code/code.go:113` `Executor.Exec`). Adding a language is a core
change, and every build carries every interpreter whether or not a config uses
one.

> **Four, not three.** `risor` (`internal/code/risor.go`) is easy to miss — it
> shipped after the other engines and is handled everywhere they are:
> `internal/config/connectors.go:1697` (local-only validation),
> `internal/code/hostinterp.go:104` (remote rejection), and the timeout
> regression table (`internal/code/timeout_test.go:18`). Any migration that
> forgets it leaves a fourth interpreter linked in and a `run: risor` config
> broken. It is in scope throughout this document.

The deeper problem is **who decides which languages exist**. Today it is the
maintainer. We want the opposite: the set of languages a code step can be
written in should be **open and community-managed**, the same way connectors and
runtimes already are — you publish an engine, anyone can reference it, no core
change, no maintainer gatekeeping.

## The cut

> **conductor owns the contract; the community owns the languages.**

conductor owns and versions the *engine ABI* — how a snippet receives its
inputs, how it returns outputs, and the `ctx` data-plane it may reach. The
community owns the *engines* — the actual interpreters/runtimes (Ruby, WASM,
Python-embedded, a stricter JS, whatever), fetched, trusted, and sandboxed
through the existing `use:` machinery.

A direct consequence: the built-in engines stop being privileged. `js` /
`go-embed` / `risor` / `lua` become *official* engine plugins (in
`conductor-plugins`), resolved the same way as a third-party engine — not a
hardcoded `switch` in the binary. They can stay bundled for zero-config/offline
UX, but the model treats them as replaceable. The binary can eventually shed the
interpreters entirely; that slimming falls out of this design rather than being
a separate project.

**What this trade actually costs, stated up front.** Today conductor can promise
"a `run: js` snippet cannot open a socket", because conductor *is* the sandbox:
the yaegi import allowlist (`internal/code/goembed.go:27` `goEmbedAllowlist`),
the four Lua libraries with the chunk loaders nil'd (`internal/code/lua.go:42`),
the risor globals opt-out (`internal/code/risor.go:26` `risorGlobals`), QuickJS
inside wazero. Under the plugin model that promise moves to the *engine author*.
conductor's promise narrows to: **the engine declared what it needs, the operator
saw that declaration, the engine cannot exceed it where the OS lets us enforce,
and the `ctx` data-plane is authorized host-side no matter what the engine
does.** §7 is explicit about which half of that is a real boundary and which
half is a visible manifest.

## What already exists (and is reused)

- **`use:` resolution** (`internal/config/use.go` `ParseUse`): one kind-aware
  search path — builtin → official repo → `owner/repo[/component]` → host →
  local path. `UseKind` (`use.go:39`) already has three members and a `Dir()`
  switch; adding a fourth is additive.
- **Plugin protocol + SDK** (`pkg/plugin/wire.go`, `internal/plugin`):
  newline-delimited JSON-RPC 2.0 over stdio, `ProtocolVersion = 1`. A plugin
  `Decl` advertises `Verbs` with `Options`/`Outputs` `Schema`s and a
  `Capabilities` permission manifest (egress / commands / fs / spawns).
- **A transport that is already bidirectional.** `internal/acp/jsonrpc.go:121`
  `Conn.dispatch` serves an inbound request *on its own goroutine*, with the
  comment "so the handler may Call back into this peer … without deadlocking the
  read loop". The daemon side needs no new transport — only a handler. The one
  thing standing in the way is a deliberate refusal:
  `internal/plugin/client.go:367` `pluginHandler.HandleRequest` returns
  `CodeMethodNotFound` with the message *"daemon exposes no plugin callbacks"*.
  That line is the gap this design closes.
- **Resource scoping** (`Field.Scope`, `pkg/plugin/wire.go:83`): a verb option
  tagged with a scope dimension has its *value* gated to what the dispatch's own
  trigger points at plus the operator allow-list
  (`internal/flow/resources.go`). The `store` dimension already exists
  (`internal/config/agentauthored.go:178` `DimStore`).
- **Trust + sandbox**: `plugin_trust` allow-list
  (`internal/config/pack_trust.go` `PluginSourceAllowed` — the official repo is
  default-trusted, third-party needs an entry, and `globMatch` is
  segment-bounded on purpose), verify-before-execute
  (`internal/plugin/verify.go`), minimal env (`internal/plugin/spawn.go:20`
  `spawnBaseEnv` → `sandbox.MinimalEnv`), PATH confined to declared `Commands`
  (`internal/plugin/manifest.go`), and the opt-in OS sandbox
  (`internal/sandbox`, namespaces/cgroups/egress proxy).
- **Supervision** (`internal/plugin/client.go`): per-call timeout, restart
  burst window, and a lifetime restart cap (`restartBurst`/`restartWindow`/
  `restartLifetimeCap`, `client.go:27`) so a crashing plugin degrades to "that
  plugin is down" rather than crash-looping the daemon.
- **The `ctx` data-plane + guards** (`internal/code`): `ctx.store(name)`,
  `ctx.sql(name)`, `ctx.memory`, each funnelled through one dispatcher per kind
  — `kvInvoke` (`kvbind.go:25`), `sqlInvoke` (`sqlbind.go:23`), `memInvoke`
  (`membind.go:23`) — and vetted by `DataGuard` (`code.go:72`), the per-store
  capability check (`kv.CheckCapability`, `internal/kv/backend.go:75`), and the
  SQL `code_access` mode (`internal/sqlstore/sqlstore.go:62`
  `Store.CheckCodeAccess`).
- **`ParseOutputs`** (`internal/code/outputs.go:22`) and `wrapValue`
  (`code.go:136`): the two halves of the output contract, shared across engines,
  feeding `{{.steps.<id>.outputs.*}}`.

> **The kv surface is `ctx.store(<name>)`, and it is sixteen ops, not ten.**
> There is no `ctx.kv`. `kvOps` (`internal/code/kvbind.go:189`) is `get set setnx
> merge delete incr append remove contains list first last index slice len pop` —
> the six list-shaped ops (`first/last/index/slice/len/pop`) are easy to overlook
> because only the list-backed backends serve them. An ABI that froze ten of
> sixteen would silently drop working configs; §5.4 pins all sixteen.

The gap is small and specific: the protocol has daemon→plugin *invoke* and
one-way plugin→daemon *events* (`MethodEvent`), but **no plugin→daemon
request/response** — which is precisely what a running snippet needs to reach
`ctx.store`/`ctx.sql`/`ctx.memory`.

---

## 1. Selection surface

`run:` stays the field. A **plugin engine is always named by a qualified
reference**; a bare name keeps exactly the meaning it has today.

```yaml
steps:
  - run: lua                              # builtin engine (bundled) — unchanged
    code: "return { n = ctx.pr.number }"
  - run: conductor-engines/wasm@^1        # OFFICIAL engine registry (reserved namespace)
    code: "..."
  - run: acme/conductor-engines/wasm@^1   # third-party repo/component
    code: "..."
  - run: ./bin/my-engine                  # local dev engine
    code: "..."
  - run: node                             # host interpreter on PATH — unchanged
    code: "..."
```

### 1.1 Resolution order (proposed, and deliberately NOT the connector rule)

1. **Builtin engine name** (`js`, `go-embed`, `risor`, `lua`, and `go` which is
   the toolchain path) → the in-binary implementation, offline, zero config.
2. **A qualified reference** — `conductor-engines/<name>`, `owner/repo[/comp]`,
   `host.tld/owner/repo`, or a path (`./`, `../`, `/`, `~/`) → an engine plugin,
   resolved through `ParseUse`.
3. **Anything else** (a bare non-builtin name) → a **host interpreter on PATH**,
   exactly as today (`internal/code/hostinterp.go:30` `execHostLocal`).

**Rationale — why engines do not get bare-name→official resolution.** Connectors
and runtimes do (`use.go:392`, cases 1 & 2) because a bare name there is already a
meaningful first question: "is this in the binary?" `run:` is different. It has
an established, load-bearing bare-name meaning that predates engines entirely —
*any interpreter on this box's PATH*. If a bare non-builtin `run:` name fell
through to the official engine repo, then `run: ruby` on a box without ruby
would stop being "ruby is not installed" and become **a network fetch of a
binary conductor then executes**. That is the exact ambiguity the pack namespace
was introduced to remove, and `use.go:119-137` already spells out the reasoning
in the code:

> *"It exists so that reaching the blessed registry over the network LOOKS like
> it: the old spelling was a bare name, indistinguishable from any other
> unqualified string… Naming the registry costs one segment and removes the
> ambiguity entirely."*

Engines get the **pack treatment, not the connector treatment**. Concretely:

```go
// internal/config/use.go

// EnginesNamespaceAlias is the RESERVED leading segment naming the official
// engine registry at the reference site: `run: conductor-engines/wasm`.
// Mirrors PacksNamespaceAlias, for the same reason: a `run:` value that
// reaches the network must look different from one that names a local
// interpreter, because `run:` already means "an interpreter on PATH".
const EnginesNamespaceAlias = "conductor-engines"
```

Unlike packs, engines are binaries, so the alias resolves to the ordinary plugin
repo — `OfficialRepo` (`NodeSpy/conductor-plugins`), component `engines/<name>`,
tag prefix `engines/<name>/`. `PluginSourceAllowed` already default-trusts
`OfficialSource`, so an official engine needs no `plugin_trust` ceremony while a
third-party one still does.

### 1.2 The new `UseKind`

```go
// internal/config/use.go

// UseKindStep is a code-step ENGINE: the implementation of the `run:` step
// form for one language. The kind string must equal the plugin's own
// Decl.Kind, which internal/plugin/resolve.go:checkDeclKind compares as a
// plain string.
const UseKindStep UseKind = "step"

func (k UseKind) Dir() string {
	switch k {
	case UseKindRuntime:
		return "runtimes"
	case UseKindPack:
		return "packs"
	case UseKindStep:
		return "engines" // see note
	default:
		return "connectors"
	}
}
```

> **Note on the one asymmetry.** For every other kind, `Dir()` is the kind name
> pluralized. For engines it is not: the wire kind is `"step"` (what the plugin
> *provides* — the implementation of a step form, which is how `Decl.Kind` is
> worded for connector/runtime too) while the directory is `engines` (what an
> operator *browses* — `conductor-plugins/engines/lua`, install path
> `<state>/plugins/engines/lua`, install key `engines/lua`). The kind string is
> constrained: `internal/plugin/resolve.go:253` compares `string(decl.Kind) ==
> ref.Kind()` for equality, so the two must match exactly. The directory is
> free. Spending the freedom on readability is deliberate; it is called out here
> so nobody later "fixes" one to match the other.

Touch points that enumerate kinds and need the new member:
`UseKind.Dir()`/`officialComponentFor` (`use.go:55`, `:79`),
`otherKind` (`use.go:593`), `plugin.Spec.Key()` (`internal/plugin/plugin.go:111`),
`Manager.specsOfKind` (`internal/plugin/manager.go:103`), and
`PruneOrphans`'s directory scan (`internal/plugin/resolve.go:304`, currently the
literal `[]string{"connectors", "runtimes"}`).

### 1.3 Deriving the plugin set

`Config.PluginRefs()` (`internal/config/plugins.go:80`) walks `ConnectorsMap`
and `Runtimes`. It gains a third pass over every step's `Run` value — across
triggers, workflows, hooks, and pack-instantiated steps — collecting the ones
that parse as a qualified reference. Engine refs carry no credentials and no
per-instance config, so the union logic that merges `Network`/`AllowSecrets`
across connector instances does not apply; the only per-reference fields are
`Isolation` (§9.4) and the version constraint. Two steps naming the same engine
share one `PluginRef`, one install, one process.

`validatePluginRefs` (`plugins.go:133`) already refuses two references claiming
the same implementation name from different sources; that check covers engines
for free once they are in the derived map.

### 1.4 Shell interpreters stay as-is

`sh`, `bash`, `node`, `python`, a bare `go`, or an interpreter path are NOT
engines. They shell out to a real binary on PATH (locally, or on a `hosts:`
target over `internal/hosts`), need no embedded runtime, and carry no ABI.
Expressing them as trivial engines would add a process hop and an ABI to a path
that has neither, for uniformity nobody asked for. This is now a **decision, not
an open question** (formerly O5): they stay. If a uniform model is ever
wanted, the cheap version is an official `exec` engine that shells out — which
anyone can publish without a core change, which is the whole point.

Back-compat: `run: js` / `go-embed` / `risor` / `lua` / `go` / `bash` / `python` /
`<path>` keep working unchanged, offline, with no config.

---

## 2. The engine ABI (what conductor owns and versions)

An engine plugin declares `Kind: "step"` and one entry point. A run carries four
things:

1. **Inputs** — the step's rendered template context, handed to the engine as a
   single JSON document (the `ctx` the snippet reads). Already scrubbed by
   `codeCtx` (`internal/flow/flow.go:1331`), which strips the legacy `steps`
   index, `secrets`, and `vaults`.
2. **Code** — the snippet source, plus optional `args`/`env` (§7.6 tightens what
   may cross for those).
3. **Outputs** — the engine returns outputs, normalized through the existing
   `wrapValue`/`ParseOutputs` semantics → `{{.steps.<id>.outputs.*}}`.
4. **`ctx` data-plane** — the snippet's `ctx.store` / `ctx.sql` / `ctx.memory`
   calls, delivered as **host callbacks**. The engine never touches a store
   directly; it asks the daemon, which authorizes and executes.

The ABI is versioned independently of the wire protocol (§3.4), because every
community engine implements it and we must be able to evolve it without breaking
the ecosystem.

---

## 3. Concrete wire schema (`pkg/plugin/wire.go`)

All additions are to the public SDK, which `internal/plugin/plugin.go` aliases —
so there stays exactly one source of truth for the schema.

### 3.1 Kind and method names

```go
// Kind values.
const (
	KindConnector Kind = "connector"
	KindRuntime   Kind = "runtime"
	// KindStep is a code-step ENGINE: it implements the `run:` step form for
	// one language. It has no verbs, no events, and no connection — an engine
	// receives no credentials, ever (see RunRequest).
	KindStep Kind = "step"
)

// Wire method names (added).
const (
	// MethodRun (daemon→plugin request) executes one code step.
	MethodRun = "plugin.run"
	// MethodCancel (daemon→plugin NOTIFICATION) asks the engine to abandon a
	// run whose deadline passed or whose dispatch was cancelled. One-way: the
	// daemon does not wait for it (see §9.5 for what happens if ignored).
	MethodCancel = "plugin.cancel"

	// The HOST CALLBACK set (plugin→daemon request/response) — the new
	// direction. An engine reaches the step's data plane through these and
	// through nothing else.
	MethodHostKV     = "host.kv"
	MethodHostSQL    = "host.sql"
	MethodHostMemory = "host.memory"
)
```

### 3.2 `plugin.run`

```go
// RunRequest is the daemon→plugin code-step call.
//
// There is deliberately NO Connection field. A connector plugin receives its
// instance's resolved credentials (InvokeRequest.Connection); an engine
// receives none, because an engine has no instances and no credentials of its
// own — everything it may reach, it reaches through a host callback the daemon
// authorizes per call.
type RunRequest struct {
	// Instance is the engine reference as the config wrote it ("lua",
	// "conductor-engines/wasm@^1"). A label for the engine's own logs; it
	// grants nothing.
	Instance string `json:"instance,omitempty"`
	// ABI is the negotiated engine-ABI version for this run (§3.4). An engine
	// that is handed a version it did not advertise must refuse the run.
	ABI int `json:"abi"`
	// RunID is the CAPABILITY that correlates a host callback to this run.
	// It is an unguessable per-invocation token, not the human run id — see
	// §7.5 for why that distinction is load-bearing.
	RunID string `json:"run_id"`
	// RunLabel is the human "<run-id>#<step-id>" for the engine's logs. It
	// authorizes nothing.
	RunLabel string `json:"run_label,omitempty"`
	// Code is the snippet source, verbatim.
	Code string `json:"code"`
	// Args/Env are the step's rendered args/env. Present ONLY when the engine
	// declared it accepts them, and secret-bearing values only under
	// allow_secrets — see §7.6.
	Args []string          `json:"args,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
	// Inputs is the scrubbed template context — the `ctx` document. Size-capped
	// (§9.6).
	Inputs map[string]any `json:"inputs,omitempty"`
	// DeadlineUnixMS is when the daemon stops waiting (the step's `timeout:`,
	// or the daemon default). Absolute rather than a duration so a slow
	// transport does not extend it. The engine SHOULD bind its interpreter to
	// it; the daemon does not rely on that (§9.5).
	DeadlineUnixMS int64 `json:"deadline_unix_ms,omitempty"`
	// Limits are advisory guest-level caps the engine applies inside its VM
	// (heap, instruction/step budget). The enforced caps are the OS ones the
	// daemon applies to the process (§9.4).
	Limits RunLimits `json:"limits,omitempty"`
}

// RunLimits are the guest-level caps an engine SHOULD apply. Advisory: an
// engine that ignores them is bounded by the process limits instead.
type RunLimits struct {
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
	// Steps is an interpreter-defined execution budget (instructions, opcodes,
	// reductions — whatever the runtime counts). 0 = unbounded.
	Steps int64 `json:"steps,omitempty"`
}

// RunResult is the plugin→daemon code-step response. EXACTLY ONE of Outputs,
// Value, or Stdout may be set; more than one is a protocol error. All three
// exist because the two output contracts conductor already honors are not the
// same function:
//
//   - Outputs  — the engine already has a named-output map; used verbatim.
//   - Value    — the snippet's raw return value; the daemon applies wrapValue
//                (internal/code/code.go:136): object → outputs, null → {},
//                scalar → {"value": v}. This is the in-process contract.
//   - Stdout   — raw text from a wrapped interpreter; the daemon applies
//                ParseOutputs (internal/code/outputs.go:22), which differs from
//                wrapValue in one place that matters: non-JSON text becomes
//                {"text": s} instead of an error.
type RunResult struct {
	Outputs map[string]any  `json:"outputs,omitempty"`
	Value   json.RawMessage `json:"value,omitempty"`
	Stdout  string          `json:"stdout,omitempty"`
}
```

A snippet-level failure (a syntax error, a raised exception, a refused callback
the snippet did not catch) is returned as a **JSON-RPC error** on `plugin.run`,
not as a `RunResult` — so it lands in the daemon's existing "step failed" path
with the engine's message attached.

### 3.3 The host-callback set

```go
// HostKVRequest is one ctx.store(<store>) call. The op set and the POSITIONAL
// args convention mirror internal/code/kvbind.go:kvInvoke exactly — args[0] is
// the namespace, args[1] the key, and so on — so the daemon handler is that
// same dispatcher with a run-scoped guard, and there is no second semantics to
// keep in sync.
type HostKVRequest struct {
	RunID string `json:"run_id"`
	Store string `json:"store"`
	Op    string `json:"op"`
	Args  []any  `json:"args,omitempty"`
}

// HostSQLRequest is one ctx.sql(<store>) call: args[0] is the statement,
// args[1] the bind list. Mirrors internal/code/sqlbind.go:sqlInvoke.
type HostSQLRequest struct {
	RunID string `json:"run_id"`
	Store string `json:"store"`
	Op    string `json:"op"` // query | exec
	Args  []any  `json:"args,omitempty"`
}

// HostMemoryRequest is one ctx.memory call. Mirrors
// internal/code/membind.go:memInvoke — no store name, because memory is the
// one process-wide configured resource.
type HostMemoryRequest struct {
	RunID string `json:"run_id"`
	Op    string `json:"op"` // remember | recall | forget | list
	Args  []any  `json:"args,omitempty"`
}

// HostResult is the daemon→plugin callback response. The JSON-shaped result of
// the op, or absent for an op that returns nothing. Refusals and failures are
// JSON-RPC errors, not a field here — an engine that ignores the error object
// cannot mistake a refusal for a null.
type HostResult struct {
	V json.RawMessage `json:"v,omitempty"`
}
```

**Error codes.** The existing codes (`pkg/plugin/serve.go:14`) are the standard
JSON-RPC subset. Host callbacks need a *refusal* to be distinguishable from a
*failure*, because the two mean different things to a snippet and to an audit
record:

```go
// Host-callback error codes (application range, -32000..-32099).
const (
	// CodeHostRefused: a guard said no — the plan write barrier, the
	// agent_authored store/scope allowlist, code_access, or a store
	// capability. The snippet MAY catch it; the daemon audits it either way.
	CodeHostRefused = -32040
	// CodeHostUnknownRun: run_id names no live run on this connection.
	// Either the run already returned, or the token is forged.
	CodeHostUnknownRun = -32041
	// CodeHostBadOp: no such op, or wrong arity/types for it.
	CodeHostBadOp = -32042
	// CodeHostFailed: the guard passed and the store itself errored.
	CodeHostFailed = -32043
	// CodeHostCancelled: the run's deadline passed or its dispatch was
	// cancelled; the engine should abandon the run.
	CodeHostCancelled = -32044
)
```

The daemon-side handler is thin by design:

```go
// internal/plugin/client.go — replacing the CodeMethodNotFound refusal at :367.
func (h pluginHandler) HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	switch method {
	case MethodHostKV, MethodHostSQL, MethodHostMemory:
		return h.host.Serve(ctx, method, params) // run-registry lookup → kvInvoke/sqlInvoke/memInvoke
	}
	return nil, acp.NewRPCError(acp.CodeMethodNotFound, "daemon exposes no such callback")
}
```

`host.Serve` resolves `run_id` against the live-run registry (§7.5), which hands
back that run's `code.DataGuard` and deadline, then calls the *existing*
dispatcher. The whole authorization story is one lookup plus code that already
exists and is already tested.

### 3.4 Version negotiation — a separate `abi`, NOT a `ProtocolVersion` bump

`ProtocolVersion` must **not** move. `internal/plugin/client.go:230` compares it
for *equality*:

```go
if decl.ProtocolVersion != ProtocolVersion {
	return nil, fmt.Errorf("plugin %s: unsupported protocol version %d (daemon speaks %d)", ...)
}
```

There is no major/minor tolerance. Bumping `ProtocolVersion` to 2 would refuse
**every plugin already built against the v1 SDK** — every connector and runtime
in the ecosystem — to add a method those plugins never call. The additions here
are purely additive to the envelope (new methods, new `Decl` field), so v1 stays
v1.

```go
type Decl struct {
	ProtocolVersion int `json:"protocol_version"`
	// ABI, on a KindStep plugin, lists the engine-ABI versions it implements,
	// ascending. Versioned separately from ProtocolVersion because the ABI is
	// the community's contract and must evolve on its own clock. Ignored for
	// other kinds.
	ABI []int `json:"abi,omitempty"`
	// ...existing fields unchanged...
}

// EngineABIVersions are the engine ABIs this daemon can drive, ascending.
var EngineABIVersions = []int{1}
```

Negotiation: the daemon intersects `Decl.ABI` with `EngineABIVersions` and picks
the **highest common** version, stamping it into `RunRequest.ABI`. An empty
intersection is a load-time error naming both sets — the same shape as the
protocol-version refusal, and visible at `conductor validate` rather than at 3am
on the first matching trigger. A `KindStep` plugin whose `ABI` is empty is
treated as `[1]` (an engine written before the field existed).

Within a version the rule is the usual one: **new optional fields may be added,
nothing may change meaning, nothing may be removed.** Adding an op to `kvOps` is
a new ABI version, because an engine that advertises v1 and is asked for a v2 op
must be able to say so rather than guess.

### 3.5 Identity anti-forgery for engines

`client.go:238` refuses a connector whose `Decl.Type` differs from the name it
was configured to provide ("identity forgery"). The check is currently
`c.spec.Kind == KindConnector`. Extend it to `KindStep`: an engine installed as
`engines/wasm` that describes itself as `lua` must be refused, for the same
reason — otherwise a `run:` reference routes a snippet into a language it was not
written for, and the failure mode is a confusing syntax error rather than a
security message.

---

## 4. Worked trace, end to end

The step:

```yaml
steps:
  - id: tally
    run: lua
    timeout: 20s
    code: |
      local s = ctx.store("cache")
      local n = s.incr("prs", tostring(ctx.pr.number))
      return { count = n }
```

(`run: lua` is shown as a *plugin* engine here — i.e. post-phase-2, or a slim
build. In a stock phase-1 build this name short-circuits to the bundled engine at
step 3 and the trace stops there.)

1. **Load.** `ParseUse(UseKindStep, "lua")` → `OriginBuiltin` (bundled) or, in a
   slim build, `OriginOfficial` → `NodeSpy/conductor-plugins`, component
   `engines/lua`, install key `engines/lua`. `PluginRefs()` records it;
   `PluginSourceAllowed` clears it (official ⇒ default-trusted);
   `conductor init` fetches and records the sha + manifest.
2. **Render.** The flow runner renders the step's `env`/`args`
   (`internal/flow/flow.go:1282`), applies the `{{secret}}` boundary for
   config-authored steps (`flow.go:1293`), and builds the `ctx` document with
   `codeCtx` (`flow.go:1331`) — `steps`, `secrets`, `vaults` stripped.
3. **Guard.** `r.planDataGuard(ctx, t)` (`internal/flow/plan.go:480`) builds
   this execution's `code.DataGuard`: nil for a config-authored step; for an
   agent-authored plan, the closure carrying the plan write barrier and the
   `agent_authored.verbs.code` store/scope allowlist.
4. **Dispatch.** `Executor.Exec` finds `spec.Run` is not a builtin engine and
   not a host interpreter, so it routes to the engine path: `Manager.Client(
   "engines/lua")`, started on demand (verify-before-execute, sandbox-wrapped,
   scrubbed env).
5. **Register the run.** The daemon mints an unguessable `run_id`, and registers
   `{run_id → (DataGuard, deadline, cancel)}` in the client's live-run registry.
6. **`plugin.run`** — daemon→plugin, on the wire:

```json
{"jsonrpc":"2.0","id":7,"method":"plugin.run","params":{
  "instance":"lua","abi":1,
  "run_id":"rk_8f2c1d6a9b0e4737a1c5d2e8f0b3a64c",
  "run_label":"2026-09-16T10:04:02Z-1d84#tally",
  "code":"local s = ctx.store(\"cache\")\n…",
  "inputs":{"pr":{"number":42,"title":"fix parser"},"repo":"me/app"},
  "deadline_unix_ms":1789041862000,
  "limits":{"memory_bytes":268435456}}}
```

7. **Guest setup.** The engine converts `inputs` into a Lua table (`goToLua`,
   `internal/code/lua.go:63`), opens base/table/string/math only, nils the chunk
   loaders, and attaches `ctx.store`/`ctx.sql`/`ctx.memory` as tables of
   closures over the host-callback client.
8. **`ctx.store("cache").incr(...)`** — plugin→daemon, on the same pipe:

```json
{"jsonrpc":"2.0","id":1,"method":"host.kv","params":{
  "run_id":"rk_8f2c1d6a9b0e4737a1c5d2e8f0b3a64c",
  "store":"cache","op":"incr","args":["prs","42",1]}}
```

> The two `id`s (`7` outbound, `1` inbound) do not collide. JSON-RPC ids are
> scoped to the originator, and `acp.Conn.dispatch` (`jsonrpc.go:121`) routes on
> shape, not id: `method`+`id` is a request to serve, `id` alone is a response to
> deliver. Each side keys its own pending map with ids it issued.

9. **Authorize, host-side.** `host.Serve` looks up `run_id` → this run's guard
   and deadline (unknown ⇒ `CodeHostUnknownRun`; expired ⇒ `CodeHostCancelled`),
   then calls `kvInvoke(guard, "cache", "incr", ["prs","42",1])`
   (`internal/code/kvbind.go:25`), which in order:
   - runs `guard("kv", "incr", "cache", args)` — the `agent_authored` store
     allowlist and, for value-writes, the secret barrier (`plan.go:495`);
   - resolves the store by name via `kv.Use("cache")` — **the engine never sees
     a handle, a path, or a DSN**;
   - runs `kv.CheckCapability("cache", st, "incr")`;
   - executes `st.Incr("prs", "42", 1)`.
10. **Response** — daemon→plugin: `{"jsonrpc":"2.0","id":1,"result":{"v":7}}`.
    Had the guard refused, it would instead be:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32040,
 "message":"agent_authored allowlist: code step touches store \"cache\" — not in policy.agent_authored.verbs.code.store (trust: full lifts this)"}}
```

    …the message verbatim from `internal/flow/plan.go:497`, unchanged.
11. **Snippet returns** a Lua table; the engine converts it with the same
    `luaToGo` semantics (`lua.go:101`) and answers `plugin.run`:
    `{"jsonrpc":"2.0","id":7,"result":{"value":{"count":7}}}`.
12. **Normalize.** The daemon deregisters `run_id` (any later callback bearing it
    now gets `CodeHostUnknownRun`) and applies `wrapValue` to `Value` — an object
    becomes the outputs map as-is.
13. **Record.** `execCode` returns `out` plus its `raw` string
    (`flow.go:1318`); the step record pins `Outputs` (`internal/store/history.go:64`).
14. **Downstream.** `{{.steps.tally.outputs.count}}` renders `7`. Nothing
    downstream can tell which engine produced it — which is the contract.

---

## 5. What the ABI freezes

### 5.1 `Capabilities` for an engine

An engine declares its own manifest, surfaced to the operator at install
(`conductor plugin add`, `plugin list --caps`, `plugin show`):

- A pure data-shaping engine (the js/go-embed/risor/lua analog) declares **no
  egress, no commands, no fs, no spawns**. It can only reach the world through
  the host-mediated `ctx`.
- An engine that legitimately needs the network (a `fetch`-style engine)
  declares `egress:` and the operator consents when adding it — the same trust
  prompt any connector plugin gets.

**One change to the default is proposed for `KindStep` specifically.** Today, a
plugin declaring nothing is confined to nothing beyond the scrubbed env —
`confineToManifest` (`internal/plugin/spawn.go:131`) says so explicitly:

> *"A plugin that declares nothing is confined to nothing beyond the scrubbed
> env: it declared no needs, so there is no allowlist to build, and inventing one
> would break plugins that predate the manifest."*

That reasoning is right for connectors (an undeclared-egress connector is
typically an *old* connector, and breaking it is worse than the exposure). It is
wrong for engines: there are no old engines, "I need no network" is the *normal*
engine posture, and an empty declaration that means "unconfined" inverts the
operator's reading of the manifest. Proposal: for `KindStep`, an **absent
`Egress` means deny**, and the launch takes the enforced-egress path with an
empty allowlist. §7.1 is explicit about where that is a real OS boundary and
where it degrades to a declaration.

### 5.2 Two trust layers stack

1. **The engine binary** is trust-gated by `plugin_trust`
   (`PluginSourceAllowed`), verified before execute on every spawn
   (`internal/plugin/client.go:193`), and pinned to the sha recorded at install.
2. **The snippet** is normally the operator's own config, confined by the
   engine's sandbox and by whatever the engine declared. For the
   agent-authored-plan case the `DataGuard` plan write-barrier applies
   unchanged, because it is applied *host-side* (§7).

### 5.3 What an engine never receives

No `Connection` map. No `secrets`/`vaults` (stripped by `codeCtx`). No daemon
environment (`spawnBaseEnv` → `sandbox.MinimalEnv`). No store handle, path, or
DSN. No other run's inputs. No way to enumerate stores — `ctx.store(name)`
resolves a name the *snippet* supplied against `kv.Use`, and an undefined name
is an ordinary error.

### 5.4 ABI v1 op surface (proposed: the full current set)

The four in-tree engines already expose identical op sets, so the surface is
de-facto frozen; publishing less than all of it would break working configs on
the first migrated engine and force a v2 immediately.

| Face | Ops | Dispatcher |
| --- | --- | --- |
| `ctx.store(<name>)` | `get set setnx merge delete incr append remove contains list first last index slice len pop` (16) | `kvInvoke`, `internal/code/kvbind.go:25` |
| `ctx.sql(<name>)` | `query exec` | `sqlInvoke`, `internal/code/sqlbind.go:23` |
| `ctx.memory` | `remember recall forget list` | `memInvoke`, `internal/code/membind.go:23` |

Conventions the ABI freezes with them, because engines must reproduce them:

- **Positional, JSON-shaped args.** `args[0]` = namespace, `args[1]` = key.
- **Absent reads fold to null.** `get/first/last/index/pop` return `null` when
  not found, so a snippet writes `if not v then`. The found flag is not on the
  wire (`kvbind.go:71` `nullable`).
- **`setnx` returns `{value, created}`; `merge` returns the merged object;
  `list` returns `{keys, entries}`; `exec` returns `{rows_affected,
  last_insert_id?}`.**
- **Value-writes** are `set setnx merge append` for kv, `exec` for sql,
  `remember` for memory — the set the write barrier vets (`kvbind.go:23`
  `kvValueWrites`, mirrored at `internal/flow/plan.go:521` `dataValueWrite`).
- **`memory` scopes are explicit.** Code-path writes carry no run provenance, so
  relative scopes need their explicit forms (`repo:<owner/repo>`,
  `agent:<name>`) — `membind.go:16`.

---

## 6. The engine SDK

### 6.1 What an engine author writes

Mirroring the connector example in `pkg/plugin/wire.go`'s package comment:

```go
package main

import (
	"context"
	"encoding/json"

	"github.com/NodeSpy/conductor/pkg/plugin"
)

func main() {
	plugin.Serve(plugin.EngineFunc(
		func() plugin.Decl {
			return plugin.Decl{
				Kind: plugin.KindStep,
				Type: "lua",
				Desc: "Lua 5.1 (gopher-lua) — data shaping only",
				ABI:  []int{1},
				// No egress, no commands, no fs, no spawns.
				Capabilities: plugin.Capabilities{},
			}
		},
		func(ctx context.Context, host *plugin.Host, req plugin.RunRequest) (plugin.RunResult, error) {
			L := newVM(ctx, req)                 // the engine's own sandbox
			bindCtx(L, req.Inputs, host)         // ctx table + ctx.store/sql/memory
			v, err := L.Run(req.Code)
			if err != nil {
				return plugin.RunResult{}, err   // → JSON-RPC error on plugin.run
			}
			raw, _ := json.Marshal(v)
			return plugin.RunResult{Value: raw}, nil
		},
	))
}
```

### 6.2 The host-callback client stub the SDK must provide

An engine author must never hand-roll JSON-RPC in the reverse direction. The SDK
hands the run's `*plugin.Host`, already bound to the `run_id`, so `run_id` never
appears in engine code and cannot be typo'd into another run:

```go
// Host is the daemon-side data plane for ONE run. The SDK constructs it from
// the RunRequest and binds the run_id; engine code never sees the token.
type Host struct{ /* conn, runID */ }

func (h *Host) KV(store string) KV
func (h *Host) SQL(store string) SQL
func (h *Host) Memory() Memory

// KV is ctx.store(<name>). Signatures follow internal/code/kvbind.go:KVHandle,
// which is already the typed face of this op set for run: go-embed — so the
// out-of-process client and the in-process handle read the same.
type KV interface {
	Get(ns, key string) (any, error)
	Set(ns, key string, v any) error
	SetNX(ns, key string, v any) (value any, created bool, err error)
	Merge(ns, key string, patch map[string]any) (map[string]any, error)
	Delete(ns, key string) error
	Incr(ns, key string, by int64) (int64, error)
	Append(ns, key string, items any, unique bool) ([]any, error)
	Remove(ns, key string, items any) ([]any, error)
	Contains(ns, key string, item any) (bool, error)
	List(ns, prefix string) (map[string]any, error)
	First(ns, key string) (any, error)
	Last(ns, key string) (any, error)
	Index(ns, key string, i int) (any, error)
	Slice(ns, key string, start, end int) ([]any, error)
	Len(ns, key string) (int, error)
	Pop(ns, key, from string) (any, error)
}

type SQL interface {
	Query(q string, args []any) ([]any, error)
	Exec(q string, args []any) (map[string]any, error)
}

type Memory interface {
	Remember(text string, tags []string, scope string) (map[string]any, error)
	Recall(q map[string]any) ([]any, error)
	Forget(id string) (bool, error)
	List() ([]any, error)
}

// HostError is what a failed callback returns. Refused() is the distinction an
// engine MUST propagate into the guest as a catchable language-level error
// (§10), because a refusal is a policy answer the snippet may legitimately
// handle, not a broken transport.
type HostError struct {
	Code    int
	Message string
}

func (e *HostError) Error() string   { return e.Message }
func (e *HostError) Refused() bool   { return e.Code == CodeHostRefused }
func (e *HostError) Cancelled() bool { return e.Code == CodeHostCancelled }
```

### 6.3 Two SDK changes this forces

**(a) `Serve` must become a peer, not just a server.** Today the loop drops
anything that is not a request (`pkg/plugin/serve.go:128`):

```go
// Only requests (method + id) are serviced; notifications and stray
// responses are ignored (a plugin never calls back into the daemon).
if m.Method == "" || m.ID == nil {
	continue
}
```

The comment's parenthetical stops being true. `Serve` needs an outbound id
counter, a pending map, and a `deliver` branch for `m.Method == "" && m.ID !=
nil` — structurally the same ~40 lines `internal/acp/jsonrpc.go` already has on
the daemon side. `write` is already mutex-guarded, and request handling is
already per-goroutine (`serve.go:142`), so a callback issued from inside an
`Invoke`/`Run` handler cannot deadlock the read loop.

**(b) The SDK's input cap is a lifetime cap, and must become per-message.**

```go
dec := json.NewDecoder(io.LimitReader(in, maxRequestBytes)) // serve.go:84
```

One `LimitReader` wraps stdin for the life of the process, so `maxRequestBytes`
(32 MiB) bounds **cumulative** daemon→plugin bytes, not one frame. A verb-only
connector rarely notices. A **pooled engine** (§9.2) receiving a fresh `inputs`
document per step reaches 32 MiB in the ordinary course of a day and then sees
`io.EOF` — which `serve` reports as a *clean shutdown* (`serve.go:121`), so the
daemon logs a plugin that exited normally and restarts it, repeatedly, with no
error anywhere naming the cause. The fix is the daemon's own approach: a
newline-scoped bounded reader, like `internal/plugin/bounded.go`
`boundedReader`, which bounds per message precisely because "newline-delimited
framing lets us bound per message rather than for the whole (long-lived)
connection".

---

## 7. Security and threat model

The register here is `use-unification.md` §D's: say exactly what is enforced and
exactly what is only declared, in that order, without rounding either up.

### 7.0 The trust boundaries

| Boundary | What it gates | Mechanism |
| --- | --- | --- |
| **B1 — the engine binary** | may this code run at all | `plugin_trust` allowlist (`PluginSourceAllowed`), sha verify-before-execute (`internal/plugin/verify.go`), safe-permissions path check, digest recorded at install |
| **B2 — the engine process** | what the binary may reach in the OS | declared `Capabilities` + PATH confinement (`manifest.go`), scrubbed env (`spawnBaseEnv`), egress proxy, optional `isolation:` namespace/cgroups |
| **B3 — the snippet** | what the *code in the config* may compute | the engine's own guest sandbox — **the engine author's responsibility now, not conductor's** |
| **B4 — the `ctx` data plane** | which stores/scopes/ops this run may touch | host-side only: `DataGuard`, `code_access`, store capability, run registry. **Unchanged by this design, and unbypassable from the guest side.** |

B4 is the one that carries the weight, and it is the one that does not move.

### 7.1 Attack: the engine or snippet exfiltrates a secret over the network

**What it would need.** A secret to steal, and a socket.

*The secret.* Secrets do not reach the engine. `codeCtx`
(`internal/flow/flow.go:1331`) strips `secrets` and `vaults` from the `ctx`
document before it is built — with the comment "Leaving vaults in exposed every
preloaded vault entry to any code step". The daemon's own environment is never
inherited (`spawnBaseEnv` → `sandbox.MinimalEnv`). `RunRequest` carries no
`Connection`. The one remaining route is `env:`/`args:` — see §7.6, which
tightens it.

*The socket.* This is where honesty is required. Layered, strongest first:

- **`isolation: { mode: namespace, network: { deny: true, egress: [...] } }`** on
  the engine reference: the process has *no* network; the only path out is the
  in-sandbox forwarder into conductor's proxy (`spawn.go:94` `EnforcedEgress`).
  This is a **real OS boundary**. Linux, non-root, `unshare` present.
- **Default path, engine declares no egress** (§5.1's proposed deny-by-absence):
  the proxy path with an empty allowlist. On Linux this is enforced; the
  preflight is fail-closed (`spawn.go:86` `spec.Check`), so a misconfigured
  sandbox refuses to launch rather than silently downgrading.
- **On macOS, or Linux-as-root, or without `unshare`:** conductor **cannot**
  structurally deny a subprocess the network. The honest claim degrades to
  "declared, recorded, and surfaced" — the §D property, not a jail. An engine
  binary that wants the network and lies about it gets the network.

**Therefore the actual control at B1/B2 is trust, not containment**, and this
design does not pretend otherwise. What it *does* buy over today: today a
malicious engine is impossible because there are no third-party engines; the
moment there are, the gate is `plugin_trust` + a surfaced manifest + a pinned
sha — the same gate that already governs connector plugins, which are strictly
more dangerous (they hold credentials).

**Which leaves the snippet**, and there the story is good: an ABI-conformant
engine gives the snippet no network API at all, because the ABI has none. The
snippet's entire reach is `inputs` in, `outputs` out, and three host callbacks.

### 7.2 Attack: the snippet writes tracked secret material into a store

An agent-authored plan emits a code step that launders a secret out of the
trigger context into `ctx.store("cache").set(...)` — the thing
`uses: kv.set` would have refused.

**Stopped, unchanged, host-side.** The callback handler calls `kvInvoke` with
*this run's* guard. `planDataGuard` (`internal/flow/plan.go:495`) is that guard;
its barrier branch is:

```go
if barrier && dataValueWrite(kind, op) && r.containsTrackedSecret(map[string]any{"args": args}) {
	return fmt.Errorf("no_secret_egress: refusing to write secret material into %s.%s from an agent plan code step — approval required", kind, op)
}
```

The engine cannot remove the guard, because the guard is not in the engine. It
is applied after the `run_id` lookup, in daemon memory, over the args the
callback carried. An engine that rewrites the args to hide the secret defeats
`containsTrackedSecret` — but an engine that rewrites args is a malicious B1
binary, and a malicious B1 binary had the secret already. Against the threat this
barrier exists for (an agent-authored *snippet*, not a malicious *engine*), the
control is exactly as strong as it is today.

### 7.3 Attack: the snippet touches a store it was not scoped to

`ctx.store("payroll")` from a step whose policy lists only `cache`.

**Stopped host-side**, by the same guard's first branch (`plan.go:496`):

```go
if rp != nil && (kind == "kv" || kind == "sql") && !rp.storeOK(resource) {
	return fmt.Errorf("agent_authored allowlist: code step touches store %q — not in policy.agent_authored.verbs.code.store (trust: full lifts this)", resource)
}
```

Three properties survive the move out-of-process intact:

- `resource` is the store **name** off the wire; the daemon resolves it. The
  engine never holds a handle, so there is nothing to smuggle past the name check.
- This is the *runtime belt* behind the static plan scan, and it exists
  precisely because a templated or computed store name is invisible to a scan
  (`internal/flow/resources.go:35`). A name computed inside a plugin engine is
  no more visible and no less caught — it is the same check on the same string.
- Memory gets the same deny-by-default treatment via `rp.memoryScopeOK(resource)`
  (`plan.go:504`), with `resource` being the scope the op touches — for `forget`,
  the *stored* entry's scope, so ownership rides the same check
  (`membind.go:311` `memScopeOf`).

Layered under it, and also host-side: `kv.CheckCapability` (a backend that
cannot serve the op), and `sqlstore.Store.CheckCodeAccess` — code steps are
query-only unless the store sets `code_access: write`, and `code_access: none`
cuts them off entirely (`internal/sqlstore/sqlstore.go:62`). The SQL statement
denylist (`sqliteDenied`/`mysqlDenied`/`postgresDenied`, `sqlstore.go:84-94`),
which refuses `ATTACH`/`COPY … PROGRAM`/`INTO OUTFILE` for **every** caller, is
also below the callback and therefore also unchanged.

### 7.4 Attack: the snippet reads another step's or another dispatch's data

*Within its own dispatch:* the `inputs` document is the step's own rendered
template context. Prior steps' outputs are reachable there **by design** — that
is what `{{.steps.<id>.outputs.*}}` is for — minus the legacy `steps` index that
`codeCtx` strips. No change.

*Across dispatches:* an engine process, pooled or not, holds only what each
`RunRequest` handed it. There is no enumeration API, no list-runs callback, and
the store callbacks are name-addressed and guard-checked per run. The only
cross-run reach is through a store both runs are authorized for — which is the
store doing its job.

*The residual:* a **pooled** engine sees several runs' inputs in one address
space. A buggy or hostile engine could leak run A's data into run B's outputs.
The ABI states the requirement (engines are stateless across runs; §9.2) and the
daemon cannot enforce it. An operator who will not accept that sets
`isolation:` on that engine reference, or turns pooling off for it (§9.2's
`pool: off`) and pays a process launch per step. Stated plainly rather than
papered over.

### 7.5 Attack: a malicious ENGINE ignores the guards

It cannot reach the data plane at all, for a structural reason: **it has no
store handle — only a callback the daemon authorizes.** Concretely, the engine
process has no DSN, no bolt path, no `kv.Use` registry (that registry is
daemon-process global, `internal/kv/backend.go:91`), and no credential. Its only
route to a store is `host.kv`, which lands in the daemon's own dispatcher behind
the guard.

The one thing it *can* try is **forging another run's authorization** — issuing
`host.kv` with a `run_id` belonging to a concurrent, better-scoped run. This is
why the design makes **`run_id` a capability, not an identifier**:

- It is an unguessable per-invocation token (128 bits from `crypto/rand`), not
  the human run id. The human id travels separately as `run_label`, which
  authorizes nothing.
- It is registered at `plugin.run` and **deregistered the moment that call
  returns**. A late callback gets `CodeHostUnknownRun`.
- The registry is **per-client**: an engine can only present tokens for runs
  *this* engine was given, so one engine can never reach another engine's run.
- An unknown-run callback is audited, not just refused. It is the signature of
  either a buggy engine or a hostile one, and it should be visible.

What remains, honestly: the engine subprocess runs as the daemon's uid unless
`isolation:` is set. It can therefore open the boltdb file directly, or read the
config, unless namespace mode's daemon-file masking is on (`SandboxDeps.MaskPaths`
→ `sandbox.NetForward.Masks`, `spawn.go:92` — namespace mode only). That is not a
new hole this design opens; it is the existing app-extension posture for every
plugin, and it is the reason B1 (trust) is the boundary that matters for the
binary. **The ABI's guarantee is about the snippet, not about the engine
binary.** Anyone quoting this design as "untrusted engines are contained" is
overselling it.

### 7.6 Attack surface this design ADDS: `env:` and `args:`

It is tempting to say the run carries `args`/`env` "as today". It does not, and
the difference matters. For the in-process engines "as today" means **ignored** —
`code.Spec` documents them as
"ignored by js/go-embed, which have no argv" (`internal/code/code.go:41-42`). But
`execCode` *does* resolve `{{secret}}` handles into `env`/`args` for
config-authored steps (`internal/flow/flow.go:1293`). So forwarding them to a
plugin engine is not parity — it is a **new path for resolved secret values to
cross into a third-party process**.

Proposal:

1. `RunRequest.Args`/`Env` are sent **only** if the engine declares it accepts
   them (`Decl.Accepts: ["args","env"]`, a new optional string set on `Decl`;
   absent = neither). A data-shaping engine declares neither and the fields are
   never populated.
2. A value that came from a resolved `{{secret}}` handle crosses **only** if the
   engine reference lists the secret under `allow_secrets:` — reusing the exact
   mechanism connectors already have (`internal/config/connectors.go:53`,
   exact-match, no globs, validated at `connectors.go:1461`), plumbed through
   `plugin.Spec.AllowSecrets` (`internal/plugin/plugin.go:98`).
3. Otherwise the step fails at render with a message naming the engine, the
   secret, and the `allow_secrets:` line that would permit it.

### 7.7 What gets BETTER

- **A runaway snippet no longer shares the daemon's fate.** Today the in-process
  engines are local-only precisely because they "share this process's fate
  (crash it, hang it, exhaust its memory)" (`internal/code/code.go:5`). Out of
  process, a hang is a kill and an OOM is a subprocess OOM.
- **Refusals become visible.** Today a refused callback is raised into the
  snippet (`throw new Error(r.err)` — `js.go:139`; `L.RaiseError` —
  `kvbind.go:391`) and a snippet that catches it leaves no trace. The callback
  handler audits every refusal at the daemon, whether or not the snippet
  swallows it.
- **Per-engine OS hardening becomes possible.** `isolation:` applies to an
  engine reference exactly as it does to a connector. There is no way to put a
  cgroup around an in-process interpreter.

---

## 8. Reference engine, fully specified: `lua` as a plugin

**Recommendation: re-implement `lua` as the phase-1 reference engine, not a
WASM engine.** The rationale is one word: *oracle.* The point of phase 1 is to
prove the ABI faithfully reproduces the existing contract, and proving that
requires something to compare against. `lua` has an in-tree counterpart, so
every fixture can be run through both the bundled engine and the plugin engine
and asserted byte-identical (§12). A WASM engine has no counterpart, so a
phase-1 WASM engine would prove only that *something* ran — every ABI gap would
surface later, in the field, as a third-party engine's bug report. WASM is the
better *flagship* (§8.6) and the worse *proof*.

### 8.1 `Decl`

```go
plugin.Decl{
	ProtocolVersion: 1,           // stamped by Serve
	Kind:            plugin.KindStep,
	Type:            "lua",       // must equal the install name (§3.5)
	Desc:            "Lua 5.1 (gopher-lua) — data shaping, no I/O",
	ABI:             []int{1},
	Capabilities:    plugin.Capabilities{},  // no egress, commands, fs, or spawns
	// Accepts is empty: lua steps take no argv and no env today.
}
```

### 8.2 Mapping `inputs` into the guest

`RunRequest.Inputs` (JSON object) → `goToLua` (`internal/code/lua.go:63`),
ported verbatim into the engine: nil→`LNil`, bool→`LBool`, string→`LString`,
numerics→`LNumber`, map→string-keyed table, slice→1-based table. Because the
document arrives as JSON either way, the value space is identical to today's
(the in-process path already funnels through a JSON-shaped `map[string]any`).

Set as the `ctx` global (`lua.go:51`).

### 8.3 Exposing `ctx` to guest code

Three keys are attached to the `ctx` table, exactly as `lua.go:47-49` does now:

```lua
ctx.store("cache")      -- table of the 16 kv ops
ctx.sql("analytics")    -- table of { query, exec }
ctx.memory              -- table of { remember, recall, forget, list }
```

Each op is a closure that collects its varargs with `luaToGo`, calls the SDK
`Host` client, and either pushes `goToLua(result)` or raises. **Raising, not
returning an error value, is the contract** — it is what `luaStoreFn`
(`kvbind.go:390`) does today, so `pcall`-based snippets keep working:

```go
v, err := host.KV(name).Call(op, args...)
if err != nil {
	L.RaiseError("%s", err.Error())   // refusal and failure both raise; snippets may pcall
	return 0
}
```

### 8.4 Guest sandbox (the engine's own B3 responsibility)

Ported from `lua.go:22-44` with nothing relaxed:

- `lua.NewState(lua.Options{SkipOpenLibs: true})`.
- Open **only** base, table, string, math. No `os`, `io`, `debug`, `package`.
- Nil out `dofile`, `loadfile`, `load`, `loadstring`.
- `L.SetContext(ctx)` bound to `DeadlineUnixMS` — gopher-lua checks the context
  in its exec loop, so `while true do end` returns a context error instead of
  spinning (`lua.go:24`, and the regression it was written for,
  `internal/code/timeout_test.go`).

### 8.5 Resource limits

| Limit | Value | Enforced by |
| --- | --- | --- |
| Wall clock (guest) | `RunRequest.DeadlineUnixMS` | `L.SetContext` inside the engine |
| Wall clock (process) | step `timeout:` + 5s grace, then `plugin.cancel`, then teardown | daemon (§9.5) |
| Memory | 256 MiB — the same cap the js engine already applies (`jsMemoryLimit`, `internal/code/js.go:13`) | cgroup `MemoryMax` via `isolation.limits.memory`; gopher-lua has no heap cap of its own, so the *only* real limit here is the OS one |
| Pids | 16 | cgroup `TasksMax` (`sandbox.Spec.Pids`) |
| CPU | 200% default | cgroup `CPUQuota` |
| Response size | 8 MiB per message | `internal/plugin/bounded.go` `DefaultMaxMessageBytes`, already applied to every plugin |
| Concurrent runs | daemon-side semaphore per engine (default = GOMAXPROCS) | daemon |

Note the honesty in the memory row: gopher-lua has no heap cap, which is exactly
why the in-process engine's doc comment says "ctx cancellation is the available
limit" (`lua.go:25`). Out-of-process, the cgroup gives that engine a memory limit
it could never have in-process — one of the concrete wins.

### 8.6 The flagship (phase 3), for contrast

A `wasm` engine — wasmtime or wazero, guest modules compiled from anything —
declares the same `Decl` shape, maps `inputs` into guest linear memory as a JSON
blob, and exposes `ctx` as three imported host functions the guest calls with
`(ptr,len)` JSON payloads. It is the strongest *snippet* sandbox available and
the right long-term default for community engines. It is not the right phase-1
proof, for the oracle reason above.

---

## 9. Execution lifecycle, concurrency, resource limits

### 9.1 Spawn and routing

One `plugin.Client` per engine (not per step), held by the `Manager` under key
`engines/<name>`, started lazily on first use — verify-before-execute, sandbox
or manifest confinement, scrubbed env, redacted stderr pump. `Executor.Exec`
gains one branch before the host-interpreter fallthrough; everything else in
`internal/code` is untouched.

### 9.2 Process reuse (formerly O2) — **proposed: pooled, warm, multiplexed**

One process per engine per daemon, with concurrent runs multiplexed over the one
stdio pipe. Spawn-per-step was the conservative option and is rejected because:

- **Both ends are already concurrent.** `acp.Conn.dispatch` goroutines each
  inbound request (`jsonrpc.go:121`); `serve` goroutines each one
  (`serve.go:142`); `write` is mutex-guarded on both sides. Multiplexing is the
  transport working as designed, not a new hazard.
- **Correlation is already solved** by `run_id`, which the design needs anyway
  for authorization (§7.5). Pooling adds no new correlation machinery.
- **Spawn cost is real.** A verify + sandbox-wrap + exec per step, on a trigger
  with several code steps, is the difference between "occasionally noticeable"
  and "why is this workflow slow".

The costs, stated:

- Engines **must be stateless across runs**; the ABI says so and the daemon
  cannot enforce it (§7.4).
- One runaway run can take the pool down (§9.5).

Escape hatch: `pool: off` on the engine reference forces spawn-per-step. An
engine that needs it (heavyweight per-run global state, a runtime that is not
reentrant) declares `Decl.Pooled: false` and the daemon honors that without the
operator having to know.

### 9.3 Teardown

The existing supervision applies with no changes: transport failure tears the
connection down so the next call restarts it (`client.go:274`), with the restart
burst window and the lifetime cap (`client.go:27`) bounding a crash-loop. A
parked engine reports "down" and the step fails with that message rather than
hanging.

### 9.4 Per-run limits

`isolation:` on an engine reference is the same `IsolationConfig` connectors
take, so `limits: { memory, cpu, pids }` lower into `systemd-run --user --scope`
(`-p MemoryMax=`, `-p CPUQuota=`, `-p TasksMax=`) or the container engine's own
flags (`internal/sandbox/sandbox.go:252` `systemdPrefix`, `:231`). Proposed
defaults for `KindStep` when no `isolation:` is written: none, matching the
app-extension posture — with the §5.1 exception that absent egress means deny.
A locked-down box sets the block.

### 9.5 Timeouts and killing a runaway

**A conflict to fix first.** `Client.call` wraps every RPC in
`DefaultCallTimeout` (30s, `client.go:16`, applied at `client.go:266`). A code
step's `timeout:` is arbitrary and routinely longer. `plugin.run` must therefore
**not** use the default per-call timeout; it takes the step's context deadline
directly — the same bypass `StartSource` already makes for the same reason
("No per-call timeout: start_source is long-lived", `client.go:121`). This needs
a `callWithDeadline` sibling to `call`; it is not a config knob.

The kill ladder:

1. Deadline passes → the daemon cancels the run's context, sends `plugin.cancel
   {run_id}` (a notification — no waiting), and fails the step with the context
   error. Any later callback on that token gets `CodeHostCancelled`.
2. A well-behaved engine unwinds (the reference engine's `L.SetContext` does it
   for free) and answers `plugin.run` with an error. Nothing further happens.
3. The engine does not return within a grace window → the daemon tears the
   client down (`teardownLocked` → `kill()` → `cmd.Process.Kill()`), **which
   kills every sibling run in that pool**. Those steps fail with a message that
   names the cause (the engine, the run that hung) rather than a bare transport
   error. The restart backoff brings the engine back for the next step.

Contrast with today: an in-process runaway that ignores its context wedges a
daemon goroutine, and the only recovery is restarting conductor.

### 9.6 Inputs size (formerly O6) — **proposed: cap it, say so, and fix the SDK**

Today the `ctx` document is an in-process map with no size limit. Over the wire
it is a JSON frame, and the two directions are bounded asymmetrically: 8 MiB per
message plugin→daemon (`bounded.go`), and — as §6.3(b) shows — *32 MiB
cumulative* daemon→plugin on the SDK side.

Proposal: cap the serialized `Inputs` at **4 MiB** (a constant, not config),
fail the step at render with a message naming the step and the size, and fix the
SDK's reader to be per-message. No chunking: a 4 MiB template context is a
design smell, and an engine that needs more data should read it from a store
through `ctx.store`, which is what the store is for.

### 9.7 `host:` engines

Deferred; see O3 in §14.

---

## 10. Failure matrix

| Failure | Snippet sees | Step outcome | Notes |
| --- | --- | --- | --- |
| Engine crashes mid-run | nothing (its process is gone) | **fails**, "engine %s exited during step %s" | `conn.Done()` fires; `ensureLocked` restarts on the next step, subject to backoff. Pooled siblings fail the same way. |
| Callback times out (daemon slow/wedged) | a raised error (`CodeHostFailed`) | **fails** unless the snippet catches it | The daemon's own store calls are already bounded by the step deadline; a callback cannot outlive its run. |
| **Guard refusal** (`DataGuard`, allowlist, `code_access`, capability) | a **catchable** language-level error — `throw` in JS, `raise` in Lua, `(nil, err)` in a Go-shaped engine | **fails** if uncaught; **succeeds** if the snippet handles it | Deliberately unchanged: this is exactly today's behavior (`js.go:139`, `kvbind.go:391`). **New:** the daemon audits the refusal regardless, so a swallowed refusal is no longer invisible. |
| Malformed `RunResult` (two of Outputs/Value/Stdout, or undecodable) | n/a | **fails**, naming the engine and the field | Protocol error attributed to the engine, not to the operator's snippet. |
| Oversized response (>8 MiB) | n/a | **fails**, "message exceeded N bytes" | `boundedReader` tears the connection down; the engine restarts on the next step. |
| Oversized `inputs` (>4 MiB) | never runs | **fails at render**, naming the step and size | §9.6. |
| Engine not installed (offline, first use) | never runs | **fails** with `Spec.NotInstalledError` — "referenced by your config but not installed — run `conductor init`" | `internal/plugin/plugin.go:123`. `conductor validate` surfaces it before a trigger ever fires. |
| Engine fetch fails (network down at install) | n/a | install-time error; **existing installs keep running** | The stay-current updater never removes a working install. A builtin-named engine is unaffected — it is in the binary. |
| ABI version mismatch (no common version) | n/a | **load-time error** naming both sets | §3.4. Caught at `conductor validate`, not at dispatch. |
| Protocol version mismatch | n/a | **error at Describe**, plugin refused | Unchanged (`client.go:230`). |
| `Decl.Type` ≠ install name | n/a | **refused**, "identity forgery" | §3.5. |
| Engine parked (crash-loop cap) | never runs | **fails**, "down for good … reload config to retry" | `client.go:164`. |
| Unknown/forged `run_id` | a raised error | **fails**, and the callback is **audited** | §7.5. Signature of a buggy or hostile engine. |
| Runaway snippet, engine honors cancel | context error | **fails** with the context error | Same message as today. |
| Runaway snippet, engine ignores cancel | process killed | **fails**; pooled siblings fail with an attributed message | §9.5 step 3. |

---

## 11. Determinism and replay (formerly O7)

**The replay unit is the step, not the engine call — so an external engine
changes nothing structural.** Concretely:

- `conductor runs retry <id>` rehydrates the recorded trigger, **pins the
  recorded outputs of every successful step before the chosen one**, and
  re-executes from there through the ordinary flow runner
  (`internal/engine/retry.go:17`). The pinned unit is `StepRecord.Outputs`
  (`internal/store/history.go:64`).
- Records are HMAC-verified before their outputs are trusted
  (`GetHistoryVerified`, `retry.go:33`) — so a plugin engine's recorded outputs
  are exactly as trustworthy as a builtin engine's.
- `ctx.store` **reads are not recorded** and re-execute live. That is true today
  for all four in-process engines; a code step is not a pure function of its
  inputs and never has been. Recording reads would be a different feature (a
  read-through replay log) with its own storage and staleness questions, and it
  is out of scope.
- Writes replay. Re-running a *succeeded* step replays its side effects, which is
  why `--force-replay` exists and why the guard refuses without it
  (`retry.go:82`). Unchanged.

**One thing to add.** A retry that lands on a *different engine build* than the
original run is currently invisible. Proposal: `StepRecord` grows an optional

```go
// Engine records which code-step engine produced this step's outputs, when it
// was a plugin engine: "engines/lua@1.2.0 sha256:abcd…". Empty for builtin
// engines and non-code steps.
Engine string `json:"engine,omitempty"`
```

…populated from `plugin.Spec.Ref()` + `Spec.Digest()`, shown by `conductor runs
show`, and compared on retry: a mismatch is a **warning** by default and an error
under `--force-replay`'s stricter sibling if one is ever wanted. It costs one
string per step record and turns "why does the retry differ" from a mystery into
a line of output.

---

## 12. Migration

Phase-by-phase so nothing breaks and the ABI is proven before we depend on it.

### Phase 1 — prove the ABI

- `KindStep`, `UseKindStep`, `EnginesNamespaceAlias`, `Decl.ABI`,
  `Decl.Accepts`, `Decl.Pooled`.
- `plugin.run` + `plugin.cancel`; the `host.*` callback set; the live-run
  registry; `pluginHandler.HandleRequest` wired to it; `callWithDeadline`.
- SDK: `EngineFunc`, `Host` client, outbound-call support in `Serve`, per-message
  bounded reader (§6.3).
- `PluginRefs()` third pass; `Executor.Exec` engine branch; `PruneOrphans` and
  the kind switches.
- **ONE reference engine** — `lua` (§8) — in `conductor-plugins/engines/lua`.
- **The proof: a differential test.** `internal/code`'s existing engine fixtures
  (`engines_test.go`, `engines_more_test.go`, `kv_ctx_test.go`, `sql_ctx_test.go`,
  `mem_ctx_test.go`, `dataguard_test.go`, `scope_guard_test.go`,
  `timeout_test.go`) run every `lua` case through **both** the bundled engine and
  the plugin engine and assert identical outputs, identical error strings, and
  identical guard refusals. Any ABI gap is a red test, in-tree, before anyone
  outside depends on it.
- The bundled js/go-embed/risor/lua stay in-process and unchanged.

### Phase 2 — de-privilege the built-ins

- Re-implement `js`, `go-embed`, `risor`, `lua` as official engine plugins.
- **Keep them bundled.** Builtin resolution short-circuits (§1.1 rule 1), so
  `run: js` still works offline with zero config. This is the same
  builtin-beats-official rule connectors have (`use.go:27`).
- Gate the in-binary interpreters behind a build tag (`//go:build !slim`). In a
  slim build the builtin engine set is empty, so rule 1 misses and — **only for a
  qualified reference** — rule 2 fetches. A bare `run: js` in a slim build is an
  error that names the fix (`run: conductor-engines/js`), because silently
  turning a bare name into a network fetch is the thing §1.1 exists to prevent.
- **Measure, do not assert.** `connector-extraction.md` is the cautionary tale:
  removing two connectors moved the stripped daemon by 98,304 bytes, 0.21%, and
  it says so rather than claiming "a much smaller daemon". yaegi +
  `stdlib.Symbols`, wazero + QuickJS, and risor's module set are plausibly a
  different order of magnitude — but the number goes in the PR from a real
  `-ldflags="-s -w"` build of both configurations, not from this document.

### Phase 3 — ecosystem

SDK example + an authoring guide; the `engines/` tree in `conductor-plugins`; a
WASM engine (§8.6); community engines (Ruby, Python-embedded).

### Back-compat guarantees, held throughout

| Held constant | Why it can be |
| --- | --- |
| The op set (16 kv / 2 sql / 4 memory) and its positional-args, null-for-absent conventions | The callback handler *is* `kvInvoke`/`sqlInvoke`/`memInvoke` |
| `DataGuard`, `code_access`, store capabilities, the SQL statement denylist | All host-side, below the callback |
| `wrapValue` / `ParseOutputs` | The daemon applies them to `RunResult`, not the engine |
| `{{.steps.<id>.outputs.*}}` | Downstream cannot tell which engine ran |
| `run: js/go-embed/risor/lua/go/bash/python/<path>` | Rule 1 and rule 3 of §1.1 |
| Local-only rejection for in-process engines (`connectors.go:1697`) | Unchanged for builtins; a *plugin* engine is a subprocess, so the rejection is about the builtin set, not about `run:` as such |

Only the **transport** of `ctx` changes (in-process for bundled engines,
host-callback for plugin engines), and only for engines that opt into the plugin
path.

---

## 13. Alternatives considered

**A. WASM mandate — every engine compiles to WASM, one uniform sandbox.**
Genuinely attractive: one containment story, one resource-limit story, no
per-engine sandbox quality to audit, and a guest that cannot syscall. Rejected as
a *mandate*, and here is the honest reason rather than a convenient one: **the
security property this design rests on does not come from the guest sandbox.**
It comes from B4 — the data plane is authorized host-side and the engine holds no
handle. A WASM mandate would harden B3 (the snippet), which is already the
engine's job and which a conformant engine already does. Meanwhile it would
exclude engines that cannot compile to WASM today — yaegi, a real CPython, an
engine wrapping an existing native runtime — which is most of the ecosystem we
are trying to open. And a WASM engine is *already expressible* under this design
(§8.6), so the mandate buys uniformity at the cost of the extensibility that is
the entire point. Adopt WASM as the recommended engine, not the required one.
*If the owner weights "we ship one sandbox we can reason about" above "any
language anyone can embed", the mandate is the better call — it is a real
tradeoff, not a strawman. This design takes the second side because the problem
statement is about who decides which languages exist.*

**B. Reuse `Verb`/`plugin.invoke` instead of a new `plugin.run`.** Tempting —
zero new daemon→plugin methods. Rejected: a `Verb` is defined by fixed
`Options`/`Outputs` `Schema`s that the connector layer validates against the
`Decl` (`internal/plugin/plugin.go:22`). An engine has neither: its input is
arbitrary code plus an arbitrary document, and its output is whatever the snippet
returned. Forcing that through a verb means a verb with one `code: string` option
and no output schema — i.e. a verb in name only, which then has to be
special-cased everywhere a verb is enumerated (skill capability cards, MCP tool
descriptions, `Verb.Usage`, the agent-facing surfaces). A separate method keeps
engines out of every one of those enumerations, which is correct: an engine is
not a capability an agent can call.

**C. Build tags only — slim the binary, no extensibility.** Delivers the byte
savings with a fraction of the work and none of the ABI risk. Rejected because it
solves the stated *secondary* problem (binary weight) and none of the primary one
(who decides which languages exist). A build tag does not let anyone publish a
Ruby engine. Worth noting it is not either/or: phase 2 ships the build tag
*because of* this design, not instead of it.

**D. Status quo — in-process forever.** The honest case for it: the current
engines are genuinely well-sandboxed (a narrow yaegi allowlist, four Lua libs,
risor globals opt-out, QuickJS in wazero), there is no RPC hop, and conductor can
truthfully say a snippet cannot open a socket. That last property is the real
cost of this design (§"The cut") and should not be waved away. Rejected because
it caps the language set at whatever pure-Go interpreters the maintainer is
willing to link, forever, and because the properties that actually protect
*stores* — which is what a code step touches — are host-side and survive the
move intact.

**E. Shell interpreters as engines** — covered and rejected at §1.4.

---

## 14. Open questions (owner-level)

Everything else above is a proposal with a rationale; override any of it. These
three are genuine judgment calls the design cannot settle on its own.

- **O1 — selection spelling.** `run: <ref>` with the reserved
  `conductor-engines/` namespace (proposed, §1.1) vs a separate `use:` key on the
  step (`run: wasm` + `use: acme/engines/wasm`). The proposal reuses one resolver
  and one field; the alternative separates "which language" from "which
  implementation", which reads better when an operator wants to pin a *different*
  implementation of an existing language name. Leaning `run:`.
- **O3 — `host:` engines.** Allow an out-of-process engine on a `hosts:` target
  in phase 1, or defer? Deferring is proposed: the host-callback channel would
  have to reach the daemon from the remote box, which means a tunnel plus an
  authenticated channel plus a story for what happens when it drops mid-run —
  and `run: node` + `host:` already covers "execute code over there". But an
  engine that runs where the data is, is a real want, and it may be worth
  designing now rather than retrofitting.
- **O4 — ABI v1 op surface.** Ship the full 22-op set on day one (proposed,
  §5.4) or a minimal `kv get/set` + outputs and grow? Full is proposed because
  the surface is already frozen by four in-tree engines and a narrower v1 breaks
  working configs the day an engine migrates. The counter-argument is real:
  every op in v1 is one every community engine must implement forever, and 22
  is a lot to ask of a first engine. A middle path exists — declare a *required*
  core and an *optional* extended set, with the daemon refusing an op the engine
  did not advertise — at the cost of configs that work on one engine and not
  another.

---

## 15. Tests

- **Differential (the ABI proof).** Every `lua` fixture in `internal/code`
  through both the bundled and the plugin engine: identical outputs, identical
  error strings, identical refusals. Extended per engine in phase 2.
- **Wire.** `plugin.run` round-trip; each `host.*` op with its positional args;
  `RunResult` precedence (Outputs > Value > Stdout) and the exactly-one rule;
  oversized inputs and oversized responses.
- **Authorization.** A callback bearing an unknown/forged `run_id`; a callback
  after its run returned; concurrent runs in one pool each reaching only their own
  guard's stores; every refusal path (`DataGuard` barrier, store allowlist,
  memory scope, `code_access: none`/`read`, missing capability) asserted to
  produce `CodeHostRefused` with today's message text; every refusal asserted to
  be audited even when the snippet catches it.
- **Negotiation.** `Decl.ABI` intersection: highest common wins; empty →
  load-time error naming both sets; absent → treated as `[1]`.
  `ProtocolVersion` unchanged, and a v1 connector plugin still loads.
- **Identity.** A `KindStep` plugin whose `Decl.Type` differs from its install
  name is refused.
- **Lifecycle.** Engine crash mid-run → step fails and the next step restarts it;
  crash-loop hits the lifetime cap and parks; a run that ignores `plugin.cancel`
  is killed within the grace window and siblings get an attributed error; a step
  `timeout:` longer than `DefaultCallTimeout` is honored (the regression
  `callWithDeadline` exists for).
- **SDK.** The per-message bounded reader: a plugin surviving >32 MiB of
  cumulative daemon→plugin traffic (the current lifetime-`LimitReader` bug,
  §6.3b).
- **Resolution.** Bare builtin → builtin; bare unknown → host interpreter (NOT
  the official repo); `conductor-engines/x` → `NodeSpy/conductor-plugins`
  component `engines/x`; `owner/repo/x`; local path; `@version`; slim build
  where the builtin set is empty.
- **Secrets.** `env`/`args` withheld unless `Decl.Accepts` declares them; a
  resolved `{{secret}}` value withheld unless `allow_secrets:` lists it;
  `codeCtx` still strips `secrets`/`vaults`.
- **Replay.** A retry across an engine version change surfaces the mismatch;
  pinned outputs of a plugin-engine step replay identically.
- `go test ./...` green, `gofmt -l` clean, `go vet ./...` clean.

## 16. Docs (ship in the same PR — repo rule: docs travel with the change)

- This file.
- `config.example.yaml`: the `run:` forms (builtin / `conductor-engines/` /
  third-party / local), `isolation:` on an engine reference, `allow_secrets:`.
- Wiki: a code-step engines page (the ABI, the op table, authoring an engine,
  the `Host` client), and an update to the plugins/trust pages for the new kind.
- README: the `run:` line, if it enumerates engines.

## 17. Non-goals

- **Replacing the shell-out interpreters.** They already give you any language on
  PATH, locally or on a host, with no ABI — that stays (§1.4).
- **A general FFI.** The `ctx` data-plane is deliberately kv/sql/memory plus
  inputs/outputs, nothing more. An engine that wants more reaches the world
  through declared, operator-consented `Capabilities`, not through a wider `ctx`.
- **Containing a hostile engine binary.** B1 is trust, not containment (§7.5).
  Anyone quoting this design otherwise is overselling it.
- **Recording `ctx` reads for replay.** A read-through replay log is a separate
  feature with its own storage and staleness design (§11).
