# Quickstart — zero to a working trigger

This page takes you from nothing to a validated, running trigger **without
any external service or credential**, then points at the next steps. Install
first ([[Installation]] — the one-liner drops the binary and seeds
`~/.config/conductor/`).

## 1. A first trigger

Replace the seeded `~/.config/conductor/config.yaml` with (or just read along
— the seeded starter is the same shape with a github connector):

```yaml
connectors:
  timer:
    use: cron
    schedules:
      hourly: { every: 1h }

triggers:
  - name: hello
    on: [ timer.hourly, manual ]
    steps:
      - { id: say, type: command, command: [echo, "hello from conductor"] }
```

Three ideas in eight lines:

- **Connectors** are services you connect to; `cron` is the simplest — its
  declared schedule names become events (`on: timer.hourly`).
- **`on:` takes a list** — this trigger fires from the schedule *and* from
  the built-in `manual` source. A trigger with `manual` in its `on:` needs a
  unique `name:`.
- **Steps do the work** — here a `type: command` step running a local
  command.

## 2. Validate, run, fire

```sh
$ conductor validate
ok: 1 connector(s), 2 trigger(s), 0 workflow(s), 0 agent profile(s)

$ conductor run &        # start the daemon (or: conductor service install)
$ conductor run hello    # fire the manual source on demand
dispatched manual trigger "hello"
```

The daemon log shows the run; the schedule fires the same steps every hour.
`conductor validate` is the contract: it resolves every `on:` kind,
`filters:` key, `uses:` verb, option, and `{{…}}` reference against the
connectors' published schemas **before** anything runs — a typo fails here,
not at 3am.

Two introspection commands you will use constantly:

```sh
conductor connectors ls    # what is configured: state, events, verbs
conductor schema timer     # a connector's full contract (works on bare type names too)
conductor schema github    # every github event, filter, verb, option, output
```

## 3. Chain steps — outputs flow forward

Steps see the trigger context plus every **prior** step's outputs. A `run:`
code step is the glue — reshape one step's output for the next, no external
interpreter needed (`js` runs in a WASM sandbox inside conductor):

```yaml
connectors:
  timer:
    use: cron
    schedules:
      nightly: { cron: "0 2 * * *" }

triggers:
  - name: disk-report
    on: [ timer.nightly, manual ]
    steps:
      - { id: disk, type: command, command: [df, -h, /] }
      - id: shape
        run: js
        code: |
          const lines = ctx.disk.stdout.trim().split("\n");
          return { summary: lines[lines.length - 1] };
      - { id: say, type: command, command: [echo, "disk: {{.shape.summary}}"] }
```

`ctx` inside code is the same scope templates see: `ctx.disk.stdout` is the
first step's output, and the returned object becomes `{{.shape.summary}}`
for the third. `conductor run disk-report` fires it on demand.

## 4. Where to go next

Follow the **learning path** on [[Home]]. The immediate next steps:

- **Connect a real service** — [[GitHub-App-Setup]] then
  [[Integration-GitHub]]: events like `gh.merge_conflict` /
  `gh.failing_checks` replace the cron tick, and verbs like `gh.comment`
  replace `echo`.
- **Run an agent** — add a `runtimes:` + `agents:` profile and a
  `type: agent` step ([[Runtimes]], [[Agents]]); the seeded starter's
  triggers show the shape:

  ```yaml
  runtimes:
    paseo: { use: paseo, default: true }
  agents:
    fixer: { provider: claude, workspace: worktree, archive_when_done: true }
  triggers:
    - on: gh.merge_conflict
      steps:
        - { id: fix, type: agent, agent: fixer,
            prompt: "Resolve the conflict on {{.repo}}#{{.pr}} against {{.base}}." }
  ```

- **React to the run itself** — `hooks:` fire at `start`/`done`/`fail`
  ([[Workflows]]); post to Slack when a fix lands, page when it fails.
- **Test without side effects** — `conductor replay <event.json>` runs a
  saved event through the pipeline with every outbound verb stubbed
  ([[Commands]]).

Related: [[Home]] · [[Installation]] · [[Configuration]] · [[Examples]]
