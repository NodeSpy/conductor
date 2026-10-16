# Dashboard: design

Status: **design + clickable prototype**. No product code yet.
Prototype: `design/dashboard-prototype/` (static HTML/CSS/JS with fake data; see
[§13](#13-the-prototype)).

This is a full web dashboard for conductor. It is served by the daemon, signed
into with a passkey, embedded in the binary, and usable on a phone. It covers
today's runs, workflows, packs, plugins and updates on `main`, and the four
features in flight:

- **bots** (called *members* in #178 and its branch): #178, `feat/members`;
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
- Give bots (#178) and the agent browser (#182) the surfaces their PRs
  deferred:
  - talking to a bot;
  - signing proposals, including Terraform plans;
  - editing a bot's memory;
  - the live takeover view, which #182 §11 names as "the dashboard follow-on".
- Let the operator edit config visually without ever giving up YAML.

**Non-goals**

- Multi-tenant or multi-user RBAC beyond conductor's existing `operators:`.
  Every operator who can sign in is a full operator, as with the CLI today.
- Any access path off the tailnet. If you need it from elsewhere, join the
  tailnet or use `ssh -L` ([§6.2](#62-listeners-and-tls)).
- Replacing paseo's own UI. The bot chat drives the home session through
  conductor (`conductor bot say`) and links to paseo for the full agent
  view.

## 2. Information architecture

### 2.1 Navigation

```
Now      Overview · Live · Needs you
Work     Run history · Workflows (+ builder) · Bots · Browsers
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
  Approvals, Bots, More.
- **⌘K palette**. Jump to any run, bot, workflow or setting, or run an
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
| `#/bots`, `#/folders/<path>` | Bots: the folder tree (a folder's subtree on desktop, one level at a time on a phone) |
| `#/bots/<m>/<tab>[/<id>]`, `#/bots/new/settings[?team=]` | A bot. Tabs: chat, cases[/id], proposals[/id], memory, workers, access, definition |
| `#/teams/<t>[/<tab>]` | A team. Tabs: overview, routing, defaults, knowledge |
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
| Needs you | `GET /api/v1/attention` | Bots ledger (pending proposals), run history (failed), audit (`escalate`/`needs_input`, which `status` already reads), hand-off drafts (web connector), backoff/hold (`state.json`), browser `needs_input`, update checker. Merging them is **NEW** (`attention` aggregator). |
| Running / runs / approvals (or model usage) / bots tiles | `GET /api/v1/status`, `GET /api/v1/usage` when `usage.mode` is not `off` | `status.go` (live agents, in-flight workflows from `runs.json`); usage per provider ([§4.1.1](#411-usage-display-usagemode)) |
| The score | `GET /api/v1/runs?since=12h&fields=lane` | History records (`Kind`, `Variant`, `Started`, `Finished`, `Status`) |
| Health | `GET /api/v1/status` | Version, pid, uptime, config sha, sandbox registry (identities, containers), paseo provider-entry merge result (#179), connector registry health, update state |
| Activity | SSE `run`, `audit`, `bot`, `browser` topics | `flow.EventHub` (`RunEvent`), audit writer tap (**NEW**) |
| Workflows (off) or Model usage (subscription/api) | `GET /api/v1/workflows`; `GET /api/v1/usage?days=14` | One card per provider, never a combined chart ([§4.1.1](#411-usage-display-usagemode)) |

Operations:

- **Pause/Resume dispatch**: `setPaused`, as `conductor pause` does.
- **Run a workflow**: control `run`.

#### 4.1.1 Usage display (`usage.mode`)

Model accounts usually sit behind a proxy or load balancer that conductor
can't see past, so anything conductor showed by default would be partial and
misleading. Usage display is therefore opt-in:

```yaml
usage:
  mode: off            # off (default) | subscription | api
  source:              # optional: who reports plan limits and tokens
    name: llm-proxy
    url: http://llm-proxy.internal:4000/usage
    token: ${secret.house/llm-proxy}
```

- **`off` (the default).** No usage or spend anywhere: no columns, tiles,
  charts, filters or tags. The screens are laid out to be complete without
  them (the overview shows an Approvals tile and a Workflows panel instead).
- **`subscription`.** Runs and turns conductor itself observes, **per
  provider**, labelled "observed by conductor". Plan-window utilization (5h,
  weekly) and token counts appear **only** when `usage.source` is configured,
  and each figure is labelled with that source ("from llm-proxy"). Never
  dollars.
- **`api`.** Dollars **per provider** (from the runtime's reports, or from
  the source when configured).
- **Never a total across providers or models.** Claude and Codex (and any
  other provider) are always shown side by side, each in its own units: one
  card per provider on the overview, a provider column and filter in run
  history, provider chips on bot, team, folder and workflow rows. A run uses
  one provider and model, so its own figure is never a mix. No "overall"
  bar, no summed tokens or dollars.
- **Budgets.** Run, turn and token caps are always available (`budget.*`,
  `proactive.budget.*`, `policy.budget.*`); token and dollar caps apply to
  each provider on its own. Dollar fields appear only in `api` mode; a
  dollar cap already in config is kept, not dropped, when the mode changes.
- **Cloud spend is not model usage.** The `commit_money` tier, the ledger's
  spend cap and the `aws-cost` connector are real-world money and show in
  every mode.

Config → General edits `usage.mode` and `usage.source` through the normal
diff. **NEW** endpoint: `GET /api/v1/usage?days=` returns
`{ providers: { <id>: { observed: {runs, turns}, reported?: {windows[], tokens, by_model{}, source}, usd? } } }`;
the daemon never adds providers together, and the UI has no code path that does.

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

- **History.** Filters: status, workflow, repo, time range, provider (when usage display is on),
  and free text over ids and errors. Reads `<state>/history/*.json`. Listing
  more than ~500 records cheaply needs a small **NEW** index: an append-only
  `history/index.jsonl` written by `PutHistory` and rebuilt when missing.
  Each record's HMAC (`Sig`) is verified, and a record that fails shows as
  **tampered**.
- **Detail.**
  - A timeline (gantt), with each agent step's usage when usage display is on.
  - Each step's inputs and outputs, log and diff, all scrubbed.
  - The run's audit lines.
  - Retry from any step.
  - Export JSONL.

### 4.4 Teams and bots (#178)

A **bot** is a persistent conversational worker with a charter, a memory
repo, a ledger of cases and a home session (#178 calls them *members*). A
bot is **solo or on a team**. "Agent" stays the word for conductor's agent
steps and profiles; a bot is not an agent, though its workers run agent
steps.

**Teams.** First-class `teams:` group bots (the follow-up to #178). The
operator cuts them any way they like; there is no org concept.

```yaml
teams:
  platform:
    description: Platform services
    lead: platform-sre                 # optional: triages team work and assigns it
    defaults:                          # inherited by bots, which can only narrow them
      operators: [ops, oncall]         # who may approve
      autonomy: { max_tier: reversible, promote: true, approval_ttl: 24h }   # the ceiling
      isolation: { mode: namespace }   # sandbox policy; container is stricter
      budget: { max_cost_per_day: 20.00, proactive_pool: { max_runs_per_day: 10, max_cost_per_day: 4.00 } }
      browser: { logins: [console.example.com], credentials: [grafana, statuspage] }   # scope
    knowledge: { repo: team-knowledge/platform, promote: approval }   # optional shared repo
    routing:                           # first match wins; to a bot, or to the lead
      - { name: pd-api, when: { on: pd.incident, service: [api, web] }, to: lead }
      - { name: web-issues, when: { on: gh.issue_opened, repo: [acme/web] }, to: web-triage }
    links: [finance]                   # teams are isolated unless both sides link
bots:
  platform-sre: { team: platform, … }  # at most one team, or none
  release-scribe: { … }                # a solo bot
triggers:
  - name: incident-intake
    on: pd.incident
    steps:
      - id: page
        bot: { team: platform }        # the team's rules pick the bot; no match → the lead
        title: "{{.title}}"
      # - { bot: platform-sre, title: … }   # or straight to one bot
```

- **Narrow, never widen.** A bot's own setting must sit inside the team
  default: a subset of operators, no tier looser than `max_tier`, no
  promotion when the team allows none, `container` where the team requires
  it, sweep budgets inside the shared pool, credentials and logins inside the
  team's scope. Validation refuses anything wider, naming the default it
  breaks.
- **Peers.** Bots of one team are peers by default (data-only `ask`, as
  #178's `peers:`). Across teams they're isolated unless both teams list each
  other in `links:`; an explicit cross-team `peers:` entry needs that link.
- **Delegation.** A bot can hand a teammate a task (`conductor bot
  delegate <bot> --case <c> --to <teammate> "<task>"`). The recipient works
  it as a child case **under the original requester's provenance**, never the
  delegating bot's, and its result reports back to the parent case. Asks
  stay data-only; delegation carries a task. Across teams, delegation exists
  only where both teams list each other in `links:`; a solo bot delegates to
  no one. The ledger records each delegation as an edge (`d<n>`: from, to,
  parent case, child case, status, result).
- **Knowledge.** An optional shared repo every bot reads. A bot
  proposes promoting a file from its own memory; by default the operator
  approves it (a step-up assertion), which writes one commit with
  `Promoted-By` and `Approved-By` trailers.
- **Routing.** Work reaches a team by rule, or through its lead, who reads
  the case and assigns it. **Requester provenance is preserved through every
  hop**: the assignee runs on no more trust than whoever asked. Each case
  records how it got there ("routed to platform by rule pd-api → assigned by
  lead platform-sre to web-triage"), and an operator reassignment is
  recorded the same way.
- Team, botship, defaults, routing, links and the knowledge policy are
  config, so every edit goes through [§7](#7-the-config-edit-pipeline).
  Widening includes adding a team approver, raising the ceiling, dropping a
  container requirement, linking a team, and automatic promotion.

**Folders.** Any bot or team may carry an optional `folder: "a/b"` (slashes
nest): everything for one company, "personal" bots, or by what they do or
watch. **Folders are organization only**: display, filter and search. They
carry no permissions, defaults or behavior; teams own behavior (shared
defaults and ceilings, peering and delegation, knowledge, routing). A bot on
a team shows under its team, and the team sits in a folder; solo bots sit in
folders beside teams. Config holds no empty folders, so a folder created in
the dashboard exists only in the browser until something moves into it.

```yaml
teams:
  platform: { folder: work/platform, … }
  finance:  { folder: work/billing, … }
bots:
  release-scribe: { folder: work/platform, … }   # solo, beside team platform
  homelab:        { folder: personal/home, … }
```

**Bots screen.** The sidebar item is **Bots**. The screen is a collapsible
folder tree (folders → teams → bots, solo bots alongside teams) with
**New bot**, **New team** and **New folder**, and:

- folder roll-ups: bots, pending approvals, open cases, live home sessions;
- create, rename and delete folders. Rename rewrites the `folder:` of
  everything inside; delete moves the contents up. Both are staged edits;
- move a team or a solo bot by drag-and-drop onto a folder, or with
  **Move to…**. Folders live in config, so moves stage into the diff and
  apply with the passkey (a bot on a team moves with its team);
- search across folders (names, descriptions, paths, teams);
- a folder breadcrumb on bot and team pages (`Bots › work › platform ›
  team platform › platform-sre`);
- a **folder filter** wherever it helps: Needs you on the overview and its
  own page (approvals), run history, browsers. Items not tied to a bot in
  that folder are hidden, and the page says how many;
- on a phone, the tree becomes a drill-down list, one level at a time;
  search there is a flat list across every folder;
- below the tree, the peers and links map: team boundaries, intra-team peers
  dashed, team links and explicit cross-team peers drawn, the lead ringed.

**Team page.**

| Tab | Shows | Operations |
|---|---|---|
| Overview | bots and the lead; the **ask & delegate activity graph** (teammates as nodes, asks dashed, delegations as arrows with counts, linked teams' bots at the edge); delegations with status and result; routing decisions with provenance; the team budget and the shared proactive pool; links inside and across teams, and each bot's **delegation reach** | add/remove a bot, make lead, link/unlink a team (config edits), **Route work here** (deliver to the team; its rules decide) |
| Routing | ordered rules, and a "try a rule" check against real past events | add, edit, reorder, delete rules (config edits) |
| Defaults | each default, and what every bot inherits, narrows, or (refused) widens | edit defaults (config edit) |
| Knowledge | the shared repo in the knowledge editor (below), with bot proposals on top | author files directly; approve, **approve with changes**, deny with a reason, in bulk |

**Bot detail.** The header shows:

- the home session (id, turns against `max_turns`, age against `max_age`);
- shadow mode or approved execution;
- the sandbox mode (namespace or container);
- operators and peers.

Header actions:

- **Rotate session**: `bot rotate`.
- **Sweep now**: `bot sweep`.
- **Deliver event**: `bot deliver --title --text --requester --urgent`.

| Tab | Purpose and data | Operations |
|---|---|---|
| **Chat** | The home conversation: operator messages, bot turns, tool calls, and fenced events (rendered as *untrusted data*). The transcript comes from paseo's agent timeline through conductor (**NEW** `GET /bots/<m>/transcript`, plus the SSE topic `bot.transcript`). A side rail shows pending proposals and open cases. | **Send**: `bot say`. The requester is `operator:<name>`, so it is the operator's own request with full operator trust. Delivered when the agent is idle, never into a turn in progress, as today. |
| **Cases** | The ledger's cases: id, title, requester, class (`operator`/`event`/`user`/`peer`/`proactive`), **how it was routed** (rule, lead, operator, or "delegated by platform-sre → platform-data from case c29"), **delegation edges** with status and the reported result, workers and proposals. Shows the spend against the cap and the ledger's health (degraded or not). | Open, note, close or reopen a case (`bot case …`); reassign within the team (operator, provenance kept); have the bot delegate a task to a teammate (`bot delegate`); cancel a running delegation. Closing a delegated case reports its last note back as the result. |
| **Proposals** | Pending, shadow, executed, refused, denied and failed proposals, each with tier, verb, case, requester class and worker. **Detail:** the exact action, or for Terraform: repo, commit, dir, workspace, var files, targets, the `+a ~c -d` counts, *replaces* list, plan text (`show -no-color`), plan sha256 and the approval digest. | **Approve / Deny with passkey** ([§6.6](#66-the-approvals-boundary)), and **Replan** for a stale plan. |
| **Memory** | The bot's memory repo (`knowledge/`, `runbooks/`, `episodes/`, `handoff/`) in the knowledge editor (below), with pinned facts first. | Read, correct, pin and delete what it knows; every save is one operator commit. Promote a file to the team's repo. The bot keeps writing its own memory. |
| **Workers** | The worker tree: home sessions (live and retired) → children, groups, sweeps. For each worker: taint (inherited requester class), narrowed verbs, read-only flag, case, cost, run link. | **Stop** a worker (**NEW**: cancel its run). There is deliberately no "tell", because only the parent instructs a child. |
| **Access** | `skill.verbs` × `autonomy.tiers`, `promote`, browser credential bindings, logins, and the operators who can approve. | Tier, promote, verb, binding and login changes are **config edits** through [§7](#7-the-config-edit-pipeline). Revoking a saved login (`browser logout`) is immediate state. |
| **Definition** | A form for the whole `bots.<m>` node: `team`, charter or `charter_file`, scope, escalation, operators, `home.rotate`, `worker.*`, autonomy (shadow, `approval_ttl`, spend cap), `proactive.*`, cross-team `peers`, `isolation`. Inherited team defaults show beside each field. Live YAML and validation sit beside it. | Review changes → the apply pipeline. **Define a bot** uses the same form with `shadow: true` preset. |

**The knowledge editor** (a team's repo and a bot's memory, one component).
The operator authors knowledge directly; it is git-backed.

- **Files:** a tree with New file (blank, runbook or fact template), New
  folder, Rename/move, Delete, and Upload/import: drop markdown files, paste
  a runbook, or import a path from a repo through a read connector
  (`Imported-From:` trailer).
- **Every save is one commit authored by the operator** (`Authored-By:
  operator:<name>`, `Edited-Via: dashboard`) with an optional message. No
  approval: operator provenance is trusted. It is a session operation, not a
  step-up ([§6.5](#65-step-up)).
- **Editor:** markdown with highlighting, Edit / Split / Preview (the preview
  is the safe renderer: no raw HTML, http(s) links only), an unsaved-changes
  guard on navigation, and **conflict detection**: if a bot committed to the
  file after it was opened, saving shows both diffs against the base and
  asks for an explicit choice: rebase the edit on theirs, overwrite theirs,
  or keep theirs.
- **History** per file and per repo: author (operator or bot, with a case
  link), the diff, **revert** as a new commit, and **who wrote this**
  (blame-lite: the file in sections, each with the bot or operator commit
  that wrote it).
- **Proposals** (team repos): approve (step-up), **approve with changes**
  (the proposed result opens in the editor; the commit keeps the bot as
  author with `Approved-By` and `Edited-By: operator` trailers), deny with a
  **reason that goes back to the proposing bot**, and bulk approve/deny.
- **Bot memory:** the same, plus **pins** (facts always in the bot's
  context, recorded in `.pins`) and deleting wrong facts.
- **Discoverability:** each file shows which bots read it (the team's bots,
  or the bot itself), and search spans the team's knowledge and its bots'
  memory (or a bot's memory and its team's knowledge).
- **Phone:** view, search, light edits and approvals; the panes stack.

**Create in place.** Every picker that references another entity offers
**+ New …** as its first option: team → bot, bot → team and folder, step →
connector instance, runtime, model fleet, credential binding and secret,
routing rule → bot, credential binding → secret and vault, folder pickers →
folder, and the step and trigger pickers → connector. It opens a drawer over
the current flow, which keeps its unsaved state, and the new thing is
selected on return. **One rule: the child is staged, never applied on its
own**, so parent and child apply together as one combined diff; pickers and
validation read the staged view, so the parent can use the child at once.
The one exception is a secret **value**, which is never config: it is
written to its vault immediately behind a step-up, as on the Secrets page,
and only its reference is staged. A team's **Add a bot** offers *Create a new
bot in this team*: the full New bot flow, prefilled with the team (its
defaults shown as inherited and narrowable), ending in the diff and landing
back on the team. A bot can be created solo.

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
- **Add a login** (replaces "Seed a login"). Browsers → **Add a login**
  (`#/browser/add`) offers three methods; each ends in the same **binding**
  step and then the config diff:
  - **Username & password.** Typed once into password fields (optional TOTP
    secret or `otpauth://` URI, with a "••• ••• · code works ✓" preview
    computed locally, never showing a code). On save the values go straight
    to conductor's vault (`house/login/<name>/{username,password,otp}`)
    behind a step-up `secret_write`, and the inputs are cleared. Afterwards
    the page only ever says **set**, with **Replace** and **Remove**.
  - **From a vault.** Pick the vault (1Password, pass, HashiCorp, conductor's).
    For 1Password there is an item browser with search that shows **titles,
    websites and field labels only**, never values; map username, password
    and one-time-password to fields; the item's website is suggested as the
    origin. Only references are stored (`op://Vault/Item/field`).
  - **Sign in yourself.** The takeover below: the operator signs in (SSO,
    CAPTCHA, SMS or email codes), conductor keeps only the site's cookies in
    `<state>/browser/logins/` (0700/0600), and the page notes when the
    session is expected to expire. Desktop only; on the phone the card is
    disabled with that reason and points at the vault method.
  - **Binding.** A name, the **exact origins** (scheme + host, checked
    atomically with the fill), **who may use it** (bots, teams or folders;
    a team or folder covers its bots) and **operator-started cases only**,
    on by default (#182's provenance rule). A team's credential scope still
    caps what its bots can use.
- **Test login.** Runs the login in a throwaway sandboxed browser and logs
  out again: *signed in*, *sign-in refused* (nothing saved), or *needs you*
  (a push to approve; the result updates when you do). No values or
  screenshots of filled fields are recorded.
- **Logins tab** (`#/browser/logins`). Every credential, passkey and saved
  session: method, site, bound bots/teams/folders, last used, last test,
  expiry; **Test**, **Usage** (which bot, which case, when — never a value),
  **Edit binding**, **Replace** (password logins), **Revoke** (through the
  diff; a password login's stored values are deleted from the vault when
  the revoke applies). Saved sessions revoke immediately (audited).
- **Inline.** Every credential picker (a team's credential scope, a workflow
  step's `browser.credentials`) offers **+ Add a login**, a create-in-place
  drawer with the password and vault methods; the new binding is staged with
  the parent, one diff.
- **API.** `POST /api/v1/browser/logins` (method `password` | `vault` |
  `session`; values in the body only for `password`, written to the vault and
  dropped, step-up), `POST /api/v1/browser/logins/{name}/test` (SSE result:
  `ok` | `refused` | `needs_input`), `GET /api/v1/browser/logins` (metadata
  and usage, never values), `GET /api/v1/vaults/{v}/items?q=` (titles,
  websites, field labels only).
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

- **List.** Name, `on:`, owner (your config, a pack, or a bot's
  synthesized trigger), 24h runs, success rate, spend, draft/staged state.
  Run one manually (inputs, or replay a past event), delete one (a config
  edit), or start a new one.
- **Builder.** A vertical list, top to bottom, in the order conductor runs
  it (the Pipedream model, not a free-form canvas):
  - **The trigger card** at the top: the `on:` sources (connector event,
    schedule, manual, conductor lifecycle), their filters (keys from the
    event's schema, `not_` prefixes), and grouping, budget, `callable:`.
    A reusable workflow shows its `inputs:` instead.
  - **Ordered step cards.** A collapsed card is one line: number, kind, id,
    a summary (`gh.comment · Comment on an issue or PR`), validation and test
    status. A `parallel: [[…],[…]]` step is a fork that splits into lanes,
    one per branch, each its own list, and joins before the next step.
  - **"+" between every step** (and inside each branch) opens a searchable
    picker: built-in kinds (agent, code with a language choice, decide,
    hand-off / approval, wait, parallel group, call a workflow, log, set,
    assert) and every connector's verbs from plugin declarations, grouped by
    connector, with icons and recently used. Verbs of plugins not yet
    installed are listed too, and the validator says what to add.
  - **Each step expands in place** into a form generated from the verb's or
    step kind's declared fields (required vs optional, types, enums, help).
    A verb step has a **connected-account picker** when a plugin has several
    instances (`tf-plan` / `tf-apply`). An agent step has prompt, runtime,
    model (fleet, id or wildcard), sandbox (`isolation`), workspace, skill
    verbs, gate and hand-off. A code step has an inline highlighted editor
    and a language choice. "More options" holds `id`, `if`, `for_each`,
    `timeout`, `retry`, `continue_on_error`.
  - **References to earlier data.** Any field can insert a reference to the
    trigger or an earlier step's output from a data tree, or by autocomplete
    after `{{.`. It writes conductor's own syntax: `{{.repo}}` for event
    fields, `{{.triage.kind}}` for a step's output, `ctx.triage.kind` in code.
    Only data in scope is offered: earlier top-level steps, earlier steps in
    the same branch, never a sibling branch.
  - **Test.** Pick a **real past event** from run history; *Test* runs the
    workflow up to a step as a dry run (writes aren't sent, agents are
    stubbed, `js` code really runs) and shows each step's status (ok, error,
    skipped by `if:`, not run) and **actual output**, which then fills later
    steps' reference pickers. Errors show on the step.
    (**NEW** `POST /api/v1/workflows/test`.)
  - **The YAML pane** is canonical and live in both directions: an edit in a
    form is a node-level edit ([§7.2](#72-node-level-edits)) that keeps
    comments, anchors and unknown keys; an edit in the YAML updates the list,
    and a parse error is shown at its line while the list keeps the last
    valid version. Selecting a step highlights its lines.
  - **Validation** runs on every change: unknown verbs and options, missing
    required options, unknown models and runtimes, references to a later or
    unknown step or a sibling branch, filters outside the event's schema.
    Errors sit on the field, the card and the YAML gutter.
  - **Undo/redo**, reorder by drag or Alt+↑/↓, duplicate, delete, an
    outline of step chips to jump, expand/collapse all.
  - **Save → Deploy.** Edits are an unsaved draft (kept across navigation);
    **Save** stages the change; **Deploy** opens the apply pipeline (diff →
    passkey → apply). Deploy is disabled while there are errors.
  - **Phone.** The same list-and-expand model in one column, with a
    Steps / YAML / Data switch; the picker and reference tree are sheets.
- **Pack-owned workflows.** The list shows the pack's steps, hatched;
  structure is read-only. A field edit (model, runtime, isolation, options)
  becomes an override in `packs.<i>.steps["<workflow>/<step>"]`; the YAML pane
  has **Pack source** (read-only) and **Your overrides**. Adding a step is
  refused with the supported alternative: call the pack's workflow from your
  own trigger, or fork the pack into a local `use: ./packs/…`.

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

Every screen works at 390px by touch: navigation, tabs, filters, forms
(as sheets), the builder's list mode and approvals. The flows that matter
most are overview, approvals and chat; the prototype's `phone.html` shows
them in device frames, and its test suite runs every route at 390×844.

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
  control socket use (`setPaused`, `doUpdate`, `retry`, the bot service,
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
| `bot` | ledger changes (case, proposal, worker, session), `transcript` turns | bots service hooks (#178) + paseo timeline relay (**NEW**) |
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
| `POST /api/v1/workflows/test` `{yaml, upto, event}` | dry-run up to a step against a pinned past event; per-step status, inputs, outputs | **NEW** (engine dry run: writes withheld, agents stubbed, code engines run) |
| `GET /api/v1/events?on=` | real past events for Test (from history's pinned triggers) | history records |
| `POST /api/v1/workflows/<name>/run` `{inputs}` | manual run | control `run` |
| `GET /api/v1/usage?days=&by=provider\|bot\|workflow` | usage per provider (never totalled); `usage.mode` off returns 404 | observed run/turn counts from history; `usage.source` for limits/tokens; history `CostUSD` per provider in `api` |
| `GET /api/v1/audit?event&actor&run&case&since&cursor` | audit query | `audit.jsonl` reader (**NEW** tail/index) |
| `GET /api/v1/logs?level&since` | daemon log | **NEW** log ring |

**Bots (#178)**

| Method & path | Does | Maps to |
|---|---|---|
| `GET /api/v1/bots`, `GET /api/v1/bots/<m>` | list, show | `bot ls`, `bot show` |
| `GET /api/v1/bots/<m>/{cases,proposals,workers}` | ledger views | `bot cases\|proposals\|workers` |
| `GET /api/v1/bots/<m>/proposals/<id>` | detail + **canonical approval payload** + digest; Terraform plan text and counts | `bot proposal` |
| `POST /api/v1/bots/<m>/proposals/<id>/decision` `{decision, assertion}` | approve/deny with WebAuthn | **NEW** WebAuthn verifier → `Executor.Decide` ([§6.6](#66-the-approvals-boundary)) |
| `POST /api/v1/bots/<m>/proposals/<id>/replan` | replan stale Terraform | `bot replan` |
| `POST /api/v1/bots/<m>/say` `{text}` | talk to the home session | `bot say` (requester `operator:<name>`) |
| `GET /api/v1/bots/<m>/transcript?cursor` | home conversation | **NEW** paseo timeline relay |
| `POST /api/v1/bots/<m>/{deliver,rotate,sweep,consolidate}` | as the CLI | `bot deliver\|rotate\|sweep\|consolidate` |
| `POST /api/v1/bots/<m>/cases/<c>/{note,close,reopen}` | case ops | `bot case …` |
| `POST /api/v1/bots/<m>/cases/<c>/assign` `{to}` | operator reassignment within the team; provenance kept | **NEW** (`bot assign`) |
| `POST /api/v1/bots/<m>/cases/<c>/delegate` `{to, task}` | hand a teammate (or a linked team's bot) a task; child case under the original requester | **NEW** (`bot delegate`) |
| `GET /api/v1/bots/<m>/delegations`, `DELETE …/<d>` | delegation edges (from, to, parent, child, status, result); cancel | **NEW** |
| `GET /api/v1/teams/<t>/activity?since=` | ask and delegate counts between bots, for the activity graph | **NEW** (from the ledgers) |
| `GET /api/v1/teams`, `GET /api/v1/teams/<t>` | teams, bots, effective defaults (inherited / narrowed per bot), routing, links | **NEW** teams follow-up to #178 |
| `POST /api/v1/teams/<t>/deliver` `{title, text \| event}` | route work to a team: rules, else the lead | **NEW** |
| `GET /api/v1/{teams/<t>/knowledge,bots/<m>/memory}/{tree,file,log,blame}?path&ref` | a repo: tree, a file with its version, history (per file or repo), blame-lite | `memrepo` reads + **NEW** blame |
| `POST …/commit` `{path, content, base, message}` | operator write → one commit (`Authored-By: operator`); **409** with their version and diff when `base` is stale | **NEW** operator write in `memrepo` |
| `POST …/move` `{from, to}`, `DELETE …/file?path`, `POST …/revert` `{sha}` | rename/move, delete, revert as a new commit | **NEW** |
| `POST …/import` `{files[] \| {connector, repo, path}}` | upload or import markdown → one commit | **NEW** (repo import via a read verb) |
| `PUT /api/v1/bots/<m>/memory/pins` `{paths}` | pinned facts | **NEW** |
| `GET /api/v1/teams/<t>/knowledge/proposals` | bot proposals with diffs | **NEW** |
| `POST /api/v1/teams/<t>/knowledge/proposals/decision` `{ids[], decision, assertion, content?, reason?}` | approve (optionally with edited content), or deny with a reason sent to the bot; one or many | **NEW** |
| `POST /api/v1/bots/<m>/workers/<w>/stop` | cancel a worker's run | **NEW** (run cancel) |
| `GET/POST /api/v1/bots/<m>/memory/…` | the bot's memory through the same repo endpoints as team knowledge (above) | `memrepo` |

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
4. A WebAuthn approval verifier plugged into the bots `Executor.Decide`.
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
7. Bots:
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

## 6. Auth and security

### 6.1 What we protect, and from whom

**Assets:**

- the approval decision (it moves money and infrastructure);
- config (one bad apply crash-loops the box, and loosening it widens every
  agent);
- secrets (write-only here, but writable);
- browser sessions with an operator's logins;
- bots' memory (it steers future behaviour);
- the audit trail.

**Adversaries:**

1. **A prompt-injected agent.** It may be a bot, worker, hand-off, paseo
   session or code step, and it may be sandboxed or deliberately not.
2. **Another device on the tailnet**, compromised or simply shared, for
   example a CI box or a server.
3. **A web page in the operator's browser** (CSRF, clickjacking, DNS
   rebinding).
4. **Someone with a stolen session cookie.**
5. **Other local processes on the box.**

Out of scope: root on the box, and a malicious plugin. Plugins are trusted
code today, and nothing here changes that.

### 6.2 Listeners and TLS

```yaml
dashboard:                      # optional; defaults shown
  enabled: true                 # localhost works out of the box; tailnet when tailscaled is up
  listen: [localhost, tailscale]   # ONLY these keywords — no addresses, no 0.0.0.0, no expose:
  port: 7788
  tailscale:
    cert: auto                  # LocalAPI cert for <host>.<tailnet>.ts.net; or { cert_file, key_file }
    allow_tagged: false         # refuse tailnet peers that are tagged nodes (servers, CI)
    # allow_users: [you@example.com]   # optional: WhoIs allowlist on top of the passkey
  session: { idle: 30m, max: 12h }
```

- **`localhost`**:
  - binds `127.0.0.1` and `::1`, plain HTTP;
  - `http://localhost` is a *potentially trustworthy* origin, so WebAuthn
    works there with RP ID `localhost`;
  - an IP literal can't be an RP ID (#182 hit the same rule), so the SPA
    redirects `127.0.0.1` to `localhost`;
  - this listener is the bootstrap: enrollment always starts here.
- **`tailscale`**:
  - binds only the node's Tailscale addresses (`100.x`, `fd7a:115c:a1e0::/48`),
    discovered through tailscaled's LocalAPI (`/localapi/v0/status`), and
    re-binds when they change;
  - TLS uses the MagicDNS name's certificate from the LocalAPI
    (`/localapi/v0/cert/<name>?type=pair`), which needs `tailscale set
    --operator=$USER` or a root daemon. `tailscale cert` files work too;
  - the certificate is reloaded daily through `GetCertificate`, with no
    restart;
  - RP ID is the MagicDNS name;
  - **Funnel is never used** and `tailscale serve` is not involved: the
    daemon binds the interface itself, so nothing is published.
- **Refused by validation:**
  - any other listen value;
  - an `expose:` on the dashboard;
  - a port that collides with the inbound (webhook/callable) listener;
  - `enabled: true` with `listen: [tailscale]` on a box where tailscaled is
    absent. That case logs and serves localhost only; it does not fail
    closed.

**Caveat.** Tailscale HTTPS certificates put the machine and tailnet name in
public Certificate Transparency logs. That is not an endpoint, but the name
becomes public. The doc and the Access screen say so. An operator who won't
accept that can set `listen: [localhost]` and use `ssh -L
7788:localhost:7788 box`. `http://localhost:7788` on the laptop is a secure
context, and passkeys work there.

**Peer checks on the tailnet listener:**

- `WhoIs` on every connection gives the node and user. Tagged nodes are
  refused unless `allow_tagged`, and `allow_users` narrows further.
- **Host header allowlist**: `localhost`, the MagicDNS name, and the
  Tailscale IPs. Anything else is refused, which defeats DNS rebinding.

### 6.3 Enrollment, sign-in, sessions

**First enrollment.** This is the CLI-printed one-time localhost link:

```
$ conductor dashboard enroll --operator ops
operator ops: open this on THIS machine within 10m (single use):
  http://localhost:7788/enroll#t=Hk3v…q9
pairing code: 417-209
```

- The CLI talks to the daemon over the **operator control socket**. That
  socket is never mounted into a sandbox (agents get only the skill socket).
  The CLI also refuses to run inside a conductor agent session, as `bot
  approve` does.
- The ticket is a 256-bit random value. It lives in the URL **fragment**,
  so it never reaches a log. It expires in 10m and is single use, with one
  live ticket per operator.
- The CLI **signs the ticket with the operator's SSH key** (`ssh-keygen -Y
  sign`, namespace `conductor-dashboard-enroll`). The daemon verifies the
  signature against `operators.<name>.ssh_key`, the same trust root as
  SSH-signed approvals. A process that can reach the socket but can't use
  the key, for example behind a confirming agent or a hardware key, can't
  enroll.
- The page asks for the pairing code, then registers a passkey:
  - **Default:** for the **MagicDNS RP**. The localhost page hands a
    one-time registration ticket to `https://<magicdns>:7788/enroll`, in the
    same browser or on another device by QR code. A passkey belongs to one
    RP ID, and this one works from the box and from the phone.
  - **"localhost only"** is the choice for a box without Tailscale.
- `userVerification: required` and `residentKey: preferred`. Attestation is
  `none`; we don't pretend to vet authenticators.

**Further passkeys.** Adding another passkey needs an **existing passkey
assertion** as well as the CLI ticket.

**Recovery.** `conductor dashboard reset --operator ops` (SSH-signed) removes
that operator's passkeys and sessions, audits it, and fires a lifecycle event
(`conductor.dashboard_reset`) so any connector can alert. Then first
enrollment applies again. Every enrollment fires `conductor.dashboard_enrolled`.

**Sign-in.**

- A discoverable-credential assertion (`allowCredentials` empty).
- The challenge is 32 random bytes, single use, with a 2 min TTL.
- The sign count is checked, and a regression flags a possible clone, locks
  that credential and raises a Needs-you item.

**Sessions.**

- The server keeps a revocable session store. The cookie is
  `__Host-conductor_session`, `Secure`, `HttpOnly`, `SameSite=Strict`, and is
  set per origin.
- Idle 30m, absolute 12h. Rotated on sign-in and on step-up.
- The session is bound to the listener (localhost vs tailnet) and to the
  WhoIs node on the tailnet. A cookie replayed from another tailnet node is
  refused.

**Lockout.**

- `login/begin` is rate-limited per client: 5 failed assertions per 15 min
  give a 15 min lock, doubling to a cap.
- Enrollment redeem: 3 per hour, and pairing-code failures burn the ticket.
- Every lock is audited and visible on the Access screen.

### 6.4 CSRF, clickjacking, XSS, rebinding

- **CSRF:**
  - `SameSite=Strict`;
  - every non-GET also needs `X-Conductor-CSRF` (a per-session token, double
    submit), `Content-Type: application/json`, and an `Origin` matching the
    listener's origin. `Sec-Fetch-Site` must be `same-origin` when present;
  - `GET` never mutates.
- **Clickjacking:** `Content-Security-Policy: frame-ancestors 'none'` and
  `X-Frame-Options: DENY`. The passkey ceremony can't be framed anyway.
- **XSS.** The realistic vector is **agent-authored text**: chat turns, run
  outputs, logs, memory files, proposal reasons, page titles from the agent
  browser.
  - Strict CSP: `default-src 'self'; script-src 'self'; style-src 'self';
    img-src 'self' data: blob:; connect-src 'self'; object-src 'none';
    base-uri 'none'; form-action 'none'`, plus `require-trusted-types-for
    'script'`.
  - Preact escapes by default. `dangerouslySetInnerHTML` is lint-banned
    except in one audited markdown renderer: a safe subset with no raw HTML,
    `javascript:` links stripped, and `rel="noopener noreferrer"` on links.
  - Screencast frames are blob images.
- **Rebinding:** the Host allowlist from [§6.2](#62-listeners-and-tls).
- **Other headers:** `Referrer-Policy: no-referrer`,
  `Cross-Origin-Opener-Policy: same-origin`,
  `Cross-Origin-Resource-Policy: same-origin`,
  `Permissions-Policy` that denies everything but
  `publickey-credentials-get/create`, and `Cache-Control: no-store` on
  `/api`.

### 6.5 Step-up

A session is enough for reads and for low-risk operations:

- pause/resume, retry, stop;
- chat;
- case notes;
- an operator commit to a bot's memory or a team's knowledge (reversible,
  `Authored-By: operator`).

A **fresh assertion bound to the request's digest** is required for:

- approve/deny (its own payload, [§6.6](#66-the-approvals-boundary));
- config apply and restore;
- a secret write;
- any widening (grants, verbs, tiers, promotions, browser sites, logins,
  credentials, evaluate, team approvers, ceilings and links);
- a team knowledge promotion;
- a browser takeover;
- update now;
- pack and plugin add/remove;
- enrolling another passkey.

The step-up token is single use, lives 2 min, and is bound to `{session,
purpose, sha256(request body)}`. The UI shows what you are signing before the
ceremony.

### 6.6 The approvals boundary

The approval payload is canonical and server-built. It is the same object the
CLI signs:

```json
{ "v": 1, "ns": "conductor-bot-approval",
  "bot": "platform-sre", "proposal": "p12", "decision": "approve",
  "action_sha256": "9f2c…",          // for terraform: covers repo, commit, dir, workspace,
                                     // var files, targets AND the plan's sha256 (as #178)
  "operator": "ops", "expires": "2026-10-16T10:42:00Z" }
```

1. `GET …/proposals/<id>` returns the payload, its digest `D =
   sha256(canonical JSON)` and a human summary. The summary is rendered from
   the **payload, not from agent text**. Agent-written reasons are shown
   separately, fenced as data.
2. `POST …/decision/begin` mints a nonce N, single use, 2 min, bound to the
   session and the proposal. The WebAuthn challenge is `sha256("conductor-
   dashboard-approval-v1" ‖ N ‖ D)`.
3. The browser calls `navigator.credentials.get({challenge, userVerification:
   "required"})`. The operator touches the authenticator.
4. `POST …/decision` sends `{decision, assertion}`. The daemon:
   - verifies the assertion: RP ID, origin, UP+UV flags, sign count, and a
     challenge recomputed from **its own** N and D;
   - maps the credential to an operator;
   - checks that the operator is one of **this bot's** operators;
   - calls `Executor.Decide(bot, id, decision, Approver{Operator, Method:
     "webauthn", Credential, Evidence})`.

   This is the seam #178 named ("a chat approval path … can be added later
   on the same `Executor.Decide`"). The atomic pending→approved transition,
   the shadow/expired/non-pending refusals, and "only the exact signed digest
   runs" are unchanged.
5. The audit records `bot_approval` with `method: webauthn`, the
   credential id, and the raw `authenticatorData`, `clientDataJSON` and
   signature. An approval can be re-verified offline against the stored
   public key, which is the same non-repudiation SSHSIG gives.

**Why an agent cannot approve:**

- No agent-reachable surface has a decision operation. The skill socket has
  none (#178), and the dashboard isn't reachable from a sandbox
  ([§6.7](#67-threat-model-can-an-agent-reach-the-dashboard)).
- Even a session doesn't approve: the decision endpoint needs an assertion
  made with user verification on an enrolled authenticator, over a nonce the
  daemon minted for this proposal.
- No verb, option, chat text or peer message approves anything (#178), and
  the dashboard doesn't change that.

SSH-signed approvals keep working. The two are alternative operator proofs
into one `Decide`.

### 6.7 Threat model: can an agent reach the dashboard?

The brief's premise is "sandboxed agents' network is cut". This design
**verified** it per backend against the branches, and adds two hard guards.

| Agent launch | Network as built | Reaches 127.0.0.1:7788? | Reaches the tailnet address? | Guard and test |
|---|---|---|---|---|
| Linux namespace sandbox (default for cli/acp/opencode/agent-deck, bot workers, paseo `<provider>-sandboxed`) | Own network namespace. `lo` is its own. The only way out is a unix socket to conductor's **egress proxy** (allowlist), plus model relays to fixed loopback endpoints (#179) | No: its loopback isn't the host's | No: no route | The proxy **hard-denies** the dashboard's addresses and names whatever `sandbox.egress`/`browser.allow` say (**NEW**). Validation refuses a model route or exposed port equal to the dashboard port. e2e group Z gains "agent curls both dashboard origins, direct and via the proxy → refused". |
| macOS Seatbelt | No network namespace. `(deny network*)` plus `localhost:<proxy port>`, `LoopbackPorts` (model) and `ListenPorts` (`seatbelt_agent.go`) | Only if 7788 were one of those ports, which validation forbids (**NEW**) | No (`localhost` only) | The profile has an **`Open`** verdict (`AgentNet.Open`, the host network). Any launch with `Open` is treated as *unsandboxed network*: the Isolation tab lists it, and the e2e macOS leg asserts the dashboard port is never allowed. |
| Container backend (`feat/agent-container`) | `--network none`, no capabilities, non-root. Every stream (proxy, model, socket) is muxed over `sandbox-net --mux` stdio. No socket is bind-mounted | No | No | Proxy hard-deny as above. The container CI leg (docker + rootless podman) asserts it. |
| Conductor's agent browser (#182) | Jailed Chromium with no network; egress only through the allowlist proxy, **loopback included if allowlisted** | Only through the proxy | Only through the proxy | The same proxy hard-deny stops an agent navigating its browser to the dashboard. |
| Code-step engines (`run: js` …) | Sandboxed namespace, network off by default (`engines:`) | No | No | — |
| `isolation: {mode: user}` (separate uid) | Host network | **Yes (login page only)** | Yes | No cookies (another uid can't read the operator's browser profile). Passkey required. |
| **`isolation: {mode: none}`, `sandbox.agents: off`, the paseo plain provider (hand-off opt-out)** | Host network, **the operator's uid** | **Yes** | Yes | This is the honest gap; see below. |
| Remote `hosts:` agents | Reach conductor by SSH reverse tunnel to the skill socket only. Their host may be a tailnet peer | n/a | Their **node** may | `allow_tagged: false` by default refuses tagged server nodes. Passkey required. |

**Unsandboxed agents.** Same uid, host network. #178 states the same limit
for SSH approvals: without the sandbox, the guarantee is exactly as strong as
the key's protection. The dashboard keeps that bar and doesn't lower it:

- It can reach the login page, but **every approval and every sensitive
  write needs a UV assertion** from an authenticator the agent can't drive:
  Touch ID, a security key, or a phone. The docs recommend these and warn
  against software passkeys stored in an unlocked CLI-scriptable password
  manager.
- It could read a dashboard **session cookie** out of the operator's browser
  profile on the same box. That gives reads and the low-risk operations in
  [§6.5](#65-step-up), but no approval, apply, secret write, widening or
  takeover. Bounds:
  - sessions are short;
  - a stolen localhost cookie only works on localhost;
  - the Sessions list shows every session.
- It could run `conductor dashboard enroll`. Enrollment needs the operator's
  **SSH signature** and, for an extra passkey, an existing passkey. With a
  confirming agent or a hardware key, the agent gets neither.
- The Isolation tab and the strip list every opt-out in effect, so the
  operator sees exactly where this applies.
- Possible hardening, open: refuse loopback TCP peers whose process is a
  conductor-launched agent, resolved through `/proc/net/tcp` → inode → pid →
  cgroup/env marker ([§12](#12-open-questions)).

**The other adversaries:**

- **Other local users:** the localhost listener needs a passkey. The state
  under `<state>/dashboard/` is 0700/0600.
- **Tailnet peers:** see the tagged-node rule, the WhoIs allowlist and the
  passkey.
- **Web pages:** see [§6.4](#64-csrf-clickjacking-xss-rebinding).

## 7. The config-edit pipeline

The config has crash-looped the fleet before, so every rule here is a guard
against a specific way that happened or could happen.

### 7.1 Invariants

1. **The file is the source of truth.** The dashboard holds no shadow
   config. It reads the files the daemon loaded and writes them back.
2. **Every edit is node-level.** Bytes outside the edited nodes don't
   change: comments, anchors and aliases, key order, flow vs block style,
   blank lines, and unknown keys (`x-…`).
3. **Validation happens on a copy.** `conductor validate` is **not
   read-only**: since v0.64, launching a plugin rewrites the install's
   `.path`. The live config and state dirs are never touched by a check.
4. **Nothing this binary can't load is ever written**, and the same holds
   for the pending update's binary.
5. **Every apply keeps a backup**, and rollback is one click.
6. **A failed boot restores the last good config by itself.**

### 7.2 Node-level edits

`internal/configedit` parses each file with `yaml.v3` into a `yaml.Node`
tree. It keeps the original bytes and a **source map**: node path → `{file,
start, end}`. `imports:` files, per-entry `import:` and glob imports are
followed, so an edit lands in the file that owns the node, and a new entry
goes to the file that owns its parent map.

Operations are path-addressed:

```json
{ "op": "set",    "path": "triggers[name=ci-fixer].steps[id=fix].model", "value": "claude-opus-5" }
{ "op": "insert", "path": "triggers[name=ci-fixer].steps", "after": "[id=comment]", "value": { "id": "notify", "uses": "slack-ops.post", "options": { "text": "…" } } }
{ "op": "delete", "path": "bots.platform-sre.peers[0]" }
{ "op": "move",   "path": "…", "to": "…" }
```

Paths use stable selectors (`[name=…]`, `[id=…]`, map keys), not indexes,
so a concurrent reorder can't retarget an edit.

**Byte-preserving splice.** `yaml.v3` re-encoding normalises formatting. So
only the **byte range of the changed node** is replaced:

- the range starts at the node's line and column;
- it ends before the next sibling's start, found by an indentation scan;
- the replacement keeps the node's original style where it can: a scalar
  keeps its quoting style, a flow map stays a flow map, and a trailing
  comment on the same line is kept at its column (the prototype's builder
  shows this);
- everything else stays byte-identical.

The property test is: for every file in a corpus (`config.example.yaml`,
`config.starter.yaml`, `testdata/`, e2e configs, every pack manifest), parse
plus a no-op edit is byte-identical, and a set followed by the inverse set is
byte-identical.

**Concurrency.** A change set records each touched file's `base_sha`. Apply
re-reads and refuses on mismatch ("config.yaml changed on disk since you
started; rebase?"). A **file watcher** detects external edits (vim, `pack
add`, the daemon's own re-vendoring) and raises a drift banner when the file
on disk is not the file the daemon loaded.

### 7.3 Validate on a copy

`POST /config/changes/<id>/validate` streams these checks. They are the ones
the prototype shows.

1. **Node edit.** Report the nodes changed and assert the preserved
   comments, anchors and unknown keys.
2. **Base unchanged.** The base sha still matches.
3. **Loads in this binary (in process).** `config.Load` from a temp dir that
   mirrors the candidate tree (config + imports + vendored packs + lock).
   This is the same loader the daemon boots with. It must have no side
   effects; a test asserts that by hashing the live dirs around it.
4. **`conductor validate` on a copy (subprocess).** The full check,
   including plugin launches:
   - a scratch dir gets the candidate tree and a **copy of plugin state**
     (`installed.yaml` with its absolute install paths rewritten into the
     scratch dir);
   - with a temp `HOME` and `XDG_*`, it runs `<this binary> validate
     --config scratch/config.yaml`;
   - on Linux it runs **inside conductor's own sandbox** (`internal/sandbox`
     `pivot_root`), with the live config and state dirs not mounted, so it
     *structurally* can't write them;
   - on macOS it runs under a Seatbelt profile that denies writes outside
     the scratch dir.

   This is the procedure that was verified by hand to keep live dirs
   unchanged, made mechanical.
5. **Loads in the pending update.** If the update checker holds a downloaded
   release, run check 4 with **that** binary too. A change that the next
   auto-update couldn't load is refused now, not discovered at 3am.
6. **Packs: narrow, never widen.** Pack lint on each pristine manifest (the
   existing load-time and `pack lint` checks), plus the **widening
   classification** of the operator's own change ([§7.6](#76-packs-overrides-and-narrow-never-widen)).
7. **References resolve.** Every secret, setting, fleet, connector and
   runtime the change references resolves. Secrets resolve in the daemon,
   values discarded.

Warnings show inline. Errors block apply and point at the YAML line in the
builder or raw editor.

**The longer-term fix** is `conductor validate --read-only`, which skips
plugin-dir writes. Until that exists everywhere, the copy is mandatory. Once
it does, step 4 still runs on a copy as defence in depth.

### 7.4 Apply, reload, verify

After a step-up assertion over the change's digest:

1. **Backup.** Every touched file is copied to
   `<state>/config-history/<id>/` (0600), with a manifest: file, sha before
   and after, operator, credential, summary.
2. **Journal.** `<state>/config-apply/pending.json` records `{change id,
   backup, new shas, attempts: 0}`.
3. **Write.** Each file is written to a temp file in the same directory,
   fsynced, then renamed. Multi-file changes are renamed in dependency
   order, and a failure mid-way restores from the backup before reload.
4. **Reload.** The existing path: SIGHUP → re-exec ("config is read at boot,
   so a re-exec is the reload"), with the in-flight checkpoint grace. The
   SSE stream announces `daemon.restarting`. The SPA shows "reloading" and
   resumes with `Last-Event-ID`.
5. **Boot guard** (**NEW**, in the boot path before anything dispatches). If
   `pending.json` exists:
   - **The config fails to load:** restore the backup, boot with it, raise
     `config.restored` in Needs you and fire `conductor.config_restored`.
   - **It loads:** increment `attempts`. If `attempts ≥ 2` (the service
     manager restarted us mid-window, which means a crash), restore as
     above.
6. **Health window** (60s). The daemon is up, connectors are registered,
   and nothing panicked. Then `pending.json` is removed and the change is
   **committed** in the history.

Steps 5 and 6 cover the case validation can't: a config that loads but
misbehaves at runtime badly enough to crash.

### 7.5 Rollback

**Restore** on any history entry creates a change whose candidate is the
backup's bytes, and runs the **same pipeline**. A backup taken before an
auto-update might not load in the current binary, and the rule "never apply
a config this binary can't load" applies to rollbacks too. A restore that
fails validation says why and offers the diff. The newest pre-change backup
is offered as **Roll back** on the success screen.

### 7.6 Packs: overrides and narrow-never-widen

- **Pack-owned content is read-only.** Vendored packs under
  `.conductor/packs/` are re-vendored by the daemon on its dependency cycle,
  so the dashboard never writes there.
- **Operator overrides.** The operator's changes to a pack go only into
  their `packs.<instance>` block. The builder and section editors translate
  an edit on pack-owned content into the supported override:

  | Edit on pack content | Becomes |
  |---|---|
  | a step's field (model, runtime, workspace, options, `isolation`, `gate`) | `packs.<i>.steps["<wf>/<step-id>"]` (deep-merge), or `"<wf>[<n>]"` for an id-less step |
  | arming or scoping a trigger | `packs.<i>.triggers.<name>` (or `"*"`; an array arms the same trigger N times) |
  | policy, budget | `packs.<i>.policy` |
  | a bundled fleet | `packs.<i>.models.<fleet>` |
  | required connector or secret | `packs.<i>.connectors` / `secrets` bindings |
  | anything structural (add or remove a step) | refused, with the alternatives: call `<i>/<wf>` from your own trigger, or fork into `use: ./packs/<name>` |

- **A pack can narrow, never widen.** This is enforced at load and by `pack
  lint` on the **pristine manifest**. A pack that sets `isolation: {mode:
  none}` on itself, or `user`/`container`, `privileged`, `network.egress` or
  `fs:` for its agents, is refused, as are its runtime/host and `trust:`
  settings. The error names the override to use instead. The dashboard
  surfaces these refusals verbatim in the Packs view and the pipeline.
- **Loosening is the operator's decision.** It is allowed in the operator's
  own config and overrides. The pipeline classifies a change as **widening**
  when it:
  - loosens isolation (`mode: none`, sandbox off, `privileged`);
  - adds egress or `fs:`;
  - adds a verb to a bot, loosens a tier or adds a promotion;
  - adds a browser site, login or credential, or turns `evaluate` on;
  - adds a team approver, raises a team's autonomy ceiling, drops its
    container requirement, links two teams, or makes knowledge promotion
    automatic;
  - sets `trust: full`, adds a `pack_trust` source, or makes a trigger
    `callable:`.

  A widening change gets a **widens** badge on the diff, an explicit
  acknowledgement line, and the step-up assertion (which every apply needs
  anyway).

## 8. Frontend stack

**Preact + @preact/signals + TypeScript, built by Vite.**

- **Small.** About 4 KB of runtime. The initial bundle budget is **≤ 200 KB
  gzipped**, with heavy pieces lazy-loaded:
  - CodeMirror 6 with `@codemirror/lang-yaml`, about 150 KB, only on
    config, builder and memory edit;
  - the screencast player.
- **Fits what this app is:**
  - live streams (signals update only the plates and log lines that
    change);
  - a canvas editor;
  - a phone layout.

  A server-rendered stack (`templ` + htmx) fights the canvas, the screencast
  and the offline-tolerant reconnect logic.
- **Familiar.** It is React-shaped, so a later paseo panel can reuse
  components or simply call the same API.
- **Rest of the stack:**
  - Router: a tiny path router (about 1 KB).
  - Data: `fetch` wrappers generated from the Go API types (`tygo`), plus
    one SSE client with resume.
  - Graph layout: our own. Workflows are series-parallel (order +
    `parallel:`), so layered layout is about 150 lines; no dagre or elk.
  - Charts: hand-built SVG, following the dataviz method (validated status
    palette, one axis, a table view).
  - Design system: CSS custom properties ported from the prototype's
    `app.css` tokens. No CSS framework.
  - Fonts: vendored and SIL OFL.

**Embedding.**

- Source lives in `web/`. `make web` runs `npm ci && vite build` into
  `internal/dashboard/dist/`.
- **`dist/` is committed**, so `go build` and `go install` never need Node.
  Node is a build-time dependency only, as decided.
- A CI job rebuilds and fails if `dist/` differs (reproducible from
  `package-lock.json`).
- `//go:embed dist` serves hashed assets with `immutable` caching and
  `index.html` with `no-store`.
- No CDN or runtime network fetches, so it works on an air-gapped tailnet.

## 9. Testing strategy

All tests are hermetic:

- `env -i` with a temp `HOME` and `PASEO_HOME`;
- `XDG_*`, `CONDUCTOR_*` and `CLAUDE_CONFIG_DIR` unset;
- live installs hashed before and after;
- throwaway daemons on their own ports. A fresh paseo home would otherwise
  bind the live port 6767.

These are the rules the bot and sandbox PRs already follow.

- **Go unit tests.** Every handler against fakes of the bot service, the
  browser manager, the store and the update checker:
  - auth middleware order (Host → Origin → session → CSRF → step-up);
  - the SSE ring and resume;
  - the attention aggregator.
- **WebAuthn.** Registration and assertion against a software authenticator
  in tests (`go-webauthn` test vectors), plus **Chromium's CDP virtual
  authenticator** in e2e (#182 already drives it). Negative cases:
  - wrong RP or origin;
  - UV not set;
  - a replayed challenge;
  - the sign count going backwards;
  - a credential of an operator who isn't this bot's;
  - a session with no assertion;
  - a nonce minted for p11 used on p12;
  - an expired proposal.
- **Mutation testing** of the approval verifier and step-up, as with #178:
  gremlins plus a guard-deletion pass. Every refusal guard must be killed.
- **`configedit`.**
  - Corpus round-trip byte identity.
  - Fuzzing (`go test -fuzz`) over random edits that asserts every
    untouched byte range is identical and the result parses.
  - Golden diffs for each editor (bot definition, builder ops, pack
    overrides).
  - Widening-classification table tests.
- **Validate-on-copy.** A test hashes the live config and state dirs (and
  the plugin install's `.path`) around a full validate run, on Linux and
  macOS CI.
- **Boot guard.** e2e:
  - apply a config that fails to load (by faking past validation) → the
    daemon boots on the backup and raises `config.restored`;
  - a config that crashes after boot → restored on the second attempt.
- **Security e2e:**
  - CSRF: a cross-origin form post and a missing header are refused;
  - `frame-ancestors`;
  - a rebinding Host is refused;
  - a tagged-node WhoIs is refused (faked LocalAPI);
  - **an agent reaching the dashboard**, per backend (group Z namespace,
    the macOS Seatbelt leg, the container legs): a direct curl to both
    origins and a request through the egress proxy fail;
  - the browser can't navigate to the dashboard.
- **Frontend.**
  - vitest for stores, the router, the SSE client and the layout.
  - Playwright e2e against a throwaway daemon with fixture state: every
    screen, the approve flow with the virtual authenticator, a config apply
    and rollback, a builder edit round-trip, the phone viewport.
  - Accessibility: axe on every route, keyboard-only approve.
  - Visual regression: screenshots of every route × {dark, light} ×
    {desktop, phone}.
  - XSS corpus: agent-authored strings with HTML, markdown and
    `javascript:` links render inert.
- **Live proof** (as #178/#179 do). Done with the stack up, temp `HOME`, on
  a tailnet node: enroll, sign in from a phone, approve a Terraform proposal
  end to end, take over a browser, apply and roll back a config.

## 10. Build plan

One feature branch, **`feat/dashboard`**, built by parallel builders who each
own a disjoint set of files. A builder merges into the feature branch through
small PRs. The branch merges to `main` when [§9](#9-testing-strategy)'s gates
pass. Bots and browser work starts after #178 and #182 (and #179) land on
`main`.

**Phase 0: the contract** (one builder, about 2 days, merged first).

- `internal/dashboard/api`: Go request and response types for every
  endpoint in [§5](#5-api), the route table with stub handlers returning
  fixtures, and the SSE envelope and topics.
- TypeScript types generated with `tygo` into `web/src/api/`.
- The `dashboard:` config block, its validation, and an off-by-default flag
  until Phase 2.
- The design tokens: `web/src/styles/tokens.css`, ported from the
  prototype.

Every other builder codes against the stubs, so the backend and the frontend
proceed in parallel.

**Phase 1: parallel builders**

| Builder | Owns | Delivers | Depends on |
|---|---|---|---|
| **B1 Server & events** | `internal/dashboard/{server,events,observe}`, history index, log ring, audit tap, step-log tap, run cancel, attention aggregator | listeners (localhost + Tailscale LocalAPI bind and cert), Host/WhoIs checks, SPA serving, SSE hub with resume, the read endpoints for status, runs, audit and logs, stop/retry/run | Phase 0 |
| **B2 Auth** | `internal/dashboard/auth`, `cmd/conductor/dashboard.go` | WebAuthn sign-in, sessions, CSRF, lockout, step-up, enrollment (SSH-signed ticket, MagicDNS hand-off), reset, the Access API, the proxy hard-deny + validation guards, the security e2e | Phase 0 |
| **B3 Frontend shell** | `web/` app shell, router, SSE client, design system, auth screens, phone layout, `make web`, `dist/` embed + CI check | the app chrome (strip, rail, tab bar, ⌘K, Needs you), sign-in/enroll UI, overview | Phase 0 |
| **B4 Runs** | `web/src/routes/{live,runs,attention}`, run graph derivation (`GET /workflows/<n>/graph`) | live run view (graph, streaming log, inspector), history, detail, retry/stop | B1 stubs |
| **B5 Config pipeline** | `internal/configedit`, `web/src/routes/config`, `…/secrets` | node edits, splices, source map, validate-on-copy runner, journal + boot guard, history/restore, config UI, secrets & vars UI, vault write API, used-by index | Phase 0 |
| **B6 Builder** | `web/src/routes/workflows`, `web/src/canvas/` | canvas, inspector, CodeMirror YAML pane with selection sync, pack override translation, inline validation | B5's op API (contract first, so they start together) |
| **B7 Ops** | `web/src/routes/{extensions,updates,connectors,logs}`, their handlers | packs/plugins (plan, add/update/hold/remove via B5), updates over `doUpdate`, connectors/runtimes/hosts/isolation, logs and audit UI | B1, B5 |

**Phase 2: in-flight features** (after their PRs merge to `main`)

| Builder | Owns | Delivers |
|---|---|---|
| **B8 Bots & teams** | `internal/dashboard/api/{bots,teams}.go`, the WebAuthn → `Executor.Decide` verifier (with B2), transcript relay, memrepo operator commit/revert, worker stop, team routing/knowledge endpoints, `web/src/routes/{bots,teams}` | all bot tabs, team pages (routing, defaults with inherit/narrow, knowledge approvals), proposal + Terraform view, approve/deny, definition editor (over B5) |
| **B9 Browser** | screencast relay, takeover lease, headless seed/passkey-register, `web/src/routes/browser` | live view, takeover, seed, passkey registration, per-identity settings (over B5) |

**Integration checkpoints:**

- **End of Phase 1:** the dashboard runs behind the flag, with read-only
  screens plus runs and config apply. Run the security e2e, then turn the
  flag on for localhost.
- **After B8:** the approval path is mutation-tested and proven live, and
  the tailnet listener is on by default.
- **After B9:** the takeover live proof.

**Rules for builders:**

- stay inside your owned files; changes to shared files (the route table,
  tokens) go through the Phase 0 owner;
- hermetic tests only;
- never touch the live service, config or state;
- every PR carries screenshots of the screens it changes, in both themes,
  on desktop and phone.

## 11. Rollout

1. **Ship behind `dashboard.enabled: false`.** Then enable it with
   `listen: [localhost]`, and add `tailscale` once B2's security e2e and the
   live proof pass.
2. `conductor status` gains a line (`dashboard: https://<magicdns>:7788 ·
   2 passkeys`). `conductor dashboard status` prints the URLs and the
   enrollment state.
3. **Docs:**
   - `docs/wiki/Dashboard.md` covers operators: enroll, phone, approvals,
     config editing, rollback, and the CT-log caveat;
   - the Isolation page links the unsandboxed-agent caveat;
   - Bots and Browser get "in the dashboard" sections.

## 12. Decisions (formerly open questions)

1. **Loopback peer attribution: yes.** On Linux the localhost listener refuses
   TCP peers whose process is a conductor-launched agent (resolved through
   `/proc`, with cgroup and env markers). It's heuristic and Linux-only:
   defense in depth for `mode: none` agents, not the primary boundary (that
   remains the passkey requirement on every approval and sensitive write).
2. **Enrollment always requires the operator's SSH signature.** No
   localhost-link-only mode, including on boxes without bots.
3. **Per-step log store: configurable,** default 1 MiB per step, persisted
   next to the history record and pruned with `history_retention`.
4. **Chat transcript: conductor records it itself,** from the inbox it
   delivers plus the agent's turns. No dependence on paseo's agent timeline
   API (conductor's paseo integration stays on public surfaces only).
5. **Device-bound approvals: offered as a per-operator policy, off by
   default.** Synced passkeys (iCloud, Google) work for approvals out of the
   box; an operator can require a device-bound key for approvals on the
   Access screen.

## 13. The prototype

Path: `design/dashboard-prototype/`. It is static HTML, CSS and JS with fake
data and no build step. Fonts are vendored, so it works offline.

```sh
python3 -m http.server 8767 --bind 127.0.0.1 --directory design/dashboard-prototype
# desktop:  http://127.0.0.1:8767/
# phones:   http://127.0.0.1:8767/phone.html
# light:    http://127.0.0.1:8767/?theme=light
```

Opening `index.html` straight from the file system also works: classic
scripts, no modules.

**Every control works** against fake state kept in `localStorage`, so it
survives navigation and reloads. **Reset demo data** (rail footer, Config,
More) puts the seed back. Config edits from every screen stage into the
real pipeline: the diff is computed from the generated `config.yaml`,
validation runs real checks (missing fields, unresolved references, team
narrowing, widening), and apply needs a (simulated) passkey and writes a
backup you can restore. Things to try:

- **Builder:** Workflows → New workflow → choose a trigger → **+** → search
  "label". Open `ci-fixer`, pick a test event, **Test** `triage`, then insert
  `{{.triage.kind}}` into `fix`'s prompt from the data tree. Type `gpt-x` as a
  model to see inline validation; edit the YAML and watch the list follow;
  Ctrl+Z.
- **Folders:** Bots → New folder; drag `finance` onto `personal`, or
  **Move to…**; filter Needs you by folder; on a phone, drill down.
- **Create in place:** on team `platform`, *Add a bot* → *Create a new bot
  in this team*. In a workflow step, the account picker → *New connector*.
- **Knowledge:** team `platform` → Knowledge: New file, edit, Split
  preview, Save; *Approve with changes* on `k4`; *Simulate a bot commit*
  while editing to see a conflict.
- **Teams:** Bots → `platform` → Defaults (inherits / narrows),
  Routing (try a rule), Route work here, Knowledge (approve a promotion).
  Add `release-scribe` to `finance` to see a narrowing refusal.
- **Delegation:** `platform-sre` → case `c29` shows its delegation edges;
  open `web-triage`'s child case `c36`, add a note and close it to report
  the result back. **Delegate a task…** from a case; try a bot in an
  unlinked team. The team page shows the ask & delegate graph.
- **Connectors & hosts:** add a connector; its form comes from the plugin's
  declared settings.
- **Approve with passkey:** Needs you → *Review* → `p12` → **Approve**.
- **Secrets:** filter, store a value (write-only), change a ref.
- **Browser:** **Take over**, Esc to release; seed a login.
- **Search:** ⌘K. **Theme:** the rail footer.

**Interaction tests** live in `design/dashboard-tests/` (Playwright; see its
README). They visit every route at 1440×900 and 390×844 and click every
control, failing on any that changes nothing and isn't disabled with a
reason; run every create/edit/delete flow through diff and apply; drive the
builder and the team flows; and fail on any console error.

The prototype is a design artefact. Its code is not the product's code, and
its data shapes follow [§5](#5-api) only loosely.
