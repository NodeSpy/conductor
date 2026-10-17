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

The approved designs are the spec (`design/CONFORMANCE.md` says, screen by screen, which approved source each follows).
The rail keeps the everyday destinations; the rarely visited system pages share one item.

```
Home · Needs you · Activity · Workflows · Bots · Browsers
Settings   Connections · Secrets · Packs · Safety · System (Config · Updates · Logs & audit · Access)
```

Some elements are on every screen:

- **Top bar** (60px). Search (⌘K); then how things are, as a status mark and a word ("All systems normal",
  "Something needs a look", "Dispatch paused", "An update needs you"); "N not applied" when config changes are staged;
  the avatar (theme, display preferences, glossary, the prototype's demo data, Lock). The status menu holds Pause or
  Resume dispatch and, quietly, an available update ("v0.72.0 installs itself within 10 min").
- **Rail** (left, full height). The brand on top, the navigation, the host card at the foot; only Needs you carries a
  hot count. It folds to icons below 1100px; below 720px it becomes a **tab bar**: Home, Needs you, Activity, Bots, More.
- **⌘K palette**. Jump to any run, bot, workflow or setting, or run an action such as pausing dispatch.
- **Needs you**. One queue of everything that waits on a person: pending approvals, failed runs, questions and
  hand-off drafts, browser push approvals.

### 2.2 Routes

| Route | Screen |
|---|---|
| `#/overview` | Home |
| `#/activity[?status=&wf=&bot=&repo=&since=&q=]` | Activity: every run, grouped by who needs to act, with filters, a time range and search (Run history merged in) |
| `#/runs/<id>`, `#/runs/<id>/compare/<other>` | A run (running or finished), and two runs compared |
| `#/runs`, `#/live`, `#/live/<id>` | Old links: redirect to Activity (with their filters) or to the run |
| `#/attention` | Needs you |
| `#/workflows`, `#/workflows/<name>` | Workflows, and the builder (Recipe, Config file, Runs, Settings) |
| `#/bots`, `#/folders/<path>` | Bots: the folder tree |
| `#/bots/<m>/<tab>[/<id>]`, `#/bots/new` | A bot. Tabs: approvals, chat, cases, memory, what it can do, settings |
| `#/teams/<t>[/<tab>]` | A team |
| `#/browser`, `#/browser/<id>`, `#/browser/logins` | Browsers and logins |
| `#/connectors/<connectors\|runtimes\|hosts>` | Connections |
| `#/secrets` | Secrets |
| `#/extensions/<packs\|plugins\|browse>`, `#/extensions/pack/<name>`, `#/extensions/source` | Packs: installed, the catalog, a pack's overrides, add from a source |
| `#/safety` | Safety: the sandbox and what each bot may do |
| `#/config`, `#/updates`, `#/logs/<daemon\|audit>`, `#/security` | System: Config, Updates, Logs & audit, Access |
| `/signin`, `/enroll` | Sign-in, enrolment |

The product uses path routes. The prototype uses hash routes so it can be
opened from a file.

## 3. Visual language

The dashboard uses **Daylight**, conductor's brand and design system (`design/brand/` on `origin/design/brand`:
`BRAND.md`, the brand book `system.html`, `tokens/tokens.json` → `tokens.css`). Warm, light-first, and written to
be read: conductor tells you how things are going in a sentence before it shows you a table. This section is how
the dashboard applies it; the brand book is the source of truth for values.

### 3.0 The approved designs are the spec

Every screen follows an approved source: Daylight's mocks (`design/brand/v2/`: Home, the failed run, the bot page with
an approval, run history, the builder), the status column as chosen (`design/brand/explore/status-column.html`: D's
grouping with A's margin marks, every row drawn), the brand book, and Home as approved in review. A screen without a
mock uses only brand book components, in the approved screens' patterns. `design/CONFORMANCE.md` records, screen by
screen, the source, the deliberate differences and why, and the approved mock beside the prototype.

The judgment rule for everything else: every element earns its place; the obvious isn't explained; fewer, well-chosen
signals. Concretely:

1. **What needs someone comes first**, as a plain sentence and one action; everything else is quieter.
2. **Aggregate the normal, list the exceptions.** Views stay readable at any scale: a runs-per-hour strip (the same
   size for ten workflows or five hundred), the problems listed under it, the rest a count or a paged group. No view
   grows one row per workflow or bot by design.
3. **Colour only for status that needs a person or is live**, through the one status vocabulary.
4. **Plain words first; technical detail one step down** (a "Details" fold, a row that opens, a ⋯ menu).
5. **No decoration derived from the logo**, no capsules, no row tints, no meters.

### 3.1 Tokens, type and shape
### 3.1 Tokens, type and shape

- **Tokens only.** `css/tokens.css` is a verbatim copy of the brand's generated file; every colour, size, radius,
  shadow and duration in the prototype is a semantic or component token (`--color-*`, `--type-*`, `--size-*`,
  `--pad-*`, `--radius-*`, `--shadow-*`, `--motion-*`). No raw colours anywhere else (`daylight.spec.js` fails on
  one). Accent as text uses `--color-text-accent`; `--color-accent-default` is for fills only.
- **Themes.** `index.html` sets `data-theme` before paint: an explicit choice wins, otherwise the OS, live.
  `color-scheme` is declared so in-app WebViews never auto-darken the page.
- **Type.** Three faces, all SIL OFL and self-hosted (`fonts/`): **Atkinson Hyperlegible Next** for everything
  people read (body 15px, `--type-body-m`), **Fraunces** (upright, SemiBold, soft) for short human titles only
  (five words or fewer, 20px or more: page titles and card titles; a generated title, such as a run's or a case's,
  is in the sans), and **Atkinson Hyperlegible Mono**
  only for literal code, ids, YAML and logs (12px or more). Generated titles (runs, cases, proposals) are `h3` in
  the sans with any machine detail on a mono line below. Nothing renders under 11.5px; tables use tabular figures.
- **Shape.** 10px controls, 14px cards, 18px overlays, 22px phone sheets; warm soft shadows in light, lighter
  surfaces in dark. No film grain or dot grid on app surfaces.
- **Icons.** Lucide (ISC, `assets/icons/`) at a 1.75 stroke, one icon per concept; Daylight's own status glyphs
  (`UI.glyph`), each a distinct shape so they read in one colour.
- **Density.** Comfortable by default (52px rows); Compact (36px rows) is a per-browser display preference wired to
  Daylight's density tokens (`data-density` on `<html>`). Phones are always comfortable (44px targets).
- **Motion.** One quiet rise on page entry; drawers and sheets from their edge; the running arc spins; nothing moves
  after an approval. `prefers-reduced-motion` stops it all.
- **The logo** is one swappable asset: `assets/logo/` holds a complete Daylight logo kit (today Fermata,
  strengthened) and `logo.js`, the one reference. The top bar, phone header, sign-in and enrolment draw the kit's
  `mark-inline.html` beside the live-text wordmark (Fraunces); `index.html` links the kit's favicon set,
  apple-touch icon and `manifest.webmanifest` (PWA icons). Swapping the logo is copying another kit's files over
  the folder.
- **The logo is never altered**, decorated, animated or turned into an illustration or motif; it appears only as the
  logo, in its approved lockups. Empty states are a plain, useful sentence and, when there is one, an action.

### 3.2 Shared components

Repeated patterns are one component each, restyled once and used everywhere; screens compose them and don't
restyle them (`css/app.css`, `css/components.css`, helpers in `js/ui.js`).

- **The status vocabulary** (`UI.statusMark`, `UI.mark`; `css/marks.css`), exactly as the brand's
  `status/marks.js`: a 12px mark plus a word. The mark always carries its state colour; the word is coloured only when
  a person is needed or the thing is live (failed, needs you, waiting, running, shadow), and bold when a person is
  needed. Passed is a quiet sage dot with an ink word; parked is slate pause bars; not reached is a dash; needs you is
  an ember diamond; running is a blue arc that turns. No capsules, no row tints. Every status on every screen goes
  through it, and each state has one look across the app (`status.spec.js`).
- **Rows** (Home's row, the model for every list): the mark · a plain title with the reason or detail under it · the
  age · one action, and a ⋯ menu for the rest. A row that needs someone gets a 3px leading edge in its state's colour
  and its reason in that colour under the name; nothing else in a list is coloured. Long values wrap inside their
  column; actions never cover text (`sweep.spec.js`).
- **Grouped run table** (Activity): "Needs attention · N" (its count in the accent), "Running now", "Finished" (paged),
  as on the approved status column; the phone shows the same groups as lists.
- **The selection bar**, one pattern for every list with bulk select: while anything is checked it replaces the
  list's header row in place and stays pinned at the top while you scroll: "3 selected", the actions that apply,
  "Select all N matching", Clear. Config changes still become one diff; destructive actions still confirm.
- **Labelled facts** (`UI.facts`, the run page's Details card): label, then value; ids and times never bare, durations
  in words ("2 h 30 min").
- **Risk, in words, only where it informs a decision**: inside approval content ("Where: Production · workspace
  prod", "Touches production", "Spends money"). There is no risk meter or ladder (the brand's proposal is pending
  review), and nothing on the operator's own controls.
- **Tooltips** only where they add something missing: an icon-only button's name, the full text of something
  truncated, why a control can't be used, a glossary term's meaning. Always anchored to their element; never a copy of
  what the row already shows.
- **Page header** (`UI.head`): the title and the page's actions. A sentence under the title earns its place only by
  saying something the page doesn't: a rule (nothing applies until you review it; values are write-only), a problem's
  consequence (a vault that can't be reached), a one-line definition of a concept, a run's story. A sentence that counts
  or restates the rows directly below it ("4 logins bots may use", "All 7 connectors are healthy") is cut: list pages
  open on their rows. (`home.spec.js` checks the list pages.)
- **Choice cards** (`UI.choice`): a neutral icon, title over description, a radio on the right; the selected card
  takes the accent edge and wash; a disabled card says why. The choice made carries into the next step (`UI.chosen`).
- **Wizard and stepper** (`UI.stepper`, the one implementation everywhere): at most five steps named for the
  decision; done is an ink check, current an ember ring; Back keeps input; the review lives inside the wizard. The
  labelled steps show with fixed gaps (no connector runs through text) only when they fit; otherwise the stepper is
  "Step 2 of 5 · Watches" over a thin bar. It never wraps, clips or overlaps (`wizard.spec.js` walks every wizard's
  states at 1440 and 390 in Chromium, and in WebKit on the iPhone).
- **The passkey glyph means "this signs".** It appears only on a control that starts a WebAuthn signing (Approve,
  Apply for a widening change, sign in, enrol, the signing sheet's own button) and on the passkeys themselves.
  Opening, reviewing and navigating never carry it; "your passkey signs this" is said in the signing sheet, at the
  point of signing (`passkey.spec.js`).
- **Empty states** (`UI.empty`): a short human title, why it's empty, the one next step. No illustration.
- **Toasts** (`UI.toast`): past tense and specific, a status glyph, 6 seconds (errors stay until dismissed), above
  the phone tab bar. **Dialogs** for decisions, **drawers** for create-in-place beside a list (layered over a dialog
  when needed), **sheets** on the phone.
- **Code, YAML and diffs**: the only long mono, on the sunken surface; no space before a colon; diffs carry +/−
  gutter marks and a 2px bar besides the tint; the header names the file and the enclosing key.

#### The value-source field

Every configurable value (connector instance settings, plugin config, env
vars, settings, workflow step inputs, bot and team settings that take
secrets, login credentials) uses one field, `type: 'value'` (`'secret'` is
the same field with `sensitive: true`). The source is a quiet menu inside the
field (Daylight), not a segmented control in front of it: for a sensitive field
**Secret** (typed, stored encrypted in conductor's vault), **Vault item** and
**Environment variable** are listed, and **Setting** sits under "Other
sources"; for a plain field, typed value and setting are the other sources. The
sources:

| Source | Writes | Notes |
|---|---|---|
| **Typed** | the literal | For a sensitive field the input is write-only: on save the value goes to conductor's vault (`house`) behind a step-up, and the field holds `{{ vault "house" "<key>" }}`. |
| **Env var** | `${env.NAME}` | Picker of known env vars across scopes; **+ Add env var** inline (staged; daemon scope goes to `conductor.env` in the same review). A sensitive field fed by a var that is plain text in `conductor.env` shows a warning suggesting a vault entry. |
| **Vault** | `{{ vault "<vault>" "<key>" }}` | **The one reference syntax** (Secrets.md). A picker of known entries (`<vault> · <key>`), a browser of vault items that shows titles, websites and field **labels** only (1Password one-time codes are `<item>/one-time password?attribute=otp`), and **+ New vault entry** inline (written to a writable vault at once, behind a step-up). When another field in the form came from a 1Password item, the field offers **Use the same item** shortcuts. The retired `op://` scheme and the named `secrets:` block are never written; an unmigrated `op://` reference shows as failing. |
| **Setting** | `${settings.name}` | Non-sensitive fields only. A sensitive field set to a setting is **blocked** with the reason: settings are plain config, in `config.yaml`, every diff and every backup. |

A **secret is a vault entry**, known by `<vault>/<key>`; the Secrets page
lists entries with their canonical reference, where they're used, and an
optional **alias that is dashboard-only** (config never sees it). Broker
grants (`skill.allow_secrets`) take `<vault>/<key>` entries and need
`skill.secrets_via: broker`, which the agent form shows beside them.
**Browser credentials** name each value as `{ vault, key }` (the
`browser.credentials` schema), so a login's username, password and one-time
code offer only **Typed** (stored in `house`) and **Vault**; Env and Setting
aren't offered there.

Sources mix per field (username typed, password from 1Password, TOTP from the
same item's OTP field). The field always shows the **reference**, never the
value, with a check: *checking…*, *resolves ✓*, or *doesn't resolve ✗* with
the reason (no such item, vault unreachable, no such entry, retired syntax).

API it implies (**NEW**):

- `POST /api/v1/refs/resolve` `{ ref }` → `{ ok: true }` or
  `{ ok: false, error }`. The daemon resolves the reference the way a run
  would, then drops the value; nothing but ok/error comes back. Rate-limited
  and audited (`ref_resolve`, the reference only).
- Metadata-only pickers: `GET /api/v1/env?names=1` (names and scopes, plus
  `plain: true` for a sensitive-looking plain var), `GET /api/v1/secrets`
  (names, vaults, usage), `GET /api/v1/vaults/{v}/items?q=` (titles,
  websites, field labels and types; never values).
- Typed sensitive values are only ever accepted by the step-up-gated
  `POST /api/v1/vaults/{v}/secrets` write, which returns the new reference.

## 4. Screens

Each screen below lists its purpose, its data (with the API from
[§5](#5-api)) and its operations. **NEW** marks something the daemon doesn't
have today.

### 4.1 Home (the Overview)

The purpose is to answer, in under five seconds: is it healthy, what is it doing, and what needs me? Home as it
stands is approved (`design/CONFORMANCE.md`); it is the model for the other screens.

In reading order:

No greeting and no summary sentence: they restated the tiles and the list below (and "everything else is running
normally" sat beside failed runs), so the page opens on the figures, with Needs you directly under them. The page's h1
("Home") is for screen readers; the rail and the phone header already name the page.

1. ~~The greeting and one sentence~~ (removed in review).
2. **At a glance**: four tiles. Running (links to Activity filtered to running; the longest-running time, no list of
   names), Runs in the last 12 hours, Approvals waiting, Bots.
3. **Needs you**: the mark, a plain title, why, the machine detail last, the age, one verb (Review is primary for
   approvals); See all.
4. Beside it: **Running now** (at most three, longest-running first, then "+N more running →"; hidden when nothing
   runs), **Last 12 hours** (the figure, runs per hour with failures stacked in the danger colour, the failures named),
   **For your information** (an update that installs itself, a sandbox block).
5. **Get started**, only while something is left: the remaining steps, the next one as the primary button, what's done
   in one quiet line.

| Panel | Data | Source today |
|---|---|---|
| Needs you | `GET /api/v1/attention` | Bots ledger (pending proposals), run history (failed), audit (`escalate`/`needs_input`, which `status` already reads), hand-off drafts (web connector), backoff/hold (`state.json`), browser `needs_input`, update checker. Merging them is **NEW** (`attention` aggregator). |
| Running / runs / approvals (or model usage) / bots tiles | `GET /api/v1/status`, `GET /api/v1/usage` when `usage.mode` is not `off` | `status.go` (live agents, in-flight workflows from `runs.json`); usage per provider ([§4.1.1](#411-usage-display-usagemode)) |
| Last 12 hours (runs per hour) | `GET /api/v1/runs?since=12h&fields=started,status` | History records, bucketed per hour |
| Health | `GET /api/v1/status` | Version, pid, uptime, config sha, sandbox registry (identities, containers), paseo provider-entry merge result (#179), connector registry health, update state |
| Activity | SSE `run`, `audit`, `bot`, `browser` topics | `flow.EventHub` (`RunEvent`), audit writer tap (**NEW**) |
| Workflows (off) or Model usage (subscription/api) | `GET /api/v1/workflows`; `GET /api/v1/usage?days=14` | One card per provider, never a combined chart ([§4.1.1](#411-usage-display-usagemode)) |

Operations (not on the Overview itself, which leads with the answer and what needs you):

- **Pause/Resume dispatch**: `setPaused`, as `conductor pause` does. It lives in the status menu (the health word
  in the top bar): it says what pausing does, asks first, and while paused the top bar reads "Dispatch paused" and
  the status menu offers Resume.
- **Run a workflow**: control `run`, from the Workflows page (each workflow's Run) and ⌘K.

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

### 4.2 Activity (Run history merged in)

Run history duplicated Activity, so they are one page (`#/activity`). It answers "what ran, what's running, and what
needs me?", as far back as you need.

- **The sentence**: "Last 12 hours: 15 runs. 3 need attention, 3 are running and 9 finished."
- **Filters on the page**: search (repo, PR, run id, error), status, workflow, bot, folder, repo, and the time range
  (last hour, 12 hours, 24 hours, 7 days, all time). Old `#/runs?…` links land here with the same filters.
- **Runs per hour** for the range: the Overview's strip, failures stacked and named. It is the same size for ten
  workflows or five hundred (the per-workflow lanes were removed: they grow with every workflow).
- **The grouped table**, as on the approved status column: **Needs attention · N** (failed runs and runs waiting on
  a person: the reason in its state's colour under the name, a leading edge, one action: Open, or Answer), **Running
  now**, **Finished** (paged: "Show 20 more of N"). Columns: Run (workflow and target), Status, When, Took, ID (and
  cost when usage display is on). On a phone the same groups are lists.
- **Selection**: a checkbox per row; while anything is checked the header row is the selection bar (Retry…, Stop…,
  Select all N matching, Clear).
- Records are kept 14 days; each is signed, and one that fails its signature shows as tampered. Listing more than
  ~500 cheaply needs a small **NEW** index (`history/index.jsonl`); the list pages by cursor (`GET /runs?cursor=`).

### 4.3 A run

One page (`#/runs/<id>`) for a running run and a finished one, as the approved run mock.

- **Back to Activity**, which keeps Activity's filters and scroll position. No run tabs, no previous/next.
- **The head**: the plain title ("Couldn't fix a failing check on acme/web#1288"), what happened in a sentence, and the
  status mark on the right; then the actions for the whole run: Retry (from the failed step), Open the pull request,
  Compare…, Dismiss; on a running run, **Stop run…** (confirmed: "It stops mid-step; anything already pushed stays.").
- **What happened**: the step tree (number, mark, what the step did, how long; a nested workflow call is one node you
  open; the failing path opens by itself). Every step selects it.
- **The selected step**: its outputs, inputs, log and diff (the tabs it recorded); a running step streams its log and,
  only when it has a live paseo session (an agent on a paseo runtime, a hand-off, a bot's home session), offers
  **Open in paseo**: "Watch or take over this agent's conversation."
- **Details**: Started (with the clock), Took or Running for, Triggered by in words, Workflow, Run, Retry of, the
  signature; Download the run record. **Audit** for this run under it.
- **Compare** (`#/runs/<id>/compare/<other>`): this run and the one compared with, step by step with human durations
  and the difference, then the chosen step's outputs as a diff.
- Operations: Stop run (**NEW** cancel through the engine's context, with a reload checkpoint's grace); Retry from a
  step (control `retry {run_id, from_step, force_replay}`).

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
    folder: work/platform                # organization only
    operators: [ops, oncall]             # who may approve the team's bots
    peers: team                          # teammates may ask each other (or none)
    delegation: { max_depth: 2 }
    autonomy:
      ceiling: reversible                # the most a bot may be promoted to (or observe)
      tiers: { propose: [terraform.apply] }   # only ever stricter than a bot's own
    budget:
      spend_cap: { per_period: 200, period: month, currency: USD }
      proactive: { max_runs_per_day: 10 }     # a shared sweep pool
    isolation: { sandbox: required }
    credentials: { secrets_via: broker, allow_secrets: [op/Agents/Grafana/password] }
    browser: { logins: [console.example.com], credentials: [grafana, statuspage] }   # scope
    knowledge: { repo: team-knowledge/platform, promotion: proposal }
    collections:
      postmortems: { from: .conductor/packs/sre-bot/collections/postmortems.yaml }
    routing:                             # first matching rule wins; then the lead triages; then the fallback
      rules:
        - { name: pd-api, match: { on: pd.incident, service: [api, web] }, to: platform-sre }
        - { name: web-issues, match: { on: gh.issue_opened, repo: [acme/web] }, to: web-triage }
      lead: platform-sre
      fallback: platform-sre
bot_links: [[platform-sre, billing-sre]]   # a pair of bots on different teams may talk
bots:
  platform-sre: { team: platform, … }  # at most one team, or none
  release-scribe: { … }                # a solo bot
triggers:
  - name: incident-intake
    on: pd.incident
    steps:
      - id: page
        bot: { team: platform }        # the team's rules pick the bot; no match → the lead, then the fallback
        title: "{{.title}}"
      # - { bot: platform-sre, title: … }   # or straight to one bot
```

- **Narrow, never widen.** A bot's own setting must sit inside the team's:
  a subset of operators, no tier looser than the team's classification, no
  promotion above `autonomy.ceiling` (none at `observe`), a sandbox where the
  team requires one, sweep budgets inside the shared pool, credentials and
  logins inside the team's scope. Validation refuses anything wider, naming the default it
  breaks.
- **Peers.** Bots of one team are peers by default (`peers: team`; data-only
  `ask`). Across teams they're isolated unless a **pair of bots** is linked:
  both list each other under `peers`, or an operator `bot_links` entry names
  the pair. There are no team-to-team links.
- **Delegation.** A bot can hand a teammate a task (`conductor bot
  delegate <bot> --case <c> --to <teammate> "<task>"`). The recipient works
  it as a child case **under the original requester's provenance**, never the
  delegating bot's, and its result reports back to the parent case. Asks
  stay data-only; delegation carries a task. Across teams, delegation exists
  only between linked bots; `delegation.max_depth` bounds the chain. The ledger records each delegation as an edge (`d<n>`: from, to,
  parent case, child case, status, result).
- **Knowledge.** An optional shared repo every bot reads
  (`knowledge.repo`). A bot proposes promoting a file from its own memory;
  with `promotion: proposal` (the default) the operator approves it (a
  step-up assertion), which writes one commit with `Promoted-By` and
  `Approved-By` trailers; `direct` lands it at once and is flagged as a
  widening.
- **Routing.** Work reaches a team by rule (`routing.rules`, each a `match`
  and a `to` bot), or through its `lead`, who reads the case and assigns it,
  or else the `fallback`. **Requester provenance is preserved through every
  hop**: the assignee runs on no more trust than whoever asked. Each case
  records how it got there ("routed to platform by rule pd-api → assigned by
  lead platform-sre to web-triage"), and an operator reassignment is
  recorded the same way.
- Team membership, the team's settings, routing, bot links and the knowledge
  policy are config, so every edit goes through
  [§7](#7-the-config-edit-pipeline). Widening includes adding a team
  approver, raising the ceiling, dropping a sandbox requirement, linking a
  pair of bots, and `promotion: direct`.

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
| Knowledge | the shared repo by kind in the knowledge editor (below), with bot proposals (schema-checked) on top | author entries by kind; approve, **approve with changes**, deny with a reason, in bulk |

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
| **Memory** | The bot's memory by kind (knowledge, runbooks, watch, log, handoff, collections) in the knowledge editor (below), with pinned entries marked. | Read, correct, pin and delete what it knows; every save is one operator commit. Annotate or flag log entries. Promote knowledge or a runbook to the team. The bot keeps writing its own memory. |
| **Workers** | The worker tree: home sessions (live and retired) → children, groups, sweeps. For each worker: taint (inherited requester class), narrowed verbs, read-only flag, case, cost, run link. | **Stop** a worker (**NEW**: cancel its run). There is deliberately no "tell", because only the parent instructs a child. |
| **Access** | `skill.verbs` × `autonomy.tiers`, `promote`, browser credential bindings, logins, and the operators who can approve. | Tier, promote, verb, binding and login changes are **config edits** through [§7](#7-the-config-edit-pipeline). Revoking a saved login (`browser logout`) is immediate state. |
| **Definition** | A form for the whole `bots.<m>` node: `team`, charter or `charter_file`, scope, escalation, operators, `home.rotate`, `worker.*`, autonomy (shadow, `approval_ttl`, spend cap), `proactive.*`, cross-team `peers`, `isolation`. Inherited team defaults show beside each field. Live YAML and validation sit beside it. | Review changes → the apply pipeline. **Define a bot** uses the same form with `shadow: true` preset. |

**The knowledge editor** (a team's repo and a bot's memory, one component).
The operator authors knowledge directly; it is git-backed. **Knowledge goes
in knowledge, runbooks go in runbooks:** there is no free-path file creation.
Every entry has a **kind**, and its kind decides its home, its fields and who
writes it:

| Kind | Home | Fields / template | Who writes |
|---|---|---|---|
| Knowledge | `knowledge/` | `title`*, `applies_to` (list), `evidence`* (why you believe it), `confidence`* (low / medium / high); body: what is true, and why | operator directly; bots in their own memory, and into team knowledge by promotion |
| Runbook | `runbooks/` | `title`*, `when_to_use`*, `prerequisites` (list), `steps`* (a list of non-empty strings; the editor writes a command as ``(run: `cmd`)`` and a verb as `(verb: x)` in the step's text), `verification`*, `rollback`*, `owner`* | operator directly; bots by proposal |
| Watch item | `watch/` (one file each, named from `what`) | `what`*, `signal`*, `threshold`*, `why`* | operator directly; bots by proposal |
| Log | `log/` | `title`*, `case`, `sweep`, `flagged_wrong`; written by the bot per case or sweep | bots only; the operator can **add a note** or **flag it wrong**: both append a section (`## Operator note — ops, <time>` / `## Flagged WRONG by the operator — …`), and flagging sets `flagged_wrong`; the bot's text is never rewritten |
| Handoff | `handoff/` | `session`*; written on rotation | read-only |
| Collection | declared on the team (`teams.<t>.collections`) or the bot (`bots.<b>.collections`), or by a pack via `from:`, e.g. `postmortems/` | the collection's `fields` (`{type, required, values}`; types string, text, list, steps, enum, number, bool) and `template`; free paths only with `free_paths: true` | `writers`: a team's `operator` / `proposal`, a bot's `operator` / `bots` |

`*` required. These are conductor's content kinds (`internal/bot/kinds.go` on
the bots branch); the dashboard adds no fields of its own.

An entry is markdown with front matter (`kind:` plus the fields), so the
files stay readable in git and the same schema check runs everywhere: on an
operator save, on a bot proposal or promotion, and on import.

- **New…** opens a kind chooser (choice cards; read-only kinds are shown
  disabled with the reason). The kind's form has structured fields and a
  markdown body where relevant; the **path is derived** from kind and title
  (slug), shown read-only, and **renaming is changing the title** (one commit
  that moves the file). A free path field appears only inside a collection
  that declares `free_paths: true`, and stays inside its folder.
- **Browsing is by kind** (Knowledge / Runbooks / Watch / Log / Handoff /
  collections) with counts, search, and filters by applies-to tag. A
  **Files** view exists as a secondary, read-mostly view: raw files, with
  *Open as …* for typed files and *Map into a kind…* for untyped ones.
- **Runbooks** get a structured step editor: add, reorder, remove, and per
  step "no command", "run a command" or "call a verb" (verbs suggested from
  your connector instances). A runbook shows the cases it was used in;
  knowledge shows which runbooks, log entries and cases cite it (`[[path]]`
  links and case notes).
- **Every save is one commit authored by the operator** (`Authored-By:
  operator:<name>`, `Edited-Via: dashboard`) with an optional message. No
  approval: operator provenance is trusted. It is a session operation, not a
  step-up ([§6.5](#65-step-up)). A save that fails the kind's schema is
  refused with the field errors.
- **Conflicts:** if a bot committed to the entry after it was opened, saving
  shows both diffs against the base and asks for an explicit choice: rebase
  (field-wise: the fields you changed win, the body merges line by line),
  overwrite theirs, or keep theirs. Unsaved changes are guarded on
  navigation.
- **History** per entry and per repo: author (operator or bot, with a case
  link), the diff, **revert** as a new commit, and **who wrote this**
  (blame-lite).
- **Proposals and promotion** (team repos) show the kind and are validated
  against its schema. A malformed proposal (say, a runbook with an empty or
  non-string step, or no rollback) lists the schema errors, and *Approve* is
  disabled with the reason; **approve with changes** opens it in the kind's
  form with the errors marked, and the commit keeps the bot as author with
  `Approved-By` and `Edited-By: operator` trailers. Deny sends a **reason
  back to the proposing bot**; bulk approve refuses a selection that
  contains a malformed proposal.
- **Import** maps uploaded or pasted markdown (or a path from a repo through
  a read connector) **into a kind**: a runbook-like structure (numbered
  steps, *When*/*Verify*/*Rollback* sections) is detected and mapped to the
  runbook fields; a note mapped as knowledge keeps its text as the body,
  with `evidence: imported from <file>`; anything can be mapped into knowledge or a collection
  instead. Each file shows its detected kind, the derived path and what was
  found; entries that pass the schema are committed together, and the first
  one missing fields opens in its form with the errors.
- **Collections** add a kind: **Config → Collections**
  (`#/config/collections`) lists every team's and bot's collections and
  declares one (which team or bot it belongs to, name, folder, fields as
  `name:type` lines with `!` for required and `enum(a|b)` for choices, a
  body template, `free_paths`, writers) as an edit to
  `teams.<t>.collections` or `bots.<b>.collections`, through the diff. A
  collection's folder may not be a built-in kind's (`knowledge`, `runbooks`,
  `watch`, `log`, `handoff`, `episodes`), and a team's writers are
  `operator`/`proposal`, a bot's `operator`/`bots`. A pack ships a
  collection as a file that the team or bot points at with `from:` (the
  `sre-bot` pack ships `postmortems`); `from:` excludes the inline keys.
- **Bot memory:** the same, plus **pins** (entries always in the bot's
  context, recorded in `.pins`).
- **Discoverability:** each entry shows which bots read it and who may write
  its kind; search spans the team's knowledge and its bots' memory (or a
  bot's memory and its team's knowledge).
- **Phone:** view, search, light edits and approvals; the panes stack.
- **API:** `GET /api/v1/knowledge/{repo}/kinds` (built-in kinds plus
  collections, with their schemas), `GET …/entries?kind=&tag=&q=`,
  `POST …/entries` (kind + fields + body → derived path, one commit; 422 with
  field errors on a schema failure), `PUT …/entries/{path}` (with the base
  sha; 409 on a conflict), `POST …/import` (`dry_run` returns the detected
  kind and mapping per file), `POST …/log/{path}/notes` (`{text, flag}`; appends the section, sets `flagged_wrong` on a flag).

**Teams and bots follow conductor's schema** (`teams.<t>` and `bots.<b>` on
the bots branch). A team has approvers (`operators`), an **autonomy ceiling**
(`reversible` or `observe`: the most a bot may be **promoted** to, i.e. run
without your signature), optional `shadow`, tier classifications that only
make a bot stricter, a `budget` (`spend_cap` for real money, `proactive` as a
shared sweep pool in runs / turns / tokens / dollars), `isolation`
(`sandbox: required|optional`, `handoff_workers`), `credentials`
(`secrets_via`, `allow_secrets`), `browser` scopes, `knowledge`
(`promotion: proposal|direct`), `delegation.max_depth`, `peers: team|none`
and `routing` (`rules: [{name, match, to}]` → the `lead`'s triage → the
`fallback`). Cross-team talk is a **pair of bots**: both list each other
under `peers`, or an operator `bot_links` entry; there are no team-to-team
links. The UI enforces this where you edit: tier options looser than the
team's classification are disabled, "runs without asking" can't be switched
on above the ceiling (each with the reason), and validation refuses either if
written by hand ("Team ceilings" in the checks, separate from "Packs"). The
**Ceilings & scopes** tab labels each bot's value *inherits*, *same as team*
or *narrows*, beside a **tier ladder** (observe < reversible < propose <
commit_money; never) that says what each tier means for execution.

**What a bot can do, in words.** Each bot's Access tab opens with "Does on
its own / Asks you first / Never", computed from its grants, the effective
tier (the stricter of its own and its team's) and its promotions; the
**Safety** page (`#/safety`, linked from the "sandbox enforcing" chip) shows
the same for every bot.

**One widening classifier** (`S.classify`) serves every edit surface:
builder (egress hosts, verb grants and their resource scopes, broker grants,
repos a trigger runs on, removed or privileged isolation, `callable`), bots
and teams (looser tiers, new promotions, leaving shadow, sandbox no longer
required, new approvers, peers, credential and login scopes, direct
promotion), packs (third-party sources only, never the official registry or a
local pack; armed triggers and their repos; override egress), browser,
sandbox, runtimes and connectors. The review lists each widening with its
path, and the apply needs an explicit acknowledgement.

**Folders are one shared organization.** The same `folder: "a/b"` path
places bots, teams **and workflows**, so `work/platform` holds that area's
bots, teams and workflows together. Placement: your workflows carry
`folder:` in config; **pack workflows** sit under `packs/<instance>/` by
default and the operator can place them elsewhere with a pack override
(`packs.<instance>.folders.<workflow>`), but a pack can't assign its own
folder; **a bot's synthesized workflow follows its bot (or its team)**.
Connector and pack instances are deliberately *not* foldered: they're shared
infrastructure used across areas, and placing them would duplicate them
across folders. The **Workflows** page has a folder tree beside the table
(per-folder runs 24h, success rate and failing count), breadcrumbs, folder
filters orthogonal to the ownership filter (all / yours / packs / bots),
search across folders, and moves by drag-and-drop or *Move to…*, staged
through the diff. **A folder's page** (`#/folders/<path>/<tab>`) shows
everything in it: roll-ups, what needs you there, and Bots / Teams /
Workflows tabs. The Bots tree shows only folders that hold bots or teams.
Folders stay organization only: no behavior, permissions or defaults.

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

On iPhones (Safari and in-app WebKit browsers such as paseo's) the shell is a
locked column the height of the *visible* viewport (`100dvh`, not `100vh`,
which reaches under Safari's floating toolbar); only the content area
scrolls, and the tab bar is an ordinary in-flow row, never `position:
fixed; bottom: 0`. The top strip, the tab bar, sheets and toasts honour the
safe areas (`env(safe-area-inset-*)`, with `viewport-fit=cover`). On a phone
the status strip collapses to a status dot and the needs-you count; the rest
(version, sandbox, agents, running, staged, address) is one tap away in a
sheet.

**Themes.** Exactly one theme applies at a time, chosen before first paint:
an explicit choice (`?theme=light|dark`, the toggle, or one saved earlier)
always wins over the OS, for every surface and text colour; with none, the
page follows `prefers-color-scheme`, live. All colours come from the theme
tokens; no stylesheet reads `prefers-color-scheme` on its own, so light
surfaces never get dark-theme text. `?theme=auto` forgets the choice.

The suite's WebKit projects check this the way it's seen: iPhone 15 in
WebKit, the OS in dark and light mode × auto/light/dark, with Safari's
toolbar (80px) and the home-indicator inset (34px) simulated
(`?sim=ios`). On every route: one theme only, nothing wider than the
viewport, the tab bar and primary actions fully visible and not under the
toolbar, and rendered text at least 3:1 against its rendered background.

Every screen works at 390px by touch: navigation, tabs, filters, forms
(as sheets), the builder's list mode and approvals. The flows that matter
most are overview, approvals and chat; the prototype's `phone.html` shows
them in device frames, and its test suite runs every route at 390×844.

### 4.12 First steps, help and power tools

These came out of two UX audits (a newcomer and a power user; responses in
`design/AUDIT-RESPONSE.md`).

- **Overview first steps.** One line says what conductor is ("runs your bots
  and workflows on this machine; anything risky waits for your passkey").
  A **Get started** panel offers four steps with ticks derived from the setup
  (connect a service, make a bot that watches something, check what bots can
  do, try an approval); hiding it is a browser preference. The timeline is
  **Runs, last 12h**, busiest rows first, the top six by default, filtered by
  folder; health folds into a collapsed **System** card; the Running tile
  lists three names and a count.
- **New bot** is a guided page (`#/bots/new`): *Purpose → Watches → Tells you
  where → What it may do → Review*. It writes `bots.<name>` (description,
  escalation, team or folder, `skill.verbs`, `autonomy.tiers/promote/shadow`,
  optional `proactive`) and one trigger per watched event whose step is
  `bot: { name, title }`. Permission presets: read only; can comment and post;
  asks before anything else. Runtime, approvers and approval TTL sit under
  Advanced; the full definition form is one link away. A bot that watches
  nothing says so on its page, with **Add what it watches**.
- **Plain words.** List pages open with a one-line definition (workflow, bot,
  pack, plugin); conductor's nouns are dotted terms that explain themselves on
  hover, focus or tap, and `#/glossary` lists them all. The top strip says
  what each chip means; no issue numbers appear in the UI.
- **⌘K** is one command line: actions that run (destructive ones confirm),
  pending approvals, pages, bots, teams, folders, workflows, runs, connectors,
  verbs, vaults, secrets, settings, config paths
  (`triggers[ci-fixer].policy.budget`) and glossary terms, deduped, with an
  empty state that suggests what to try. Single keys: `?` shortcuts, `/`
  filter, `g` + o/l/a/r/w/b/c/s, `j`/`k`/Enter/`x` in lists.
- **Run detail** reads one run record (every step's status, start offset,
  duration, inputs, outputs, log and, for agent steps, the diff). Every
  timeline row selects its step (↑/↓ too); tabs show only what the step
  recorded; the audit panel is scoped to the run; the timeline has a time
  axis. Failed, waiting and parked runs say **What happened / What you can
  do**. **Compare** sets two runs of a workflow side by side (status,
  durations, the outputs diff with word highlights, both diffs). A running
  step offers **Restart this step** (stop first, confirmed), never "retry".
- **Bulk.** Lists take checkboxes with shift-click ranges and a sticky bulk
  bar: workflows (move to folder, enable, disable) and run history (retry the
  failed ones, export). Config changes from one bulk action are one change
  set and one diff.
- **Scale.** Run history pages by cursor (50, then *Load 50 more*); a
  Compact density (one-line rows) sits with the other display preferences.
- **Env** (Secrets & vars → Env, `#/secrets/env`): variables by scope
  (`conductor.env`, runtimes, hosts, workflows) with where each is used;
  secret-looking plain values are flagged and moved into the vault in one
  signed step; import vaults secret-looking values; export redacts them. The
  **effective environment** for a context (narrowest scope wins; the sandbox
  passes daemon variables only on `sandbox.env_allow`) shows on the Env tab,
  on run steps and on a bot's Workers tab, names and sources only.
- **Packs.** Browse is the catalog; **Add** opens the install wizard: bind
  each required connector role, arm triggers with their repos (arming a
  github trigger without repos is refused, as the loader does), a *pack plan*
  with the grants shown loudly, and the `conductor.lock.yaml` change, which
  also appears as its own hunk in the apply diff. Badges: official registry,
  **local · yours**, third party (needs `pack_trust`). Overrides are edited as
  YAML with the pack's addressable members listed and unknown ones refused.
- **Connect with Slack / GitHub.** The connector form offers an app-manifest
  flow: conductor generates the manifest, the service creates and installs the
  app, and the tokens it returns go to conductor's vault (a signed write) while
  the connector is staged with references. The manual form stays for people
  who have tokens. It then says what bots could do with the connection.
- **Builder.** Workflow `hooks:` have their own lane (start/done/fail/stop);
  steps show their hooks; `output_schema` is summarized; keys the form doesn't
  model show as "+N keys only in YAML" with an in-place YAML editor. Untouched
  required fields are a neutral to-do checklist ("Fill in labels"), not errors.
  The step picker tags every action *reads only*, *changes things* or *high
  impact*, and offers Run a command, Wait for a condition, Hand to a team and
  Fail the run. Test can use a pasted payload, and says when history holds no
  event for the trigger. A new trigger key is inserted where you'd write it,
  before `steps:`.
- **History.** *Revert this change* undoes one applied change and keeps the
  later ones (refused, with the reason, if a later change touched the same
  node); *Restore to before this* names the later changes it also undoes.
  Backup ids are labelled UTC beside local times.

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
- a config apply or restore **that widens** what bots or agents can do, as
  the one widening classifier decides ([§7.3](#7-the-config-edit-pipeline)):
  more autonomy (a tier loosened, a promotion, a ceiling raised, leaving
  shadow), new verbs or grants (including broker secrets), credentials or
  logins bound, egress or hosts added, isolation loosened, a trigger armed or
  its repo scope widened, a pack added or a source trusted, operators or team
  approvers added, links between bots;
- a secret write;
- a team knowledge promotion;
- a browser takeover;
- update now;
- pack and plugin add/remove;
- enrolling another passkey.

**Non-widening config changes need no passkey**: folder moves, renames,
descriptions, tightening a limit, disabling something, removing a grant.
They get a plain **Apply** after the diff review in the signed-in session,
and still go through validate-on-a-copy, the backup, the boot guard and
rollback. A change set that mixes both needs the passkey. The review screen
says which and why, in words: "Needs your passkey: binds a login to
platform-sre", or "No passkey needed: nothing here lets bots or agents do
more". The audit line records which path was taken.

**Display preferences are not config.** Theme, density and the usage
display are stored per browser and apply instantly; they never touch
`config.yaml` or the pipeline.

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

After the diff review: a step-up assertion over the change's digest when the
change widens anything ([§6.5](#65-step-up)), otherwise the session alone:

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
survives navigation and reloads. **Demo data** (✦ in the rail footer) picks the
day the prototype shows: *a mostly healthy day* (the default: dozens of runs,
one approval and one failure), *all quiet*, or *everything at once* (every kind
of exception; the test suite runs on it), and resets the current one. The
before/after review of the calm-by-default pass is at `review/calm.html`. Config edits from every screen stage into the
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
- **Search:** ⌘K. **Theme:** the rail footer. **Density:** Config → General
  (Comfortable or Compact, this browser only).
- **Calm:** switch to *All quiet* (✦) and open the Overview: one line says
  nothing needs you.

**Interaction tests** live in `design/dashboard-tests/` (Playwright; see its
README). They visit every route at 1440×900 and 390×844 and click every
control, failing on any that changes nothing and isn't disabled with a
reason; run every create/edit/delete flow through diff and apply; drive the
builder and the team flows; check Daylight conformance (tokens only, type
floors, the serif rule) and the calm rules (the answer first, a colour budget);
sweep layout and rendered contrast at the brand's minimums in both themes and
on WebKit iPhone 15 with Safari's toolbar simulated; and fail on any console
error.

The prototype is a design artefact. Its code is not the product's code, and
its data shapes follow [§5](#5-api) only loosely.
