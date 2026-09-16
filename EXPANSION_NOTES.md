# Expansion notes — `docs/design/code-step-engines.md`

Skeleton: 213 lines. Now: ~1540. The growth is schema blocks, the wire trace,
the threat model, and the failure/test matrices; no section is padding, but §3
(schema) and §7 (security) are where most of it went, deliberately.

## Sections added

| § | What |
| --- | --- |
| 1.1–1.3 | Resolution order for `run:`, the `EnginesNamespaceAlias` reservation, the new `UseKind`/`Dir()` and every kind-enumerating call site that needs the member, and the `PluginRefs()` third pass |
| 3 | The whole concrete wire schema: `KindStep`, `plugin.run`/`plugin.cancel`, `RunRequest`/`RunLimits`/`RunResult`, `HostKVRequest`/`HostSQLRequest`/`HostMemoryRequest`/`HostResult`, the host error-code range, the daemon handler, ABI negotiation, identity anti-forgery |
| 4 | 14-step end-to-end trace with real JSON on the wire for `plugin.run`, one `host.kv` callback, its result, and its refusal |
| 5.4 | The ABI v1 op table (16 kv / 2 sql / 4 memory) plus the conventions it freezes (positional args, null-for-absent, the `setnx`/`merge`/`list`/`exec` return shapes, the value-write set, explicit memory scopes) |
| 6 | The engine SDK: author-facing `EngineFunc` example, the typed `Host`/`KV`/`SQL`/`Memory` client stub, `HostError.Refused()`, and the two changes the SDK itself needs |
| 7 | Threat model — four boundaries, five attack scenarios, plus 7.6 (the surface this ADDS) and 7.7 (what gets better) |
| 8 | The reference engine fully specified: `Decl`, inputs→guest mapping, `ctx` exposure, guest sandbox, a seven-row resource-limit table |
| 9 | Execution lifecycle: spawn/route/teardown, pooling decision and its costs, per-run limits, the kill ladder, the inputs cap |
| 10 | Failure matrix (15 rows) |
| 11 | Determinism/replay, with the proposed `StepRecord.Engine` provenance field |
| 13 | Alternatives: WASM-mandate, `Verb` reuse, build-tags-only, status quo |
| 15/16 | Tests and docs, per the repo rule that docs travel with the change |

## Sections substantially deepened

- **Problem** — corrected to four engines (see contradictions below) and pinned
  to the actual dispatch switch.
- **The cut** — added an up-front statement of what the trade *costs*: snippet
  containment moves from conductor to the engine author.
- **What already exists** — every bullet now carries a real anchor, and the
  "gap" is pinned to one line of code (`pluginHandler.HandleRequest`).
- **Migration** — phase 1 now names the differential test that constitutes the
  ABI proof, and phase 2 names the build tag, the slim-build resolution
  behaviour, and a "measure, don't assert" rule modelled on
  `connector-extraction.md`'s 0.21% finding.

## Open → proposed (one line of rationale each)

| Was | Now | Why |
| --- | --- | --- |
| O2 process reuse | **Pooled, warm, multiplexed**, with `pool: off` and `Decl.Pooled` escapes | Both ends already goroutine-per-request and `run_id` correlation is needed for authorization anyway, so pooling adds no new machinery — only the stateless-across-runs requirement, which is stated and cannot be enforced |
| O5 shell interpreters | **Stay as-is**, permanently | An `exec` engine is publishable by anyone if uniformity is ever wanted; wrapping PATH interpreters in an ABI adds a process hop for nothing |
| O6 inputs size | **4 MiB cap, fail at render**, plus fix the SDK's reader | A 4 MiB template context is a smell; anything bigger belongs in a store |
| O7 determinism/replay | **Sound unchanged**; add `StepRecord.Engine` provenance | The replay unit is the step's recorded outputs, not the engine call; reads already re-execute live for all four in-process engines |
| Selection (part of O1) | **Pack treatment, not connector treatment** — no bare-name→official | `run:` already means "an interpreter on PATH"; letting `run: ruby` fall through to a network fetch is exactly the ambiguity `PacksNamespaceAlias` was created to remove |
| `ProtocolVersion` bump vs `abi_version` | **Separate `Decl.ABI`**, `ProtocolVersion` stays 1 | `client.go:230` compares for equality — a bump refuses every existing v1 plugin (see contradictions) |
| Engine `Capabilities` default | **Absent `Egress` means deny for `KindStep`** | `confineToManifest`'s "declared nothing ⇒ confined to nothing" is right for old connectors and wrong for engines, where "no network" is the normal posture |
| `run_id` semantics | **An unguessable capability token**, human id carried separately as `run_label` | A pooled engine holds several runs' tokens; if the token were the human run id it would be guessable and cross-run forgery would be trivial |
| Reference engine | **`lua`**, not WASM | Phase 1's job is proving ABI fidelity, which needs an oracle — `lua` has an in-tree counterpart to diff against; WASM is the better flagship and the worse proof |
| `Decl.Type` check | **Extend identity anti-forgery to `KindStep`** | Currently gated on `KindConnector`; an engine lying about its language is the same class of problem |

Still open (§14), because each is a judgment call rather than a fact: **O1**
selection spelling (`run:` + reserved namespace vs a separate step `use:` key),
**O3** `host:` engines in phase 1, **O4** full 22-op ABI v1 vs
minimal-and-grow (full is recommended, with the counter-argument stated).

## Where the code contradicted the skeleton

1. **There are four in-process engines, not three.** `risor`
   (`internal/code/risor.go`, `Executor.Exec` case at `internal/code/code.go:119`)
   is handled everywhere js/go-embed/lua are —
   `internal/config/connectors.go:1697`, `internal/code/hostinterp.go:104`,
   `internal/code/timeout_test.go`. A migration that forgets it leaves an
   interpreter linked in and a `run: risor` config broken.
2. **The kv op set is sixteen, not ten.** `kvOps` (`internal/code/kvbind.go:189`)
   adds `first last index slice len pop` to the skeleton's list. Freezing ten of
   sixteen in ABI v1 would break working configs on the first migrated engine.
3. **There is no `ctx.kv`.** The face is `ctx.store(<name>)` (`js.go:135`
   `jsKVShim`, `lua.go:47`, `kvbind.go:338` for risor, `conductor/store` for
   go-embed). The skeleton used `ctx.kv` throughout.
4. **`args`/`env` are NOT passed to the in-process engines today** —
   `code.Spec` says "ignored by js/go-embed, which have no argv"
   (`internal/code/code.go:41-42`) — yet `execCode` resolves `{{secret}}` handles
   into them for config-authored steps (`internal/flow/flow.go:1293`). So the
   skeleton's "plus optional args/env (as today)" is not parity: it is a new
   path for resolved secret values into a third-party process. §7.6 proposes
   `Decl.Accepts` + `allow_secrets:` to close it.
5. **Bumping `ProtocolVersion` would be a breaking change to the whole plugin
   ecosystem.** `internal/plugin/client.go:230` compares for equality with no
   major/minor tolerance, so v2 refuses every v1 connector and runtime. The
   skeleton left "ProtocolVersion bump vs a separate abi_version" as a live
   choice; the code decides it.
6. **The transport is already bidirectional; only the handler refuses.**
   `internal/acp/jsonrpc.go:121` already serves inbound requests on their own
   goroutine explicitly so a handler can call back. `internal/plugin/client.go:367`
   returns `CodeMethodNotFound` — "daemon exposes no plugin callbacks". The
   skeleton's "JSON-RPC is already bidirectional… this is a new method set, not
   a new transport" is right about the daemon and wrong about the SDK, which
   drops responses outright (`pkg/plugin/serve.go:128`).
7. **`Client.call` imposes a 30s `DefaultCallTimeout` on every RPC**
   (`client.go:16`, applied at `:266`). A code step's `timeout:` is arbitrary
   and routinely longer, so `plugin.run` needs the deadline bypass `StartSource`
   already uses (`client.go:121`). Not a skeleton error, but a blocker it did
   not see.

## Things I was unsure about / flagged rather than settled

- **The `Kind` vs `Dir()` asymmetry.** `resolve.go:253` compares
  `string(decl.Kind) == ref.Kind()` for equality, so the wire kind and the
  `UseKind` string must match exactly; the *directory* is free. I kept the
  skeleton's `Kind = "step"` (as briefed) and spent the freedom on `Dir() ==
  "engines"` for the install path and repo layout. It is the only place in the
  tree where `Dir()` is not the kind pluralized, so it is called out in the doc
  to keep someone from "fixing" it later. If the owner prefers symmetry, the
  clean version is `Kind = "engine"` everywhere.
- **The SDK's 32 MiB lifetime input cap** (`pkg/plugin/serve.go:84`,
  `io.LimitReader` over the whole of stdin). I am confident about the mechanism
  and less so about whether it has bitten anyone yet — a verb-only connector
  would take a very long time to reach it, a pooled engine would reach it in a
  day, and the failure presents as a *clean* EOF shutdown with nothing naming
  the cause. Flagged in §6.3(b) and in the tests list. It is arguably a bug
  worth fixing independently of this design.
- **Whether the egress deny-by-absence for `KindStep` is acceptable.** It
  deviates from `confineToManifest`'s stated reasoning, which was written for
  connectors. I argued the deviation but the owner may prefer uniformity.
- **Pool blast radius.** One runaway run killing its pool siblings is a real
  cost of the pooling recommendation. I proposed attributed error messages and
  the `pool: off` escape rather than a mechanism to isolate runs within a
  process, because isolating them *is* spawn-per-step.
- **Statelessness is unenforceable.** A pooled engine that leaks run A's data
  into run B's outputs cannot be caught by the daemon. Stated in §7.4 rather
  than mitigated.
- **Binary-size claims.** Deliberately not quantified. The doc says to measure
  in the PR, citing `connector-extraction.md`'s 98,304-byte / 0.21% result as
  the reason not to assert.

## Anchors cited

`pkg/plugin/wire.go` — `ProtocolVersion`, `MethodEvent`, `Kind`, `Field.Scope:83`,
`Capabilities`, `Decl`, `InvokeRequest.Connection:157`.
`pkg/plugin/serve.go` — `Serve`, `dispatch`, error codes `:14`, the
`io.LimitReader` `:84`, the response-drop `:128`, per-request goroutine `:142`,
clean-EOF `:121`.
`internal/acp/jsonrpc.go` — `Conn.dispatch:121`, `serveRequest:138`,
`deliver:154`, `Call:170`.
`internal/plugin/client.go` — `DefaultCallTimeout:16`, restart consts `:27`,
`StartSource:121`, down-for-good `:164`, `verify` `:193`, protocol check `:230`,
identity forgery `:238`, `call`'s timeout `:266`, teardown-on-error `:274`,
`pluginHandler.HandleRequest:367`.
`internal/plugin/spawn.go` — `spawnBaseEnv:20`, `buildCommand`, preflight `:86`,
`NetForward`/masks `:92`, `EnforcedEgress:94`, `confineToManifest:131`.
`internal/plugin/{manifest.go,verify.go,bounded.go,manager.go,plugin.go,resolve.go,install.go}` —
`EffectiveManifest`, `DefaultMaxMessageBytes`, `specsOfKind:103`, `Spec.Key:111`,
`Spec.NotInstalledError:123`, `Spec.AllowSecrets:98`, `checkDeclKind:249/253`,
`PruneOrphans`'s kind literal `:304`, `SpecFromRef`, `InstallDir`.
`internal/code/code.go` — package doc `:5`, `Spec.Args:41-42`, `Spec.DataGuard:56`,
`DataGuard:72`, `Exec`'s switch `:113`, `wrapValue:136`,
`errRemoteInProcessEngine`.
`internal/code/kvbind.go` — `kvValueWrites:23`, `kvInvoke:25`, `nullable:71`,
`kvOps:189`, `kvInvokeJSON:198`, `KVHandle:230`, `kvGoEmbedExports:321`,
`kvRisorStoreFn:338`, `luaStoreFn:374` and its `RaiseError:391`.
`internal/code/sqlbind.go` — `sqlInvoke:23` + its `CheckCodeAccess` call `:37`,
`sqlOps:82`, `SQLHandle:118`.
`internal/code/membind.go` — `memInvoke:23` (+ scope-then-guard order `:38-53`),
`memOps:156`, `MemHandle:205`, `memScopeOf:311`.
`internal/code/{js.go,lua.go,goembed.go,risor.go,hostinterp.go,outputs.go}` —
`jsMemoryLimit:13`, `execJS`, `jsKVShim:133` and its `throw new Error:139`;
`execLua` libs/loaders `:22-44`, `goToLua:63`, `luaToGo:101`;
`goEmbedAllowlist:27`, pinned GoPath `:102`; `risorGlobals:26`;
`execHostLocal:30`, remote engine rejection `:104`; `ParseOutputs:22`.
`internal/config/use.go` — resolution comment `:26-27`, `UseKind:39`, `Dir:55`,
`officialComponentFor:79`, `OfficialRepo:110`, `PacksNamespaceAlias:119-137`,
`OfficialSource:147`, `Use`, `ParseUse:302`, bare-name branch `:392`,
`otherKind:593`, `builtinRuntimes:607`, `builtinConnectors:621`.
`internal/config/plugins.go` — `PluginRef:16`, `AllowSecrets:33`, `Kind():46`,
`PluginRefs:80`, `validatePluginRefs:133`.
`internal/config/pack_trust.go` — `SourceAllowed`, `trustMatch`,
`PluginSourceAllowed`, segment-bounded `globMatch`.
`internal/config/connectors.go` — `AllowSecrets:53`, the `run:` field comment
`:848`, `allow_secrets` glob rejection `:1461`, local-only engine validation
`:1697`.
`internal/config/agentauthored.go` — `DimRepo/DimStore/DimSecret/DimScope:176-185`,
`StepClassCode:192`, `ScopesFor`.
`internal/flow/flow.go` — `execCode:1277`, env/args render `:1282`, secret
handles `:1293`, `code.Spec` build + `planDataGuard` `:1305-1306`, raw output
`:1318`, `codeCtx:1331`.
`internal/flow/plan.go` — `planBarrierKey:452`, `containsTrackedSecret:462`,
`planDataGuard:480` (store branch `:496`, memory branch `:504`, barrier branch
`:511`), `dataValueWrite:521`, barrier install `:543`.
`internal/flow/resources.go` — header `:11-40`, two-layer enforcement note `:35`,
`resourcePolicy:62`, `planResourcePolicy:92`.
`internal/kv/backend.go` — `Capabilities:65`, `Supports:72`,
`CheckCapability:75`, the store registry `:91`.
`internal/sqlstore/sqlstore.go` — `SetCodeAccess:49`, `CheckCodeAccess:62`,
`sqliteDenied:84`, `mysqlDenied:88`, `postgresDenied:94`, `checkStatement`.
`internal/sandbox/sandbox.go` — package doc `:1-31`, `Spec:45`, `FromConfig:70`,
container flags `:231`, `systemdPrefix:252`; `internal/sandbox/env.go`
`MinimalEnv`.
`internal/store/history.go` — `RunHistory:24`, `StepRecord:56` (`Inputs:63`,
`Outputs:64`), `Sig:52`.
`internal/engine/retry.go` — header `:16-22`, `RetryRunByID:29`,
`GetHistoryVerified:33`, `retryRun:42`, force-replay guard `:82`.
`cmd/conductor/plugins.go` — `pluginManagerFor:54`.
Docs: `docs/design/use-unification.md` §D; `docs/design/runtimes-models-packs.md`
(§5.1's superseded-note style, the Evidence section's register);
`docs/design/connector-extraction.md` (the measured 98,304 bytes / 0.21%).
