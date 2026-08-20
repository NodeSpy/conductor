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
| `failing_checks` | CI fails | flake-rerun once, then agent: fix + push |
| `changes_requested` | a review requests changes | agent: address + push |
| `new_comment` | a comment / bugbot review | agent: act + reply |
| `merge_ready` | fully green: mergeable, approved by another reviewer, all threads resolved, not draft | `gh pr merge` |

**Reviews**

| Kind | Trigger | Action |
| --- | --- | --- |
| `review_requested` | your review is requested on a PR | run [critique](https://github.com/EdnitionCode/critique), post as you |
| `self_review` | you open/update your own PR | critique your own PR |

**Issues**

| Kind | Trigger | Action |
| --- | --- | --- |
| `issue_assigned` | an issue is assigned to you | agent: start work on a fresh branch |
| `issue_ready` | an issue gets a "Ready" label | agent: start work on a fresh branch |
| `issue_project_moved` | a Projects v2 status field → "Ready" | agent: start work on a fresh branch |

Plus scheduled jobs via the [cron integration](#scheduled-jobs-cron-integration). Each kind is a
configurable action — enable, disable, and tune it per repo in [config](#configuration).

## Reacting to issues

Issue triggers are configured like any other kind — under a github rule's `actions`. Assignee
matching uses the rule's `assignee: { logins: [...] }`, and the App must subscribe to the `issues`
event.

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
      assignee: { logins: [octocat] }     # who "issue_assigned" must name
      actions:
        issue_assigned:                    # an issue is assigned to you
          type: agent
          agent: fixer
          checkout: branch-off             # start on a fresh branch (no PR yet)
          prompt: "Issue {{.repo}}#{{.issue}} was assigned to you — implement it and open a draft PR."
        issue_ready:                       # an issue gets a "Ready" label
          type: agent
          agent: fixer
          checkout: branch-off
          labels_any: ["Ready"]
          prompt: "Issue {{.repo}}#{{.issue}} is Ready — start work on a fresh branch."
    rules:
      - match: { repos: ["octocat/*"] }
agents:
  fixer: { provider: claude, workspace: worktree }
```

For a smarter flow — assess the issue, then implement it *or* ask the reporter for more detail —
make `issue_ready` a [multi-step workflow](#multi-step-workflows).

## Multi-step workflows

An action can be a single run, or a **`steps:`** list — an ordered workflow where each step can use
a different agent/model, produce structured **output**, and gate on an **`if:`** condition over
earlier outputs. This lets you plan with a cheap model, then act with a stronger one, and branch on
what the plan found.

```yaml
issue_ready:
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
- **Conditions** (`if:`): dotted paths, `==` / `!=` against literals (`true`, `"question"`, `7`),
  truthiness (`x` / `!x`), and `&&` / `||` — e.g. `steps.evaluate.outputs.kind == "question"`.
- Steps run in order, fail-fast, and the whole workflow runs off the main loop so long steps don't
  block other events.

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

## Identity & rate limits

The rate-limit pain came from doing reads on your personal `gh` token. paseo-conductor separates
duties:

- **Reads/enrichment** (PR state, checks) use the **GitHub App installation token** —
  its own generous rate pool. The API is used freely (REST or GraphQL).
- **API writes/posts** (review replies, critique's submitted review) use **your `gh` token**, so
  they're authored **as you**, never a bot.
- **Commits & pushes** go over **SSH** with your git identity — no token, no API cost.

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
day, installs any new release, and re-execs into it:

```yaml
update:
  auto: true
  interval: 8h        # default; check cadence
  apply: true         # re-exec into the new binary after updating
```

## Install from source

```sh
git clone https://github.com/NodeSpy/paseo-conductor.git
cd paseo-conductor
./scripts/install.sh          # builds, seeds config, then prompts to install the service
```

Requires the local `paseo` CLI (authenticated to your daemon) and `gh` on PATH.

### Running as a service

`scripts/install.sh` (and the one-liner) offer this for you; you can also run `scripts/service.sh`
directly, or set it up by hand:

**Linux (systemd --user):**
```sh
systemctl --user daemon-reload
systemctl --user enable --now paseo-conductor
loginctl enable-linger "$USER"                 # keep running across logout/reboot
journalctl --user -u paseo-conductor -f
```

**macOS (launchd):**
```sh
launchctl load -w ~/Library/LaunchAgents/sh.paseo-conductor.plist
tail -f ~/Library/Logs/paseo-conductor.log
```

Secrets live in `~/.config/paseo-conductor/conductor.env`; the daemon loads them itself at startup
(so both systemd and launchd work without extra env wiring).

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

## Configuration

Config lives at `~/.config/paseo-conductor/config.yaml`; secrets referenced via `${VAR}` come from
the sibling `conductor.env`, which the daemon loads at startup (so systemd and launchd both work).
The installer seeds both files. `paseo-conductor validate` checks everything before you start.

### Full example

This is the complete annotated config (mirrors [`config.example.yaml`](config.example.yaml)):

```yaml
integrations:
  # A LIST of typed instances. List `github` more than once for separate
  # Apps/orgs (each App has its own webhook secret).
  - type: github
    name: ednition
    enabled: true
    # The App carries webhooks + ALL API reads/enrichment (its own rate pool).
    # Writes/posts use your gh token; commits/pushes go over SSH as you.
    app:
      app_id: 0
      private_key_path: ~/.config/paseo-conductor/ednition-app.pem
      webhook_secret: ${EDN_WEBHOOK_SECRET}
      # smee re-serializes the body so HMAC often won't match — keep false with
      # smee; with a DIRECT `listen` receiver the raw body is intact, so set true.
      verify_signature: false
    webhook:
      # Use smee.io (no inbound port) and/or a direct HTTP listener — either or both.
      smee_url: ${EDN_SMEE_URL}       # https://smee.io/<channel>
      # listen: 127.0.0.1:8787        # direct receiver (point the App webhook / your tunnel here)
      # path: /webhook                # default
    sweep:
      enabled: false                  # off by default; REST catch-up for missed events
      interval: 1h
      repos: ["EdnitionCode/RosterStream"]

    # OPTIONAL shared defaults; every rule merges over these.
    defaults:
      reviewer: { logins: [danielcbaldwin], teams: [] }   # who "review_requested" must name
      assignee: { logins: [danielcbaldwin] }              # who "issue_assigned" must name
      actions:
        merge_conflict:                                   # your PR conflicts with base
          type: agent
          agent: fixer
          max_attempts_per_head: 2                        # cap → then escalate (notify), no looping
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
          prompt: "Address the requested changes on {{.repo}}#{{.pr}}, commit, push, reply."
        new_comment:                                      # a comment / bugbot review
          type: agent
          agent: fixer
          from_users: []                                  # empty = any; e.g. ["coderabbitai[bot]"]
          prompt: "New comment by {{.author}} on {{.repo}}#{{.pr}}: {{.comment_body}} — act if needed."
        review_requested:                                 # your review requested on someone's PR
          type: command
          backend: local
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
        issue_assigned:                                   # issue assigned to you
          type: agent
          agent: fixer
          checkout: branch-off                            # start work on a fresh branch (no PR yet)
          prompt: "Issue {{.repo}}#{{.issue}} assigned to you — start work; open a draft PR."
        issue_ready:                                      # issue labeled "Ready"
          # A MULTI-STEP workflow: plan cheaply, then branch on the result.
          labels_any: ["Ready"]                           # gate: only "Ready"-labeled issues
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
        issue_project_moved:                              # Projects v2 Status field → "Ready"
          type: agent
          agent: fixer
          enabled: false
          checkout: branch-off
          project: { field: Status, to: Ready }           # one GraphQL lookup per event
          prompt: "Issue moved to Ready in the project — start work."
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

    # RULES: the primary structure. The FIRST rule whose `match` applies wins,
    # merged over `defaults`. `match` takes repo globs (`owner/*`, `*/*`).
    rules:
      - match: { repos: ["EdnitionCode/*", "TapResearch/*"] }        # inherits defaults
      - match: { repos: ["EdnitionCode/RosterStream"] }              # override one kind for one repo
        actions:
          failing_checks: { agent: fixer, prompt: "RosterStream checks failing — fix and push." }

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
  pause_label: conductor:off          # (label-based pause is a follow-up; enabled/shadow work today)
  shadow: false                       # run the whole pipeline but skip the final push/merge/post

notify:
  push: true                          # Paseo push (surfaced via the service log today)
  comment_on_escalate: true           # post a one-line summary comment (as you) on escalation
  on: [dispatch, escalate]            # which events to notify on: dispatch|complete|escalate

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

dispatch:
  default_backends: { agent: paseo, command: local }   # backend per action type when unset
  backends:
    paseo: { bin: paseo }             # path to the paseo CLI
    local: {}                         # direct subprocess exec
  identity: { read_token: app, write_token: gh_auth, commit_author: self }  # reads=App, posts/commits=YOU

update:
  auto: false                         # periodically self-update to the latest release
  interval: 8h                        # check cadence (a few times a day)
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
| `dispatch` | Backends (`paseo`, `local`), the default backend per action type, and the read/write identity split. |
| `control` | Kill switch (`enabled`), `pause_label`, global `shadow`. |
| `notify` | Notifications: `push`, `comment_on_escalate`, and `on` (which events). |
| `update` | Auto-update: `auto`, `interval`, `apply`. |
| `store` | Dedup-state + audit paths and their TTL/LRU/rotation bounds. |
| `dry_run` | Global dry run — build and log actions but never execute. |

**github instance** — `app` (App id / key path / webhook secret / `verify_signature`), `webhook`
(`smee_url` and/or `listen`+`path`), optional `sweep`, optional shared `defaults`, and the `rules`
list. A rule's `match` takes repo globs (`owner/*`, `*/*`); `reviewer`/`assignee`/`actions`/`workspace`
on the matched rule merge over `defaults`.

**Actions** — keyed by kind. `type: agent` references an `agents:` profile + a `prompt` and runs a
Paseo agent; `type: command` runs a subprocess (`command`, `env`, `workdir`, `backend`). Common
options: `enabled`, `checkout` (`checkout-pr`|`branch-off`|`none`), `shadow`, `workdir`, plus
kind-specific ones (`max_attempts_per_head`, `flaky_rerun`, `from_users`, `labels_any`,
`require_label`, `method`, `gates`, `project`). Templates (`{{.repo}}`, `{{.pr}}`, `{{.base}}`,
`{{.head}}`, `{{.author}}`, `{{.app_token}}`, `{{.gh_token}}`, …) are filled per event.

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
paseo-conductor version
```

`replay` fixtures are `{"event": "<x-github-event>", "body": { ...payload... }}` — see
[`testdata/`](testdata/).

## Safety

- **Kill switch**: `control.enabled: false`, or `paseo-conductor` shadow mode
  (`control.shadow: true`) runs everything but skips the final push/merge/post.
- **Loop-safety**: per-`(pr,kind,head)` attempt caps; on the cap it **escalates** (notifies you)
  instead of looping. A running-agent guard avoids double-dispatch.
- **Nothing acts on an invalid config**: `validate` gates the service start, and disabled
  integrations/actions never fire.
- **Auto-merge is deliberate**: `merge_ready` ships disabled in the example and is label-gated, and
  it acts only when every gate is green. Turn it on when you're ready.

## License

Private (NodeSpy).
