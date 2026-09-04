# conductor

![conductor](conductor.png)

Event-driven agent orchestration for your Paseo daemon. Connect to services
once (**connectors**), declare where agents run (**runtimes** + **agents**),
and wire events to work (**triggers**): a GitHub review request can research
in an agent, ask you on Slack, and submit the review; a PagerDuty incident can
be investigated by an agent and paged to a channel; a cron tick can deploy
over SSH and report back. Steps run agents, host commands, inline code, or
any connector's verbs — crossing service boundaries freely — as config-as-code,
self-hosted, acting as you.

```yaml
connectors:
  gh:        { type: github, app: { … }, me: { logins: [you] }, repos: ["org/*"] }
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

  - on: gh.release
    steps:
      - { id: announce, uses: slack-ops.post, options: { channel: "#releases", text: "released {{.tag_name}}: {{.url}}" } }
```

The previous schema (`integrations:` / `notify:` / `handoffs:` / `controllers:`)
still loads and runs unchanged, and **migrates automatically** — see
[Migration](#migration-from-the-legacy-schema).

## Quick start

Install the latest release. It drops the binary in `~/.local/bin`, seeds a starter config, and
**asks whether to install the background service** (systemd on Linux, launchd on macOS):

```sh
curl -fsSL https://raw.githubusercontent.com/NodeSpy/conductor/main/scripts/install-release.sh | bash
```

Then:

1. Create a GitHub App + a smee channel — see [GitHub App setup](#github-app-setup) —
   or skip the App entirely (`token: ${GH_PAT}` + a plain webhook or sweep polling).
2. Fill in `~/.config/conductor/config.yaml` (app id, repos, your login) and
   `~/.config/conductor/conductor.env` (secrets), then set the `gh` connector
   `enabled: true`. (The seeded starter is valid but disabled.)
3. `conductor validate` → start the service (the installer offers this).

Later: `conductor update` (or `update.auto`) keeps it current — and a release
that changes the config schema migrates your file itself, with a backup.

## The model

- **Connectors** — external services you connect to (`github`, `slack`,
  `discord`, `web`, `cron`, `webhook`, `sentry`, `pagerduty`, `rss`). Each has
  two faces addressed by one name: **events** (`on: <conn>.<event>`) and
  **verbs** (`uses: <conn>.<verb>`). A connector declares its connection
  (credentials, identity, defaults, policy) once — Slack is no longer
  configured three times for triggers, notifications, and hand-offs.
- **Runtimes + agents** — the things that do the work: a runtime
  (paseo / agent-deck / cli / acp) is where agents run; an agent is a named
  profile (provider/model/prompt posture). `runtimes:` replaces
  `controllers:` (which still loads); the paseo runtime's `bin:` replaces the
  global `paseo_bin`.
- **Triggers** — `on:` / `filters:` / `steps:` / `hooks:`. Filters gate the
  event (keys come from the event's schema, all AND-ed); steps are the
  workflow; hooks are lifecycle actions.

Every connector type is **self-describing**: events publish filter and
context schemas, verbs publish option and output schemas. `conductor
connectors ls` lists what is configured; `conductor schema <conn>` prints the
full contract; `conductor validate` resolves every reference in your config
against those schemas — **and against the scope at each position** — before
the daemon runs.

## The trigger grammar

A step is one of five forms (all share `id` and `if`):

| form | what it does |
|---|---|
| `type: agent` | run an agent profile: `agent`, `prompt`, `checkout`, `output_schema`, `background`, `rerequest_review` |
| `type: command` | run a host command (argv list); with `host:` it runs over SSH and outputs `{stdout, stderr, exit_code}` |
| `run: <engine>` | run inline code — see [Code steps](#code-steps) |
| `uses: <conn>.<verb>` | call a service verb — see [Verbs](#verbs-options-and-identity) |
| `workflow: <name>` | call a reusable workflow — see [Reusable workflows](#reusable-workflows) |

**Context is positional.** A step's templates and `if:` see the trigger
context (the event's published facts: `{{.repo}}`, `{{.comment_body}}`,
`{{.slack.channel}}`) plus every **prior** step's outputs
(`{{.<stepid>.<field>}}`), the named secrets (`{{.secrets.x}}`), and the
batch (`{{.group.*}}`) when grouped. `validate` rejects a reference to a
value that will not exist at that position — a typo or an out-of-scope read
fails at load, not at 3am (a config that validates cannot crash-loop the box).

**Hooks** are verb action units `{at, uses, options, if}` at `start` (on
match, before steps, synchronous), `done`, or `fail` — and they **nest on
steps** too, scoped to that step: announce before it, post its result the
moment it finishes, or handle its own failure. `at: start` sees the trigger
context only; `at: done` adds the outputs; `at: fail` adds `{{.error}}` and
`{{.failed_step}}`. Hook verbs are best-effort — logged and audited, never
fatal.

**Failure semantics:** a step error stops the workflow and fires the fail
hooks — unless the step sets `continue_on_error: true` (its outputs become
`{error, failed: true}`) or a `retry:`. Control flow: `for_each:` (with
`parallel: true` to fan out), `parallel:` branch lists, `retry: {max,
backoff}` and the defer-retry `retry: {while_output_matches, interval,
timeout}`, and `timeout:`.

**Resume idempotency:** runs checkpoint each completed step in `runs.json`; a
daemon restart resumes *after* the last completed step, so a `slack.post` or
`gh.comment` that already ran never re-fires (the interrupted step re-runs,
at-least-once). App tokens are re-minted on resume, never persisted.

## Verbs, options, and identity

A connector may declare default `options:`; each call's options merge over
them, the call winning (nested maps merge). Templates work in any option
value, and a value that is exactly one reference keeps its type
(`pr: "{{.pr}}"` stays a number).

**Identity is an option, defaulting to you.** `as: me` (the default) posts
with your token; `as: bot` posts as the GitHub App's bot user — set it on the
connector for every call or override per call. App credentials stay read-only
unless something opts into `as: bot`.

**GitHub credentials don't require an App.** The chain is `app:` (installation
tokens + webhook installs) → `token:` (a PAT) → the `gh` CLI's login. App-less,
events arrive via a plain webhook + secret or sweep polling (explicit repos).

Every verb invocation is audited — connector, verb, options with **secret
values redacted**, outcome — so `conductor report` reflects cross-boundary
activity without leaking credentials. Per-connector `policy.rate_limits`
caps outbound calls.

## Asks (request-response) and hand-offs

`ask` verbs present to a human and return the answer into the step's outputs:

```yaml
steps:
  - { id: draft,  type: agent, agent: critique, checkout: none, prompt: "Draft the review for {{.repo}}#{{.pr}}." }
  - { id: review, uses: slack-ops.ask, options: { to: dm, user: U0123ABCD, prompt: "Submit this?", draft: "{{.draft.text}}" } }
  - { id: submit, if: "{{.review.action}} == approve",
      uses: gh.submit_review, options: { repo: "{{.repo}}", pr: "{{.pr}}", event: COMMENT, body: "{{.review.text}}" } }
```

Outputs: `{action: approve|revise|discard, text, ref}`; `timeout:` (default
1h) bounds an unanswered ask. The channels are connector types: `web` (an
approve/revise/discard page on the inbound listener, with per-ask tunnel
providers — cloudflared, ngrok, tailscale, ssh, …), `slack` (dm/thread,
replies over Socket Mode), `discord` (conductor runs the bot gateway itself).
This folds the legacy `handoffs:` subsystem into verbs; the legacy block
still loads.

A `background: true` agent step launches a live agent you drive; its
`handoff:` names an ask-capable connector for the present → approve/revise →
submit review loop, and with none the hand-off stays runtime-native (open the
live agent in paseo). Either way the agent is protected from the reaper.

## Code steps

Inline code is the glue between agent outputs and service verbs. Two tiers,
one `run:` key:

- **Baked-in, sandboxed, zero-install (local-only):**
  `run: js` — QuickJS compiled to WASM under wazero (pure Go, no CGo): a real
  WASM sandbox, identical on every OS. `run: go-embed` — yaegi, a Go
  interpreter in Go, sandboxed by a stdlib import allowlist (no os/exec/net);
  define `func run(ctx map[string]any) (any, error)`. `run: risor` — Risor, a
  Go-flavored scripting language in pure Go, behind an explicit global
  allowlist; the final expression is the result. `run: lua` — Lua 5.1 on
  gopher-lua (pure Go); only base/table/string/math are opened and the
  file/chunk loaders are removed; the script `return`s its result.
- **Host interpreters:** `run: go` (the real toolchain via `go run`),
  `run: sh | bash | ruby | node | python | php | perl | /usr/bin/…` — resolved
  via PATH or explicit path. `sh` is the portable default.

Data flow is uniform: the step's scope is injected as `ctx` (a global in
js/risor/lua, the `run(ctx)` argument in go-embed, JSON on stdin for host
interpreters) and the return
value / stdout becomes the step's outputs (a JSON object as-is, other JSON
under `value:`, text under `text:`).

## Remote execution (hosts)

Define SSH targets once and reference them by `host:`:

```yaml
hosts:
  build-box: { host: build01.internal, user: ci, key: ~/.ssh/id_ed25519 }
```

- **Code steps** (host-interpreter tier) run through the remote box's
  interpreter; a missing interpreter is a clear remote error.
- **Command steps** run remotely and output `{stdout, stderr, exit_code}`.
- **Runtimes** launch on the host: cli / acp / agent-deck wrap their
  subprocess in the ssh launch; a **paseo** runtime runs its entire CLI
  (run, clone, workspace create, ls, send, wait, archive — and its reaper)
  on that box, with checkouts under the remote user's `~/.conductor`; an
  **opencode** runtime's server binds the remote 127.0.0.1 and is reached
  through an `ssh -W` stdio forward — no port opens on either machine. An
  agent profile's own `host:` overrides its runtime's (cli/acp/agent-deck).

Everything goes through the system `ssh` (BatchMode, key auth, `known_hosts`
pinning); env exports and code travel inside the ssh channel, never local
argv. The in-process engines (`js`, `go-embed`, `risor`, `lua`) are
local-only.

## Event grouping

```yaml
- on: gh.new_comment
  group: { key: "{{.repo}}#{{.pr}}", window: 15s }
  steps:
    - id: handle
      type: agent
      agent: fixer
      prompt: |
        Address the comments on {{.repo}}#{{.pr}}:
        {{range .group.events}}- {{.comment_body}}
        {{end}}
```

`key` groups events (default: the event's own id — every event its own run);
`window` (default 15s) debounces — it resets per event and fires when the
group goes quiet, capped by `max_wait` (default 4×window). **At most one run
per key is in flight**; events landing during a run form the next batch, so
`group: { key: "{{.pr}}" }` is one-agent-per-PR with no branch collisions.
The batch is addressable as `{{.group.key}}`, `{{.group.events}}`,
`{{.group.count}}`, `{{.group.first}}`/`{{.group.last}}`.

## Reusable workflows

```yaml
workflows:
  assess-and-post:
    inputs:
      repo:    { type: string,  required: true }
      pr:      { type: integer, required: true }
      channel: { type: string,  default: "#reviews" }
    outputs:
      decision: "{{.triage.decision}}"
    steps:
      - { id: triage, type: agent, agent: planner, checkout: none,
          output_schema: { type: object, required: [decision], properties: { decision: { enum: [auto, manual] } } },
          prompt: "Assess {{.inputs.repo}}#{{.inputs.pr}}; decide auto vs manual." }
      - { id: ping, if: "{{.triage.decision}} == manual",
          uses: slack-ops.post, options: { channel: "{{.inputs.channel}}", text: "Needs review: {{.inputs.repo}}#{{.inputs.pr}}" } }

triggers:
  - on: gh.review_requested
    steps:
      - { id: a, workflow: assess-and-post, with: { repo: "{{.repo}}", pr: "{{.pr}}" } }
      - { id: auto, if: "{{.a.decision}} == auto", uses: gh.submit_review, options: { … } }
```

A workflow declares `inputs:` (type/required/default) and `outputs:` (mapped
from its internal steps); the caller supplies `with:` and reads outputs off
the call step's id. Inside, the workflow sees its inputs and the trigger
context — not the caller's other step outputs (pass those via `with:`), so it
stays an encapsulated, composable unit. Workflows nest; `validate` rejects
cycles, unknown/missing inputs, and outputs referencing steps that don't
exist.

## Policy

One `policy:` block, three scopes — global, connector, trigger — most
specific wins per key:

```yaml
policy:                                   # global
  quiet_hours: { tz: America/Denver, from: "22:00", to: "07:00", hold: true }
  concurrency: { max_agents: 8 }

connectors:
  gh:
    policy:
      ignore: { users: ["dependabot[bot]", your-login] }
      pause_label: "conductor:hold"
      rate_limits: { per_minute: 60 }
      backoff: { base: 10s, max: 30m }

triggers:
  - on: gh.review_requested
    policy: { quiet_hours: { hold: false }, pause_label: "review:hold" }
```

`quiet_hours` defers (`hold: true`, re-queued when the window ends) or drops;
`concurrency.max_agents` is the global cap (per-target serialization is
grouping's job); `ignore`/`rate_limits`/`backoff` are connection properties;
`pause_label` parks a target — per trigger, each workflow can have its own
hold label. Any connector or trigger turns off in place with
`enabled: false`; the global kill switch stays the runtime `conductor pause`
/ `resume`.

## Secrets

`conductor.env` works exactly as before (`${VAR}`, chmod-600 sibling file).
Opt-in on top: reference a secret anywhere a value goes —

```
env:GH_PAT · op://Vault/Item/field · pass:conductor/gh-token · vault:gh-token · file:/run/secrets/gh
```

— and name reused references once in a `secrets:` block, read as
`{{.secrets.<name>}}`. Values are resolved at load, cached in memory,
**redacted from logs and audit**, never written back. `conductor secrets
check` validates every reference; a secret that won't resolve **disables that
connector and notifies** instead of crash-looping the box.

The built-in vault (`conductor vault init|add|show|ls|rm`) seals the whole
entry map — names, values, and count — as one padded secretbox blob; the
master key is never in the file and resolves **non-interactively** —
`$CONDUCTOR_VAULT_KEY`, then a systemd encrypted credential
(`$CREDENTIALS_DIRECTORY/conductor-vault-key`, TPM/host-bound), then the OS
keyring, then a chmod-600 key file seeded by `conductor unlock`.
Non-interactive is the requirement, not a convenience: the daemon updates and
restarts itself, so a passphrase prompt at boot would hang the fleet. If the
vault file may end up somewhere public (e.g. committed), keep the key out of
the repo (env / OS keyring / a gitignored key file), use a random key or a
strong passphrase with `vault init --sensitive` (scrypt N=2^20), and note
that git history is permanent — a key that was ever committed stays
extractable, so rotate what it sealed.

## Introspection and dry-run

```sh
conductor connectors ls          # each connector: state, events, verbs, trigger count
conductor schema gh              # full event/filter/context/verb/option/output schemas
conductor secrets check          # resolve every secret reference; print states, not values
conductor replay event.json      # run a fixture through the pipeline, verbs stubbed
```

`replay` (and `shadow:`/`dry_run:`) stubs every outbound verb — the audit
logs what *would* post, agents are mocked, later steps see schema-shaped stub
outputs — so a workflow can be authored and tested without side effects.

## Migration (from the legacy schema)

Both schemas coexist: the binary accepts legacy config unchanged. On boot, a
legacy config is **transformed automatically** — per file (`imports:`
included), with the original backed up alongside (`<file>.pre-connectors`),
the whole config re-validated after each swap, and **any failure restoring
the original**: the daemon keeps running on legacy and notifies
`config needs manual migration`. Idempotent; deployed boxes that auto-update
migrate themselves.

The transform is total or it refuses: every construct maps — all seven
integration types, every github kind/filter/variant and the rules/defaults
most-specific-repo model (flattened to per-trigger `repos`/`exclude_repos`
with the same winner per repo), slack ack/on_done/on_fail → hooks,
`handoffs:` → ask connectors, `controllers:`/`paseo_bin` → runtimes,
`control:` → policy — and anything unmappable is a **hard error naming what
failed**, never a quiet loss (fields the legacy engine never read are dropped
with an explicit summary note). `${VAR}` references survive verbatim; secrets
are never inlined. Manual: `conductor config migrate [--dry-run]`.

Behavioral equivalence is tested: golden tests feed identical webhook
payloads through the legacy integration and the migrated config's lowered one
and assert the same triggers fire the same work. The legacy example ships as
`config.example.legacy.yaml` until legacy retires in a later release.

## What it does (GitHub)

The github connector turns PR/issue/check/release activity into events —
`review_requested`, `changes_requested`, `new_comment`, `merge_conflict`,
`pr_behind`, `failing_checks`, `stuck_checks`, `merge_ready`, `self_review`,
`issue_matched`, `release`, `deployment_status`, `dependabot_alert`,
`secret_scanning_alert` — each with typed filters (`conductor schema gh`).
Webhooks arrive over a smee channel and/or a direct listener; the **sweep**
re-derives PR-state kinds from live state on an adaptive cadence (immediate
on startup/reconnect, backing off toward `sweep.interval`), so a lost webhook
is recovered rather than dropped. `conductor sweep --now` forces a sweep;
`conductor force <kind> <owner/repo>#<n>` injects one action, bypassing
dedup/backoff gates.

Dispatch behavior carries over from the previous model unchanged: dedup per
`(pr, kind, head)`, liveness-gated kinds that re-fire until the underlying
condition clears, growing backoff past the attempt threshold instead of
abandonment, one worker per PR (new feedback queues to the live agent), the
reaper that archives finished agents but never one that needs you.

**Bot-authored comments.** `new_comment` and `changes_requested` publish
`author_is_bot` (account type `Bot`, or a `[bot]` login) and take an
`author_bot` filter. `policy.reply_to_bots` gates the reply back to a bot —
the fix itself always runs: `decline_only` (default) instructs the agent to
skip pleasantries and reply only to state a concrete reason for not applying
a suggestion; `off` skips `comment`/`reply` verbs to the bot structurally;
`full` leaves replies ungated.

## Notifications

Daemon lifecycle events (`dispatch`, `complete`, `escalate`, `needs_input`,
plus the periodic `digest`) deliver through connector verbs — `notify.via:`
routes, each an action unit with `{{.message}}` and the event facts in scope.
The sink connector types (`ntfy`, `pushover`, `notifiarr`, and slack/discord's
post-only `webhook_url:` mode) cover everything the legacy sink fields did;
those fields still work on a legacy config, and migration maps each onto a
connector + route with a byte-identical wire payload. Workflow-level feedback
is better expressed as hooks calling verbs — per-trigger and position-scoped.

## Install (released binary, one-liner)

The installer ([`scripts/install-release.sh`](scripts/install-release.sh)) fetches the release asset
for your OS/arch (mac amd64/arm64, linux amd64/arm64/386) — no auth needed:

```sh
curl -fsSL https://raw.githubusercontent.com/NodeSpy/conductor/main/scripts/install-release.sh | bash
```

Pin a version with `... | bash -s -- v0.6.4`. Installs to `~/.local/bin/conductor`, seeds a
starter config, and then **installs the background service by default** (press Enter to confirm) —
a `systemd --user` unit on Linux or a `launchd` LaunchAgent on macOS. Answer `n` to skip (set
`CONDUCTOR_INSTALL_SERVICE=yes|no` to answer non-interactively). It only *starts* the service
once your config validates, so a fresh install won't crash-loop.

### Updating

```sh
conductor update                # self-update to the latest release (uses gh)
conductor update --tag v0.2.0   # or pin a version; --force to reinstall
```

Or let it update itself — enable `update.auto` in config and the running daemon checks a few times a
day, installs any new release, and **restarts into it**. Under a service manager it exits cleanly so
systemd (`Restart=always`) / launchd (`KeepAlive`) relaunch the new binary; run in the foreground, it
re-execs in place. Each check is a cheap conditional request (`If-None-Match`), so a tight interval
costs nothing. A release that changes the config schema **migrates your config on the new binary's
first boot** — backup, transform, validate-or-restore (see [Migration](#migration-from-the-legacy-schema)).

## Install from source

```sh
git clone https://github.com/NodeSpy/conductor.git
cd conductor
./scripts/install.sh          # builds, seeds config, then prompts to install the service
```

Requires the local `paseo` CLI (authenticated to your daemon) and `gh` on PATH.

### Running as a service

```sh
conductor service install      # write the unit and start it (if the config validates)
conductor service sync         # rewrite the unit if its template changed, and reload
conductor service uninstall    # stop and remove it
```

The unit is named `conductor`. Logs: `journalctl --user -u conductor -f` (Linux) /
`tail -f ~/Library/Logs/conductor.log` (macOS). On Linux, `loginctl enable-linger "$USER"`
keeps it running across logout/reboot (the installer does this). Updates keep the unit current
and restart the service. Secrets live in `~/.config/conductor/conductor.env`; the daemon loads
them itself at startup, and the generated unit bakes a PATH that can find `paseo`/`gh`/`go`.

## GitHub App setup

1. **Create a GitHub App** (personal: <https://github.com/settings/apps/new>; org:
   `https://github.com/organizations/<ORG>/settings/apps/new`). Name it uniquely
   (this becomes the bot login); point the Webhook URL at a smee channel
   (<https://smee.io/new> — conductor connects to the channel itself, there is
   no client to run) and set a generated webhook secret.
2. **Permissions** — Repository: Contents (RW), Pull requests (RW), Issues (RW),
   Checks (RO), Metadata (RO); Organization: Projects (RO).
3. **Subscribe to events** — `pull_request`, `pull_request_review`,
   `pull_request_review_comment`, `pull_request_review_thread`, `issue_comment`,
   `check_run`, `check_suite`, `workflow_run`, `push`, `issues`, `projects_v2_item`.
4. **Generate a private key**, install the App on your repos, and put the app id,
   key path, and secrets in `config.yaml` / `conductor.env`.

Transports: `webhook.smee_url` (no inbound port; keep `verify_signature: false`
— smee re-serializes the body) and/or `webhook.listen` (direct receiver; set
`verify_signature: true`). The sweep is the catch-up net for anything a relay
drops.

**Without an App:** set `token: ${GH_PAT}` on the connector — events via a
plain repository webhook (+ secret) pointed at `webhook.listen`, or sweep
polling with explicit repos. `as: bot` verb calls are the one thing that
still needs App credentials.

## Configuration

The full annotated example is [`config.example.yaml`](config.example.yaml)
(the retained legacy example: [`config.example.legacy.yaml`](config.example.legacy.yaml)).
Secrets go in the sibling `conductor.env`. The config splits across files:
each map section takes an `imports:` key (`connectors: { imports:
[conf.d/*.yaml] }` — entries merge, a duplicate name across files is a load
error) and `triggers:` takes `- imports: [globs]` items; a step's `workflow:` can also name
a workflow from a file directly (`from: ./workflows/review.yaml`, or a bare
path when the file holds one workflow).
The wiki carries the full reference — Configuration, Connectors, Workflows,
Verbs, Code-Steps, Hosts, Grouping, Policy, Secrets, Runtimes, Migration —
and `conductor schema <conn>` prints any connector's exact contract.

## Commands

```
conductor run [--config PATH]              start the daemon
conductor validate                         load & validate (both schemas, full semantic pass)
conductor replay <event.json>              run a saved webhook through the pipeline, verbs stubbed
conductor sweep [--now]                    catch-up sweep (preview / signal the daemon)
conductor force <kind> <owner/repo>#<n>    force one action now, bypassing dedup gates
conductor status | report [--days N]       live snapshot / activity summary
conductor pause | resume                   runtime kill switch (no restart)
conductor connectors ls | schema <conn>    introspection
conductor secrets check                    resolve every secret reference and report
conductor vault init|add|show|ls|rm        the built-in encrypted vault
conductor unlock                           seed the vault key for unattended restarts
conductor config migrate [--dry-run]       legacy → connectors transform
conductor update | service …               self-update / service unit management
```

## Safety

- **Kill switches**: `conductor pause` at runtime; `policy: { enabled: false }`
  in config; `policy: { shadow: true }` previews everything and dispatches
  nothing (per trigger, connector, or globally).
- **Nothing acts on an invalid config**: `validate` gates the service start
  and resolves every schema and template reference against its position — and
  a bad *connector* (unresolvable secret, dead credentials) disables that
  connector and notifies instead of crash-looping the box.
- **Migration is fail-safe**: transform → backup → validate → swap, restoring
  the original on any failure; unmappable constructs refuse loudly.
- **Loop-safety**: dedup per `(pr, kind, head)`, attempt caps that escalate
  and back off (10m→30m→…→24h) rather than abandon, a running-agent guard
  against double-dispatch, and `concurrency.max_agents` +
  `max_agents_per_hour` bounding fan-out.
- **Work persists until actually done**: externally-checkable kinds
  (`review_requested`, `merge_conflict`, `changes_requested`) are re-derived
  by the sweep until the condition clears, not marked done on dispatch.
- **Resumable workflows**: per-step checkpoints in `runs.json`; completed
  steps (a posted comment, a sent Slack message) never re-run after a
  restart. Tokens are re-minted, never persisted.
- **One worker per PR**: new feedback queues to the live agent instead of
  spawning a duplicate; grouped triggers hold one run per key.
- **Won't cull an agent that needs you**: the reaper skips held/asking agents.
- **Verb audit with redaction**: every outbound call is recorded with secret
  values scrubbed.

## License

Private (NodeSpy).
