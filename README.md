<h1 align="center">conductor</h1>

<p align="center">
  <img src="conductor.png" alt="conductor" width="320">
</p>

<p align="center">
  Event-driven orchestration for AI coding agents — self-hosted, config-as-code.
</p>

Connect to your services once (**connectors**), declare where agents run
(**runtimes** + **agents**), and wire events to work (**triggers**). A GitHub
review request can research in an agent, ask you on Slack, and submit the review;
a PagerDuty incident can be investigated and paged to a channel; a cron tick can
deploy over SSH and report back. Steps run agents, host commands, inline code, or
any connector's verbs — crossing service boundaries freely.

```yaml
connectors:
  gh:        { type: github, app: { … }, me: { logins: [your-login] }, repos: ["your-org/*"] }
  slack-ops: { type: slack, app_token: ${SLACK_APP_TOKEN}, bot_token: ${SLACK_BOT_TOKEN} }

runtimes:
  paseo: { type: paseo, default: true }

agents:
  fixer: { provider: claude, workspace: worktree, archive_when_done: true }

triggers:
  - on: gh.merge_conflict
    steps:
      - { id: fix, type: agent, agent: fixer,
          prompt: "Resolve the conflict on {{.repo}}#{{.pr}} against {{.base}}." }
    hooks:
      - { at: start, uses: slack-ops.post, options: { text: "conflict on {{.repo}}#{{.pr}} — on it" } }
      - { at: done,  uses: slack-ops.post, options: { text: "resolved {{.repo}}#{{.pr}}" } }
      - { at: fail,  uses: slack-ops.post, options: { text: "couldn't fix {{.repo}}#{{.pr}}: {{.error}}" } }
```

Full documentation is in the **[wiki](https://github.com/NodeSpy/conductor/wiki)**.
This README is the overview.

---

## What it is

conductor is a self-hosted daemon — a single static binary — that turns events
into agent work. You describe, in YAML:

- **Connectors** — one connection per service, each exposing event **sources**
  (`on:`) and callable **verbs** (`uses:`). Built in: GitHub, Slack, Sentry,
  PagerDuty, RSS, cron, generic webhook, durable stores, and secret vaults —
  plus generic **REST** and **GraphQL** connectors for any other API.
- **Triggers** — `on:` a source, optional `filters:`, then `steps:` and lifecycle
  `hooks:`. Steps run **agents**, host or SSH **commands**, inline **code**
  (js / lua / risor / go, sandboxed), or any connector verb — mixing services in
  one flow.
- **Runtimes + agents** — where and how agents run (Paseo by default; ACP,
  OpenCode, CLI and others via controllers), with per-agent workspaces.

It governs what agents are allowed to do, and is config-as-code: every change is
validated, audited, and — across schema changes — migrated for you.

## What sets it apart

Most workflow tools wire events to API calls. conductor's steps run **autonomous
coding agents** — they check out a repo, edit in a worktree, run commands, and open
a PR — and the platform is built to direct and contain that:

- **Agent-authored workflows** — an agent can plan a multi-step workflow at runtime
  and run it under guardrails: an allow-list of the steps, connectors, and secrets it
  may touch, so the plan can't reach past what you granted. — [Workflows](https://github.com/NodeSpy/conductor/wiki/Workflows)
- **Quality gates on agent output** — tests, lint, or a critic agent must pass before
  a proposed change is allowed to land; a failure loops back a revise. — [Gates](https://github.com/NodeSpy/conductor/wiki/Gates)
- **Governance for autonomous work** — cost/token budgets, OS-enforced isolation with
  an egress allow-list, and a secret broker so agents act *through* conductor without
  ever seeing raw secret values. — [Cost](https://github.com/NodeSpy/conductor/wiki/Cost-Accounting) · [Isolation](https://github.com/NodeSpy/conductor/wiki/Isolation) · [Agent Skill](https://github.com/NodeSpy/conductor/wiki/Agent-Skill)
- **Multi-agent teams** (planner / workers / critic), scoped **memory** that carries
  context across runs, and an **outcome-learning** loop. — [Teams](https://github.com/NodeSpy/conductor/wiki/Teams) · [Memory](https://github.com/NodeSpy/conductor/wiki/Memory) · [Outcomes](https://github.com/NodeSpy/conductor/wiki/Outcomes)

It also complements the tools you already run: another orchestrator (n8n and the
like) can call conductor's authenticated `/invoke` API for the agent work it does —
[Callable Service](https://github.com/NodeSpy/conductor/wiki/Callable-Service).

## What it can do

**The model**
- Uniform trigger grammar — `on:` sources + `uses:` verbs, with `filters:`,
  `steps:`, and `hooks:` — [Connectors](https://github.com/NodeSpy/conductor/wiki/Connectors) · [Verbs](https://github.com/NodeSpy/conductor/wiki/Verbs)
- Control flow (`if` / `for_each` / `parallel` / `retry` / `timeout`) and event
  grouping / fan-in — [Workflows](https://github.com/NodeSpy/conductor/wiki/Workflows) · [Grouping](https://github.com/NodeSpy/conductor/wiki/Grouping)
- Inline **code steps** and remote **host/SSH** commands — [Code Steps](https://github.com/NodeSpy/conductor/wiki/Code-Steps) · [Hosts](https://github.com/NodeSpy/conductor/wiki/Hosts)
- Reusable **workflows** (typed inputs/outputs, nesting) and **multi-agent teams**
  (planner / workers / critic) — [Workflows](https://github.com/NodeSpy/conductor/wiki/Workflows) · [Teams](https://github.com/NodeSpy/conductor/wiki/Teams)

**Agents & governance**
- Pluggable runtimes and agent profiles; request-response **asks** and interactive
  **hand-offs** to Slack / Discord / a web link (ephemeral, delivered over your
  own notify channel) — [Runtimes](https://github.com/NodeSpy/conductor/wiki/Runtimes) · [Agents](https://github.com/NodeSpy/conductor/wiki/Agents) · [Hand-offs](https://github.com/NodeSpy/conductor/wiki/Hand-offs)
- **Quality gates** on agent output — run tests, lint, or a critic agent before a
  proposed change promotes, looping a revise on failure — [Gates](https://github.com/NodeSpy/conductor/wiki/Gates)
- **Cost & token budgets** with spend reporting; **agent isolation / sandboxing**
  (user / namespace / container, with an OS-enforced egress allowlist); and
  **policy** precedence (global · connector · trigger) — [Cost](https://github.com/NodeSpy/conductor/wiki/Cost-Accounting) · [Isolation](https://github.com/NodeSpy/conductor/wiki/Isolation) · [Policy](https://github.com/NodeSpy/conductor/wiki/Policy)

**Data & secrets**
- Durable **stores** — KV (bolt / redis / http) and SQL (postgres / mysql /
  sqlite, parameterized) — [Connectors](https://github.com/NodeSpy/conductor/wiki/Connectors)
- Unified secret **vaults** (conductor / 1Password / pass / file / HashiCorp) and a
  broker so agents act *through* conductor without ever seeing raw secret values —
  [Secrets](https://github.com/NodeSpy/conductor/wiki/Secrets) · [Agent Skill](https://github.com/NodeSpy/conductor/wiki/Agent-Skill)
- Scoped, opt-in agent **memory** and binary / **blob** data handling — [Memory](https://github.com/NodeSpy/conductor/wiki/Memory) · [Binary Data](https://github.com/NodeSpy/conductor/wiki/Binary-Data)

**Interop & operations**
- A **callable service** — an authenticated `POST /invoke/<name>` API plus an MCP
  tool face — so another orchestrator (n8n and the like) can call conductor for the
  agent work it does — [Callable Service](https://github.com/NodeSpy/conductor/wiki/Callable-Service)
- Execution **history**, live **watch**, **retry-from-step**, and an
  **outcome-learning** loop — [Runs](https://github.com/NodeSpy/conductor/wiki/Runs) · [Outcomes](https://github.com/NodeSpy/conductor/wiki/Outcomes)
- Introspection and dry-run; self-update; and a config that **auto-migrates**
  itself across schema changes (backup + validate-before-commit, never a restart
  loop) — [Migration](https://github.com/NodeSpy/conductor/wiki/Migration)

## Quick start

Prove the pipeline with a config that needs no credentials at all — a cron
schedule plus the built-in `manual` source and a local command step:

```yaml
# ~/.config/conductor/config.yaml
connectors:
  timer:
    type: cron
    schedules:
      hourly: { every: 1h }

triggers:
  - name: hello
    on: [ timer.hourly, manual ]
    steps:
      - { id: say, type: command, command: [echo, "hello from conductor"] }
```

```sh
$ conductor validate
ok: 1 connector(s), 2 trigger(s), 0 workflow(s), 0 agent profile(s)

$ conductor run &          # start the daemon (or install the service)
$ conductor run hello      # fire the trigger on demand
```

The schedule fires the same steps every hour; `conductor run hello` fires them on
demand through the same validation, policy, and audit. From here, add real
connectors — see [Quickstart](https://github.com/NodeSpy/conductor/wiki/Quickstart)
and the per-integration pages below.

## Install

```sh
# Released binary: drops it in ~/.local/bin, seeds a starter config, and offers
# to install the background service (systemd on Linux, launchd on macOS).
curl -fsSL https://raw.githubusercontent.com/NodeSpy/conductor/main/scripts/install-release.sh | bash
```

```sh
# From source (requires the paseo CLI and gh on PATH):
git clone https://github.com/NodeSpy/conductor.git
cd conductor && ./scripts/install.sh
```

```sh
conductor service install     # run as a service (unit named "conductor")
conductor update              # self-update to the latest release
```

See [Installation](https://github.com/NodeSpy/conductor/wiki/Installation) for the
full walkthrough, service management, and updating.

## Documentation

The **[wiki](https://github.com/NodeSpy/conductor/wiki)** is the complete reference:

- **Start here** — [Home](https://github.com/NodeSpy/conductor/wiki) · [Quickstart](https://github.com/NodeSpy/conductor/wiki/Quickstart) · [Installation](https://github.com/NodeSpy/conductor/wiki/Installation) · [Configuration](https://github.com/NodeSpy/conductor/wiki/Configuration) · [Commands](https://github.com/NodeSpy/conductor/wiki/Commands)
- **The model** — [Connectors](https://github.com/NodeSpy/conductor/wiki/Connectors) · [Verbs](https://github.com/NodeSpy/conductor/wiki/Verbs) · [Workflows](https://github.com/NodeSpy/conductor/wiki/Workflows) · [Code Steps](https://github.com/NodeSpy/conductor/wiki/Code-Steps) · [Hosts](https://github.com/NodeSpy/conductor/wiki/Hosts) · [Grouping](https://github.com/NodeSpy/conductor/wiki/Grouping) · [Policy](https://github.com/NodeSpy/conductor/wiki/Policy)
- **Agents** — [Runtimes](https://github.com/NodeSpy/conductor/wiki/Runtimes) · [Agents](https://github.com/NodeSpy/conductor/wiki/Agents) · [Teams](https://github.com/NodeSpy/conductor/wiki/Teams) · [Hand-offs](https://github.com/NodeSpy/conductor/wiki/Hand-offs) · [Agent Skill](https://github.com/NodeSpy/conductor/wiki/Agent-Skill)
- **Governance** — [Gates](https://github.com/NodeSpy/conductor/wiki/Gates) · [Cost Accounting](https://github.com/NodeSpy/conductor/wiki/Cost-Accounting) · [Isolation](https://github.com/NodeSpy/conductor/wiki/Isolation) · [Secrets](https://github.com/NodeSpy/conductor/wiki/Secrets) · [Outcomes](https://github.com/NodeSpy/conductor/wiki/Outcomes)
- **Data** — [Memory](https://github.com/NodeSpy/conductor/wiki/Memory) · [Binary Data](https://github.com/NodeSpy/conductor/wiki/Binary-Data)
- **Operating** — [Runs](https://github.com/NodeSpy/conductor/wiki/Runs) · [Callable Service](https://github.com/NodeSpy/conductor/wiki/Callable-Service) · [Migration](https://github.com/NodeSpy/conductor/wiki/Migration)
- **Integrations** — [GitHub](https://github.com/NodeSpy/conductor/wiki/Integration-GitHub) · [Slack](https://github.com/NodeSpy/conductor/wiki/Integration-Slack) · [Sentry](https://github.com/NodeSpy/conductor/wiki/Integration-Sentry) · [PagerDuty](https://github.com/NodeSpy/conductor/wiki/Integration-PagerDuty) · [RSS](https://github.com/NodeSpy/conductor/wiki/Integration-RSS) · [Webhook](https://github.com/NodeSpy/conductor/wiki/Integration-Webhook) · [Cron](https://github.com/NodeSpy/conductor/wiki/Integration-Cron)
- **Extending** — [Authoring Connectors](https://github.com/NodeSpy/conductor/wiki/Authoring-Connectors) · [Controllers](https://github.com/NodeSpy/conductor/wiki/Controllers) · [Examples](https://github.com/NodeSpy/conductor/wiki/Examples)

The annotated [`config.example.yaml`](config.example.yaml) is the reference config,
and `conductor schema <connector>` prints any connector's exact contract.

## Safety

- **Kill switches** — `conductor pause` at runtime; `enabled: false` disables one
  connector or trigger in place; `policy: { shadow: true }` previews everything and
  dispatches nothing.
- **Nothing acts on an invalid config** — `validate` gates service start; a bad
  *connector* (unresolvable secret, dead credentials) disables that connector and
  notifies, rather than crash-looping.
- **Fail-safe migration** — transform → backup → validate → swap, restoring the
  original on any failure; unmappable constructs refuse loudly.
- **Loop safety** — dedup per event key, attempt caps that escalate and back off,
  a running-agent guard against double-dispatch, and bounded fan-out.
- **Secrets stay out of agents** — verbs act through conductor; every outbound call
  is audited with secret values scrubbed, and tokens are re-minted, never persisted.

## License

Private (NodeSpy).
