# One-shot execution mode — build report

Branch `feat/oneshot-actions`, implementation commit
**`07e4ed79e8f88b7e7f90e0f72ce17920ff7a095d`** (`07e4ed7`, on top of `5cdb96d`).

Builds the "keystone" section of `docs/design/conductor-in-github-actions.md`:
the one-shot, no-daemon execution mode, plus a minimal action scaffold. Not the
daemon-hosting variants.

## Files

| File | |
| --- | --- |
| `cmd/conductor/once.go` | the mode (new, ~590 lines) |
| `cmd/conductor/once_test.go` | 19 tests (new) |
| `cmd/conductor/main.go` | `case "once"`, `--help` line, `exitError` handling, package doc (+21/−3) |
| `internal/core/targetreads_meta_test.go` | two exceptions for the new target reads, with reasons |
| `docs/action/{action.yml,Dockerfile,entrypoint.sh,README.md}` | the action scaffold (new) |
| `docs/wiki/One-Shot.md` | the wiki page (new); `_Sidebar`, `Commands`, `Callable-Service`, `README.md` cross-link it |

`cmdRun` — the daemon path — is not touched. The `main.go` diff is five hunks:
the package doc list, the dispatch case, the usage line, and the `exitError`
branch in the error handler.

## Verification

### gofmt / build / vet

```
=== gofmt -l . ===
(empty above = clean)

=== go build ./... ===
ok

=== go vet ./... ===
ok
```

### `go test ./...`

```
=== go test ./... (failures only) ===
(empty above = all pass)
```

### `CGO_ENABLED=1 go test -race ./...` (uncached, `-count=1`)

```
ok  	github.com/NodeSpy/conductor/cmd/conductor	12.037s
ok  	github.com/NodeSpy/conductor/internal/acp	1.055s
ok  	github.com/NodeSpy/conductor/internal/blob	1.025s
ok  	github.com/NodeSpy/conductor/internal/callable	1.780s
ok  	github.com/NodeSpy/conductor/internal/code	10.019s
ok  	github.com/NodeSpy/conductor/internal/config	4.700s
ok  	github.com/NodeSpy/conductor/internal/connector	2.222s
ok  	github.com/NodeSpy/conductor/internal/controller	1.345s
ok  	github.com/NodeSpy/conductor/internal/core	2.436s
ok  	github.com/NodeSpy/conductor/internal/cost	1.019s
ok  	github.com/NodeSpy/conductor/internal/dispatch	5.572s
ok  	github.com/NodeSpy/conductor/internal/engine	2.208s
ok  	github.com/NodeSpy/conductor/internal/expr	1.023s
ok  	github.com/NodeSpy/conductor/internal/flow	18.937s
ok  	github.com/NodeSpy/conductor/internal/gitdiff	1.214s
ok  	github.com/NodeSpy/conductor/internal/gitwt	4.491s
ok  	github.com/NodeSpy/conductor/internal/handoff	3.380s
ok  	github.com/NodeSpy/conductor/internal/hosts	1.047s
ok  	github.com/NodeSpy/conductor/internal/inbound	2.569s
ok  	github.com/NodeSpy/conductor/internal/integrations/cron	1.078s
ok  	github.com/NodeSpy/conductor/internal/integrations/github	11.835s
ok  	github.com/NodeSpy/conductor/internal/integrations/rss	1.057s
ok  	github.com/NodeSpy/conductor/internal/integrations/slack	2.076s
ok  	github.com/NodeSpy/conductor/internal/integrations/webhook	1.175s
ok  	github.com/NodeSpy/conductor/internal/kv	2.353s
ok  	github.com/NodeSpy/conductor/internal/memory	2.768s
ok  	github.com/NodeSpy/conductor/internal/migrate	3.718s
ok  	github.com/NodeSpy/conductor/internal/models	2.394s
ok  	github.com/NodeSpy/conductor/internal/netguard	1.018s
ok  	github.com/NodeSpy/conductor/internal/notify	1.551s
ok  	github.com/NodeSpy/conductor/internal/plugin	4.504s
ok  	github.com/NodeSpy/conductor/internal/sandbox	4.182s
ok  	github.com/NodeSpy/conductor/internal/secrets	4.268s
ok  	github.com/NodeSpy/conductor/internal/skill	1.017s
ok  	github.com/NodeSpy/conductor/internal/sqlstore	1.077s
ok  	github.com/NodeSpy/conductor/internal/store	1.133s
ok  	github.com/NodeSpy/conductor/internal/vaults	1.039s
ok  	github.com/NodeSpy/conductor/pkg/githubkit	1.013s
ok  	github.com/NodeSpy/conductor/pkg/plugin	1.030s
ok  	github.com/NodeSpy/conductor/pkg/sourcekit	1.387s
```

### The one-shot tests

```
=== RUN   TestOnceRealExecution              --- PASS (0.01s)
=== RUN   TestOnceStepFailureExits3          --- PASS (0.01s)
=== RUN   TestOnceFailOnNonePasses           --- PASS (0.01s)
=== RUN   TestOnceFailOnGateRejectOnly       --- PASS (0.01s)
=== RUN   TestOnceNoMatchExitsZero           --- PASS (0.01s)
=== RUN   TestOnceRequireMatchFails          --- PASS (0.01s)
=== RUN   TestOncePaseoRejected              --- PASS (0.00s)
      /implicit_default
      /explicit_paseo_runtime
=== RUN   TestOnceBackgroundHandoffRejected  --- PASS (0.00s)
=== RUN   TestOnceAskVerbRejected            --- PASS (0.00s)
=== RUN   TestOnceUnknownTrigger             --- PASS (0.00s)
=== RUN   TestOnceManualTriggerRefused       --- PASS (0.00s)
=== RUN   TestOnceEphemeralStateByDefault    --- PASS (0.01s)
=== RUN   TestOnceDurableStateWhenAsked      --- PASS (0.01s)
=== RUN   TestParseOnceFlags                 --- PASS (0.00s)
=== RUN   TestReadOnceEvent                  --- PASS (0.00s)
=== RUN   TestOnceResultFiredCategories      --- PASS (0.00s)
=== RUN   TestParseFailOn                    --- PASS (0.00s)
=== RUN   TestWalkOnceSteps                  --- PASS (0.00s)
=== RUN   TestOnceRuntimeOf                  --- PASS (0.00s)
=== RUN   TestOnceStartsNoDaemonSurfaces     --- PASS (0.00s)
```

`TestOnceRealExecution` is the one that matters: the matched trigger's
`use: cli` step writes a marker file, and the test reads it back. Not a stub —
the subprocess ran. It also asserts the log contains no `dry-run` / `would run`
text.

## The surface

```
conductor once <trigger> [--event PATH] [--event-name NAME] [--config PATH]
                         [--fixture PATH] [--fail-on LIST] [--require-match]
                         [--state-dir PATH]
```

| Flag | Default | |
| --- | --- | --- |
| `<trigger>` | — | required positional: the trigger's `name:` |
| `--event PATH` | `$GITHUB_EVENT_PATH` | the raw event body |
| `--event-name NAME` | `$GITHUB_EVENT_NAME` | the webhook event name |
| `--fixture PATH` | — | a `replay` fixture `{"event":…,"body":{…}}`, instead of the pair above |
| `--config PATH` | `~/.config/conductor/config.yaml` | via the shared `configPath` |
| `--state-dir PATH` | — | keep state here instead of a throwaway dir (shared flag) |
| `--fail-on LIST` | `step-error,gate-reject` | comma-separated; `none` = never fail |
| `--require-match` | off | a non-match becomes a failure |
| `--help` / `-h` | — | prints the block above with the exit-code table |

`conductor --help` gained one line; `conductor once --help` documents the flags,
the exit codes, and where credentials come from.

**A dedicated verb, not a mode of `run`.** `run` already means "start the
daemon" (bare) and "fire a manual trigger via the running daemon" (with a name).
One-shot is neither, and overloading it would make `conductor run review` mean
two different things depending on whether a daemon is up.

## Reuse vs. divergence from `cmdReplay`

Reused verbatim — the event→pipeline half is the same code path:

- `loadConfig` → `buildIntegrations` → `validateAll` → `buildFlowStack`
- each lowered source integration's `Translate(ctx, eventName, body)`
- `stack.Runner.SpecFor(act.FlowRef)` → `FilterMatch(t, spec)` → `Runner.Run(...)`
- the fixture shape (`--fixture` reads exactly what `replay` reads)

Diverged:

| | `replay` | `once` |
| --- | --- | --- |
| dry | `buildFlowStack(cfg, nil, nil, true)`, `dispatch.New(…, true)`, `Run(…, dryRun=true)` | `buildFlowStack(cfg, st, notifier, false)`, `dispatch.New(…, false)`, `Run(…, shadow=false)` |
| store | none | an ephemeral `store.Open` (real; needed for history/events) |
| notifier | none | a live `notify.New` — a configured escalate route still reaches a human |
| triggers | every one the event yields | exactly one, named |
| runtimes | not wired | full controller registry + git-worktree provisioner + egress proxy manager, via `engine.New` |
| output | "would invoke …" prints | real step/gate events streamed from `flow.EventHub` |
| exit | always 0 | the outcome |

And from `cmdRun` (the daemon), the divergence is what it *doesn't* do. `once`
calls `engine.New` — which is what installs `flowAgentServices` onto the runner
(runtime resolution, identity tokens, guidance, model selection, budgets, rate)
— and then never calls `Engine.Run`. `New` starts no goroutines; `Run` owns the
trigger queue, the GC loop and the affinity sweep.

## Outcome → exit code

| Exit | |
| --- | --- |
| `0` | the run succeeded, or the event did not match |
| `3` | a `--fail-on` category fired |
| `1` | conductor could not run: bad flags, bad config, an unsupported step |

The 1-vs-3 split is deliberate: "the trigger rejected the change" and "conductor
could not start" are different problems and a job log should not conflate them.
`main` grew an `exitError` type to carry a non-1 status; every other command is
unaffected. When an `exitError` carries an empty message, `main` prints no
`error:` line — the run already told the story on stdout.

| Category | Fires when |
| --- | --- |
| `step-error` | a `step_done` event reported `failed`, **or** the run ended `failed` with no gate rejection attributed |
| `gate-reject` | a `gate` event reported `escalated` — the discard verdict, where the change was not promoted |
| `none` | the explicit empty set: report everything, gate on nothing |

Real CLI output for each path:

```
$ conductor once review --config …          # step failure
running trigger "review": review_requested AcmeCorp/Widget#5300 (1 step(s))
  · build
  ✗ build (failed) 2ms: code: cli: sh: exit status 7: compile error: undefined symbol
step failed build: code: cli: sh: exit status 7: compile error: undefined symbol
outcome: failed: step "build": code: cli: sh: exit status 7: compile error: undefined symbol
once: trigger "review" failed (step-error)
exit=3

$ conductor once review --fail-on none …    # the same failure, reported not gated
…
outcome: failed: step "build": code: cli: sh: exit status 7: compile error: undefined symbol
exit=0

$ conductor once comment …                  # no match
no match: pull_request did not fire trigger "comment"
exit=0

$ conductor once comment --require-match …
no match: pull_request did not fire trigger "comment"
error: once: event pull_request did not match trigger "comment" (--require-match)
exit=3

$ conductor once review …                   # a bare agent step → implicit paseo
error: config: trigger review fix: runtime "paseo" needs the paseo daemon, which a
one-shot run does not have — use a cli/acp runtime, an engine plugin, a verb or
a command in `conductor once`
exit=1

$ conductor once nope …
error: once: no trigger named "nope" (named triggers: review)
exit=1
```

### Where the outcome comes from

`flow.Runner.Run` returns nothing — it records into the run history and
publishes to `flow.EventHub`. One-shot subscribes to the hub (the same surface
`conductor watch` tails), streams each event to stdout, and derives the exit
code from what it saw. Consequence: the run must be *recorded* for events to
fire at all (`beginHistory` needs a non-nil `Store`, a non-empty `run.ID`, and
non-shadow), which is why one-shot opens a real store even in its ephemeral
default rather than passing `nil`. `stop()` unsubscribes and joins the streaming
goroutine before the exit code is computed, so no event can be lost to a race.

## paseo rejection

`onceUnsupportedSteps` runs after the flow stack is built and **before any step
executes**. It walks every step the named trigger can reach — its own steps,
parallel branches, compensations, and the steps of any workflow it calls
(transitively, each workflow expanded once; `walkOnceSteps`, the per-trigger
analogue of `config.WalkSteps`, which walks the whole config).

For an `agent`/`team` step it resolves the runtime by the same ladder every
other resolution uses (`onceRuntimeOf`: explicit `runtime:` → `DefaultRuntimeName()`
→ the built-in paseo) and errors if the answer is paseo. A **paseo-type named**
runtime (`runtimes: { box: { use: paseo } }`, then `runtime: box`) collapses onto
paseo too, so the refusal cannot be renamed around. Both cases are tested.

Non-agent steps are untouched: `cli` engines, engine plugins, verbs, commands
and code steps all run.

## Hand-off degradation — what I did, and why

**Refused up front, not degraded.** A `background: true` step and any
`ask`-class verb (`Decl.Verb(…).Ask` — slack/discord/web) are rejected by the
same preflight, naming the step:

```
config: trigger review handoff: `background: true` hands an agent off for a
human to drive, and there is no human in a one-shot run — remove it, or run
this trigger on the daemon
```

The design's own words were "degrade to a decision (fail / auto-approve per
policy) rather than block". I took the fail half and declined to build the
auto-approve half, for two reasons:

1. **There is no CI-shaped decision to degrade *to*.** The hand-off exists so a
   human reviews a proposed change. Auto-approving a review nobody read is a
   worse outcome than a failed job — it would report green for a gate that never
   ran. "Fail" is the only honest automatic answer, and a config that wants
   unattended promotion can simply not have the hand-off step.
2. **Failing at load beats failing at the step.** Left alone, a background step
   would not actually hang — `Wait: false`, and `AgentServices.Background` would
   fire with no channel — but it would *launch an interactive agent nobody ever
   drives* and then return success. An `ask` verb genuinely blocks, up to its
   1h default timeout. Refusing both before the first step runs is cheap, clear,
   and cannot silently half-run a workflow.

This is the noted decision. If a future policy grows an explicit
`handoff_in_ci: auto-approve|fail` knob, this preflight is the single place to
honor it.

## Stores default

Ephemeral (design O4). Unless told otherwise, `runOnce` creates a temp dir,
points `store.state_file` / `audit_log` at it, sets the process state-dir
override (so install state, the model catalog and the blob store follow), and
`defer`s both the removal and the override's restoration. Dedup, attempt counts,
backoff, run history and blobs go with the job — and dedup/attempt tracking is
largely moot for one synchronous run anyway.

Two escapes, both honored:

- `--state-dir PATH` — noted during flag parsing (`configPath` consumes the flag
  itself), so state stays where you put it.
- `store.state_file` set in the config — detected by comparing the loaded value
  against the default; an operator who pointed it somewhere on purpose gets it.

The `stores:` block (kv/sql/memory ctx stores) is untouched either way. A
`stores:` entry pointed at a remote backend is how a stateful CI workflow is
meant to work — that is the durable memory, not the daemon's local state file.

Tested both ways: `TestOnceEphemeralStateByDefault` asserts the configured state
dir is *empty* after a default run; `TestOnceDurableStateWhenAsked` asserts the
history directory exists with `durableState`.

## No daemon surfaces started

Empirically: every CLI run above returned immediately, and a run with an
explicit `--state-dir` left `history/`, `blobs/`, `runs.json` and `audit.jsonl`
but **no `control.sock` and no `conductor.pid`**.

Structurally: `TestOnceStartsNoDaemonSurfaces` greps `once.go` for each thing
`cmdRun` launches and fails if any appears — `serveControl(`, `eng.Run(`,
`ResumeWorkflows(`, `autoUpdateLoop(`, `digestLoop(`, `dispatch.Reaper{`,
`gitProv.Run(`, `callable.New(`, `inbound.Register(`, `memory.ListenSocket(`,
`memory.ServeIPC(`, `handoff.RunDiscordGateway(`, `ig.Start(`,
`connector.SetSweepHook(`, `connector.SetConductorOps(`, `notifier.SetPublisher(`,
`dispatch.InitSkillTunnels(`, `wireConnectorSurfaces(`, `signal.Notify`, the
pidfile write, and even `controlSockPath(` / `pidPath(`. The same test asserts
the positive half: the three call sites that must be non-dry.

A grep-based guard is coarse, but it is the right shape here — the failure mode
it protects against is a future edit copying a wiring block over from `cmdRun`,
and that is exactly what it catches.

Two daemon facilities *are* wired, deliberately, because they start nothing
until used and would otherwise silently weaken the run:

- the **egress proxy manager** — isolation network policy is enforced in CI as
  it is on a box; it starts a proxy lazily on the first dispatch that needs one,
  and `Close` tears down whatever it started.
- the **git worktree provisioner** — the `cli` runtime needs it to check out.
  Its orphan-reaper loop (`gitProv.Run`) is *not* started.

`notifier.SetPublisher` is the one daemon call I intentionally left out with a
comment: it feeds lifecycle events into `conductor.*` source triggers, which
need an engine loop to drain, and there is none.

## The action scaffold (`docs/action/`)

A Docker action. `action.yml` declares inputs `config`, `trigger`, optional
`event`, `event-name`, `fail-on`, `require-match`, and `engines`; `entrypoint.sh`
maps them onto one `conductor once` call and `exec`s it, so the run's exit code
is the step's. `$GITHUB_EVENT_PATH` / `$GITHUB_EVENT_NAME` pass through the
container environment untouched — the flags default to them, so the normal case
sets neither input.

The `Dockerfile` builds the slim conductor from the repo root, has an optional
engine pre-bake stage (`--build-arg ENGINES="js lua"` → `conductor plugin add`)
so code steps need no network at job time, and installs the entrypoint. It is
deliberately not the production daemon image: that one has a `/config` + `/data`
volume layout, a persistent non-root HOME, and a `run` entrypoint.

**Verified end-to-end.** The image builds, and driven exactly as an Actions job
would — `--network none`, workspace mounted at `/github/workspace`, event at
`$GITHUB_EVENT_PATH`:

```
$ docker run --rm --network none -v $W:/github/workspace -v $W/gh:/github/workflow \
    -e GITHUB_EVENT_PATH=/github/workflow/event.json -e GITHUB_EVENT_NAME=pull_request \
    conductor-action:test .conductor/ci.yaml review "" "" step-error,gate-reject false
no packs: or remote plugins: block — nothing to initialize
running trigger "review": review_requested AcmeCorp/Widget#5300 (1 step(s))
  · inspect
  ✓ inspect (ok) 2ms
outcome: ok
action-exit=0
```

and the failure path, same harness:

```
  ✗ tests (failed) 1ms: code: cli: sh: exit status 1: FAIL: auth_test.go:88
step failed tests: code: cli: sh: exit status 1: FAIL: auth_test.go:88
outcome: failed: step "tests": code: cli: sh: exit status 1: FAIL: auth_test.go:88
once: trigger "review" failed (step-error)
action-exit=3  (a failing job)
```

It is a demonstrated wrapper, not the deliverable. A published action would live
in its own repo (so `uses: NodeSpy/conductor-action@v1` resolves a tag) and ship
a prebuilt image rather than building one per job. `docs/action/README.md` says
so.

## Docs

- `docs/wiki/One-Shot.md` — new page: the surface, what it does not start, the
  two intakes, explicit trigger selection, the exit-code and `--fail-on` tables
  with sample job output, the stores default, credentials, both refusals, the
  action, and "not the fleet".
- `docs/wiki/_Sidebar.md` — listed under Operations, above Callable-Service.
- `docs/wiki/Commands.md` — the usage line plus a Notes entry.
- `docs/wiki/Callable-Service.md` — a See-also contrasting the two non-daemon
  entry points (callable reaches a *running* daemon over HTTP; `once` is the
  runtime itself).
- `README.md` — the Operating doc row.

## Decisions and notes

1. **Hand-off: fail fast, no auto-approve.** Covered above. The single noted
   deviation from the design's wording, and the reasoning is in `once.go`'s
   `onceUnsupportedSteps` doc comment as well as here.

2. **`ask` verbs refused too.** The design named `review-flow/handoff` steps;
   an `ask`-class verb (`uses: slack.ask`, `web.ask`, `discord.ask`) is the same
   problem through a different door and actually *does* block. Same preflight,
   same message shape. Slightly beyond the literal ask; refusing one and not the
   other would have been arbitrary.

3. **Exit 3, not 1, for a failed run.** Actions only cares zero/non-zero, but
   distinguishing "the work failed" from "conductor could not start" is free and
   useful. Cost: a new `exitError` type in `main`, inert for every other command.

4. **Manual triggers (`on: manual`) are refused** with a pointer to
   `conductor run <name>`. One-shot's contract is "process an event"; a manual
   trigger has none, and its `inputs:` surface is a different feature. Cheap to
   add later if wanted.

5. **A legacy config with no `connectors:` block is refused.** `buildFlowStack`
   returns `(nil, nil)` there, and the one-shot path is entirely connectors-model.
   The error points at `conductor config migrate`.

6. **An event producing several triggers for the same spec takes the first.**
   One job, one run. Grouping/batching is a daemon concern (it debounces a burst
   over time, which a single synchronous run has no window for) and `Run` is
   called with a `nil` batch.

7. **`--event-name` does not override a `--fixture`'s own event name.** The
   fixture names its event; a stray `$GITHUB_EVENT_NAME` in the environment must
   not silently retarget it. Encoded as `fixtureNameOverridden()` returning
   `false`, with the reasoning, rather than left implicit.

8. **The state-dir override is restored on return.** `config.SetStateDir` is
   process-global; irrelevant to the CLI (one command, then exit) but it matters
   to anything calling `runOnce` twice in a process, and leaving a deleted temp
   dir pinned would be a trap.

9. **Two new entries in `internal/core`'s target-read meta-test**, with reasons —
   the run record's display fields (mirroring `engine.newRun`, which has the same
   exception) and the job-log label. Both are display; the run ID itself goes
   through `Trigger.Key()`, which is trust-aware.

10. **`docs/design/conductor-in-github-actions.md` is referenced but is not on
    this branch.** It lives in commit `889c48b`, which is not an ancestor of
    `main` yet. I did not cherry-pick it — the design commit is clearly headed
    for `main` on its own, and duplicating it here would just create a second
    copy to reconcile. The path references in `once.go`, the wiki page and the
    action resolve as soon as that lands. Flagging it because until then those
    are dangling paths.
