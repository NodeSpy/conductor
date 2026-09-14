# Extracting paseo as a runtime plugin (issue #59) — investigation result

**Verdict: NOT extractable via the existing `kind: runtime` (ACP) plugin path
without dropping features.** This document is the honest design account: what
blocks a clean extraction (file:line evidence), what conductor would have to
build to do it right, and a recommended path. No plugin was built against the
current runtime-plugin mechanism — that would be a hollow "extraction" that
silently regresses dedup, workspace reclamation, the reaper, and hand-off
recovery-after-restart. The bundled paseo controller is untouched.

## The question

Can conductor drive paseo through the existing runtime-plugin mechanism
(`cmd/conductor/plugins.go:106` `pluginRuntimeControllers`, which turns any
`kind: runtime` plugin into a `config.ControllerConfig{Transport: "acp", ...}`
and hands it to the ordinary ACP controller, `internal/controller/acp.go`) —
i.e. is "the paseo runtime plugin" just a thin process that runs
`paseo --protocol acp` and speaks ACP, mirroring `plugins/conductor-github`
and `plugins/conductor-sentry`?

**No.** The runtime-plugin mechanism is explicitly scoped to ACP-shaped
runtimes — `docs/wiki/Plugins.md` ("The protocol" section) says so directly:
"an external runtime is an **ACP-speaking** subprocess … conductor verifies
it, then drives it through the existing ACP controller — session
create/resume, streamed status/output, cancel/cleanup." That is the whole
contract a runtime plugin gets: one subprocess, spawned per session, whose
stdio *is* the JSON-RPC transport for exactly that session, torn down when the
session closes. paseo's actual integration with conductor is not that shape.

## What conductor's paseo integration actually is

`internal/dispatch/paseo.go` does not spawn a paseo agent process and hold a
pipe open to it for the session's life. It shells out to a **stateless CLI
client** (`paseo run`, `paseo ls`, `paseo inspect`, `paseo send`, `paseo
archive`, `paseo workspace ...`) against paseo's own **persistent, multi-agent
daemon** — a daemon conductor doesn't spawn, doesn't own the lifecycle of, and
that keeps running (and keeps tracking agents, by id, across independent CLI
invocations, indefinitely, across conductor restarts) whether or not
conductor's process is even alive. Every feature below depends on that daemon
being queryable *as a whole* — "what agents exist right now, on any PR, from
any profile, any dispatch" — not on a single subprocess's stdio.

The ACP controller (`internal/controller/acp.go`) is the opposite shape: one
subprocess *is* one session. `spawnACP` (`internal/controller/acp.go:235`)
starts it, `acpSession.Close` (`internal/controller/acp.go:407`) kills it.
There is no daemon underneath for the controller layer to query, and the ACP
protocol itself has no `ls`/`inspect`/`workspace` verbs — it has
`initialize`/`session/new`/`session/prompt`/`session/cancel`.

### Features that are paseo-daemon-native, not portable to a Session/Runner

1. **Cross-dispatch, cross-profile "one worker per PR" dedup, run BEFORE
   worktree creation** — `queueOrAdopt`
   (`internal/dispatch/paseo.go:1029`), called at
   `internal/dispatch/paseo.go:59-63` before any worktree/cwd decision is
   made. It calls `liveAgentForPR` (`internal/dispatch/paseo.go:1063`), which
   runs `paseo ls --json --label conductor=1 --label pr=<key>` — a query
   against **every** agent the daemon knows about, launched by **any**
   profile/dispatch, not just ones this conductor process itself opened a
   `Session` for. If a live one exists, the new turn is queued onto it via
   `sendToAgent` (`internal/dispatch/paseo.go:1197`, i.e. `paseo send <id>`)
   *instead of* opening a new session and provisioning a new worktree at all.
   The generic `controllerRunner.Dispatch` (`internal/controller/runner.go:83`)
   has no equivalent hook: it always provisions a worktree and always opens a
   session, once per call, for whatever controller it wraps. There is nowhere
   in the `Controller`/`Session` contract to say "check the runtime's global
   state before deciding whether to launch anything."

2. **Shared scratch-workspace pooling across unrelated dispatches** —
   `resolveScratchWorkspace` / `findWorkspaceByTitle` /
   `createScratchWorkspace` (`internal/dispatch/paseo.go:786-855`). For
   `checkout: none` triage steps, conductor reuses (or lazily (re)creates) one
   shared paseo *workspace* by title, memoized under a mutex
   (`internal/dispatch/paseo.go:790-798`) and re-resolved every time in case
   the reaper archived it. This is state paseo's daemon owns (a workspace,
   independent of any one agent/session) and that many separate `Dispatch`
   calls, over the process's whole lifetime, coordinate through. An ACP
   subprocess has no notion of "workspace" at all — cwd is just an argument to
   `exec.Command`, freshly re-derived every spawn
   (`internal/controller/acp.go:144, 240`).

3. **The reaper's daemon-wide idle sweep, decoupled from any session
   object** — `internal/dispatch/reaper.go`. `Reaper.reap`
   (`internal/dispatch/reaper.go:78`) runs on its own timer
   (`internal/dispatch/reaper.go:62` `Run`), independent of the engine loop or
   any live `Session`/`Controller` instance, and polls `paseo ls --json
   --label archive=1` (`internal/dispatch/reaper.go:86`) plus `paseo workspace
   ls --json` (`internal/dispatch/reaper.go:332`) to decide what's idle. When
   an idle agent lives in a worktree it created, the reaper archives the
   **workspace**, which reclaims the worktree *and* the agent in one call
   (`internal/dispatch/reaper.go:155-160`, mirrored in `Dispatcher.Archive` at
   `internal/dispatch/paseo.go:964-972` — see point 5). It also reads a
   `.paseo-hold` marker file from the agent's cwd
   (`internal/dispatch/reaper.go:316-327`, `holdMarkerPresent`) and inspects
   `PendingPermissions`/`LastUsage` via `paseo inspect --json`
   (`internal/dispatch/reaper.go:283-303`) to decide if an agent is "waiting
   on you" vs "genuinely finished" vs "still spinning up"
   (`withinStartupGrace`, `internal/dispatch/reaper.go:311`). None of this is
   representable against an ACP `Session`: ACP has no `ls`, no `inspect`, no
   filesystem hold-marker convention, and ties a session to a live
   subprocess conductor itself must be holding open to observe it at all —
   the whole point of the reaper is to observe agents conductor is **not**
   currently holding a live handle to (because it's a separate CLI
   invocation, possibly in a different daemon process instance, hours later).

4. **Daemon-wide liveness dedup that survives a conductor restart** —
   `Dispatcher.HasLiveAgent` (`internal/dispatch/paseo.go:938-949`) runs
   `paseo ls --label conductor=1 --label pr=... --label kind=...` — true if
   *any* non-archived paseo agent matches, regardless of which conductor
   process (or restart) launched it. Compare
   `controllerRunner.HasLiveAgent` (`internal/controller/runner.go:142-146`):
   it checks an **in-process map** (`r.byPR`) that only knows about sessions
   *this* controller instance opened since it was constructed — it is
   silently empty after a restart, and blind to a second conductor process.
   The comment on `controllerRunner` even says as much:
   "deliberately process-local" (`internal/controller/runner.go:53`). Using
   it for paseo's re-dispatch dedup gate (reviews; #36) would change behavior
   across every restart.

5. **Archive-the-workspace-not-just-the-agent to reclaim worktrees** —
   `Dispatcher.Archive` (`internal/dispatch/paseo.go:964-972`): if the agent
   lives in an isolated worktree conductor created, it archives the
   *workspace* (`paseo workspace archive <id>`), which is what actually frees
   the worktree; archiving only the agent would strand the worktree forever
   (documented in the same comment, `internal/dispatch/paseo.go:955-963`).
   `controllerRunner.Archive` (`internal/controller/runner.go:149-162`) only
   calls `Session.Close`, which for an ACP session tears down the subprocess
   connection (`internal/controller/acp.go:407-413`) — there is no "workspace"
   for it to reclaim, because ACP was never given one to track.

6. **A safety net around paseo's own silent-fallback failure mode** —
   `verifyWorktree` / `agentInHome` (`internal/dispatch/paseo.go:396-430`):
   after a background `paseo run`, conductor separately calls `paseo inspect
   <id> --json` to confirm the agent didn't silently fall back to `$HOME`
   (a real observed failure mode when worktree creation flakes — see the
   comment at `internal/dispatch/paseo.go:384-392`), and archives + fails the
   dispatch if so. This is a paseo-CLI-specific post-hoc check against a
   paseo-specific fallback behavior; nothing like it exists, or is needed, for
   an ACP subprocess (its cwd is exactly what conductor passed at spawn,
   `internal/controller/acp.go:144` `spec.Cwd` → `NewSession` → `connect`).

7. **Retry-with-git-lock-clearing around the one-shot CLI invocation** —
   `internal/dispatch/paseo.go:236-270` (`isTransientPaseoErr`,
   `clearStaleGitLock`): bounded retries specifically for `paseo run`'s own
   argv-level transient git-lock failures, keyed off parsing paseo's `--json`
   error object (`paseoErrDetail`, `internal/dispatch/paseo.go:320-340`).
   Not a session-turn concept; ACP has no equivalent retry point (a
   `session/new` failure is just a failure).

8. **Interactive hand-off's worktree pinning and reaper-hold interlock** —
   `effectiveStrategy` (`internal/dispatch/paseo.go:349-363`) special-cases
   `req.Interactive` to force a dedicated PR/branch worktree even for
   `checkout: none` steps, and explicitly *never* pins it to the shared
   scratch (`internal/dispatch/paseo.go:128-129` `case req.Interactive:`).
   The `HoldSet` (`internal/dispatch/hold.go`) then keeps the reaper's hands
   off it, **persisted to disk** so the protection survives a conductor
   restart (`internal/dispatch/hold.go:16-19`). This composes worktree
   strategy + paseo's own workspace pinning (`--workspace`) + the reaper's
   daemon-wide poll + a disk-persisted hold set — four paseo/dispatch-native
   mechanisms working together. `internal/handoff` (the channel abstraction:
   `internal/handoff/handoff.go`) is genuinely controller-agnostic and *would*
   carry over to any transport — it's the layer above this that isn't.

### What already IS portable (so the plugin path isn't hopeless in general)

To be fair to the ACP runtime-plugin design: worktree provisioning, the
acts-as-you env, prompt rendering, and permission/input hand-off *are*
factored out into transport-agnostic helpers and already work for every
non-paseo controller (gemini, opencode-over-acp, agent-deck, cli):
`ProvisionWorktree`, `AgentEnv`, `RenderPrompt`, `RenderField`
(`internal/dispatch/provision.go:26,66,98,105`), consumed generically by
`controllerRunner.Dispatch` (`internal/controller/runner.go:83`) and
`acpController.NewSession` (`internal/controller/acp.go:130`). If paseo's
integration were "spawn one process, one turn, one worktree, done" — the
shape opencode/gemini/agent-deck already have — this would be a genuinely thin
wrapper, exactly like `plugins/conductor-github`. It is items 1–8 above,
specifically the **cross-dispatch, daemon-wide, restart-surviving** state
(dedup, scratch pooling, the reaper, hand-off/hold persistence) that don't
fit — because paseo, uniquely among today's runtimes, *is* a persistent
multi-agent daemon conductor talks to statelessly across many independent
invocations, not a process conductor spawns and owns for one conversation.

## What a real extraction would require

Keeping every feature above means the plugin protocol has to expose paseo's
**daemon-wide** surface, not just a session's. Two honest paths:

**Path 1 — a new `kind: runtime` sub-protocol for daemon-backed runtimes.**
Add an optional capability a runtime plugin can declare at `describe` time
(e.g. `session_model: "daemon"` vs today's implicit "acp-subprocess"), and a
second small RPC surface alongside ACP's session verbs:
`runtime.list_agents(labels) -> [...]`, `runtime.inspect(id) -> {...}`,
`runtime.archive(id_or_workspace)`, `runtime.send(id, prompt)`,
`runtime.resolve_workspace(title) -> id`. Conductor's dedup
(`HasLiveAgent`/`queueOrAdopt`), the reaper, and hand-off/hold logic would be
rewritten against this RPC surface instead of shelling out to `paseo` argv
directly — but they'd stay conductor-side (the daemon-wide *policy* — dedup,
sweep cadence, hold semantics — is conductor's, only the *queries* move
behind RPC). This preserves every feature, is the most work, and is the only
option that doesn't regress anything. It also generalizes: any future
runtime that is itself a persistent multi-agent daemon (not just paseo) gets
the same path.

**Path 2 — accept the feature tradeoff and ship a thin ACP wrapper anyway.**
Build `plugins/conductor-paseo` as a bare `paseo --protocol acp` launcher (if
that flag truly gives one ACP session per process launch — it exists per this
task's brief; nothing in this repo currently exercises it, so its actual
session/worktree/lifecycle semantics are unverified here). Ship it as an
**opt-in alternative** runtime (`runtime: paseo-acp` or similar), explicitly
documented as dropping: cross-profile dedup (each dispatch always opens a
fresh subprocess — no `queueOrAdopt`), scratch-workspace reuse (no pooling —
paseo would have to create/reclaim its own workspace per spawn, if it even
can outside its normal CLI-driven lifecycle), the reaper (idle ACP
subprocesses are just killed by `Session.Close`/timeout, not swept with
hold-marker/pending-permission awareness), and restart-surviving hand-off
protection (the `HoldSet` has nothing to protect once the agent isn't a
daemon-tracked entity anymore). This is a real, shippable thing, but it is a
**different, weaker runtime**, not an extraction of the one conductor uses
today — it must not be presented as a drop-in replacement for the bundled
`paseo` controller, and the bundled controller must keep being the default.

## Recommendation

Do not build the plugin now. File issue #59 follow-up work as Path 1
(daemon-aware runtime sub-protocol) if/when paseo's own daemon RPC surface is
something conductor can reasonably target from a plugin process — that is a
paseo-side design question as much as a conductor-side one (does paseo expose
a stable RPC for `ls`/`inspect`/`workspace`/`archive`/`send` outside its own
CLI, or would the plugin just re-shell to the `paseo` binary — in which case
the "plugin" buys isolation/versioning but not a cleaner protocol). Path 2 is
viable only as a clearly-labeled, opt-in, lesser runtime, never as the
default, and never presented as feature-equivalent.

## Evidence index

| Claim | File:line |
|---|---|
| Runtime plugins are ACP-only by design | `docs/wiki/Plugins.md` ("The protocol" → "Runtime plugin") |
| Runtime plugin → always `Transport: "acp"` | `cmd/conductor/plugins.go:134-139` |
| `Controller`/`Session`/`Runner` contract | `internal/controller/controller.go:174-237` |
| Built-in paseo controller delegates straight to `Runner.Dispatch` | `internal/controller/paseo.go:60-69` |
| ACP controller: one subprocess = one session, killed on Close | `internal/controller/acp.go:130-177, 235-287, 407-413` |
| Generic controller runner: worktree provision + open session, once per call | `internal/controller/runner.go:83-120` |
| Generic `HasLiveAgent`/`Archive` are in-process-only | `internal/controller/runner.go:53, 140-162` |
| `Dispatcher.Dispatch` routes agents to `d.paseo` (CLI shell-out) | `internal/dispatch/dispatch.go:198-217` |
| `paseo run` argv builder (the CLI-shell-out path) | `internal/dispatch/paseo.go:22-270` |
| `queueOrAdopt` cross-profile dedup before worktree creation | `internal/dispatch/paseo.go:59-63, 1011-1063` |
| Scratch-workspace pooling | `internal/dispatch/paseo.go:786-855` |
| Reaper: daemon-wide poll, workspace reclamation, hold markers | `internal/dispatch/reaper.go:62-182, 279-327, 329-357` |
| `HasLiveAgent` (daemon-wide, label-filtered) | `internal/dispatch/paseo.go:938-949` |
| `Archive` (workspace-aware reclamation) | `internal/dispatch/paseo.go:964-972` |
| `verifyWorktree`/`agentInHome` fallback safety net | `internal/dispatch/paseo.go:384-430` |
| Transient-error retry + stale git-lock clearing | `internal/dispatch/paseo.go:236-318` |
| Interactive hand-off worktree pinning + hold-set interlock | `internal/dispatch/paseo.go:90-136, 349-363`, `internal/dispatch/hold.go:1-19` |
| Portable helpers other controllers already reuse | `internal/dispatch/provision.go:26-107` |
