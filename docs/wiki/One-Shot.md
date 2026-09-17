# One-shot mode (`conductor once`) and GitHub Actions

Conductor's advanced machinery — multi-step agent workflows, code-step engines,
verbs, quality gates, the ctx data-plane — without running a daemon.
`conductor once` takes **one event**, matches it to **one named trigger**,
executes that trigger's steps **for real, in this process, to completion**, and
exits with the outcome.

It is the entry point an ephemeral runner uses. A GitHub Actions job already
has everything conductor needs: the event that fired it (`$GITHUB_EVENT_PATH`),
a checkout, and a write token. All that was missing was a way into the pipeline
that does not route through a running daemon's control socket.

```
conductor once <trigger> [--event PATH] [--event-name NAME] [--config PATH]
                         [--fixture PATH] [--fail-on LIST] [--require-match]
                         [--state-dir PATH]
```

It is a different verb from `conductor run` on purpose. `run` means "start the
daemon" (bare) or "fire a manual trigger **via** the running daemon" (with a
name); `once` is neither — it *is* the runtime, for exactly one event.

## What it does not start

The daemon's value is that it keeps watching. A runner's value is that it dies.
So one-shot mode starts **none** of the daemon's background machinery:

no control socket · no catch-up sweep · no webhook/smee watcher · no auto-update
loop · no activity digest · no archive reaper · no git-worktree orphan sweep ·
no callable HTTP service · no memory IPC socket · no Discord gateways · no
inbound listener · no workflow resume · not even the engine's own event loop.

The engine is *constructed* — that is what wires runtime resolution, identity
tokens, guidance and model selection onto the flow runner — but `Engine.Run` is
never called. The flow runner is invoked directly, synchronously, once.

## The event

Two intakes, and no HMAC in either. An Actions payload is a **trusted local
file** the runner was handed, not an inbound webhook, so there is no signature
to verify — that is the one real difference from the daemon's intake.

| Flag | Default | What it is |
| --- | --- | --- |
| `--event PATH` | `$GITHUB_EVENT_PATH` | the raw event body — the JSON GitHub wrote for this job |
| `--event-name NAME` | `$GITHUB_EVENT_NAME` | the webhook event name (`pull_request`, `issue_comment`, …) |
| `--fixture PATH` | — | a `replay`-style `{"event": "...", "body": {…}}` file, for local testing |

The body goes through the **same** source integration the daemon uses, so the
connector's own normalization (event→kind mapping, target extraction, author
facts, `filter:` routing keys) runs on an Actions payload exactly as on a
delivery.

## The trigger is explicit

`once` takes the trigger's `name:` as its one positional argument. It does not
match the event against every trigger the way the daemon does: a single job
quietly firing several workflows is surprising in a step whose success is the
job's.

```yaml
triggers:
  - on: gh.review_requested
    name: review          # <- `conductor once review`
    steps: [ … ]
```

Only named, non-manual triggers are addressable. A manual trigger (`on: manual`)
has no event to process — fire it with `conductor run <name>` against a daemon.

### A non-match is a pass

If the event does not produce this trigger, or produces it and the trigger's
`filter:` does not hold, `once` prints `no match` and exits **0**. A job that
fired on `pull_request` but whose trigger only cares about `review_requested`
has not failed — its predicate simply did not hold.

Pass `--require-match` when the whole point of the job is that the trigger
fires; the same non-match then exits non-zero.

## Outcome → exit code

| Exit | Meaning |
| --- | --- |
| `0` | the run succeeded, or the event did not match |
| `3` | the run produced a `--fail-on` outcome |
| `1` | conductor could not run at all — bad flags, bad config, an unsupported step |

The split matters in CI: "the trigger rejected the change" and "conductor could
not start" are different problems, and a job log should not conflate them.

`--fail-on` tunes which outcomes count, as a comma-separated list:

| Category | Fires when |
| --- | --- |
| `step-error` | any step failed — including a run that failed without attributing a step |
| `gate-reject` | a [[Gates\|quality gate]] escalated: the change was **not** promoted |
| `none` | nothing fails the job — the run is still reported in full |

Default: `step-error,gate-reject`. Step and gate results stream to stdout as
they happen, from the same event hub `conductor watch` tails — so the job log
and a daemon operator's live tail show the same run.

```
running trigger "review": review_requested AcmeCorp/Widget#5300 (3 step(s))
  · shape
  ✓ shape (ok) 42ms
  · fix
  ✓ gate fix: pass
  ✓ fix (ok) 18204ms
  · comment
  ✓ comment (ok) 310ms
outcome: ok
```

## State is ephemeral

A runner is thrown away, so one-shot mode defaults its store to a **temp
directory** that goes with it: dedup, attempt counts, backoff, run history and
blobs do not persist between jobs. Dedup and attempt tracking are largely moot
for a single synchronous run anyway.

Two ways to keep it:

- `--state-dir PATH` — put this run's state somewhere you control (a cache, a
  mounted volume).
- `store.state_file` in the config — an operator who pointed state somewhere on
  purpose gets it honored.

The [[Stores]] block is untouched either way. A `stores:` entry pointed at a
remote backend is exactly how a stateful CI workflow is meant to work — **that**
is the durable memory, not the daemon's local state file.

## Credentials

- **Writes** (comments, reviews, commits) use `identity.write_token` — in
  Actions, `${{ github.token }}` or a PAT the workflow provides.
- **App auth is optional and usually unnecessary.** The token is present and the
  event was delivered by Actions, so the App-less mode
  ([GitHub App setup → "Running without an App"](https://github.com/NodeSpy/conductor-plugins/blob/main/docs/connectors/github.md#running-without-an-app)) is the natural fit: no
  webhook, no smee, no installation token to mint.
- **Agent credentials** (`ANTHROPIC_API_KEY`, …) come from Actions secrets into
  the runtime's environment, gated by the runtime's normal env-scrub rules.

## What a runner cannot run

Both of these are refused **before the first step executes**, with a message
naming the step. Neither degrades silently: a step that quietly did something
else would report a green job for work that never happened.

### paseo

The paseo runtime's agents are children of the paseo daemon, which is not in the
runner and would not survive the job if it were. A step resolving to it —
explicitly, via the fleet default, or via a paseo-**type** named runtime — is a
config error:

```
config: trigger review fix: runtime "paseo" needs the paseo daemon, which a
one-shot run does not have — use a cli/acp runtime, an engine plugin, a verb or
a command in `conductor once`
```

An Actions deployment is a [[Runtimes|cli]] + [[Plugins|engine plugin]] + [[Verbs|verb]]
+ command world. That subset already runs headless; the `cli` runtime provisions
its own git worktree and needs no daemon at all.

### Anything waiting for a human

A `background: true` hand-off step and any `ask`-class verb block for a reply
that cannot arrive in CI. There is no CI-shaped decision to degrade *to* —
auto-approving a review nobody read is worse than failing — so one-shot mode
fails fast and names the step, instead of a job that hangs until its timeout.

## The action

`docs/action/` is a minimal Docker action wrapping the above. It is a
demonstrated wrapper, not the deliverable:

```yaml
- uses: actions/checkout@v4
- uses: NodeSpy/conductor-action@v1
  with:
    config: .conductor/ci.yaml
    trigger: review
  env:
    ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
    GITHUB_TOKEN: ${{ github.token }}
```

Its `Dockerfile` builds the slim conductor, optionally bakes engine plugins in
(`--build-arg ENGINES="js lua"`) so code steps need no network at job time, and
its entrypoint runs `conductor init` then `conductor once` — letting the run's
exit code be the step's. See `docs/action/README.md`.

## Not the fleet

One-shot mode is **one rich step inside a job**. It does not schedule, matrix,
or cache — Actions already does that. It does not persist fleet state from CI.
If you want durable memory, dedup across events, the sweep, the callable
service, or an interactive hand-off, run the daemon; the action is for
stateless-per-event richness.

## See also

- [[Callable-Service]] — the other non-daemon entry point (HTTP, via a *running* daemon)
- [[Runs]] — the run record `--state-dir` keeps
- [[Gates]] — what `gate-reject` means
- [[Code-Steps]], [[Plugins]] — the engines a runner can run
