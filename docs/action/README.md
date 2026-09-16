# conductor-action (scaffold)

A minimal Docker action that runs **one conductor trigger** against the event
that fired the job, using [`conductor once`](../wiki/One-Shot.md).

This directory is a **demonstrated wrapper**, not the deliverable. The
deliverable is the one-shot execution mode in the binary; everything here is
~100 lines of glue showing how an Actions job reaches it. A published action
would live in its own repo (so `uses: NodeSpy/conductor-action@v1` can resolve
a tag) and ship a prebuilt image instead of building one per job.

## Files

| File | What it is |
| --- | --- |
| `action.yml` | The action definition: inputs `config`, `trigger`, optional `event`, `event-name`, `fail-on`, `require-match`, `engines`. |
| `Dockerfile` | Builds the slim conductor from this repo, optionally bakes engine plugins in (`--build-arg ENGINES="js lua"`), and installs the entrypoint. |
| `entrypoint.sh` | Maps the inputs onto `conductor once` and `exec`s it, so the run's exit code is the step's. |

## Using it

```yaml
name: conductor
on:
  pull_request:
    types: [opened, synchronize, review_requested]

jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: NodeSpy/conductor-action@v1     # or ./docs/action from this repo
        with:
          config: .conductor/ci.yaml
          trigger: review
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
          GITHUB_TOKEN: ${{ github.token }}
```

The action passes `$GITHUB_EVENT_PATH` and `$GITHUB_EVENT_NAME` straight
through — they are already in the container's environment, and `conductor once`
defaults `--event` / `--event-name` to them. You only set the inputs when you
want to run against something else (a fixture, a synthesized payload).

## What the step's result means

| Exit | Meaning |
| --- | --- |
| 0 | The run succeeded — or the event did not match the trigger, which is a pass unless you set `require-match: true`. |
| 3 | The run produced a `fail-on` outcome: a step errored, or a quality gate rejected the change. |
| 1 | conductor could not run: bad config, a missing plugin, or a step this mode does not support. |

## What does not work in a runner

- **The paseo runtime.** Its agents are a separate daemon's children, and that
  daemon is not in the runner. A step that resolves to it is a config error with
  a clear message, never a silent fallback. Use `cli` runtimes, engine plugins,
  verbs, and commands.
- **Interactive hand-offs.** A `background: true` step and any `ask` verb wait
  for a human who is not there; both are refused before the run starts, rather
  than hanging the job until its timeout.
- **State across jobs.** One-shot mode uses a throwaway store by default. Point
  a `stores:` entry at a remote backend if a workflow genuinely needs memory
  between runs — or run the daemon.

See [[One-Shot]] in the wiki for the full surface.
