# Dashboard prototype — handoff

Branch `design/dashboard` (worktree `~/Projects/paseo-home/worktrees/2ccqu2oy/design-dashboard`).
Prototype: `design/dashboard-prototype/` (static HTML/CSS/JS, hash routes, fake state in
localStorage key `cd-proto-state-v6`, demo day in `cd-demo`, "Reset" and the demo days under ✦ in the rail). Served live
by the user service `dashboard-prototype` on this machine's tailnet address, port 8767 (don't stop or replace it).
`design/CONFORMANCE.md` sets each screen beside the approved design it follows.
Design doc: `docs/design/dashboard.md` (§3 is the visual language: Daylight, with the approved designs as the spec). Tests: `design/dashboard-tests/`.

Guardrails: `df -h /` before heavy work (stop under 25 GB); **all scratch, caches,
node_modules, browsers, screenshots and test output on LOCAL disk under `~/.cache/conductor-design/`,
never under `~/Projects` (NFS)**; run suites sequentially, few browsers at once; sequential work, no parallel subagents;
nothing that signals processes outside a PID namespace; commit and push early and often under the repository owner's git identity, no AI attribution and no personal names in commit messages or files (`privacy.spec.js` fails on one);
prototype + docs only; neutral fake data only (`platform-sre`, `acme/api`).

## How to run the tests

```
cd design/dashboard-tests
BASE_URL=http://<tailnet-address>:8767 WORKERS=3 ./run.sh --project=desktop --reporter=line   # against the live prototype
BASE_URL=http://<tailnet-address>:8767 ./run-webkit.sh --reporter=line                         # WebKit iPhone 15 sweep (Docker)
```

While editing, run against a frozen snapshot so edits don't race the run: `~/.cache/conductor-design/daylight/suite.sh`
rsyncs the prototype to local disk and serves it on 127.0.0.1:8799 inside an `unshare` PID namespace (the server dies
with it), then runs `run.sh` with your arguments. Deps in `~/.cache/conductor-design/dashboard-tests-node` (Playwright
1.58.2), browsers in `~/.cache/conductor-design/ms-playwright`, output in `~/.cache/conductor-design/out/`. The suite
runs on the "everything at once" demo day (fixtures.js); `home.spec.js` switches to the quiet days itself.
Every test fails on any console error; `UI.act` logs an error on a duplicate action name.

New specs in this pass: `daylight.spec.js` (tokens only: no raw colours or retired variables; rendered type floors,
mono ≥ 12px, the serif only on short titles ≥ 20px; no tinted capsules), `home.spec.js` (Home as approved; no header
sentence that restates the rows below it), `status.spec.js` (one status vocabulary) and `sweep.spec.js` (every route
at desktop, tablet and phone: overlaps, spills, one word per line, centred or blown-up rows, dropped interpolations, a
sentence split into columns). `audit.js` now enforces the brand's
rendered contrast minimums (4.5:1, 3:1 large; disabled exempt) in the layout and iPhone sweeps.

Screenshot helpers (local disk): `~/.cache/conductor-design/daylight/batch.js <outdir> <theme> <w> <h> <density> <os>
[routes…]` (DEMO=calm|quiet|full), `pairs.js <outdir>` (the five pair screens × desktop/phone × light/dark),
`review.py` (compresses them into `review/shots/`).

## Code map (prototype)

- `css/tokens.css` is a verbatim copy of Daylight's generated tokens (origin/design/brand `design/brand/tokens/tokens.css`):
  re-copy it, never edit it. `css/app.css` base + shell + core components, `css/marks.css` the only place status marks,
  labels, filter chips, counts, row states and grouped-list heads are drawn, `css/components.css` shared patterns,
  `css/screens.css` screens, `css/builder.css` the builder. No raw colours anywhere but tokens.css (daylight.spec).
- `fonts/` Atkinson Hyperlegible Next and Mono, Fraunces (OFL). `assets/icons/` Lucide licence; `js/icons.js` the Lucide
  subset + Daylight status glyphs (`UI.icon`, `UI.glyph`).
- `js/scenario.js` the demo days: `calm` (default, mostly healthy), `quiet`, `full` (everything at once; the suite
  sets it in fixtures.js). Per browser in localStorage `cd-demo`; switch from ✦ in the rail.
- `js/yaml.js` comment-preserving YAML; loads before `data.js`.
- `js/data.js` fake state (several dated addenda at the end: usage, logins, typed knowledge, env).
- `js/schema.js` plugin/connector/verb/event declarations. `js/store.js` collections, staging, the config
  pipeline (`S.stage`, `S.view`, `S.configText`, `S.envText`, `S.validate`, `S.apply`).
- `js/ui.js` router, forms (`UI.form`, `UI.fieldHTML`), drawers, guarded dismissal (`UI.dismissModal`), shared
  component helpers (`UI.choice`, `UI.chosen`, `UI.stepper`, `UI.statusMark`, `UI.mark`, `UI.facts`, `UI.bulkBar`
  (the selection bar), `UI.systemTabs`, `UI.ph` (escapes plain placeholders in prose)). `css/daylight.css` holds the
  approved screens' components, ported from the brand's mocks with a `dl-` prefix. `css/components.css` holds the shared components.
- `js/usage.js` usage display (off by default, per provider). `js/valuesource.js` the value-source field.
- `js/runrec.js` one record per run (steps, timing, inputs, outputs, logs, diffs), the run story and run-scoped
  audit. `js/glossary.js` terms (`UI.term`) and `#/glossary`. `js/palette.js` ⌘K and single-key shortcuts.
- `js/screens/botwizard.js` New bot (loaded before bots.js), `env.js` the Env tab and effective environment,
  `connect.js` the Slack/GitHub manifest flows. `assets/logo/` is a complete Daylight logo kit (Fermata, strengthened)
  plus `logo.js`, the one reference (`UI.logo()` inlines the kit's `mark-inline.html`); `manifest.webmanifest` PWA icons.
- `design/CONFORMANCE.md` and `design/conformance/` (the approved mock beside the prototype, rendered by
  `design/conformance/render.js`). (The earlier calm pass's `review/calm.html` and `calm.spec.js` were removed in the reset.)
- `index.html` head script picks the theme before paint (explicit wins, else the OS); `?sim=ios` is the
  test-only iPhone emulation (toolbar overlay + safe areas). Asset URLs carry `?v=<stamp>`; bump it on deploys.
- `js/create.js` create-in-place drawers (`UI.CREATE`). `js/wf.js` workflow engine + validator.
- Personal details and payment cards (Browsers → Personal details, Payment cards; purchases in Needs you, a bot's
  Approvals and What it can do, receipts on tickets): `js/walletdata.js` (fixtures, the backend's `browser-wallet` and
  purchase shapes; loaded before `scenario.js`), `js/screens/wallet.js`, `css/wallet.css`, `dashboard-tests/wallet.spec.js`;
  the semantics and what's NEW in `docs/design/dashboard.md` §4.5.1. Spend limits stack (the lowest wins): `UI.limitLayers`
  (card, the card for the caller, the bot's `purchases:`, its team's `budget.purchases:`, the ticket's `spend_limit`),
  shown on the approval, a ticket (`UI.ticketBudget`, New ticket's optional field), a bot's What it can do and a team's
  Ceilings & scopes (`UI.teamWalletHTML`). The value-source field (`js/valuesource.js`) picks a reference from a lookup
  (`UI.VS.options/find`), never a typed reference.
- Screens in `js/screens/`: overview, runs, repo (knowledge by kind), bots (teams, bots), botlist (the Bots page: teams are the page), browser, logins, builder (workflows list + builder), config, secrets, system (packs,
  plugins, updates, connectors, runtimes, hosts, isolation, logs), catalog (`#/extensions/browse`, and the picker behind Add a connector),
  access (sign-in, enroll, passkeys).

## Done (commits on this branch, newest first)

- **Usability pass** (each screen around its person-level question; `design/AUDIT-RESPONSE.md` "Usability"): Home's
  Needs you tile, the bot page (tasks, waiting approvals, the plain-list permission editor), the run page's one
  recommended step, Activity on a phone, workflow sentences, connection states, Secrets/Packs/Config hierarchy, and a
  Preferences page (`#/preferences`).
- **Round 2 of the adversarial reviews** (`design/AUDIT-RESPONSE.md` "Round 2"; every fix pinned in
  `dashboard-tests/round2.spec.js`): the review screen computes its checks and forms change only what you changed;
  every statement about a bot's reach comes from one function (`UI.verbMode`); the enforced egress allowlist validates;
  one source for counts and times (`UI.syncRunStats`, `UI.syncAttention`, the run records); dialogs focus themselves,
  trap Tab and return focus; the catalog's picker is Add a connector; a double-click acts once; the phone header follows
  every route and a phone approval stands alone; the entrance animation never hides content; the status vocabulary
  (marks only on states, labels as plain words, no tier ladder); generated titles in the sans; the step picker's risk
  words; the run page's primary retry and per-workflow logs; validation; only documented semantics (NEW where not);
  tokens and voice passes. Not done: bulk on the bots tree and bulk retarget/set-model (scope).
- **Header sentences earn their place** (from review: the Overview's greeting and summary were "useless info"): Home
  opens on At a glance and Needs you; the count-and-health sentence under the title is gone from Activity, Needs you,
  Workflows, Bots, Browsers, Logins, Packs, Connectors, Logs and Updates (the rows say it). Kept where the sentence says
  something the page doesn't: Config (nothing applies until you review it), Secrets (write-only; an unreachable vault's
  consequence), Env (plain-text secrets), Access (who and from where), pack overrides, a run's story, a bot's role, and
  the one-line definitions of workflow, bot and pack. Generated titles (a run's, a case's) moved to the sans. The Env
  Export button the export removal missed is gone.
- **Reset to the approved designs** (the approved designs are the spec; `design/CONFORMANCE.md` maps every screen to
  its source with the mock beside the prototype). Daylight's v2 mocks for the shell, Home, the run page, the bot page
  and approval, run history and the builder; the chosen status column (D's grouping with A's margin marks, every row
  drawn) through one status vocabulary (`UI.statusMark`/`UI.mark`, exactly `status/marks.js`); the brand's current
  `tokens.css`. Structural decisions folded in: Run history merged into **Activity** (filters, time range, search,
  paged Finished, a runs-per-hour strip instead of per-workflow lanes); one **Safety** page (the sandbox in plain words
  and what each bot may do); one **System** area (Config with readable applied changes, Updates in plain words, Logs,
  Access in plain words); Packs with the catalog first, guided overrides and add-from-a-source; Secrets one line each;
  the pinned **selection bar** for every bulk list. Removed: risk meters and the risk ladder, object/action icons, the
  row anatomy grid, environment badges, step counts as status, "Since you last looked", exports, the sunrise motif, run
  tabs and previous/next, explanatory captions on obvious controls, red buttons outside confirmations, tooltips that
  repeat the row. New checks: `sweep.spec.js` (overlap, spill, one-word lines, centred or blown-up rows, dropped
  interpolations, at 1440/900/390) and `status.spec.js`.
- **From the iPhone review**: one stepper component everywhere, compact ("Step 2 of 5 · Watches" + a thin bar)
  whenever the labelled steps don't fit, never overlapping (`wizard.spec.js`, plus stepper checks in the layout and
  iPhone sweeps); the passkey glyph only on controls that sign (`passkey.spec.js`); Pause all automation lives in
  System → Safety and ⌘K (explained, confirmed; while paused, Needs you says so with Resume); Run a workflow off the Overview (it's on
  each workflow and in ⌘K).

- **Calm by default** (from the first review: "all of it just looks so busy"): exception-first on every key screen. The Overview's
  answer line ("2 things need you." / "All quiet. Nothing needs you."), large needs-you rows, calm lanes (passing runs as
  a faint tick strip; failures and waits labelled where they are; running in ink), everything normal in quiet one-line
  rows; run history as grouped lists with finished work folded to one line; Needs you single-column; bots tree rolls up
  only what needs you; bot and team headers in one sentence; connectors, packs, workflows and cases one line per row with
  detail on hover; calm approval head. Demo days (calm default, quiet, everything at once). (Superseded by the reset; its spec and
  review page are gone.)
- **Daylight adopted** in one pass: brand tokens, fonts, the Fermata (strengthened) logo kit as the one swappable asset
  with favicon/apple-touch/PWA icons, Lucide icons and Daylight status glyphs, every screen on semantic tokens (no raw
  colours; old variables gone; no compatibility shim), the type scale and floors, Comfortable/Compact on the density
  tokens (phones always comfortable), the shared components (margin-mark statusMark, label, filterChip/selectChip,
  count, rowState, grouped lists, quiet rows, summary header, choice cards with radios, stepper, empty states with the
  sunrise, toasts, the value-source field's source menu), a summary sentence on every page, human times and schedules
  ("Mondays at 06:00", "52 min ago (10:19)"), plain run titles and errors, the run story tree, the Activity page with
  Score's lanes, Broadsheet's "since you last looked" digest, the approval card. Voice: no codenames ("handoff"), verbs
  on buttons, plain words first.
- Audit P1 and minors, and everything before them: see `design/AUDIT-RESPONSE.md` and `git log`.

## Open (waiting on the brand designer)

- The final **status-and-label system** (margin marks, filter chips, counts, row emphasis): it swaps into
  `css/marks.css` and the helpers in `js/ui.js` (`UI.statusMark`, `UI.label`, `UI.filterChip`, `UI.selectChip`,
  `UI.count`, `UI.rowState`, `UI.groupHead`). Screens draw none of this markup themselves.
- The calm components and tokens the brand designer is adding (quiet rows, tick strips, the answer line): today they are
  built from existing tokens in `css/screens.css` (search "calm").

## Queued directions (status)

| Direction | Status |
|---|---|
| Original brief (no dead controls, CRUD via diff, builder, phone, Playwright) | done; re-verify in final run |
| Pipedream-style builder | done; audit adds hooks/isolation/filter/test-events (P1 18) |
| Teams; members → bots; delegation; the Bots page (teams are the page; no folders); inline creation; knowledge authoring | done; teams move to the real schema in step D |
| Usage mode (off default, per provider, source-only limits) | done |
| Planned multi-user behind a toggle | deferred (toggle says so); issue numbers removed from copy |
| Add a login | done; folder principals removed in step D |
| Environment variables (scopes, effective env, import, restart via boot guard) | done (`#/secrets/env`) |
| Packs & plugins catalog (browse/detail/install wizard/sources/trust) | done (routed; lockfile hunk) |
| Connectors catalog (real builtin vs plugin split, custom git connector) | **not started** (SCHEMA still marks github builtin); facts in summary: builtins web, cron, webhook, rss, command, rest, graphql, kv, sql, memory, workflow, conductor (+ lan, tunnel); everything vendor is an official plugin |
| Value-source field | done; changes in step B (canonical vault syntax, no named secrets) |
| Knowledge kinds | done, on conductor's real kinds and collection locations |
| Visual QA bar + shared components | done: layout (both themes), iPhone WebKit sweep, dismiss, copy-number, Daylight, home, status and sweep specs |
| Daylight adoption | done (this pass) |
| Calm by default (exception-first) | superseded by the reset to the approved designs (the calm variant and the later icon/risk/row-anatomy pass are gone) |
| Run detail (no tabs, back with state, labelled ids, paseo per step, one status look) | done; `status.spec.js` |
| Reset to the approved designs | done: `design/CONFORMANCE.md`; other open directions frozen |
| Passkey enroll cancel + every flow has a way out | done (dismiss spec) |
| Folders | removed: conductor has no folders; teams group bots, sources group workflows |

## Known gaps

- The phone breakpoint stays at 720px (Daylight's phone is < 600px): tablets in portrait between 600 and 720 get the
  phone layout, which keeps the tab bar and sheets. Revisit with real tablet use.
- The WebKit projects need Docker (the Playwright Ubuntu image); WebKit's Ubuntu build doesn't run on the Arch host.
- Deferred from the audits (why in AUDIT-RESPONSE): bulk on bots tree/packs/connectors/secrets/cases; pack dependency
  tree; visual AND/OR filter groups; new-trigger comment placement; multi-user previews.
