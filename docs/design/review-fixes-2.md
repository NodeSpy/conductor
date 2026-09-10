# PR #60 round-2 review fixes (all verified in-code by the maintainer)

Every item was found by a round-2 reviewer/e2e AND re-verified in the code by me.
Fix ALL. The dominant theme: the round-1 fixes were correct at their core but
MISSED SIBLING PATHS sharing the same root cause — most fixes here are "apply the
existing fix to the path it missed." Add a test per fix that exercises the missed
path (fail-before/pass-after). Keep `gofmt`/`go vet`/`go test ./...` green, and the
fleet-critical degraded-boot invariant intact.

Phase in this order (severity). Flag any item that needs design rather than
half-doing it — §25–27 (non-paseo transports) are the largest.

## CRITICAL
### 1. H8 memory global-scope guard — two unguarded write paths remain
`CheckAgentScope` is wired into `harvest.go`+`ipc.go` but NOT:
- `internal/connector/memory.go` — the `memory.remember` verb calls `m.Remember(..., str("scope"), src)` with agent-supplied scope, no guard. Reachable by any `skill.verbs:[memory.*]` grant.
- `internal/code/membind.go:58` (and `MemHandle.Remember` :188) — the `run:code` memory binding, no guard.
FIX: call `memory.CheckAgentScope(scope)` (reject agent-supplied `global`, case-fold+trim) in BOTH before `Remember`. TEST: each path rejects `scope:"global"`/`"Global"`.

## HIGH — missed sibling paths (fix the same root cause the round-1 fix left open)
### 2. C1 — `workflow:` calls don't apply `WorkflowScope`
`internal/flow/flow.go` `execWorkflowCall` (~:1065-1161) runs `runSteps(stepCtx, …)` without `withIdentityScope(ctx, config.WorkflowScope(name))`, so a called workflow's steps get the CALLER's identity, diverging from `WalkSteps`. FIX: wrap the child `stepCtx` in `WorkflowScope(<called-workflow-name>)`. TEST: dispatch identity of a step in a `{workflow: helper}` call == `WalkSteps` identity `workflow:helper/<slot>`.
### 3. C1 — compensate steps collide with their primary (MED, fold here)
`internal/config/steps.go:56` `walkStep` recurses into `s.Compensate` with the SAME scope+slot → identical identity. FIX: namespace the compensate scope (e.g. a `compensate` slot suffix) so primary and its undo differ. Apply consistently in dispatch + `WalkSteps`.
### 4. C2 — `validateSourceDeclarations` reads empty `On` for list-form
`internal/config/pack_sources.go:186-209` builds "used" via `t.Connector()` (empty for list-form) → a pack using a connector only via list-form `on:` fails to load. FIX: also union `t.OnSources[*]`'s connector types. TEST: a pack whose only github use is list-form + a `connectors:{github:gh}` disambiguation loads clean.
### 5. C2 — list-form loop corrupts manual/conductor/already-bound sources
`internal/config/pack_sources.go:53-67`: the list-form loop rewrites `Source = bound+"."+event` unconditionally; the single path special-cases `bound=="" && servable` as a no-op. FIX: add the same `bound==""` no-op guard in the list loop (leave the source untouched). TEST: `on:[manual.rerun, github.pull_request]` keeps `manual.rerun` intact.
### 6. H5 — migration backup-write failure skips `restoreAll()`
`internal/migrate/auto.go:126-130`: the backup-`WriteFile` failure branch bare-returns; the tmp-write and rename branches call `restoreAll()`. FIX: call `restoreAll()` before returning on backup-write failure too — no error path may leave migrated-but-unvalidated files on disk (crash-loop risk). TEST: inject a backup-write failure after ≥1 file swapped → all originals restored.

## HIGH — new areas
### 7. Webhook empty-secret accepted (footgun default)
`pkg/sourcekit/sourcekit.go:33` `VerifyHMAC` returns `true` when secret=="", and no connector requires it. FIX (strong-default): `connectors/{github,sentry,pagerduty}` `StartSource` REFUSE to start when the resolved webhook secret is empty, unless an explicit `allow_unsigned: true` opt-in is set. (Keep `VerifyHMAC` as-is; enforce at the connector.) TEST: no-secret StartSource errors; `allow_unsigned:true` starts.
### 8. Plugin sync-loop DoS
`pkg/plugin/serve.go` runs `Invoke` synchronously in the stdin read loop (only StartSource gets a goroutine); `runtimes/paseo` `wait`/`run`/etc. shell with no timeout. FIX: (a) dispatch `Invoke` on its own goroutine (mirror StartSource) so one stuck call can't block the loop; (b) bound the paseo plugin's `exec.Command`/`cmd.Output()` and `githubkit.Invoke` with a context deadline. TEST: a slow verb doesn't block a concurrent `Describe`/other Invoke.
### 9. roster cache poisoning (from the M2 fix)
`internal/models/resolve.go:49` caches a caller's `ctx` cancel/deadline error durably in `r.failed[name]`, never cleared → bare-launch forever. FIX: do NOT durably cache `context.Canceled`/`DeadlineExceeded` — treat as "retry next call"; or run the shared `List` with a background-derived context so one caller's timeout can't poison the cache. TEST: a leader with a 1ns ctx fails; a later `context.Background()` call still resolves.
### 10. catalog cache poisoning
`internal/models/catalog.go:7` sets `loaded=true` BEFORE fetch, caches `loadErr`, never retries. FIX: set `loaded=true` only on successful load; on failure allow a later call to retry (own short backoff). TEST: first fetch fails → a later call refetches (and succeeds against a healthy source).
### 11. Legacy `steps:` bypasses both budget layers
`internal/engine/steps.go` `runSteps` gates neither the agents/hour cap nor the `$`/token spend cap that `flow.go`/`process()` apply. FIX: gate each agent dispatch in `runSteps` through the same `overAgentBudget`/`recordAgentDispatch` + `checkSpendBudget`/`recordUsage` calls. TEST: a `steps:` workflow over the cap is throttled/denied like the flow path.

## MEDIUM
### 12. `CheckRequired` unwired
`internal/models/resolve.go:488` `CheckRequired` has no callers. Design says `required:true` hard-errors at LOAD. FIX: call it from `cmdValidate` (and boot-validate) so an operator gets the guardrail; keep DISPATCH degrade-safe (log+bare-launch, never crash-loop). Fix the lying comment. TEST: `conductor validate` errors on an unsatisfiable `required:true` fleet; dispatch still degrades.
### 13. `requires.connectors` hard-error vs dormant (doc/impl mismatch)
`internal/config/pack_instantiate.go:405` hard-errors every unbound `requires.connectors`, contradicting §5.2's "dormant." RESOLUTION (strong-default = fail loud): KEEP hard-required as the default (a pack's declared connector must be bound), add optional per-connector `required:false` to opt into dormancy, and FIX the docs (§5.2 + runtimes-models-packs) to say requires.connectors is a hard requirement by default. TEST: unbound required connector errors; `required:false` goes dormant.
### 14. `StateDir` ignores `XDG_STATE_HOME`
`internal/config/config.go` `StateDir()` hardwires `$HOME/.local/state/conductor`. FIX: honor `XDG_STATE_HOME` when set (and add a `--state-dir` override for CLI/test isolation). TEST: `XDG_STATE_HOME` redirects install state.
### 15. pack `models:` unreachable
`internal/config/pack_refs.go` `rewriteStep` never rewrites `Step.Model`, so a pack-local `models:` fleet can't be referenced by the pack's steps. FIX: either namespace `Step.Model` when it names a pack-local fleet, OR remove `PackManifest.Models` as vestigial and document settings/presets. Do NOT leave it silently mis-resolving to consumer globals. TEST accordingly.
### 16. store save lock doesn't cover write+rename
Every `save*` in `internal/store/*` releases `s.mu` before `WriteFile`+`Rename`; concurrent saves of different keys (now reachable via `parallel:` branches) can lost-update at rest. FIX: a per-file write mutex (or hold through the rename). TEST: concurrent `RecordEngagement` for distinct keys never drops a record on disk.
### 17. unescaped `path` in github content URLs
`pkg/githubkit/invoke.go` `file`/`put_file`/`delete_file` interpolate raw `path`. FIX: `url.PathEscape` per segment, matching `ref`/`label`/`id`. TEST: a `path` with `?`/`#`/`..` is escaped.
### 18. paseo CLI flag-injection
`runtimes/paseo/main.go` passes user-content (`repo`/`dir`/`branch`/…) as positional argv with no leading-`-` guard. FIX: reject values starting with `-` for those fields, or insert `--` before positionals. TEST: a `--flag`-looking value is rejected.
### 19. `Runner.IndexOf` length-only match
`internal/flow/flow.go:220` compares only `len(Steps)`, not the deep equality its doc claims. FIX: implement the documented deep-equality match, or delete `IndexOf` if truly unused in prod (the test harness uses it — keep it correct). TEST: two unnamed same-`on:` equal-length triggers resolve distinctly.
### 20. stale affinity resume through reconfigured controller
`internal/controller/affinity.go:417` resumes by controller NAME against current config. FIX: record + re-validate the controller's shape (type/agent/command) on resume; on mismatch, evict+respawn rather than launch the wrong binary against a foreign session id.

## LOW
### 21. `GetPlan` returns aliased `Outputs` map — deep-copy it (`internal/store/plans.go:37`).
### 22. plugin dispatch: wrap `Invoke`/`Describe`/`StartSource` in `recover()` → JSON-RPC error, not a process crash; bound the stdin `json.Decoder` (`pkg/plugin/serve.go`).
### 23. opencode `ResumeSession` roots the server at cwd `""` not `spec.Cwd` (`internal/controller/opencode.go:171`) — pass the worktree.
### 24. orphaned `(runtime,model,key)` affinity bindings on model change held until idle-out (`internal/controller/affinity.go:164`) — drop bindings unreproducible from current config at startup instead of `hold()`-ing them.

## Pre-existing non-paseo transport bugs (now in scope — "fix them all") — LARGER, flag if any needs design
### 25. ACP session resume never loads (CRITICAL-for-ACP)
`internal/controller/acp.go` `ResumeSession` spawns fresh + `initialize`, never issues `session/load` (defined `MethodLoadSession` unused); `internal/acp/client.go` has no `LoadSession`. And `Affinity` never caches the real `Session` object, so `followup` always takes the (broken) resume path. FIX: implement `session/load` in the ACP client, call it from `ResumeSession` when `AgentCapabilities.LoadSession`, and have the controller-runner's live `Session` reachable for reuse; close the abandoned process. TEST: an ACP follow-up reuses the session, doesn't leak the process.
### 26. Non-paseo duplicate dispatch
`internal/engine/engine.go:700` hardcodes the live-agent gate to the paseo `e.disp.HasLiveAgent`; non-paseo `Runner()` returns a fresh instance with empty tracking each call. FIX: route the live-agent check through the RESOLVED controller, and cache a persistent per-controller runner in the registry so "is an agent live for this PR+kind" survives across events. TEST: a second review event for a non-paseo in-flight PR doesn't spawn a duplicate.
### 27. Non-paseo `archive_when_done` leak
`internal/engine/{steps.go:223,flow.go:394}` call `e.disp.Archive` (paseo only). FIX: archive through the resolved controller's runner; add a non-paseo idle reaper or document the controller's own cleanup. TEST: an ACP/opencode step with `archive_when_done:true` actually closes its session.

## After
`gofmt -l . && go vet ./... && go test ./...` green; a test per fix exercising the
previously-missed path; commit per severity tier; push; update PR #60. Report a
per-finding table (fixed / alt-fix+why / flagged-for-design), the real gate output,
and paste the tests proving the H8 CRITICAL is closed on ALL paths.
