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

**First trigger, no services needed.** Before wiring GitHub or Slack, prove
the pipeline with a config that needs no credentials at all — a cron schedule
plus the built-in `manual` source and a local command step:

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

$ conductor run --config ~/.config/conductor/config.yaml &   # or start the service
$ conductor run hello
dispatched manual trigger "hello"
```

The schedule fires the same steps every hour; `conductor run hello` fires them
on demand through the same validation, policy, and audit.

Then connect real services:

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
  For everything without a built-in type, `type: rest` and `type: graphql`
  declare verbs (and polled events) straight from config — templated
  requests, `output:` extraction, shared auth including OAuth2 with
  refresh-token rotation (see the wiki's Configuration page).
  A `stores:` section defines named data stores in two families: KV
  (`boltdb` files, `redis`, a generic `http` shim) served by the **`kv.*`
  verbs**, and SQL (`postgres`/`mysql`/`sqlite`, pure-Go drivers) served by
  **`sql.query`/`sql.exec`** — parameterized only, values bind through
  `args:` to driver placeholders. Every call names its store
  (`store: cache`), KV reads compose inline via
  `{{ kv "cache" "ns" "key" }}`, and code steps get `ctx.store("cache")`
  and `ctx.sql("analytics")`. Durable state survives restarts and is
  shared across runs.
  A `memory:` section adds **shared agent memory** on the same storage
  model: durable notes with provenance and scope (global / per-repo /
  per-agent), written by the `memory.*` verbs, by a `remember:` block in an
  agent's final output, or by a live remember/recall tool on runtimes with
  live-tool injection — and injected into the prompts of agent profiles
  that opt in with `memory: true` (see the wiki's Memory page).
- **Runtimes + agents** — the things that do the work: a runtime
  (paseo / agent-deck / cli / acp) is where agents run; an agent is a named
  profile (provider/model/prompt posture). `runtimes:` replaces
  `controllers:` (which still loads); the paseo runtime's `bin:` replaces the
  global `paseo_bin`.
  A profile's `session:` block adds **session affinity**: one live agent per
  rendered key (`"{{.repo}}#{{.pr}}"`), shared across every trigger using
  that agent — later events arrive as follow-ups with full prior context,
  serialized per key, persisted across restarts, and evicted on idle/age or
  an `end_on` event like `gh._closed` — github's close-or-merge signal (see
  the wiki's Agents page).
  And agents can **program conductor**: emit a `plan:` of ordinary steps
  that runs deterministically (token-free unless it spawns sub-agents),
  choose an existing workflow from the `workflow.list` catalog, get failures
  routed back to their session for a bounded revise-and-resume loop, and
  promote recurring patterns with `workflow.save` — all under
  `policy.agent_authored`: allowlist + approval gates + a sandbox host +
  hard limits, enforced structurally, off until you opt in (see the wiki's
  Workflows and Policy pages).
- **Triggers** — `on:` / `filters:` / `steps:` / `hooks:`. Filters gate the
  event (keys come from the event's schema, all AND-ed); steps are the
  workflow; hooks are lifecycle actions. `on:` takes one source, a **list**
  of sources fanning into the same steps (each with its own per-source
  `filters:`/`policy:`/`hooks:` block), or the built-in `manual` source — fired on demand with
  `conductor run <name> --input k=v`.

Every connector type is **self-describing**: events publish filter and
context schemas, verbs publish option and output schemas. `conductor
connectors ls` lists what is configured; `conductor schema <conn>` prints the
full contract; `conductor validate` resolves every reference in your config
against those schemas — **and against the scope at each position** — before
the daemon runs.

The library **accretes** rather than shipping a big catalog: the generic
`rest`/`graphql` connectors cover anything not yet typed, and adding a typed
connector is deliberately cheap — one file, one registration, schemas as the
contract, heavy-dependency backends behind build tags. Start from the
executable template in `internal/connector/authoring_example_test.go` and
the [Authoring-Connectors wiki page](../../wiki/Authoring-Connectors).

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
(`{{.<stepid>.<field>}}`), vault reads (`{{ vault "house" "gh" }}`), and the
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

## Agent isolation & sandboxing

Per-dispatch isolation for the runtimes conductor launches itself
(acp / cli / opencode / agent-deck), selectable per agent profile or per
runtime (the profile's `isolation:` wins) — see the
[Isolation wiki page](../../wiki/Isolation):

```yaml
agents:
  risky-fixer:
    runtime: gemini            # a runtime conductor launches (not paseo)
    isolation:
      mode: user               # user | namespace | container
      user: sandboxagent       # mode user: sudo -n -u sandboxagent
      limits: { memory: 2g, cpu: 200%, pids: 256 }
      network:
        egress: [ "api.github.com:443", "*.internal:443" ]
```

- **`mode: user`** runs the agent as a distinct low-privilege OS user
  (`sudo -n`); works on any Unix with the matching sudoers rule.
- **`mode: namespace`** (Linux) wraps the launch in user/pid/mount
  namespaces via `unshare`, with cgroup limits via
  `systemd-run --user --scope` when `limits:` is set;
  `network: {deny: true}` adds a network namespace — structurally **no**
  network.
- **`mode: container`** launches inside `docker`/`podman run` with the
  worktree bind-mounted; `deny: true` becomes `--network=none`.

**Egress allowlist.** A `network:` block routes the runtime's HTTP(S)
traffic through conductor's own loopback filtering proxy: `egress:` patterns
(`host`, `host:port`, `*.glob:443`) are allowed, everything else is denied
with a 403 and an `egress_denied` audit record. An empty `network: {}` is
deny-all. **Agent-authored dispatches (§11 plans) get the deny-all proxy by
default** — no config needed; an explicit `network.egress:` on the profile
opts specific targets back in. The proxy is authoritative for well-behaved
runtimes; when you need a structural guarantee, use `deny: true` under
namespace/container mode.

A `hosts:` entry can carry `isolation:` too (modes user/namespace): every
script that host runs — including `policy.agent_authored.host` sandbox code —
is wrapped de-privileged on the remote box. A **paseo** runtime's agents are
children of the paseo daemon, which conductor cannot wrap; `conductor
validate` rejects `isolation:` there rather than pretending. Non-Linux boxes
degrade gracefully: user/container modes work wherever sudo/docker do,
namespace mode is rejected with a clear error.

## Binary / file data (blobs)

Files pass between steps and agents as **content-addressed blobs**, not
base64-in-JSON: `blob.put` stores a file (or inline text) and returns an
opaque JSON-friendly handle (`{$blob, name, media_type, size}` — metadata
only, redacted like any output), `blob.get` writes the bytes to a path (e.g.
into an agent's worktree), `blob.read`/`blob.stat` cover text and metadata.
Connector verbs can declare binary in/out in their schema — handles resolve
to on-disk paths going in, raw bytes become handles coming out. Artifacts are
**GC'd with the run** that produced them. See the
[Binary-Data wiki page](../../wiki/Binary-Data).

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

## Quality gates on agent output

A `gate:` runs named checks — tests, lint, a critic-agent verdict, any verb
with a pass/fail reading — against an agent's **proposed change** (in its
worktree) before the step's result promotes:

```yaml
checks:
  test:   { type: command, command: ["make", "test"] }
  critic: { type: agent, agent: reviewer, prompt: "Review {{.gate.workdir}}; output {\"pass\": bool}" }

triggers:
  - on: gh.review_requested
    steps:
      - { id: fix, type: agent, agent: fixer, prompt: "…",
          gate: { run: [ test, critic ], max_revisions: 2 } }
```

Pass → promote. Fail → the failing checks' detail loops back to the **same
agent** as a revise follow-up (bounded by `max_revisions`), then the run
escalates and the step fails — discard, audited at every round. Gates sit on
agent steps or as trigger/workflow defaults; checks run in the agent's
worktree with `{{.gate.*}}` scope. See the
[Gates wiki page](../../wiki/Gates).

## Cost & token accounting

Every agent run is metered (#36 §14): token usage and `$` cost come from the
runtime's reported usage where its output carries it (claude-code
`--output-format json`, paseo `run --json`, OpenAI-style `usage:` blocks) and
are otherwise **estimated** from model + prompt/output size and marked
*approximate*. Usage lands on the run record, in the audit (`agent_usage` per
run, `workflow_cost` per workflow run), and in `conductor report`'s spend
section (totals, per repo / per workflow / per day, $ per run, estimated
share). A `pricing:` block overrides the built-in model→$ table.

Hard caps ride the same `policy:`/profile machinery — see the
[Cost-Accounting wiki page](../../wiki/Cost-Accounting):

```yaml
policy:
  budget: { window: 24h, max_cost_usd: 25 }     # global: $25 / rolling 24h
agents:
  fixer:
    budget: { window: 1h, max_tokens: 500k }    # profile scope
triggers:
  - on: gh.review_requested
    policy:
      budget: { max_cost_usd: 5 }               # workflow scope
```

All governing scopes must be under cap for a dispatch to run; an over-cap
dispatch **sheds** exactly like the agents-per-hour budget — the attempt is
recorded (backoff/sweep re-derives once the rolling window frees), a
`budget_shed` audit row is written, and a notification goes out.

## Secrets & vaults

`conductor.env` works exactly as before (`${VAR}` / `env:VAR`, chmod-600
sibling file) — the baseline, nothing to declare. Everything beyond env is a
**vault** in a `vaults:` section:

```yaml
vaults:
  house: { type: conductor }                  # the built-in encrypted vault.json
  op:    { type: onepassword, service_account: env:OP_SA }
  files: { type: file, dir: /run/secrets }
  hcv:   { type: hashicorp, addr: https://vault.internal, unlock: { token: env:VAULT_TOKEN } }
```

One reference syntax everywhere — `{{ vault "op" "Private/GitHub/token" }}`
(or `{{.vaults.house.gh}}`) — resolved at load, cached in memory, **tainted
and redacted from logs and audit even after flowing into a later step**,
never written back. Steps read and write with `uses: <vault>.read` /
`<vault>.write` (write only on writable types — conductor, pass, hashicorp);
OAuth2 connectors store and rotate their tokens through the same path
(`token_vault:` + `conductor connector auth <name>`, `auth ls`, `--revoke`).
`conductor secrets check` unlocks every vault and resolves every reference;
a vault that won't unlock **disables its dependents and notifies** instead
of crash-looping the box. The old scheme URIs (`op://`, `pass:`, `vault:`,
`file:`) and the `secrets:` block auto-migrate to this model at boot.

The `conductor` vault type (`conductor vault <name> init|add|get|ls|rm`)
seals the whole entry map — names, values, and count — as one padded
secretbox blob; the master key is never in the file and resolves
**non-interactively** through each vault's `unlock:` ref (`creds:` systemd
credentials, `keyring:`, `env:`, `file:`) or the default chain
(`$CONDUCTOR_VAULT_KEY` → systemd credential → OS keyring → a chmod-600 key
file seeded by `conductor unlock`). Non-interactive is the requirement, not
a convenience: the daemon updates and restarts itself, so a passphrase
prompt at boot would hang the fleet. If the vault file may end up somewhere
public (e.g. committed), keep the key out of the repo, use a random key or a
strong passphrase with `vault <name> init --sensitive` (scrypt N=2^20), and
note that git history is permanent — a key that was ever committed stays
extractable, so rotate what it sealed.

## Agent access to conductor (the skill)

`skill:` on an agent profile lets a dispatched agent reach back into
conductor over the daemon socket. Off by default; everything under it denies
unless config allows.

```yaml
agents:
  deployer:
    skill:
      secrets_via: broker          # broker | env (deprecated) | none (default)
      allow_secrets: [house/deploy_key]  # exact vault entries (<vault>/<key>) the broker may issue
      verbs: [gh.comment, rest.*]  # conductor verbs exposed as agent tools
```

Agent-authored workflows are also bound by **resource allowlists** on
`policy.agent_authored` — `allow_secrets`, `allow_stores`, `allow_targets`,
each deny-by-default (an empty list means agent-authored steps may not touch
that resource kind at all; the triggering repo is implicitly allowed;
`"*"` grants a kind; `trust: full` lifts all three). Config-authored steps
are unaffected.

`{{secret "<vault>/<key>"}}` templates a vault entry as an **opaque handle**
(`«secret:house/deploy_key»`) everywhere — prompts, env, tool args, audit — and the real
value replaces the handle only at conductor's own egress boundary (verb
invocation, code-step env/args, remote-command env/argv), only in
config-authored steps whose own template literally names it. Agent-authored
steps (plans, saved workflows) and anything relayed through data keep the
inert handle.

The principle: **the credential never leaves the daemon by default**. The
default path is acting *through* conductor — verbs as agent tools, where the
daemon injects credentials at its own egress boundary. When an agent must
run a raw tool that itself needs a credential, the **secret broker** issues
one as a minimized last resort: scoped to one named secret, single-use,
expiring in 60 s, and fully audited (issue, use, expiry). Authorization is
bound server-side to the real dispatch via an unguessable session token the
daemon mints at launch — never to client-asserted identity. Once redeemed,
the value is in the runtime's hands; if a runtime should never hold a
secret, don't list any in its `allow_secrets`. The tool surface reaches ACP
runtimes (`session/new` mcpServers) and native opencode (`OPENCODE_CONFIG`);
the paseo CLI exposes no MCP launch surface today, so a `skill:` profile on
a paseo runtime is inert and `conductor validate` says so. Details: the
Agent-Skill wiki page.

## Execution history, live watch & retry

Every run leaves a full record — per-step inputs / outputs / status / timing
/ cost, secret-scrubbed like the crash-resume checkpoints — and streams live
while it runs:

```
conductor watch                         # live: steps, gate rounds, outcomes as they happen
conductor runs                          # recent executions: status, trigger, cost
conductor runs <id>                     # one run's step-by-step detail
conductor runs retry <id>               # re-run from the recorded failed step
conductor runs retry <id> --from build  # …or from a chosen step
```

A foreground agent step also captures its **proposed diff** (uncommitted +
unpushed, from its worktree, scrubbed + clipped) into its outputs
(`{{.<id>.diff}}` — present it on an ask verb for approve-before-apply) and
into the run record; an interactive review hand-off shows the live diff on
every presentation.

A retry pins the recorded outputs of every earlier successful step into
scope — those steps don't re-run — and resumes through the ordinary flow
runner (checkpoints, budgets, policy, audit; the new record backlinks the
original via `retry_of`). Retention is configurable
(`store.history_retention` / `history_max_runs`, default 14d / 500). See the
[Runs wiki page](../../wiki/Runs).

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

## Conductor itself: `conductor.*`

Conductor's own lifecycle is a built-in source — alerting is an ordinary
trigger, not a separate notify subsystem: `on: [conductor.escalate,
conductor.needs_input]` → steps of sink verbs (`ntfy`, `pushover`,
`notifiarr`, slack/discord's post-only `webhook_url:` mode), with
`{{.message}}` and the event facts in scope. Events: `dispatch`, `escalate`
(gave up after retries), `needs_input`, `complete`, `failed` (a run
errored), `updated`, `update_available`. Routing, fan-out, quiet hours
(`policy:`), and digests (`group: { window }`) fall out of the trigger
grammar; a loop guard keeps a notification workflow's own events from
re-feeding, and escalations stay audit-logged with no trigger at all. The
retired `notify:` block auto-migrates onto these triggers with
byte-identical wire payloads.

Conductor also exposes verbs on itself — `conductor.update` / `pause` /
`resume` / `restart` / `reload` / `run`, plus `gh.sweep` — so a workflow
can act on conductor. `update: { auto: true }` still self-applies
unattended (the default is unchanged); `apply: workflow` makes detection
emit `conductor.update_available` instead, so a trigger drains, runs
`uses: conductor.update` (hooks before/after), and smoke-tests around the
restart.

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
a workflow from a file directly (`workflow: review-flow, import: ./workflows/review.yaml`,
or a bare `workflow: ./workflows/review.yaml` path when the file holds one workflow).
The wiki carries the full reference — Configuration, Connectors, Workflows,
Verbs, Code-Steps, Hosts, Grouping, Memory, Agents, Policy, Secrets,
Runtimes, Migration — and `conductor schema <conn>` prints any connector's
exact contract.

## Commands

```
conductor run [--config PATH]              start the daemon
conductor validate                         load & validate (both schemas, full semantic pass)
conductor replay <event.json>              run a saved webhook through the pipeline, verbs stubbed
conductor sweep [--now]                    catch-up sweep (preview / signal the daemon)
conductor force <kind> <owner/repo>#<n>    force one action now, bypassing dedup gates
conductor status | report [--days N]       live snapshot / activity summary
conductor pause | resume                   runtime kill switch (no restart)
conductor run <name> [--input k=v]         fire a named `on: manual` trigger via the daemon
conductor connectors ls | schema <conn>    introspection
conductor connector auth <name> | auth ls  one-time OAuth2 login / login state + expiry
conductor secrets check                    resolve every secret reference and report
conductor vault <name> init|add|get|ls|rm  manage a named vaults: entry
conductor unlock                           seed the vault key for unattended restarts
conductor workflows [ls|review|rm]         saved (agent-promoted) workflows: state + health
conductor config migrate [--dry-run]       legacy → connectors transform
conductor update | service …               self-update / service unit management
```

## Safety

- **Kill switches**: `conductor pause` at runtime; `enabled: false` turns off
  one connector or trigger in place; `policy: { shadow: true }` previews
  everything and dispatches nothing (per trigger, connector, or globally).
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
