# Dashboard: design

Status: **design + clickable prototype**. No product code yet.
Prototype: `design/dashboard-prototype/` (static HTML/CSS/JS with fake data; see
[§13](#13-the-prototype)).

This is a full web dashboard for conductor. It is served by the daemon, signed
into with a passkey, embedded in the binary, and usable on a phone. It covers
today's runs, workflows, packs, plugins and updates on `main`, and the four
features in flight:

- **members:** #178, `feat/members`;
- **the agent sandbox:** #179, `feat/agent-sandbox`;
- **the agent browser and vault credential fills:** #182, `feat/agent-browser`;
- **the container backend:** `feat/agent-container`.

## 0. Decisions already made

These came in with the brief and are not reopened here.

| Decision | Consequence in this design |
|---|---|
| Served by the conductor daemon on **localhost and the Tailscale interface only**. | There is no public endpoint, no `0.0.0.0`, no LAN address, and no exposure connector (the `tunnel`/`lan` builtins, `expose:`). The public HTTPS endpoint removed earlier (`babcfcb`: remote agents reach conductor over an SSH reverse tunnel, not a public URL) stays removed. |
| Sign-in is a **passkey** (WebAuthn). | A secure context is required: `http://localhost` for bootstrap, and HTTPS on the tailnet with a Tailscale certificate for the MagicDNS name ([§6.2](#62-listeners-and-tls)). |
| Approvals made in the UI stay **operator-authenticated**. | A WebAuthn assertion over the approval payload's digest feeds the same `Executor.Decide` as today's SSH-signed approvals. An agent has no path to it ([§6.6](#66-the-approvals-boundary)). |
| One **SPA embedded with `go:embed`**, responsive down to a phone. | Preact + TypeScript, built by Vite. Node is needed at build time only, and the built assets are committed ([§8](#8-frontend-stack)). A paseo panel can later use the same API. |
| Config editing must never crash-loop the fleet. | The file stays the source of truth. Edits are node-level and comment-preserving, validated **on a copy**, shown as a diff, applied with a backup, reloaded, and guarded at boot. Rollback is one click ([§7](#7-the-config-edit-pipeline)). |

## 1. Goals and non-goals

**Goals**

- One place to see what conductor is doing now, what it did, and what needs
  the operator. Every screen maps to real daemon state.
- Make every operator action the CLI has available in the UI, with **at least
  the CLI's safety**. Approvals, config applies, secret writes, grants and
  browser takeovers ask for a fresh passkey assertion.
- Give members (#178) and the agent browser (#182) the surfaces their PRs
  deferred:
  - talking to a member;
  - signing proposals, including Terraform plans;
  - editing a member's memory;
  - the live takeover view, which #182 §11 names as "the dashboard follow-on".
- Let the operator edit config visually without ever giving up YAML.

**Non-goals**

- Multi-tenant or multi-user RBAC beyond conductor's existing `operators:`.
  Every operator who can sign in is a full operator, as with the CLI today.
- Any access path off the tailnet. If you need it from elsewhere, join the
  tailnet or use `ssh -L` ([§6.2](#62-listeners-and-tls)).
- Replacing paseo's own UI. The member chat drives the home session through
  conductor (`conductor member say`) and links to paseo for the full agent
  view.

## 2. Information architecture

### 2.1 Navigation

```
Now      Overview · Live · Needs you
Work     Run history · Workflows (+ builder) · Members · Browsers
System   Config · Secrets & vars · Packs & plugins · Connectors & hosts
         (connectors, runtimes, hosts, isolation) · Updates · Logs & audit · Access
```

Some elements are on every screen:

- **Status strip** (top, 34px, monospace). It shows:
  - daemon up/paused and uptime;
  - the version, and the available version;
  - the sandbox verdict;
  - paseo health;
  - the running count;
  - **"N need you"**, the one place the ember accent is used for counts.

  It also shows the MagicDNS name and TLS state, so the operator always
  knows which origin they're on.
- **Rail** (left). Grouped navigation with live counts. It collapses to icons
  below 1100px. Below 720px it becomes a **bottom tab bar**: Overview, Live,
  Approvals, Members, More.
- **⌘K palette**. Jump to any run, member, workflow or setting, or run an
  action such as pausing dispatch or applying an update.
- **Needs you**. One queue merges everything that waits on a human:
  - pending proposals;
  - failed runs;
  - `needs_input` and escalations;
  - hand-off drafts awaiting an answer;
  - browser push approvals;
  - parked work;
  - an available update;
  - a vault that can't be reached;
  - a restored config.

### 2.2 Routes

| Route | Screen |
|---|---|
| `#/overview` | Overview |
| `#/live`, `#/live/<run>` | Live runs: step graph, streaming log, step inspector |
| `#/attention` | Needs you |
| `#/runs`, `#/runs/<id>` | Run history, run detail |
| `#/workflows`, `#/workflows/<name>` | Workflows, builder |
| `#/members`, `#/members/<m>/<tab>[/<id>]` | Members. Tabs: chat, cases, proposals[/id], memory, workers, access, definition |
| `#/browser`, `#/browser/<id>`, `#/browser/seed`, `#/browser/passkey` | Browsers |
| `#/config`, `#/config/raw`, `#/config/apply` | Config, raw editor, the apply pipeline |
| `#/secrets` | Secrets & vars |
| `#/extensions/<packs\|plugins\|browse>` | Packs & plugins |
| `#/connectors/<connectors\|runtimes\|hosts\|isolation>` | Connectors & hosts |
| `#/updates`, `#/logs/<daemon\|audit>`, `#/security` | Updates, logs and audit, access |
| `/signin`, `/enroll` | Sign-in, enrollment |

The product uses path routes. The prototype uses hash routes so it can be
opened from a file.

## 3. Visual language

The tool should be one you can keep open all day. It is dense but calm. It
uses color for state and almost nothing else.

- **Type.** Three typefaces, all SIL OFL and vendored:
  - *Instrument Sans* for the UI;
  - *Instrument Serif* italic for titles and big numbers (the "score sheet"
    voice);
  - *Martian Mono* for anything a machine said: ids, digests, times, costs,
    YAML and logs.
- **Color.**
  - Surfaces are warm graphite (dark) or paper (light). Each theme is its own
    set of steps, not an inversion.
  - One **ember** accent (`#ff6a2b`, light `#cc4a0e`) means *you / act here*:
    the primary button, Needs you, the "now" line, the focus ring.
  - Status hues are reserved: ok, running, waiting, failed, parked, shadow.
    Each always ships with a glyph and a label, so meaning never rides on
    hue alone. The status set was run through the dataviz palette validator.
    Red/green deuteranopia separation is below the floor, as it is for every
    red/green pair, which is why the glyph and label are mandatory.
- **Signature elements.**
  - **The score**, on the overview: one staff per workflow and every run of
    the last 12h as a mark on it. Running marks are animated stripes,
    waiting marks are hollow, and the ember "now" line pulses.
  - **The seal**, the passkey ring that fills as you sign.
  - **The step plates** of the run graph, with a status edge and a flowing
    dash on the active edge.
- **Motion.** One staggered rise on page entry, log lines sliding in, and
  pulses on live things. `prefers-reduced-motion` turns all of it off.
- **Texture.** A faint dot grid on the page and a 3–5% grain overlay. The
  builder canvas has a denser dot grid.

## 4. Screens

Each screen below lists its purpose, its data (with the API from
[§5](#5-api)) and its operations. **NEW** marks something the daemon doesn't
have today.

### 4.1 Overview

The purpose is to answer, in under five seconds: is it healthy, what is it
doing, and what needs me?

| Panel | Data | Source today |
|---|---|---|
| Needs you | `GET /api/v1/attention` | Members ledger (pending proposals), run history (failed), audit (`escalate`/`needs_input`, which `status` already reads), hand-off drafts (web connector), backoff/hold (`state.json`), browser `needs_input`, update checker. Merging them is **NEW** (`attention` aggregator). |
| Running / runs / spend / members tiles | `GET /api/v1/status`, `GET /api/v1/cost?by=day` | `status.go` (live agents, in-flight workflows from `runs.json`), `RunHistory.CostUSD` |
| The score | `GET /api/v1/runs?since=12h&fields=lane` | History records (`Kind`, `Variant`, `Started`, `Finished`, `Status`) |
| Health | `GET /api/v1/status` | Version, pid, uptime, config sha, sandbox registry (identities, containers), paseo provider-entry merge result (#179), connector registry health, update state |
| Activity | SSE `run`, `audit`, `member`, `browser` topics | `flow.EventHub` (`RunEvent`), audit writer tap (**NEW**) |
| Spend | `GET /api/v1/cost?by=day&days=14` | History `CostUSD`/`ApproxCost`; the shed count comes from `budget_shed`/`step_shed` audit events |

Operations:

- **Pause/Resume dispatch**: `setPaused`, as `conductor pause` does.
- **Run a workflow**: control `run`.

### 4.2 Live (running workflows)

This screen watches one run step by step.

- **Step graph.** The data comes from the config's step list. Steps run in
  order, and `parallel: [[…],[…]]` branches join before the next step; there
  is no `needs:` key. The graph is derived (**NEW**: `GET /api/v1/workflows/<name>/graph`),
  and since it is series-parallel it lays out trivially. Step states come
  from `RunHistory.Steps` and the live `step_started`/`step_done` events.
  `for_each`, `call:` and `team:` render as compound plates.
- **Streaming log.** Interleaved per-step output: agent turns, tool calls,
  egress denials, `git.push`. Today `watch` streams coarse events only. A
  **per-step log tap** is **NEW**:
  - a bounded ring per running step, fed by the cli/acp runtime's output
    (with the existing redaction) and by paseo's agent timeline for paseo
    steps;
  - persisted next to the history record (`history/<id>.steps/<step>.log`,
    clipped and scrubbed).
- **Step inspector.** Kind, runtime, the model resolved through the fleet
  ladder (rung), the sandbox identity, the inputs and outputs (from the
  record), cost and tokens, and the proposed diff so far. The diff is the
  same capture as `{{.<id>.diff}}`, re-read on demand for a running step.

Operations:

- **Stop run**: **NEW** cancel through the engine's context, with the same
  grace as a reload checkpoint.
- **Retry from step**: control `retry {run_id, from_step, force_replay}`.
- **Open in paseo**: a deep link.

### 4.3 Run history and run detail

- **History.** Filters: status, workflow, repo, time range, minimum cost,
  and free text over ids and errors. Reads `<state>/history/*.json`. Listing
  more than ~500 records cheaply needs a small **NEW** index: an append-only
  `history/index.jsonl` written by `PutHistory` and rebuilt when missing.
  Each record's HMAC (`Sig`) is verified, and a record that fails shows as
  **tampered**.
- **Detail.**
  - A timeline (gantt) with per-step cost.
  - Each step's inputs and outputs, log and diff, all scrubbed.
  - The run's audit lines.
  - Retry from any step.
  - Export JSONL.

### 4.4 Members (#178)

**List.** For each member: the home session's state, open cases, pending
proposals, live workers, the next sweep, and a peer-link map.

**Member detail.** The header shows:

- the home session (id, turns against `max_turns`, age against `max_age`);
- shadow mode or approved execution;
- the sandbox mode (namespace or container);
- operators and peers.

Header actions:

- **Rotate session**: `member rotate`.
- **Sweep now**: `member sweep`.
- **Deliver event**: `member deliver --title --text --requester --urgent`.

| Tab | Purpose and data | Operations |
|---|---|---|
| **Chat** | The home conversation: operator messages, member turns, tool calls, and fenced events (rendered as *untrusted data*). The transcript comes from paseo's agent timeline through conductor (**NEW** `GET /members/<m>/transcript`, plus the SSE topic `member.transcript`). A side rail shows pending proposals and open cases. | **Send**: `member say`. The requester is `operator:<name>`, so it is the operator's own request with full operator trust. Delivered when the agent is idle, never into a turn in progress, as today. |
| **Cases** | The ledger's cases: id, title, requester, class (`operator`/`event`/`user`/`peer`/`proactive`), workers and proposals. Shows the spend against the cap and the ledger's health (degraded or not). | Open, note or close a case (`member case …`). |
| **Proposals** | Pending, shadow, executed, refused, denied and failed proposals, each with tier, verb, case, requester class and worker. **Detail:** the exact action, or for Terraform: repo, commit, dir, workspace, var files, targets, the `+a ~c -d` counts, *replaces* list, plan text (`show -no-color`), plan sha256 and the approval digest. | **Approve / Deny with passkey** ([§6.6](#66-the-approvals-boundary)), and **Replan** for a stale plan. |
| **Memory** | The memory repo's tree (`knowledge/`, `runbooks/`, `episodes/`, `handoff/`), a file in read view, and `git log` with the `Member-*` trailers. | **Edit → Commit** (**NEW** operator write through `memrepo`: one commit, author `<operator> (dashboard)`, trailers `Member-Role: operator`, `Edited-Via: dashboard`), **Revert** a commit, **Consolidate now**. |
| **Workers** | The worker tree: home sessions (live and retired) → children, groups, sweeps. For each worker: taint (inherited requester class), narrowed verbs, read-only flag, case, cost, run link. | **Stop** a worker (**NEW**: cancel its run). There is deliberately no "tell", because only the parent instructs a child. |
| **Access** | `skill.verbs` × `autonomy.tiers`, `promote`, browser credential bindings, logins, and the operators who can approve. | Tier, promote, verb, binding and login changes are **config edits** through [§7](#7-the-config-edit-pipeline). Revoking a saved login (`browser logout`) is immediate state. |
| **Definition** | A form for the whole `members.<m>` node: charter or `charter_file`, scope, escalation, operators, `home.rotate`, `worker.*`, autonomy (shadow, `approval_ttl`, spend cap), `proactive.*`, `peers`, `isolation`. Live YAML sits beside it. | Review changes → the apply pipeline. **Define a member** uses the same form with `shadow: true` preset. |

### 4.5 Browsers (#182)

- **List.** One tile per live browser session, with a screencast thumbnail.
  Each tile shows the identity, the agent (run/worker), the requester, the
  profile (*anonymous* or *logged-in browser, narrowed to its sites*) and the
  action count. Below the tiles is a per-identity table: allowlist,
  logged-in sites, credentials, `evaluate`.
- **Live view.**
  - A CDP screencast (**NEW**: `Page.startScreencast` over the existing
    `--remote-debugging-pipe` client, relayed as WebSocket frames).
  - The URL and tabs.
  - The session panel: identity, agent, requester, profile, evaluate state.
  - The audited action list, which never shows values.
- **Takeover** (**NEW**). The operator takes a **lease** (step-up passkey)
  and drives the page with CDP `Input.dispatchMouseEvent`/`dispatchKeyEvent`:
  - The agent's next `conductor browser` call **waits** until the lease is
    released, or is refused with "operator has control" after a timeout.
  - Keystrokes are audited as a length only, as fills are.
  - On release, the agent gets a fresh snapshot.
  - Cookie write-back keeps #182's rule: only cookies the saved session
    already had, and only for that browser's logged-in sites.
- **Seed a login.** This replaces `conductor browser seed`'s headed window
  with a **headless jailed browser in takeover mode**, so it works from a
  phone over the tailnet. Only the site's cookies are kept, in
  `<state>/browser/logins/` (0700/0600).
- **Register a passkey.** `browser passkey register` through the same
  takeover. An empty virtual authenticator is armed on that tab only, the
  operator adds a passkey on the site, and conductor stores the one
  credential whose RP covers the bound host in the credential's writable
  vault. The key is never sent to the dashboard.
- **Credential bindings and per-identity settings** are config
  (`browser.allow`, `logins`, `credentials`, `evaluate`) and go through the
  pipeline. Adding a site, login, credential or evaluate mode is **widening**
  ([§7.6](#76-packs-overrides-and-narrow-never-widen)).

### 4.6 Workflows and the builder

- **List.** Name, `on:`, owner (your config, a pack, or a member's
  synthesized trigger), 24h runs, success rate, spend. Run one manually.
- **Builder.** Three columns:
  - a palette of step kinds and the verbs from `conductor schema`;
  - a **canvas** with the trigger, steps and `parallel:` boxes;
  - the **YAML**, which is canonical.

  Selecting a plate highlights its YAML lines. An inspector edits the
  step's fields. Each edit is a node-level YAML edit
  ([§7.2](#72-node-level-edits)), and the YAML pane updates with comments
  and unknown keys intact. Validation runs on every change (debounced,
  against a copy), with errors in the gutter and on the plate. **Review &
  apply** opens the apply pipeline, and is disabled while there are errors.
- **Pack-owned workflows.** The plates are hatched. The YAML pane has two
  tabs: **Your overrides** (editable, `packs.<instance>`) and **Pack source**
  (read-only, the vendored file at its locked sha). An edit to a pack step
  becomes an override in `packs.<i>.steps["<workflow>/<step>"]`. A
  structural change (adding a step to a pack workflow) is refused with the
  supported alternative: call the pack's workflow from your own trigger, or
  fork the pack into a local `use: ./packs/…`.

### 4.7 Config

- **Files.** Every file the daemon loaded, each with its owner:
  - `config.yaml` and its `imports:`, owned by you;
  - vendored packs, owned by the pack;
  - `conductor.lock.yaml`, owned by conductor.

  Also shows each file's sha and whether **on disk = loaded** (drift
  detection).
- **Sections.** One chip per top-level section. Each opens its visual
  editor, or the raw editor scrolled to that node.
- **Raw editor.** CodeMirror with YAML mode, schema completion from
  `conductor schema`, an outline, and node breadcrumbs. Raw edits are parsed
  into the same node tree, so they go through the same pipeline.
- **Applied changes.** The history of backups, with **Restore**, which is the
  pipeline in reverse ([§7.5](#75-rollback)).
- **Apply pipeline.** Edit → Validate on a copy → Review diff → Apply →
  Reload & verify ([§7](#7-the-config-edit-pipeline)).

### 4.8 Secrets and vars

- **Vaults** (`vaults:`). For each vault: type (conductor, onepassword, pass,
  file, hashicorp), reachability, and whether it is writable. A vault whose
  unlock ref fails (for example an expired `VAULT_TOKEN`) raises a Needs-you
  item.
- **Secrets.** Every secret **reference** in config, each with its source
  (vault and key, or `env:`) and **where it is used**. The used-by data comes
  from the config node tree (**NEW** reverse index). The value column is
  always `••••••••`.
  - **Set/rotate** writes a value to a writable vault (`<vault>.write` /
    `conductor vault <name> add`) after a step-up assertion. There is **no
    endpoint that returns a value.**
  - **Change ref** points a secret at a 1Password/pass/HashiCorp item. Its
    "resolves" check runs in the daemon and returns only ok or an error.
- **Settings** (`settings:`). Editable inline. Shows chains
  (`${settings.org}/deploys`), `${env.…}` sources and reference counts.
  Changes go through the pipeline.

### 4.9 Packs and plugins, updates, connectors and hosts, logs

- **Packs** (`packs:` + `conductor.lock.yaml`):
  - version and sha, official or third party, hold;
  - armed/dormant triggers;
  - the operator's override count;
  - dormant-trigger notices ("needs a pagerduty connector").

  **Plan** (`conductor pack plan`) shows what an update changes, whether
  overrides still apply, the pack lint result, and new disarmed triggers.
  **Add / update / remove / hold** are `packs:` edits plus the fetch and lock
  that `conductor init` does, all validated on a copy. `pack_trust:` limits
  sources.
- **Plugins.** Kind, version, source, instances, and whether an update
  hot-reloads (`SameReloadSurface`) or restarts. Update, pin, remove.
  **Browse** lists the official registry.
- **Updates.**
  - The available release and its preflight: the **downloaded** binary's
    `validate` against this config, on a copy, with
    `--require-plugins`.
  - Release notes.
  - Policy (`update.auto`, `deps`, `reload`, `apply: workflow`, `interval`).
  - **Apply now** runs **the same `doUpdate` path as auto-update**:
    preflight, `<exe>.prev`, atomic swap, restart. Auto-update stays on by
    default.
- **Connectors & hosts.**
  - Connectors: health, events and verb calls, rate-limit state.
  - Runtimes: the isolation each launches with.
  - Hosts: reachability and RTT.
  - Isolation:
    - `sandbox.*`, with the backend (namespace or Seatbelt vs container) and
      `egress`;
    - live identities and containers (`conductor sandbox ls` / `archive`);
    - every `isolation: {mode: none}` opt-out in effect, and who set it.

  All edits go through the pipeline.
- **Logs & audit.**
  - The daemon log: a **NEW** in-memory ring of the daemon's `logf`, with
    redaction at the source. journald and launchd logging are unchanged.
  - The audit log: `audit.jsonl`, filterable by event, actor, run or case.
  - Every dashboard action is audited (`dashboard_*` events) with the
    operator, the credential and the client address.

### 4.10 Access

- **Passkeys** per operator. Each shows its relying party, authenticator
  type, sign count and last use. A passkey can be revoked, which ends its
  sessions.
- **Sessions.** Device, address and age. End one, or sign out everywhere.
- **Listeners.** Where the dashboard listens, the certificate, and what is
  refused.
- **Step-up list and lockout state.**

### 4.11 Phone

At 390px:

- the bottom tab bar replaces the rail;
- tables become stacked label/value rows;
- dialogs become bottom sheets;
- chat takes the screen;
- the proposal page puts **Approve** in reach of a thumb.

The phone flows that matter are overview, approvals and chat. They are first
class, and the prototype's `phone.html` shows them in device frames.

## 5. API

### 5.1 Conventions

- **Transport.** JSON over HTTPS (or HTTP on `localhost`) under `/api/v1`,
  same origin as the SPA. There is no CORS: cross-origin requests are
  refused.
- **Reads** are `GET`, side-effect free and cacheable with `ETag`. **Writes**
  are `POST`/`PUT`/`DELETE` with `Content-Type: application/json` only
  (form encodings are refused, [§6.4](#64-csrf-clickjacking-xss-rebinding)).
- **Errors** look like `{error: {code, message, hint?}}`, where `hint` is the
  CLI's actionable advice reused.
- **Lists** are cursor-paginated (`?cursor=&limit=`, at most 200) and return
  `next`.
- **Step-up.** A sensitive write carries `X-Conductor-Stepup: <token>`,
  which is minted by a passkey assertion bound to that request's digest
  ([§6.5](#65-step-up)).
- **Shared code.** Handlers call the same internal functions the CLI and the
  control socket use (`setPaused`, `doUpdate`, `retry`, the member service,
  the browser manager). There is no second implementation of any operation.
- **Package layout.** The API lives in a new `internal/dashboard` package
  with subpackages `api`, `auth`, `events`, `configedit` and `web`.
  `internal/configedit` is CLI-reusable (a later `conductor config set` can
  use it).

### 5.2 Live stream

**Server-Sent Events** at `GET /api/v1/events?topics=…`.

- Each event has a monotonically increasing `id`. The server keeps a ring of
  10k events, so a reconnect with `Last-Event-ID` resumes. If the gap is
  larger than the ring, the client gets `event: reset` and refetches its
  snapshots.
- Heartbeats are sent every 15s.
- **The daemon re-execs on every reload and update**, so the SPA treats a
  dropped stream as "reloading". It shows the reconnecting state and resumes.
  The server announces `daemon.restarting` first.

| Topic | Payload | Source |
|---|---|---|
| `run` | `RunEvent` (`run_started`, `step_started`, `step_done`, `gate`, `run_done`) | `flow.EventHub.Subscribe` (exists; `watch` uses it) |
| `step_log` | `{run, step, seq, kind, text}` | **NEW** per-step log tap |
| `audit` | each audit line (redacted) | **NEW** tap in the audit writer |
| `attention` | add / update / remove | **NEW** aggregator |
| `member` | ledger changes (case, proposal, worker, session), `transcript` turns | members service hooks (#178) + paseo timeline relay (**NEW**) |
| `browser` | session list changes, status, `needs_input` | browser manager (#182) |
| `config` | drift, apply progress, restored | **NEW** pipeline |
| `daemon` | health, paused, update found/applied, `restarting` | status/update loop |

**WebSocket** is used only where input must flow upstream at low latency: a
browser's screencast and takeover, at `GET /api/v1/browsers/<id>/screencast`
(binary JPEG frames down, input events up). Chat stays HTTP plus SSE.

### 5.3 Endpoints

Rows marked **NEW** need new daemon code. The others wrap code that exists on
`main` or in the named PR.

**Session and access**

| Method & path | Does | Maps to |
|---|---|---|
| `GET /api/v1/auth/state` | signed in? operator, session expiry, step-up freshness | **NEW** |
| `POST /api/v1/auth/login/{begin,finish}` | WebAuthn assertion → session | **NEW** (`go-webauthn/webauthn`) |
| `POST /api/v1/auth/stepup/{begin,finish}` | assertion over `{purpose, digest}` → single-use step-up token | **NEW** |
| `POST /api/v1/auth/logout` | end this session | **NEW** |
| `GET/DELETE /api/v1/passkeys[/<id>]`, `GET/DELETE /api/v1/sessions[/<id>]` | list, revoke | **NEW** (`<state>/dashboard/`) |
| `GET /enroll` (localhost only), `POST /api/v1/enroll/{redeem,begin,finish}` | operator enrollment ([§6.3](#63-enrollment-sign-in-sessions)) | **NEW** + `conductor dashboard enroll` |

**Overview, daemon, runs**

| Method & path | Does | Maps to |
|---|---|---|
| `GET /api/v1/status` | version, uptime, pid, paused, config sha, sandbox, paseo, connectors, update | `status.go`, sandbox registry, connector registry, update checker |
| `POST /api/v1/daemon/{pause,resume}` | pause/resume dispatch | `setPaused` |
| `GET /api/v1/attention` | the Needs-you queue | **NEW** aggregator |
| `GET /api/v1/runs?status&workflow&repo&since&q&cursor` | history list | `Store.ListHistory` + **NEW** index |
| `GET /api/v1/runs/<id>` | record with steps, signature verdict | `Store.GetHistory`, `signHistory` check |
| `GET /api/v1/runs/<id>/steps/<step>/log?from=` | step log | **NEW** log tap |
| `GET /api/v1/runs/<id>/steps/<step>/diff` | proposed diff (live for running) | diff capture (`gitdiff`) |
| `POST /api/v1/runs/<id>/retry` `{from_step, force_replay}` | retry-from-step | control `retry` |
| `POST /api/v1/runs/<id>/stop` | cancel a running run | **NEW** |
| `GET /api/v1/inflight` | in-flight workflows | `runs.json` (as `status`) |
| `GET /api/v1/workflows`, `GET /api/v1/workflows/<name>/graph` | list; derived graph + node→YAML source map | config + **NEW** graph derivation |
| `POST /api/v1/workflows/<name>/run` `{inputs}` | manual run | control `run` |
| `GET /api/v1/cost?by=day\|workflow&days=` | spend series | history `CostUSD` |
| `GET /api/v1/audit?event&actor&run&case&since&cursor` | audit query | `audit.jsonl` reader (**NEW** tail/index) |
| `GET /api/v1/logs?level&since` | daemon log | **NEW** log ring |

**Members (#178)**

| Method & path | Does | Maps to |
|---|---|---|
| `GET /api/v1/members`, `GET /api/v1/members/<m>` | list, show | `member ls`, `member show` |
| `GET /api/v1/members/<m>/{cases,proposals,workers}` | ledger views | `member cases\|proposals\|workers` |
| `GET /api/v1/members/<m>/proposals/<id>` | detail + **canonical approval payload** + digest; Terraform plan text and counts | `member proposal` |
| `POST /api/v1/members/<m>/proposals/<id>/decision` `{decision, assertion}` | approve/deny with WebAuthn | **NEW** WebAuthn verifier → `Executor.Decide` ([§6.6](#66-the-approvals-boundary)) |
| `POST /api/v1/members/<m>/proposals/<id>/replan` | replan stale Terraform | `member replan` |
| `POST /api/v1/members/<m>/say` `{text}` | talk to the home session | `member say` (requester `operator:<name>`) |
| `GET /api/v1/members/<m>/transcript?cursor` | home conversation | **NEW** paseo timeline relay |
| `POST /api/v1/members/<m>/{deliver,rotate,sweep,consolidate}` | as the CLI | `member deliver\|rotate\|sweep\|consolidate` |
| `POST /api/v1/members/<m>/cases/<c>/{note,close}` | case ops | `member case …` |
| `POST /api/v1/members/<m>/workers/<w>/stop` | cancel a worker's run | **NEW** (run cancel) |
| `GET /api/v1/members/<m>/memory/{tree,file,log}?ref&path` | browse memory | `memrepo` reads |
| `POST /api/v1/members/<m>/memory/commit` `{path, content, base, message}` | operator edit → one commit | **NEW** operator write in `memrepo` |
| `POST /api/v1/members/<m>/memory/revert` `{sha}` | revert | **NEW** (`git revert` via `memrepo`) |

**Browser (#182)**

| Method & path | Does | Maps to |
|---|---|---|
| `GET /api/v1/browsers`, `GET /api/v1/browsers/<id>` | sessions, actions | `browser sessions`, audit |
| `GET /api/v1/browsers/<id>/peek` | masked screenshot | `browser peek` |
| `WS /api/v1/browsers/<id>/screencast` | frames down, input up (input only with a lease) | **NEW** CDP screencast relay |
| `POST/DELETE /api/v1/browsers/<id>/takeover` | take/release the lease (step-up) | **NEW** lease in the browser manager |
| `POST /api/v1/browser/seed` `{identity, site}` → session; `POST …/save` | seed a login via takeover | `browser seed` (headless variant **NEW**) |
| `POST /api/v1/browser/passkey/register` `{credential}`; `POST …/save` | dedicated passkey via takeover | `browser passkey register` |
| `DELETE /api/v1/browser/logins/<identity>/<site>` | log out | `browser logout` |
| `POST /api/v1/browser/cookies/import` | one domain's cookies (upload) | `browser cookies import` |

**Config, secrets, extensions**

| Method & path | Does | Maps to |
|---|---|---|
| `GET /api/v1/config/files`, `GET /api/v1/config/tree`, `GET /api/v1/config/raw?file` | files, node tree with source map, owners, used-by index | **NEW** `configedit` over `config.Load`'s inputs |
| `GET /api/v1/schema[/<connector>]` | events/verbs/options | `conductor schema` |
| `POST /api/v1/config/changes` `{base_sha, ops[]}` | stage a change set → candidate | **NEW** ([§7](#7-the-config-edit-pipeline)) |
| `POST /api/v1/config/changes/<id>/validate` (SSE progress) | validate on a copy | **NEW** |
| `GET /api/v1/config/changes/<id>/diff` | unified diff per file + effects + widening list | **NEW** |
| `POST /api/v1/config/changes/<id>/apply` (step-up) | backup, write, reload, verify | **NEW** journal + boot guard |
| `GET /api/v1/config/history`, `POST /api/v1/config/history/<id>/restore` | backups; restore = a validated change | **NEW** |
| `GET /api/v1/vaults`, `GET /api/v1/secrets` | vault status; references + usage (never values) | `vaults` framework + used-by index |
| `PUT /api/v1/secrets/<vault>/<key>` `{value}` (step-up), `DELETE …` | write-only set/rotate; delete | `<vault>.write`, `conductor vault … add\|rm` |
| `POST /api/v1/secrets/check` `{ref}` | does it resolve? → ok/error only | `vaults.ReadFresh` (value discarded) |
| `GET /api/v1/packs`, `POST /api/v1/packs/<i>/plan` | packs, plan | `pack list`, `pack plan` |
| `POST /api/v1/packs` / `…/<i>/{update,hold}` / `DELETE …/<i>` | add/update/hold/remove → config change + fetch/lock on a copy | `pack add\|update\|remove`, `init` |
| `GET /api/v1/plugins`, `POST /api/v1/plugins[/<p>/update]`, `DELETE …/<p>` | plugins | `plugin list\|add\|update\|remove` |
| `GET /api/v1/updates`, `POST /api/v1/updates/{check,apply}` (apply: step-up) | releases; check; apply | `releaseChecker`, **`doUpdate`** (the auto-update path) |
| `GET /api/v1/connectors`, `/runtimes`, `/hosts`, `/sandbox` | health views | connector registry, runtimes, `hosts`, `sandbox ls` |
| `POST /api/v1/sandbox/containers/<identity>/archive` | remove an identity's container + HOME volume | `sandbox archive` |

### 5.4 What's new in the daemon (summary)

1. `internal/dashboard`:
   - listener(s), TLS, Host checks;
   - SPA serving (`go:embed`);
   - API handlers;
   - SSE hub with a resumable ring;
   - the WebSocket screencast relay.
2. `internal/dashboard/auth`:
   - WebAuthn registration and assertion;
   - sessions, CSRF, step-up, lockout;
   - an enrollment ticket store in `<state>/dashboard/` (0700; files 0600).
3. `conductor dashboard enroll | reset | status`, over the operator control
   socket.
4. A WebAuthn approval verifier plugged into the members `Executor.Decide`.
5. `internal/configedit`:
   - node-level edits with byte-preserving splices;
   - a source map across `imports:`;
   - the owner and used-by indexes;
   - validate-on-copy;
   - the apply journal and boot guard;
   - backups and history.
6. Observability:
   - a per-step log tap;
   - a daemon log ring;
   - an audit writer tap;
   - the history index;
   - run cancel;
   - the attention aggregator.
7. Members:
   - paseo transcript relay;
   - operator memory commit and revert;
   - worker stop.
8. Browser:
   - CDP screencast and input relay;
   - takeover lease;
   - a headless seed / passkey register over takeover.
9. Egress proxy: a **hard deny** of the dashboard's own addresses
   ([§6.7](#67-threat-model-can-an-agent-reach-the-dashboard)).
10. Config: a `dashboard:` block ([§6.2](#62-listeners-and-tls)) and its
    validation.
