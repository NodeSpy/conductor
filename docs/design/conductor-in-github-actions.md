# Conductor in GitHub Actions

Status: **proposal** (feasibility + design, for review). No code yet.

## Motivation

Conductor runs as an always-on daemon: it watches a forge for events (webhook /
smee / sweep) and dispatches agents, engines, verbs, workflows, and gates in
response. That is the right shape for a fleet operator. But a team that already
lives in GitHub Actions — and does not want to run a daemon — should be able to
reach the *same advanced machinery* (multi-step agent workflows, the review
pack, code-step engines, verbs, quality gates, the ctx data-plane) as a **step
in an Actions job**, triggered by whatever event fired the job.

The ask, concretely: `uses: NodeSpy/conductor-action@v1` runs a named conductor
trigger against `$GITHUB_EVENT_PATH`, in-process, to completion, and the job
passes or fails on the outcome.

## Why the architecture already fits an ephemeral runner

- **The event→pipeline path exists.** `conductor replay <event.json>`
  (`cmd/conductor`, main.go `case "replay"`) already feeds a real forge event
  through the entire trigger→steps pipeline; it is dry-run today, but that is a
  flag, not a structural limit. A runner has the exact event that fired it at
  `$GITHUB_EVENT_PATH`.
- **The runtimes that matter in a runner are subprocess-based.** The `cli`
  runtime (claude-code) is git-native after the checkout decouple
  (`docs/design/cli-git-worktrees.md`) — no paseo daemon — so it runs headless
  in a runner with `ANTHROPIC_API_KEY` + the checkout Actions already does. The
  **code-step engine plugins** (`docs/design/code-step-engines.md`) are
  subprocess plugins that work unchanged in a runner, as are connector plugins
  and verbs.
- **Actions hands conductor its inputs already:** the event payload, a checkout,
  and `GITHUB_TOKEN` (which maps straight onto conductor's write-token identity,
  `identity.write_token`).

## The keystone: a one-shot execution mode

Everything daemon-facing today routes through the running daemon's control
socket — `conductor run <name>` / `force` / the callable invoke surface
(`internal/callable`, `POST /invoke/<name>`). The missing piece is a **one-shot,
no-daemon mode**:

> load config → take ONE event (`--event $GITHUB_EVENT_PATH`) → match it to a
> named trigger → execute that trigger's steps **for real**, in-process, to
> completion → exit non-zero on failure.

Proposed surface (illustrative):

```
conductor run <trigger> --event $GITHUB_EVENT_PATH --once [--config PATH]
```

It is a small combination of parts that already exist:

- **`replay`** proves the event→pipeline half; `--once` makes it non-dry and
  synchronous, and turns the daemon's background loops **off** (no sweep, no
  auto-update, no webhook watcher, no reaper) — a runner processes one event and
  dies.
- **`callable`** proves the "invoke a named workflow, return the run + outcome"
  half (`callable: true` triggers, the run/outcome reporting). One-shot mode
  reuses the same run→outcome plumbing, minus the HTTP surface.
- **Exit code = outcome.** The recorded-run outcome (`conductor runs`) becomes
  the process exit status so the Action step passes/fails naturally; step output
  and gate results stream to the job log.

Estimated effort: days, not weeks — the pipeline, the runtimes, the engines, and
the outcome model are all in place; this is a new *entry point* into them, not
new machinery.

## What it is NOT

- **Not paseo.** The paseo runtime needs its own daemon + workspaces; it does not
  belong in a runner. An Actions deployment is a `cli` + engines + verbs +
  connectors world — the same subset that already runs headless. A config that
  pins `runtime: paseo` for a step is a config error in `--once` mode (clear
  message), not a silent fallback.
- **Not stateful across jobs.** A runner is ephemeral: dedup/attempts/backoff and
  the ctx stores (kv/sql/memory) do not persist between jobs unless pointed at a
  remote/cached backend. One-shot mode should default the stores to ephemeral
  (in-memory / a tmp sqlite) and document that a stateful workflow must configure
  a remote store. Dedup/attempt tracking is largely moot for a single synchronous
  run.
- **Not the fleet.** Sweep, auto-update, the callable HTTP service, the interactive
  hand-off — all daemon concerns — are off. An interactive hand-off step
  (`review-flow/handoff`) has no human to hand off to in CI; it must degrade to a
  decision (fail / auto-approve per policy) rather than block.

## Packaging

A `conductor-action` published from its own small repo (or a folder), either:

- a **Docker action** bundling the slim conductor binary + the official engines
  (self-contained, no network fetch at job time — pairs well with the binary
  slimming from the engine port), or
- a **composite action** that installs the release binary and the engines it
  needs, then invokes one-shot.

```yaml
- uses: actions/checkout@v4
- uses: NodeSpy/conductor-action@v1
  with:
    config: .conductor/ci.yaml
    trigger: review
  env:
    ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
    GH_TOKEN: ${{ github.token }}
```

Internally: resolve config → read `$GITHUB_EVENT_PATH` → `conductor run <trigger>
--event … --once` → exit code drives the step.

## Credentials & identity

- **Writes** (comments, reviews, commits) use `GITHUB_TOKEN` (or a PAT the
  workflow provides) as `identity.write_token` — the Action already scopes it.
- **App auth is optional** and usually unnecessary in a runner: the token is
  present and the event is delivered by Actions, so the App-less mode
  (`docs/wiki/GitHub-App-Setup` "Running without an App") is the natural fit —
  no webhook, no smee, the event comes from `$GITHUB_EVENT_PATH`.
- Agent credentials (`ANTHROPIC_API_KEY`, etc.) come from Actions secrets into the
  `cli` runtime's env, gated by the runtime's normal env-scrub rules.

## Open questions

- **O1 — trigger selection.** Does the Action name a `trigger:` explicitly
  (proposed), or does conductor match `$GITHUB_EVENT_PATH` against all of the
  config's triggers as the daemon would (closer to real behavior, but a single
  job firing several triggers is surprising)?
- **O2 — event fidelity.** The Actions event payload is not byte-identical to a
  webhook delivery (no `X-Hub-Signature`, some fields differ). How much of the
  github source's event-normalization (`internal/integrations/github`) must run
  on an Actions payload, and where does it need an adapter?
- **O3 — outcome→exit-code mapping.** What is "failure" for the job — any step
  error, a gate rejection, a declined review, a hand-off that couldn't resolve?
  Needs an explicit, documented mapping (and an input to soften it, e.g.
  `fail-on: [step-error, gate-reject]`).
- **O4 — stores default.** In-memory vs a tmp sqlite vs required-remote; and
  whether ctx writes that would normally hit a durable store should warn in
  one-shot mode.

## Non-goals

- Reimplementing Actions' own primitives — conductor does not schedule, matrix,
  or cache; it is one rich step inside a job.
- Persisting fleet state from CI. If you want durable memory/dedup, run the
  daemon; the Action is for stateless-per-event richness.
- paseo-in-CI.

## Relationship to other work

This composes cleanly with the code-step engine plugins
(`docs/design/code-step-engines.md`): a Docker action bundling the slim binary +
official engines gives an offline, self-contained CI step with the full engine
surface. It does not block, and is not blocked by, that work — the only shared
dependency is the `cli` runtime + subprocess-plugin model, which already exists.
