<div align="center" width="100%">
  <img src="paseo-conductor.png" width="320" alt="paseo-conductor" />
</div>

# paseo-conductor

Event-driven agent orchestration for your local [Paseo](https://paseo.sh) daemon.

paseo-conductor receives events from external services and runs Paseo coding agents (or
deterministic tools) on your machine in response. **GitHub is the first integration**; the core is
integration-agnostic so more can be added.

It is **webhook-primary** (GitHub App → [smee.io](https://smee.io) → conductor) and **single-user**:
no database, no web UI, no accounts — one Go binary run as a per-user service.

## Quick start

Install the latest release (the repo is private, so this uses the authenticated `gh` CLI). It drops
the binary in `~/.local/bin`, seeds a starter config, and **asks whether to install the background
service** (systemd on Linux, launchd on macOS):

```sh
curl -fsSL https://gist.githubusercontent.com/danielcbaldwin/3504ed91ac1014b3f073f44916e443a7/raw/install-release.sh | bash
```

Then:

1. Create a GitHub App + a smee channel — see [GitHub App setup](#github-app-setup).
   (smee.io is just a webhook relay — there's **nothing to install**; the conductor connects to the
   channel itself.)
2. Fill in `~/.config/paseo-conductor/config.yaml` (app id, repos, your login) and
   `~/.config/paseo-conductor/conductor.env` (secrets), then set the github integration
   `enabled: true` — see [Configuration](#configuration). (The seeded starter is valid but disabled.)
3. `paseo-conductor validate` → start the service (the installer offers this).

Details and other install paths are under [Install](#install-released-binary-one-liner). Later:
`paseo-conductor update` (or `update.auto`) keeps it current.

## What it does (GitHub)

Every kind below is a configurable `action` (see [Configuration](#configuration)).

**Your authored PRs**

| Kind | Trigger | Action |
| --- | --- | --- |
| `merge_conflict` | PR becomes conflicting | agent: resolve + push |
| `pr_behind` | PR is behind base | `gh pr update-branch` |
| `failing_checks` | CI fails (a check reaches a failing conclusion) | flake-rerun once, then agent: fix + push |
| `stuck_checks` | a CI run is stuck `in_progress` past `stuck_after` (dead runner — never completes, so `failing_checks` can't catch it) | anything: re-run the run (`{{.run_id}}`) so checks finish. Its own periodic watcher (not the sweep): polls the rule's repos every `poll_interval` (default 15m), your open PRs |
| `changes_requested` | a review requests changes | agent: address + push |
| `new_comment` | a comment / bugbot review | agent: act + reply |
| `merge_ready` | fully green: mergeable, approved by another reviewer, all threads resolved, not draft | `gh pr merge` |

**Reviews**

| Kind | Trigger | Action |
| --- | --- | --- |
| `review_requested` | your review is requested on a PR — or a draft with you already requested is marked **ready for review** | run [critique](https://github.com/EdnitionCode/critique), post as you |
| `self_review` | you open/update your own PR | critique your own PR |

**Issues**

| Kind | Trigger | Action |
| --- | --- | --- |
| `issue_matched` | an issue **assigned to you** matches your filters (labels/author/assignees/gates) — re-evaluated on any issue change *or* Projects v2 move | agent: start work on a fresh branch |

`issue_matched` is the one issue trigger: it fires on `issues` events (opened/edited/labeled/assigned/…)
and on `projects_v2_item` moves, re-checking the issue's *current* state each time — so a plain
assignment, a label change, or a drag to a "Ready" column all funnel through the same matcher. With no
filters it's just "assigned to me"; add `labels_all`/`authors`/`sole_assignee`/`gates` to narrow it.

**Releases**

| Kind | Trigger | Action |
| --- | --- | --- |
| `release` | a release is **published** in a matched repo | anything: cut docs, post an announcement, kick a downstream job |

`release` fires on the `release` event's `published` action. Prereleases are **skipped by default**
(set `include_prereleases: true` to opt in); draft publishes are ignored. `{{.tag_name}}`,
`{{.prerelease}}` and the release URL (`{{.url}}`) are available to the action's prompt/command.
Dedup is per tag. Subscribe the App to the `releases` event.

**CI/CD & security**

| Kind | Trigger | Action |
| --- | --- | --- |
| `deployment_status` | a deployment reports **failure/error** | agent: start triage (`{{.environment}}`/`{{.state}}`/`{{.url}}`) |
| `dependabot_alert` | a Dependabot alert is **created** | agent: assess/bump (`{{.severity}}`/`{{.package}}`/`{{.summary}}`) |
| `secret_scanning_alert` | a secret-scanning alert is **created** | rotate/investigate (`{{.secret_type}}`/`{{.url}}`) |

Deployment fires only on `failure`/`error` (success/pending ignored); the two alert kinds fire on the
`created` action and dedup per alert number. Subscribe the App to `deployment_status`,
`dependabot_alert`, and/or `secret_scanning_alert`.

Plus scheduled jobs via the [cron integration](#scheduled-jobs-cron-integration). Each kind is a
configurable action — enable, disable, and tune it per repo in [config](#configuration).

## Reacting to issues

Issue triggers are configured like any other kind — under a github rule's `actions`.
`issue_matched` matches when the assignee is **you** (`me`) by default; set the action's own
`assignee: { logins: [...] }` only to override. Subscribe the App to the `issues` event (assign/label/
edit changes) and, if you match on board columns, `projects_v2_item` (Projects v2 moves).

```yaml
integrations:
  - type: github
    name: default
    app:
      app_id: 123
      private_key_path: ~/.config/paseo-conductor/github-app.pem
      webhook_secret: ${GH_WEBHOOK_SECRET}
    webhook:
      smee_url: ${GH_SMEE_URL}
    defaults:
      me: { logins: [octocat] }          # your GitHub login(s) — also the default assignee/reviewer
      actions:
        issue_matched:                       # STATE-BASED: fires when an issue assigned to you
          type: agent                        #   matches these filters — re-evaluated on any issue
          agent: fixer                       #   change (labeled/assigned/edited/…) or Projects v2 move,
          checkout: branch-off               #   once per issue. With no filters below it's just
          labels_all: ["Ready", "backend"]   #   "assigned to me". require ALL of these labels
          exclude: { labels: ["blocked"] }   # ...and NONE of these (also exclude.title)
          authors: [octocat]                 # ...opened by one of these (optional)
          sole_assignee: true                # ...and you're the only assignee (optional)
          gates:                             # GraphQL-backed, checked only if the above matched:
            no_branch: true                  #   skip if a branch/PR already exists for the issue
            project: { Priority: [High, Urgent] }  #   Projects v2 field must match
          prompt: "Issue {{.repo}}#{{.issue}} is ready — start work on a fresh branch."
    rules:
      - match: { repos: ["octocat/*"] }
agents:
  fixer: { provider: claude, workspace: worktree }
```

`issue_matched` can be a **list** of named variants (different labels → different agents); see
[Named action variants](#named-action-variants). For a smarter single flow — assess the issue, then
implement it *or* ask the reporter for more detail — make it a
[multi-step workflow](#multi-step-workflows).

### Adopting an open workspace

By default, PR feedback (`new_comment`, `changes_requested`) that isn't already owned by a conductor
agent spawns a fresh worktree. If you often start work on a PR yourself and keep that workspace open,
set top-level **`adopt_open_workspaces: true`**: conductor then looks for an agent whose checkout is
already on the PR's head branch — the one you opened — and routes the feedback *there* (`paseo send`)
instead of duplicating a worktree. If several agents sit on that branch it picks the most-recently
active; if none, it starts fresh as usual. An adopted agent is yours — conductor never relabels or
reaps it.

## Multi-step workflows

An action can be a single run, or a **`steps:`** list — an ordered workflow where each step can use
a different agent/model, produce structured **output**, and gate on an **`if:`** condition over
earlier outputs. This lets you plan with a cheap model, then act with a stronger one, and branch on
what the plan found.

```yaml
issue_matched:
  labels_any: ["Ready"]
  steps:
    - id: evaluate                       # cheap model assesses the issue
      type: agent
      agent: planner
      output_schema:                     # agent returns JSON matching this
        type: object
        properties: { has_context: { type: boolean }, summary: { type: string } }
      prompt: "Is there enough detail to implement {{.repo}}#{{.issue}}? Return has_context + summary."
    - id: work                           # only if there's enough context
      if: "steps.evaluate.outputs.has_context == true"
      type: agent
      agent: fixer
      checkout: branch-off
      prompt: "Implement {{.repo}}#{{.issue}} — {{.steps.evaluate.outputs.summary}}. Open a draft PR."
    - id: ask                            # otherwise, ask the reporter
      if: "steps.evaluate.outputs.has_context == false"
      type: command
      command: ["gh","issue","comment","{{.repo}}#{{.issue}}","--body","Please add repro/scope."]
      env: { GH_TOKEN: "{{.gh_token}}" }
```

- **Outputs**: agent steps set `output_schema` (JSON schema) and emit a matching object; command
  steps expose their stdout as JSON (or `.text`). Reference them with
  `{{ .steps.<id>.outputs.<key> }}` in later prompts/commands.
- **Conditions** (`if:`): dotted paths; `==` / `!=` against literals (`true`, `"question"`, `7`);
  numeric ordering `>` / `<` / `>=` / `<=` (e.g. `steps.evaluate.outputs.score >= 8` for scoring);
  truthiness (`x` / `!x`); and `&&` / `||`.
- Steps run in order, fail-fast, and the whole workflow runs off the main loop so long steps don't
  block other events.
- **`background: true`** on a step hands off a live agent for *you* to drive and close (e.g. a manual
  review). The reaper never archives a hand-off, and it never shares the auto scratch workspace: with
  a PR/repo it gets its own PR/branch worktree (PR-centric, even if `checkout: none` was set),
  otherwise its own dedicated workspace. The shared scratch is reserved for auto, non-interactive
  `checkout: none` steps (like a triage/assess step). By default that review is driven in paseo
  itself; see [Controllers](#controllers) for running it over a portable web/Slack hand-off instead
  (useful when the step's agent isn't on paseo).

## Controllers

Every dispatched agent runs on the built-in **paseo** controller unless you say otherwise —
controllers are optional. With no `controllers:` block in config, resolution always yields paseo and
existing behavior is unchanged. Configure `controllers:` (a map, like
`agents:`) to run some or all agents on a different runtime instead — an ACP agent, opencode's own
HTTP API, agent-deck, or a bare CLI recipe — and pick one per agent via `agents.<name>.controller`.

### Resolution order

1. An explicit `controller:` on the agent's profile.
2. The controller entry flagged `default: true` (at most one may be; validated at load).
3. The built-in `paseo` controller.

```yaml
controllers:
  gemini-review:                    # `agent:` + no transport → ACP; runs the gemini CLI over
    agent: gemini                   #   stdio. The runtime IS gemini, so provider/model below don't apply.
  opencode:                         # `type: opencode` → opencode's HTTP API (`opencode serve`),
    type: opencode                  #   which routes by provider/model.
  claude-cli:                       # `transport: cli` → a bare per-tool recipe (claude-code and
    agent: claude-code              #   codex ship built-in recipes; anything else needs `command:`).
    transport: cli

agents:
  reviewer:
    controller: gemini-review       # runs gemini over ACP — no provider needed (the controller is the agent)
  planner:
    controller: opencode            # runs via opencode…
    provider: anthropic             # …which routes to this provider + model
    model: claude-sonnet-4-5
  fixer:
    provider: claude                # no controller: → the built-in paseo runtime runs claude
    model: opus
```

`provider`/`model` select the model for the controllers that route by provider — the built-in
**paseo**, plus **opencode** and **agent-deck**. For an **acp** or **cli** controller the runtime *is*
the named `agent`/`command`, so `provider`/`model` on the profile don't apply. Add `default: true` to
one controller entry to make it the fleet default for agents that set no explicit `controller:`.

### Kinds

| Config | Runtime | Session model |
| --- | --- | --- |
| `type: paseo` (built-in, always registered as `paseo`) | The existing `paseo` CLI dispatcher | native |
| `agent: <name>` (or explicit `transport: acp`) | An ACP agent over JSON-RPC 2.0 on stdio — gemini, claude-code-acp, codex-acp, goose, opencode over ACP, … (the [Agent Client Protocol](https://agentclientprotocol.com), Zed's open standard; conductor is the ACP client) | native, upgraded to resumable if the agent negotiates `loadSession` |
| `type: opencode` (or `agent: opencode` + `transport: native`) | opencode's native HTTP server (`opencode serve`) — its own model routing/cost accounting | resumable |
| `type: agent-deck` | The `agent-deck` CLI orchestrator (`launch` / `session send` / `session show` / `remove`) | native |
| `transport: cli` | A bare per-tool command recipe run as a direct subprocess (built-in recipes for `claude-code` and `codex`; anything else needs an explicit `command:`) | resumable (e.g. claude-code `--resume`) or oneshot (codex, a generic `command:`) |

An unrecognized `type`/`transport` stays registered as a stub: it negotiates capabilities but returns
an error when selected instead of falling back to another runtime, so config can name a controller
this build doesn't drive yet without silently running the agent somewhere else.

### Session models

- **native** — the controller owns the whole session lifecycle (paseo, agent-deck).
- **resumable** — a session survives by id and is resumed on demand, rather than held as a live
  process (an ACP agent advertising `loadSession`, opencode, or a `cli` recipe that can `--resume`).
- **oneshot** — each turn is a fresh process; no persistent session.

### Session broker and hand-off channels

Two portable capabilities sit above the controller layer, so a session behaves the same regardless of
which runtime it runs on:

- The **session broker** keeps one live/resumable session per PR and funnels follow-ups to it — a
  burst of triggers collapses onto the same session instead of spawning duplicates, and the PR→session
  binding is persisted so an interactive hand-off survives a conductor restart (the next follow-up
  re-attaches by id instead of orphaning the old session).
- An optional **`handoff:`** block adds a portable web-link review channel (a draft page with
  approve/revise/discard, served on conductor's inbound listener):
  ```yaml
  handoff:
    web:
      base_url: https://conductor.example.com   # public origin the draft link points at
      listen: :8099                             # inbound address the draft page is served on (default :8099)
  ```
  With it configured, a `background: true` step's review (see [Multi-step workflows](#multi-step-workflows))
  runs over that channel's present → await → revise/submit loop instead of paseo's own UI — the only
  way to interactively review an agent on a controller with no native interactive surface (`cli`, or
  opencode's native transport). With no `handoff:` block, review hand-off keeps today's paseo-native
  behavior, unchanged. A Slack hand-off channel (thread-based approve/revise/discard) exists in
  `internal/handoff` but isn't wired into the daemon yet.

Escalations and hand-off prompts go out through the same [notify](#configuration) channels as
everything else (journal, plus any configured Slack/Discord/ntfy/Pushover/Notifiarr) — controllers
don't add a separate notification path.

### Testing

The Dockerized e2e harness at [`test/e2e/`](test/e2e/README.md) exercises the full controller matrix
(every kind above, resolution, the session broker, hand-off, and failure/escalation) against an
isolated local forge + mock GitHub API — `make e2e` (hermetic stubs, no keys, CI-safe) and
`make e2e-live` (the real agent CLIs installed on your box, manual).

## Named action variants

Any kind's action can be a **single mapping** (the common case) **or a list of named variants** —
same repo, same kind, different routes. On a matching event every variant is evaluated
independently; each one that applies dispatches its own agent with its own dedup/attempt state
(keyed `kind#name`). An unnamed single action keeps the bare `kind` key (nothing changes for
existing configs).

```yaml
actions:
  merge_conflict: { type: agent, agent: opus }     # single, unnamed — unchanged

  issue_matched:                                    # a list → named variants
    - name: backend
      agent: backend-fixer
      labels_all: [Ready, backend]
    - name: frontend
      agent: frontend-fixer
      labels_all: [Ready, frontend]
```

The variant name also appears as a `variant=<name>` label on the dispatched agent and in the logs.

## Scheduled jobs (cron integration)

A second built-in integration, `cron`, runs actions on a schedule — the same `command`/`agent`
actions the GitHub integration uses. Each schedule takes a `cron` spec (standard 5-field, or
`@daily`/`@every 6h`) or an `every: <duration>`, plus an `action`:

```yaml
integrations:
  - type: cron
    name: chores
    schedules:
      - name: rate-limit-check
        every: 6h
        action: { type: command, backend: local, command: ["gh", "api", "rate_limit"] }
```

Agent actions with no repo context run in the base workspace (`checkout: none`).

## Generic webhooks (webhook integration)

The `webhook` integration is a force-multiplier: any service that can POST JSON — CloudWatch/SNS,
Statuspage, a vendor, a form — becomes a trigger with no bespoke Go. One instance hosts many named
**sources**; each maps body fields into the trigger via templates (`{{.body.<...>}}`), optionally
verifies an HMAC signature, and carries one or more [actions](#named-action-variants).

Delivery is either a direct HTTP `listen:` address or a `smee_url:` channel (no public ingress — the
same trick the GitHub integration uses). The parsed body is exposed to the action's own
prompt/command as `{{.body.<...>}}`.

```yaml
integrations:
  - type: webhook
    name: inbound
    smee_url: ${WEBHOOK_SMEE_URL}      # and/or  listen: ":8099"
    sources:
      - name: cloudwatch               # becomes the trigger Kind
        path: /hooks/cloudwatch        # required with a listener; smee routes by `match`
        sign: { header: X-Amz-Signature, secret: ${CW_SECRET}, scheme: hex }   # optional HMAC
        match: '{{if eq .body.detail.state "ALARM"}}true{{end}}'  # optional: only fire when true
        title: "{{.body.detail.alarmName}} → {{.body.detail.state}}"
        dedup: "{{.body.detail.alarmName}}-{{.body.time}}"        # "" = fire on every delivery
        repo: EdnitionCode/infra       # optional: a real repo enables checkout; omit → runs in scratch
        actions:
          type: agent
          agent: fixer
          prompt: "CloudWatch alarm {{.body.detail.alarmName}} fired — investigate {{.repo}}."
```

A source with no `repo` runs `checkout: none` automatically (its synthetic id isn't clonable). With a
`smee_url` and more than one source, each source needs a `match` predicate to route the shared channel.

## Sentry alerts (sentry integration)

The `sentry` integration turns a production error/regression/spike into an agent that root-causes it
against the affected repo. It consumes Sentry's Integration-Platform webhooks (resources `issue`,
`error`, `event_alert`) — no field mapping needed, it knows the payload shape — and verifies the
`Sentry-Hook-Signature` HMAC with your integration's client secret. Delivery is a direct `listen:` or
a `smee_url:` channel.

`rules` route by `project` / `level` / `environment` (empty = match-any, case-insensitive); the
**first** matching rule wins and names the repo to investigate. The alert's `{{.sentry.title}}`,
`{{.sentry.level}}`, `{{.sentry.culprit}}`, `{{.sentry.short_id}}` and `{{.url}}` (permalink) are
available to the action. Dedup is per Sentry short-id, so repeated alerts on the same issue don't pile up.

```yaml
integrations:
  - type: sentry
    name: prod
    smee_url: ${SENTRY_SMEE_URL}       # and/or  listen: ":8098"
    client_secret: ${SENTRY_CLIENT_SECRET}
    rules:
      - match: { projects: [rosterstream], levels: [error, fatal], environments: [production] }
        repo: EdnitionCode/RosterStream          # investigated + checked out
        actions:
          type: agent
          agent: fixer
          checkout: branch-off
          prompt: "Sentry {{.sentry.short_id}}: {{.sentry.title}} ({{.sentry.culprit}}). Root-cause in {{.repo}} — see {{.url}}. Open a draft PR or a findings issue."
```

Set it up as an **Internal Integration** in Sentry (Settings → Developer Settings), subscribe to the
`issue`/`error` webhooks, and point the webhook URL at your `listen` address or a smee channel.

## PagerDuty incidents (pagerduty integration)

The `pagerduty` integration turns a page into an agent that starts triage against the affected repo.
It consumes PagerDuty **V3 webhook subscriptions** over a direct `listen:` or a `smee_url:` channel, and
verifies the `X-PagerDuty-Signature` HMAC (it accepts the multiple `v1=` signatures PagerDuty sends
during secret rotation).

`rules` route by `event_types` (`incident.triggered`, `incident.escalated`, …), `services` (summary or
id), `urgencies` (`high`/`low`) and `priorities` (`P1`, …) — empty = match-any; the **first** matching
rule names the repo to triage. The incident's `{{.pagerduty.title}}`, `{{.pagerduty.urgency}}`,
`{{.pagerduty.priority}}`, `{{.pagerduty.service}}` and `{{.url}}` (the incident page) are available to
the action. Dedup is per incident id + event type.

```yaml
integrations:
  - type: pagerduty
    name: oncall
    smee_url: ${PAGERDUTY_SMEE_URL}    # and/or  listen: ":8097"
    signing_secret: ${PAGERDUTY_SIGNING_SECRET}
    rules:
      - match: { event_types: [incident.triggered, incident.escalated], urgencies: [high], services: [RosterStream API] }
        repo: EdnitionCode/RosterStream          # triaged + checked out
        actions:
          type: agent
          agent: fixer
          checkout: branch-off
          prompt: "PagerDuty {{.pagerduty.title}} ({{.pagerduty.priority}}/{{.pagerduty.urgency}}) on {{.pagerduty.service}} — {{.url}}. Start triage in {{.repo}}: find the likely cause, propose a mitigation."
```

Create a **V3 webhook subscription** in PagerDuty (Integrations → Generic Webhooks) for the incident
events you care about, and point it at your `listen` address or a smee channel.

## Upstream feeds (rss integration)

The `rss` integration polls RSS/Atom feeds and turns new items into triggers — watch the Canvas API
changelog, an LTI spec feed, or a key dependency's releases and have an agent assess "does this
affect RosterStream?" Feed parsing is stdlib-only (no new dependency).

Each `feed` has a `url`, an `interval` (default 30m), an optional case-insensitive `match` regex (over
title+summary), an optional `repo`, and [actions](#named-action-variants). New items expose
`{{.item.title}}`, `{{.item.link}}` (`{{.url}}`), `{{.item.summary}}`, `{{.item.published}}`.

```yaml
integrations:
  - type: rss
    name: upstream
    feeds:
      - name: canvas-changelog
        url: https://example.instructure.com/doc/api/file.changelog.html   # any RSS/Atom URL
        interval: 1h
        match: "deprecat|breaking|remov|LTI|roster"   # only items worth a look
        actions:
          type: agent
          agent: planner
          checkout: none
          prompt: "Canvas changelog: {{.item.title}} ({{.url}}). Does this affect RosterStream's adapters? File a heads-up issue if so."
```

**Cold-start:** the first poll of each feed silently seeds a seen-set so an existing backlog isn't
back-emitted; later polls emit only new items. Each item carries a stable dedup (its GUID/id), so the
engine store also suppresses re-acting across restarts. A feed with no `repo` runs `checkout: none`.

## Slack control plane (slack integration)

The `slack` integration is a control plane: an @-mention, an emoji reaction, or a slash command
dispatches an agent, and you steer it from the thread. It connects over **Socket Mode** (an outbound
WebSocket), so it needs no public URL.

`triggers` route by `on` (`app_mention` / `reaction_added` / `slash_command`), with an optional
`reaction` emoji or `command` filter, to [actions](#named-action-variants). The event exposes
`{{.slack.text}}`, `{{.slack.channel}}`, `{{.slack.user}}`, `{{.slack.thread_ts}}` and the bot token
(`{{.slack_bot_token}}`) so a command action can post a threaded reply.

Each trigger can carry three optional feedback blocks — `ack` (fired when the rule matches and
dispatches), `on_done` (fired when the dispatched work finishes successfully), and `on_fail` (fired
when it fails):

```yaml
integrations:
  - type: slack
    name: ops
    app_token: ${SLACK_APP_TOKEN}      # xapp-… (Socket Mode; needs connections:write)
    bot_token: ${SLACK_BOT_TOKEN}      # xoxb-… (posting)
    triggers:
      - on: app_mention                # "@conductor <task>" in any channel the bot is in
        ack:
          react: eyes                  # reactions.add on the mention message
        on_done:
          react: white_check_mark
        on_fail:
          react: x
          say: "couldn't finish: check the logs"
        actions:
          type: agent
          agent: fixer
          checkout: none
          prompt: "Slack request from <@{{.slack.user}}>: {{.slack.text}}. Do it and report back."
      - on: reaction_added             # react :eyes: on a message to trigger
        reaction: eyes
        actions: { type: agent, agent: fixer, checkout: none, prompt: "Look into: {{.slack.text}}" }
      - on: slash_command
        command: /conductor
        ack:
          say: "on it"
          ephemeral: true             # visible only to the user who ran the command
        actions: { type: agent, agent: fixer, checkout: none, prompt: "{{.slack.text}}" }
```

A trigger with no `ack`/`on_done`/`on_fail` block is silent for that moment (the default — dispatching
alone produces no Slack response). Each block is:

| field       | meaning                                                                             |
|-------------|--------------------------------------------------------------------------------------|
| `react`     | emoji name without colons; `reactions.add` on the triggering message. `slash_command` has no message to react to, so `react` there is a no-op (logged, not an error). |
| `say`       | text posted via `chat.postMessage` (or `chat.postEphemeral`, see below). Templated over `{{.channel}}`, `{{.user}}`, `{{.text}}`, `{{.ts}}`, `{{.thread_ts}}`, `{{.reaction}}`, `{{.command}}`. |
| `ephemeral` | `say` goes only to the triggering user via `chat.postEphemeral` instead of the channel. Falls back to a normal post if the event carries no user id. Requires `say`. |
| `in_thread` | post `say` in the triggering message's thread instead of the channel. Default `true`. |

`on_done`/`on_fail` correlate to the trigger(s) a rule dispatched by the engine's dispatch outcome
(`ok`/`adopted`/`queued` → `on_done`; `failed` → `on_fail`). If a rule's actions dispatch more than one
variant, feedback fires once, after every variant has reported, and `on_fail` wins if any variant
failed.

Create a Slack app with **Socket Mode enabled**, an app-level token (`connections:write`), event
subscriptions (`app_mention`, `reaction_added`), and a bot token with `chat:write`. No inbound URL to
expose.

**Slack notifications** are separate: set `notify.slack_webhook_url` (an incoming-webhook URL) and the
enabled `notify.on` events (escalations, hand-offs, completions) also post to that channel — no Socket
Mode needed for that half.

## Notifications

paseo-conductor posts short messages when something needs your attention. Every event is written to
the journal (the audit log) regardless; `notify.on` selects which events also push to the configured
sinks.

Events (`notify.on`, default `[escalate]`):

- `escalate` — a dispatch failed after its retries; you should look.
- `needs_input` — a workflow handed a PR to a live agent and is waiting on your review (this carries the
  hand-off link — see [Hand-offs](#hand-offs)).
- `complete` — a workflow finished.
- `dispatch` — every dispatch (noisy; usually left off).

`notify.digest: <interval>` (e.g. `24h`) additionally posts a periodic activity summary; `0` = off.

Set any combination of sinks — each is skipped unless its keys are present:

```yaml
notify:
  on: [escalate, needs_input]
  slack_webhook_url: ${SLACK_WEBHOOK_URL}       # Slack incoming webhook (Slack app → Incoming Webhooks)
  discord_webhook_url: ${DISCORD_WEBHOOK_URL}   # Discord channel → Integrations → Webhooks
  ntfy: { server: https://ntfy.sh, topic: my-conductor }  # server defaults to https://ntfy.sh
  pushover: { token: ${PUSHOVER_TOKEN}, user: ${PUSHOVER_USER} }   # both required; from pushover.net
  notifiarr: { api_key: ${NOTIFIARR_API_KEY}, channel_id: "0000" } # relays to Discord; channel_id optional
  digest: 24h
```

- **Slack / Discord webhooks** post to the channel the webhook is bound to.
- **ntfy** publishes to `<server>/<topic>` — subscribe to the topic in the ntfy app to get it on your phone.
- **Pushover** needs both an application `token` and your `user` key; delivered to the Pushover app.
- **Notifiarr** relays to a Discord channel via its passthrough integration.

These sinks are also how a hand-off link reaches you.

## Hand-offs

A `background: true` step in a [workflow](#multi-step-workflows) presents a draft — the agent's proposed
comment or change — and waits for you to **approve**, **revise**, or **discard** it. `handoffs:` defines
the channels that carry that decision to you. It's a named map with the same resolution rules as
[controllers](#controllers):

```yaml
handoffs:                       # named channels
  page:
    web: { listen: :8099, base_url: https://conductor.example.com, ttl: 30m }
    default: true
```

Each entry sets exactly one channel and may be flagged `default: true`. A step selects one by name:

```yaml
steps:
  - background: true
    handoff: page               # explicit; else the default:true entry, or the sole entry if only one
```

Resolution order: the step's `handoff:` → the entry flagged `default: true` → the single entry if only
one is defined.

**`web`** serves the draft as a page on conductor's inbound HTTP listener (`listen`, default `:8099`) at
`/handoff`. The link surfaced to you is `base_url` + `/handoff?id=<token>`:

- `id` is a 192-bit crypto-random token, so the URL is unguessable — the URL is the capability.
- It is delivered only over your `notify` sinks (the `needs_input` event), never printed publicly.
- It is invalidated the moment you decide, and expires after `ttl` (default `30m`) as a backstop.

`base_url` must be an origin you can actually reach the page at. On a local machine with no public URL,
set `tunnel:` instead of (or alongside) `base_url` and conductor opens one for you:

```yaml
handoffs:
  page:
    web:
      listen: :8099
      tunnel:
        provider: cloudflared   # lan | static | cloudflared | ngrok | tailscale | ssh | localxpose | command
```

### Tunnel providers

| provider | mechanism | exposure |
|---|---|---|
| `lan` | no process; `http://<host>:<port>` — `host:` if set, else an auto-detected private IPv4 | LAN only |
| `static` (or `tunnel:` unset) | no process; uses `base_url` as-is | as configured |
| `cloudflared` | `cloudflared tunnel --url http://127.0.0.1:<port>` — an anonymous quick tunnel; for a named/authenticated Cloudflare tunnel use `command:` | public |
| `ngrok` | `ngrok http <port>` (`authtoken:` if set); the URL is read from ngrok's local API (`127.0.0.1:4040`) | public |
| `tailscale` | `tailscale serve`/`funnel <port>`; `mode: serve` (tailnet-only, default) or `mode: funnel` (public); works transparently over headscale — the tailnet client, not conductor, points at the control server | tailnet or public |
| `ssh` | `ssh -R … <ssh_host>`; presets via `ssh_host:` — `localhost.run`, `serveo.net`, `a.pinggy.io` | public |
| `localxpose` | `loclx tunnel http --to localhost:<port>` | public |
| `command` | your own `command:` (a template — `{{.port}}`/`{{.addr}}` are substituted) plus a `url_pattern:` regex to pull the URL out of its output; every preset above is one instance of this | depends on the command |

Each hand-off opens its tunnel fresh — a new subprocess, a new URL — and tears it down (kills the
process) the moment you decide (approve/revise/discard) or the draft's `ttl` expires. `lan` and `static`
open no process, so their origin stays the same across hand-offs. A spawning provider needs its binary
on `PATH` (`cloudflared`, `ngrok`, `ssh`, `loclx`, `tailscale`); conductor checks with `exec.LookPath`
first and the error names the missing binary rather than surfacing a generic exec failure.

### Slack hand-offs

**`slack`** posts the draft as a DM or a channel thread instead of a web link — no `base_url`, no
tunnel:

```yaml
handoffs:
  phone:
    slack:
      to: dm                        # dm | thread
      user: ${SLACK_USER_ID}        # to: dm — a Slack user id (e.g. U0123ABCD); required
      bot_token: ${SLACK_BOT_TOKEN}
  war-room:
    slack:
      to: thread
      channel: C0123456             # to: thread — required
      bot_token: ${SLACK_BOT_TOKEN}
```

- `to: dm` posts to a direct message (`conversations.open` against `user`, then `chat.postMessage`
  with no thread). `user` is a Slack user id, not a GitHub handle — there is no GitHub→Slack identity
  mapping, so it is never inferred or defaulted; look it up from the Slack profile ("Copy member ID").
- `to: thread` posts to `channel` and replies land in that message's thread — today's default
  behavior, `channel` is required.
- `bot_token` is required either way and needs the `chat:write` scope (`im:write` too, for `to: dm`).

Capturing your reply depends on the [`slack` control-plane integration](#slack-control-plane-slack-integration)
being configured and running (Socket Mode) — it is what receives the DM/thread message and feeds it
back to the waiting hand-off. A `slack:` hand-off with no `slack` integration configured logs a
startup warning: the draft still posts, but a reply is never captured. This is unrelated to the
tunnel/URL machinery above — no inbound port, no tunnel provider, no `base_url`.

`discord` hand-offs (schema only in this build) validate today but aren't wired yet.

## Identity & rate limits

The rate-limit pain came from doing reads on your personal `gh` token. paseo-conductor separates
duties:

- **Conductor's own reads/enrichment** (PR state, checks, labels) use the **GitHub App
  installation token** — its own generous rate pool. The API is used freely (REST or GraphQL).
- **Everything a dispatched agent or command does is attributed to YOU.** Their `GH_TOKEN`/
  `GITHUB_TOKEN` is **your** write token, so every comment, review, reply, and `gh`/API write posts
  **as you, never the App bot**. The App token is handed to them only as **`PC_GH_APP_TOKEN`** for
  optional rate-limited *reads* — it is never the default and is never used to write. (This is
  structural: the agent's default identity is you, so a stray `gh pr review` can't post as the bot.)
- **Commits & pushes** go over **SSH** with your git identity — no token, no API cost.

This split is configurable per github integration via `identity:` (`read_token` / `write_token` /
`commit_author`). Defaults are the above (`read_token: app`, `write_token: gh_auth`). If you don't
have `gh` installed, point writes at a **PAT** instead: `write_token: ${GH_PAT}` — any value that
isn't the `gh_auth`/`app` keyword is used as a literal token.

## Install (released binary, one-liner)

The repo is private, so the installer uses the authenticated `gh` CLI to fetch the release asset
for your OS/arch (mac amd64/arm64, linux amd64/arm64/386):

```sh
curl -fsSL https://gist.githubusercontent.com/danielcbaldwin/3504ed91ac1014b3f073f44916e443a7/raw/install-release.sh | bash
```

Pin a version with `... | bash -s -- v0.2.1`. Installs to `~/.local/bin/paseo-conductor`, seeds a
starter config, and then **installs the background service by default** (press Enter to confirm) —
a `systemd --user` unit on Linux or a `launchd` LaunchAgent on macOS. Answer `n` to skip (set
`PASEO_CONDUCTOR_INSTALL_SERVICE=yes|no` to answer non-interactively). It only *starts* the service
once your config validates, so a fresh install won't crash-loop.

### Updating

```sh
paseo-conductor update            # self-update to the latest release (uses gh)
paseo-conductor update --tag v0.2.0   # or pin a version; --force to reinstall
```

Or let it update itself — enable `update.auto` in config and the running daemon checks a few times a
day, installs any new release, and **restarts into it**. Under a service manager it exits cleanly so
systemd (`Restart=always`) / launchd (`KeepAlive`) relaunch the new binary — a real restart that
re-reads the unit's environment. Run in the foreground, it re-execs in place instead.

```yaml
update:
  auto: true
  interval: 10m       # default; check cadence
  apply: true         # restart into the new binary after updating (default true)
```

Each check is a **cheap conditional request** — conductor remembers the release repo's `ETag` and
sends `If-None-Match`, so an unchanged repo answers `304 Not Modified` (a tiny reply GitHub doesn't
bill against your rate limit). That makes a tight interval effectively free, so a newly-published
release is installed within one interval (minutes) rather than hours — no webhook or per-operator
setup, the same for anyone running conductor. GitHub exposes no release *push* a non-admin can
subscribe to, so a near-free conditional poll is the portable stand-in. With `apply: false` the new
binary is downloaded and staged and the daemon logs `restart to apply` instead of restarting itself.

## Install from source

```sh
git clone https://github.com/NodeSpy/paseo-conductor.git
cd paseo-conductor
./scripts/install.sh          # builds, seeds config, then prompts to install the service
```

Requires the local `paseo` CLI (authenticated to your daemon) and `gh` on PATH.

### Running as a service

The installer offers this; you can also manage it directly with the binary (it generates the right
unit for your OS — a `systemd --user` unit on Linux, a `launchd` LaunchAgent on macOS):

```sh
paseo-conductor service install      # write the unit and start it (if the config validates)
paseo-conductor service sync         # rewrite the unit if its template changed, and reload
paseo-conductor service uninstall    # stop and remove it
```

Logs: `journalctl --user -u paseo-conductor -f` (Linux) / `tail -f ~/Library/Logs/paseo-conductor.log`
(macOS). On Linux, `loginctl enable-linger "$USER"` keeps it running across logout/reboot (the
installer does this).

**Updates keep the unit current and restart the service.** `paseo-conductor update` and the
auto-updater regenerate the installed unit and reload it if a new release changed the template — so
you don't have to reinstall the service after an upgrade. `paseo-conductor update` also restarts an
already-running service into the new binary; the auto-updater restarts itself the same way (by
exiting for the manager to relaunch). (Both only touch a unit that's already installed.)

Secrets live in `~/.config/paseo-conductor/conductor.env`; the daemon loads them itself at startup
(so both systemd and launchd work without extra env wiring).

**PATH is baked into the unit.** A `--user` service otherwise inherits a minimal PATH and can't find
`paseo`/`gh`/`go`/`claude` in `~/.local/bin`. The generated unit sets `PATH` to `~/.local/bin` +
the standard bin dirs (incl. Homebrew and Go) + your install-time PATH, so the tools resolve with no
manual drop-in. Tools in an unusual location? Add a systemd drop-in (`Environment=PATH=…`) or set
`PATH` in `conductor.env`.

## GitHub App setup

1. **Create a GitHub App** — register a new one at:
   - Personal account: <https://github.com/settings/apps/new>
   - Organization: `https://github.com/organizations/<ORG>/settings/apps/new`
     (e.g. <https://github.com/organizations/NodeSpy/settings/apps/new>)

   ([GitHub's guide](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/registering-a-github-app).)

   Suggested field values:
   - **GitHub App name** — must be globally unique, so personalize it, e.g.
     `paseo-conductor-<your-handle>` or `<your-org>-paseo-conductor` (this becomes the bot login).
   - **Homepage URL** — anything valid; use the repo (<https://github.com/NodeSpy/paseo-conductor>)
     or <https://paseo.sh>.
   - **Webhook URL** — a smee.io channel. Open <https://smee.io/new>, copy the URL it shows
     (e.g. `https://smee.io/AbC123`), and paste it here. Put the **same URL** in `conductor.env` as
     `GH_SMEE_URL`.

     > **You do NOT install or run the smee client.** smee.io is just a public relay; paseo-conductor
     > connects to your channel itself and receives the forwarded webhooks. Nothing else to start.
   - **Webhook secret** — generate a random string (e.g. `openssl rand -hex 32`) and put it in
     `conductor.env` as `GH_WEBHOOK_SECRET`.
2. **Permissions** (set these *first* — GitHub only lists events for permissions you've granted).
   Grant the full set so every feature works:
   - **Repository:** Contents (Read & write), Pull requests (Read & write), Issues (Read & write),
     Checks (Read-only), Metadata (Read-only).
   - **Organization:** Projects (Read-only).  *(An org permission — required for `projects_v2_item`
     to appear as an event, and only delivered for org-installed Apps.)*
3. **Subscribe to events** — check all of these:
   `pull_request`, `pull_request_review`, `pull_request_review_comment`, `pull_request_review_thread`,
   `issue_comment`, `check_run`, `check_suite`, `workflow_run`, `push`, `issues`, `projects_v2_item`.

   (`projects_v2_item` only appears once you've granted Organization → Projects above.)
4. **Generate a private key** and save it at the `private_key_path` in your config.
5. **Install the App** on the repos/orgs you want covered.
6. Put the App id, key path, and webhook secret in `config.yaml` / `conductor.env`.

### Transports: smee and/or direct HTTP

Set `webhook.smee_url`, `webhook.listen`, or both:

- **smee.io** — no inbound port. Get a channel at <https://smee.io/new> and use that URL as **both**
  the App's Webhook URL and `GH_SMEE_URL`. **You don't run the `smee` client** — the conductor
  subscribes to the channel itself (auto-reconnecting). Caveat: smee re-serializes the JSON body, so
  HMAC verification usually won't match — keep `verify_signature: false` (the channel URL is the
  shared secret).
- **Direct HTTP** — `listen: 127.0.0.1:8787` (optional `path: /webhook`) runs a plain webhook
  receiver. Point the GitHub App's webhook URL at it (typically via your own tunnel, e.g. pangolin).
  The raw body is intact here, so **set `verify_signature: true`**.

**Recovery.** The smee stream auto-reconnects with backoff; tuned TCP keep-alive surfaces a half-open
connection (a network blip with no clean close) in ~80s rather than the kernel's ~15-min default, so
a silent drop is short. smee.io does **not** buffer, so any webhook delivered while you're
disconnected is lost — the [sweep](#full-example) (`sweep.enabled`) is the catch-up net: it
re-derives `review_requested`/`merge_conflict`/`pr_behind`/`changes_requested`/`new_comment` from live
PR state (plain-issue events aren't re-derivable). For `new_comment` the sweep re-lists each of your
PRs' recent comments (conversation + inline review) from the last 24h and replays any it hasn't seen; a
per-PR **comment high-water mark** (advanced whenever a `new_comment` dispatches, kept separately for
conversation and inline review comments since GitHub numbers them from different sequences) drops the
ones already handled, so a recovered comment fires once, not on every sweep. A comment older than one
already handled of the same kind on the same PR isn't recovered — the mark only moves forward — which
the prompt reconnect sweep keeps rare.

The sweep runs on an **adaptive cadence**: it sweeps immediately on startup and on a reconnect, then
follows up starting at `sweep.min_interval` (default `10m`) and backs off ×2 toward the ceiling
`sweep.interval` (default `6h`) while nothing disrupts it — so a quiet, connected daemon settles at
the ceiling and doesn't sweep for nothing. When smee **reconnects after a drop** (the exact moment a
webhook may have been lost), it sweeps right away and resets to the tight follow-up, so a gap is
caught promptly instead of waiting up to a full `interval`. You don't need to hand-tune the interval
down for recovery anymore.

Need a sweep **right now** (e.g. a review is waiting and you don't want to sit through the backoff)?
`paseo-conductor sweep --now` signals the running daemon (via `SIGUSR1`) to run a catch-up sweep
immediately and reset the cadence. (Plain `paseo-conductor sweep` is a dry-run *preview* that prints
what a sweep would emit, in a separate process — it doesn't touch the daemon.)

Want to force **one specific action** for **one target** — not a whole sweep? `paseo-conductor force
<kind> <owner/repo>#<n>` injects that action into the running daemon over a local control socket
(a sibling of the state file):

```sh
paseo-conductor force review_requested EdnitionCode/RosterStream#5332
paseo-conductor force merge_conflict acme/widget#7 --integration default
```

It builds the trigger from live PR state (bypassing the applicability filters — reviewer match, draft,
exclude) and marks it `Force`, so the engine **skips its dedup / liveness / backoff gates** and runs
the action now even if it thinks the state is already handled. The kill switch and pause still apply.
`--integration` is only needed when more than one integration configures the repo. Force is aimed at
PR-state kinds (`review_requested`, `merge_conflict`, `pr_behind`, `self_review`, `merge_ready`); kinds
that need event-specific data (a comment body, a CI run id) get empty values.

## Configuration

Config lives at `~/.config/paseo-conductor/config.yaml`; secrets referenced via `${VAR}` come from
the sibling `conductor.env`, which the daemon loads at startup (so systemd and launchd both work).
The installer seeds both files. `paseo-conductor validate` checks everything before you start.

This split means `config.yaml` holds no secrets and can live in a dotfiles repo, symlinked into
place — `conductor.env` is looked up next to the config *path*, not the symlink target, so it stays
private in `~/.config/paseo-conductor/`. Referencing a `${VAR}` that isn't defined anywhere is a load
error naming the variable (a deliberately empty `KEY=` is fine), so a missing `conductor.env` on a
fresh machine fails fast instead of silently blanking a secret.

### Splitting the config across files (imports)

Everything can live in one file — or you can split it. A top-level **`imports:`** list pulls in other
YAML files (paths or globs, relative to the importing file's directory) and deep-merges them: **maps
merge recursively, lists concatenate** (so `integrations:` entries from every file combine), and the
**importing file's own keys win** on conflicts. Each file is included once (diamond/cyclic imports are
de-duped). `${VAR}` expansion applies per file. Configs with no `imports:` load exactly as before.

```yaml
# ~/.config/paseo-conductor/config.yaml
imports:
  - conf.d/*.yaml          # one integration per file, say
agents:
  fixer: { provider: claude, workspace: worktree }
control: { enabled: true }
```

```yaml
# ~/.config/paseo-conductor/conf.d/github.yaml
integrations:
  - type: github
    name: default
    app: { app_id: 123, private_key_path: ~/…/github-app.pem, webhook_secret: ${GH_WEBHOOK_SECRET} }
    webhook: { smee_url: ${GH_SMEE_URL} }
    # rules, defaults, actions…
```

An imported file is just a partial config — put whatever top-level sections you like in it
(`integrations:`, `agents:`, `notify:`, …). Split by integration, or keep the big github `rules` in
their own file, however you prefer.

### Full example

This is the complete annotated config (mirrors [`config.example.yaml`](config.example.yaml)):

```yaml
integrations:
  # A LIST of typed instances. List `github` more than once for separate
  # Apps/orgs (each App has its own webhook secret).
  - type: github
    name: github
    enabled: true
    # The App carries webhooks + ALL API reads/enrichment (its own rate pool).
    # Writes/posts use your gh token; commits/pushes go over SSH as you.
    app:
      app_id: 0
      private_key_path: ~/.config/paseo-conductor/github-app.pem
      webhook_secret: ${GH_WEBHOOK_SECRET}
      # smee re-serializes the body so HMAC often won't match — keep false with
      # smee; with a DIRECT `listen` receiver the raw body is intact, so set true.
      verify_signature: false
    webhook:
      # Use smee.io (no inbound port) and/or a direct HTTP listener — either or both.
      smee_url: ${GH_SMEE_URL}       # https://smee.io/<channel>
      # listen: 127.0.0.1:8787        # direct receiver (point the App webhook / your tunnel here)
      # path: /webhook                # default
    sweep:
      enabled: false                  # off by default; REST catch-up for missed events. Adaptive cadence:
      interval: 6h                     #   tight at min_interval after start / smee-reconnect, backing off
      min_interval: 10m                #   ×2 toward interval (the quiet-system ceiling). Recovers pending
      repos: ["your-org/your-repo"]    #   review_requested + on your PRs conflict/behind/unresolved-threads.

    # OPTIONAL shared defaults; every rule merges over these.
    defaults:
      me: { logins: [your-login] }                    # your GitHub login(s) — defines "you"
      actions:
        merge_conflict:                                   # your PR conflicts with base
          type: agent
          agent: fixer
          max_attempts_per_head: 3                        # soft threshold (default 3) → growing backoff, not a hard stop
          prompt: |
            This PR conflicts with base {{.base}}. Merge/rebase base, resolve the
            conflicts, make sure it builds, commit, and push to the PR branch.
        pr_behind:                                        # behind base but not conflicting
          type: command
          backend: local
          command: ["gh", "pr", "update-branch", "{{.repo}}#{{.pr}}"]
          env: { GH_TOKEN: "{{.gh_token}}" }
        failing_checks:                                   # CI failed
          type: agent
          agent: fixer
          flaky_rerun: { enabled: true, max: 1 }          # rerun failed checks once before fixing
          prompt: "Checks failing on {{.repo}}#{{.pr}} — diagnose, fix, verify, commit, push."
        changes_requested:                                # a review requested changes
          type: agent
          agent: fixer
          rerequest_review: true                          # re-request the reviewer(s) after pushing
          prompt: "Address the requested changes on {{.repo}}#{{.pr}}, commit, push, reply."
        new_comment:                                      # a comment / bugbot review
          type: agent
          agent: fixer
          from_users: []                                  # empty = any; e.g. ["coderabbitai[bot]"]
          prompt: "New comment by {{.author}} on {{.repo}}#{{.pr}}: {{.comment_body}} — act if needed."
        review_requested:                                 # your review requested on someone's PR
          type: command
          backend: local
          # reviewer defaults to `me`; set it here to broaden (e.g. a team)
          gates: { not_draft: true }                      # opt-in: skip while draft, fire when ready
          command: ["critique", "--review", "{{.repo}}#{{.pr}}", "--post"]
          env:
            CRITIQUE_GITHUB_TOKEN: "{{.app_token}}"       # reads on the App pool
            CRITIQUE_SUBMIT_TOKEN: "{{.gh_token}}"        # submits the review as YOU
        self_review:                                      # critique your own PRs
          type: command
          backend: local
          enabled: false
          checkout: none
          command: ["critique", "--review", "{{.repo}}#{{.pr}}"]
          env: { CRITIQUE_GITHUB_TOKEN: "{{.app_token}}", CRITIQUE_SUBMIT_TOKEN: "{{.gh_token}}" }
        issue_matched:                                      # issue assigned to you + labeled "Ready"
          # Fires on `issues` changes AND `projects_v2_item` moves; assignee defaults to `me`.
          # A MULTI-STEP workflow: plan cheaply, then branch on the result.
          labels_any: ["Ready"]                           # payload filter: only "Ready"-labeled issues
          gates:                                          # optional GraphQL gate — e.g. a board column:
            project: { Status: Ready }                    #   Projects v2 Status field must be "Ready"
          steps:
            - id: evaluate                                # cheap model assesses the issue
              type: agent
              agent: planner
              checkout: none                              # assessing only — no branch/worktree
              output_schema:
                type: object
                required: [has_context, summary]
                properties:
                  has_context: { type: boolean }
                  summary: { type: string }
              prompt: "Is there enough detail to implement {{.repo}}#{{.issue}}? Return has_context + summary."
            - id: work                                    # only if there's enough context
              if: "steps.evaluate.outputs.has_context == true"
              type: agent
              agent: fixer                                # stronger model to do the work
              checkout: branch-off
              prompt: "Implement {{.repo}}#{{.issue}} — {{.steps.evaluate.outputs.summary}}. Open a draft PR."
            - id: ask                                     # otherwise, ask the reporter for detail
              if: "steps.evaluate.outputs.has_context == false"
              type: command
              backend: local
              command: ["gh", "issue", "comment", "{{.repo}}#{{.issue}}", "--body", "Please add repro steps and scope."]
              env: { GH_TOKEN: "{{.gh_token}}" }
        merge_ready:                                      # auto-merge when fully green
          type: command
          backend: local
          enabled: false
          require_label: automerge                        # PR must carry this label to be eligible
          method: squash                                  # squash | merge | rebase
          gates: { merge_state: clean, review_decision: approved, non_author_approval: true,
                   threads_resolved: true, not_draft: true }
          command: ["gh", "pr", "merge", "{{.repo}}#{{.pr}}", "--squash"]
          env: { GH_TOKEN: "{{.gh_token}}" }

    # RULES: the primary structure. The MOST-SPECIFIC matching rule wins (order-
    # independent): an exact `owner/repo` beats `owner/*` beats `*/*`; ties keep the
    # earlier rule. The winner is merged over `defaults`. `match` takes repo globs.
    rules:
      - match: { repos: ["your-org/*"] }        # inherits defaults
      - match: { repos: ["your-org/your-repo"] }              # override one kind for one repo
        actions:
          failing_checks: { agent: fixer, prompt: "checks failing on {{.repo}}#{{.pr}} — fix and push." }

  # A schedule-driven integration: run commands/agents on a cron or interval.
  - type: cron
    name: chores
    schedules:
      - name: rate-limit-check
        every: 6h                     # or `cron: "0 */6 * * *"` / "@daily" / "@every 6h"
        action: { type: command, backend: local, command: ["gh", "api", "rate_limit"] }
      - name: tidy-cache
        cron: "0 4 * * *"
        run_on_start: false           # also fire once at startup when true
        action:
          type: command
          backend: local
          workdir: ~/Projects/myrepo  # cwd for the command ('~' and {{.repo}}-style templates ok)
          command: ["make", "clean"]

control:
  enabled: true                       # master kill switch
  pause_label: conductor:off          # per-PR/issue opt-out: an object with this label is left alone
  shadow: false                       # run the whole pipeline but skip the final push/merge/post
  max_concurrent_agents: 3            # cap running coding agents (0 = unlimited); extra work waits

notify:
  push: true                          # Paseo push (surfaced via the service log today)
  on: [dispatch, escalate, needs_input]  # events to notify on: dispatch|complete|escalate|needs_input
  # Notifications are private to you (journal + paseo's attention flag). The
  # conductor never posts comments on PRs.

agents:                               # reusable named agent profiles, referenced by actions
  fixer:
    provider: claude                  # -> paseo run --provider  (or provider/model form)
    model: ""                         # -> --model     (optional)
    thinking: ""                      # -> --thinking  (optional)
    mode: ""                          # -> --mode      (optional)
    workspace: worktree               # local | worktree  -> --new-workspace
    wait_timeout: 30m                 # -> --wait-timeout
    archive_when_done: true           # reaper archives the agent+worktree once idle
    labels: { team: autopilot }       # extra --label pairs
  planner:                            # cheaper/faster model for planning/triage steps
    provider: claude
    model: claude-haiku-4-5-20251001
    workspace: local
    archive_when_done: true

paseo_bin: paseo                      # path to the paseo CLI (default "paseo"); set only if not on PATH
# Credential policy + retry live under the github integration (see `identity:` / `retry:` there).

update:
  auto: false                         # periodically self-update to the latest release
  interval: 10m                       # check cadence (cheap conditional request → 304 when nothing new)
  apply: true                         # re-exec into the new binary after updating

store:
  state_file: ~/.local/state/paseo-conductor/state.json
  audit_log: ~/.local/state/paseo-conductor/audit.jsonl
  state_ttl: 720h                     # evict PR records untouched this long (default 30d)
  max_tracked_prs: 5000               # LRU backstop
  audit_max_size: 50MB                # rotate the audit log at this size

dry_run: false                        # build+log every action but never execute it
```

### Field reference

**Top level**

| Key | Meaning |
| --- | --- |
| `integrations` | List of typed instances (`type: github` / `type: cron`). List a type more than once for separate setups. |
| `agents` | Reusable named agent profiles referenced by `agent` actions. |
| `paseo_bin` | Path to the paseo CLI (default `paseo`). Agents always run via paseo; commands run as a local subprocess. |
| `control` | Kill switch (`enabled`), `pause_label`, global `shadow`, `max_concurrent_agents` (cap on running agents), and `max_agents_per_hour` (rolling-hour dispatch cap; over it, work is shed + retried). |
| `notify` | Private notifications (journal + paseo attention flag; never a PR comment): `push` and `on` (which events). |
| `update` | Auto-update: `auto`, `interval`, `apply`. |
| `store` | Dedup-state + audit paths and their TTL/LRU/rotation bounds. |
| `dry_run` | Global dry run — build and log actions but never execute. |

**github instance** — `app` (App id / key path / webhook secret / `verify_signature`), `webhook`
(`smee_url` and/or `listen`+`path`), optional `sweep`, optional `project_map` / `project_rewrite`,
optional shared `defaults`, and the `rules` list. **`project_map`** remaps a repo (`owner/name`) to
the paseo project name of an existing workspace so worktree checkouts reuse it instead of cloning a
fresh one (when the forge repo and the registered paseo project differ in org or casing, e.g.
`EdnitionCode/RosterStream: ednition/rosterstream`); keys are case-insensitive. **`project_rewrite`**
is an org-wide shortcut applied to every repo without an explicit `project_map` entry — `org`
replaces the owner segment (e.g. `org: ednition` turns any `EdnitionCode/AnyRepo` into
`ednition/anyrepo`); `project_map` wins over it. Casing is handled automatically for both — the
rewrite normalizes to lowercase and workspace matching is case-insensitive, since paseo project
names are lowercased. Both only affect checkout, not forge operations. A rule's `match.repos` takes **glob patterns** (Go `path.Match`): `owner/*`, `*/*`,
`owner/svc-*`, `owner/[a-z]*`. Note `*` does **not** cross `/`, so a bare `*` won't match `owner/name`
— use `owner/*` or `*/*`. `me`/`actions`/`workspace` on the matched rule merge over `defaults`.
**`me`** (rule/`defaults` level) is your GitHub login(s) — it defines "you" for ignoring your own
comments, detecting your own PRs (`self_review`), and picking your authored PRs during sweep. The
gating actors live on their checks: **`reviewer`** on `review_requested`, **`assignee`** on
`issue_matched` — but both **default to `me`**, so you usually don't set them; specify one only to
broaden (e.g. a team, or a different login). (Rule-level `reviewer`/`assignee` still work as a
fallback ahead of the `me` default.) `sweep.repos` accepts concrete `owner/name` or an **owner glob** (`owner/*`,
`owner/svc-*`) — a glob is expanded to the repos the App installation can access (so an owner
segment is required there; `*/*` isn't supported).

**Actions** — keyed by kind. `type: agent` references an `agents:` profile + a `prompt` and runs a
Paseo agent; `type: command` runs a subprocess (`command`, `env`, `workdir`, `backend`). Common
options: `name` (for named variants), `enabled`, `checkout` (`checkout-pr`|`branch-off`|`none`),
`shadow`, `workdir`, plus kind-specific ones (`max_attempts_per_head`, `flaky_rerun`, `from_users`,
`labels_any`, `labels_all`, `authors`, `sole_assignee`, `exclude`, `require_label`, `method`,
`gates`, `project`, `rerequest_review`).
`rerequest_review: true` (agent actions) tells the fixer to re-request review from the reviewer(s)
who requested changes once it has addressed the feedback and pushed — closing the review loop.
`exclude` skips PRs the action shouldn't touch (e.g. release PRs) by head-branch glob, label, or a
case-insensitive title substring: `exclude: { branches: ["release/*"], labels: ["release"] }`
(currently applied to `review_requested`). `gates` are conditions that must hold before the
action fires: `merge_ready`'s gates (`merge_state`, `review_decision`, `non_author_approval`,
`threads_resolved`, `not_draft`) default **on**; `review_requested` supports an **opt-in**
`not_draft` gate (`gates: { not_draft: true }`) to skip drafts until they're marked ready.
Templates (`{{.repo}}`, `{{.pr}}`, `{{.base}}`,
`{{.head}}`, `{{.author}}`, `{{.app_token}}`, `{{.gh_token}}`, …) are filled per event.

A **re-requested review** (you click "re-request review" on a reviewer running conductor) re-engages
that reviewer's `review_requested` workflow **only when the PR head has advanced** since it last
reviewed — i.e. you pushed new commits. A re-request on an unchanged head, or a duplicate webhook
delivery, is suppressed while an agent is still working/parked on that head (no double-review of
identical code). This is keyed on the per-head dispatch mark, so a stale parked agent no longer blocks
review of freshly-pushed code.

**cron instance** — a `schedules` list; each has a `name`, a `cron` spec or `every` interval,
optional `run_on_start`, and an `action` (same action shape as above).

### Agent providers & models

An `agents:` profile's `provider`, `model`, and `mode` come straight from your **Paseo daemon** — so
list the valid values with the `paseo` CLI:

```sh
paseo provider ls                    # providers + status (available/enabled) + available modes
paseo provider models claude         # model IDs for a provider (use the ID column as `model:`)
paseo provider diagnostic claude     # troubleshoot a provider's install/auth/availability
```

Example — `paseo provider models claude`:

```
ID                     MODEL          DESCRIPTION
claude-opus-5          Opus 5         Opus 5 · Latest release
claude-sonnet-5        Sonnet 5       Sonnet 5 · Best for everyday tasks
claude-haiku-4-5       Haiku 4.5      Haiku 4.5 · Fastest for quick answers
claude-opus-4-8[1m]    Opus 4.8 1M    Opus 4.8 with 1M context window
```

Put the **ID** in a profile's `model:` and the provider name in `provider:`:

```yaml
agents:
  fixer:   { provider: claude, model: claude-opus-5 }        # strong model for doing work
  planner: { provider: claude, model: claude-haiku-4-5 }     # cheap/fast model for triage steps
```

Notes:
- `provider:` also accepts the `provider/model` shorthand (e.g. `codex/gpt-5.5`).
- `mode:` takes one of the provider's modes from the `paseo provider ls` "MODES" column (e.g.
  `plan`, `default`, `bypass`). Only **available/enabled** providers work — run `agent-login` /
  enable them in Paseo first.
- Omit `model:` to use the provider's default.

## Commands

```
paseo-conductor run                    # start the daemon (the systemd unit runs this)
paseo-conductor validate               # load & validate config, then exit
paseo-conductor replay <event.json>    # run a saved webhook through the pipeline (dry-run)
paseo-conductor sweep                  # one catch-up sweep (dry-run print)
paseo-conductor status                 # snapshot: live agents, in-flight workflows, stuck work, attention
paseo-conductor report [--days N]      # activity summary: dispatches by kind/outcome + attention (default 7d)
paseo-conductor pause                  # stop dispatch now (writes a control file; no restart)
paseo-conductor resume                 # resume dispatch
paseo-conductor version
```

`status` reads the on-disk state/runs/audit files and `paseo ls` (never opening the store, so it's
safe to run against a live daemon). It shows the service state, conductor's live agents, in-flight
multi-step workflows (e.g. a review awaiting you), tracked objects sitting in retry backoff, and the
most recent escalate / needs-input events — the "what's it doing / what's stuck / what needs me" view
without digging through `journalctl`.

`replay` fixtures are `{"event": "<x-github-event>", "body": { ...payload... }}` — see
[`testdata/`](testdata/).

## Safety

- **Kill switch**: `control.enabled: false`, or `paseo-conductor` shadow mode
  (`control.shadow: true`) runs everything but skips the final push/merge/post.
- **Loop-safety**: per-`(pr,kind,head)` attempt caps; on the cap it **escalates** (notifies you)
  instead of looping. A running-agent guard avoids double-dispatch.
- **Work persists until actually done, not "dispatched once"**: kinds whose completion is external
  and re-checkable — `review_requested` (you're still a requested reviewer), `merge_conflict` (PR
  still dirty), `changes_requested` (threads still unresolved) — are **not** marked done on dispatch.
  The sweep re-derives reality each run and re-fires until the condition clears, skipping only while
  an agent is already working/parked for it. Past `max_attempts_per_head` (default 3) retries don't
  stop — they switch to a **growing backoff** (10m→30m→90m→…→24h), so a struggling PR keeps getting
  periodic attempts (with a one-time "backing off" notification) rather than being abandoned. So an
  agent that fails, is culled, or finishes without resolving doesn't leave the work silently
  dropped. (Event-specific kinds like `new_comment` still dedup per comment id and stay uncapped.)
  Every sweep logs a summary — repos/PRs scanned, what it emitted, and *why* it skipped candidates
  (draft-gated, excluded).
- **Bounded fan-out**: `control.max_concurrent_agents` (default 3) caps how many coding agents run
  at once, so a catch-up sweep can't swamp the machine or collide on a repo's git locks; excess
  work waits for a slot. Transient worktree-creation failures (git lock/timeout) are retried
  (`dispatch.retry`).
- **Resumable workflows**: a multi-step workflow persists its progress (completed steps + their
  outputs) to `runs.json`, so if the conductor restarts or crashes mid-flight it resumes on startup —
  completed steps are not re-run; the one step that was in progress re-runs (at-least-once). The App
  token is re-minted on resume (never persisted).
- **One worker per PR (queue, don't drop)**: when feedback arrives for a PR that already has a live
  conductor agent — a burst of comments, or a change-request landing mid-fix — the new work is handed
  to that agent (`paseo send`) so it drains the queue, instead of spawning a duplicate or dropping it.
  Sweep re-derivations skip while an agent is live (no re-nudging); only fresh webhook events queue.
- **Won't cull an agent that needs you**: an `archive_when_done` agent that pauses to ask you
  something isn't reaped — the reaper skips agents blocked on a permission, and an agent can keep
  itself alive by creating a `.paseo-hold` marker in its worktree (guidance for this is added to its
  prompt automatically); it removes the marker when it no longer needs you.
- **Nothing acts on an invalid config**: `validate` gates the service start, and disabled
  integrations/actions never fire.
- **Auto-merge is deliberate**: `merge_ready` ships disabled in the example and is label-gated, and
  it acts only when every gate is green. Turn it on when you're ready.

## License

Private (NodeSpy).
