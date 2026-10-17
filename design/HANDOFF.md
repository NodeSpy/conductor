# Dashboard prototype — handoff

Branch `design/dashboard` (worktree `~/Projects/paseo-home/worktrees/2ccqu2oy/design-dashboard`).
Prototype: `design/dashboard-prototype/` (static HTML/CSS/JS, hash routes, fake state in
localStorage key `cd-proto-state-v5`, demo day in `cd-demo`, "Reset" and the demo days under ✦ in the rail). Served live
by the user service `dashboard-prototype` on this machine's tailnet address, port 8767 (don't stop or replace it); the
before/after review is at `/review/calm.html` on it.
Design doc: `docs/design/dashboard.md` (§3 is the visual language: Daylight and calm by default). Tests: `design/dashboard-tests/`.

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
runs on the "everything at once" demo day (fixtures.js); `calm.spec.js` switches to the calm and quiet days itself.
Every test fails on any console error; `UI.act` logs an error on a duplicate action name.

New specs in this pass: `daylight.spec.js` (tokens only: no raw colours or retired variables; rendered type floors,
mono ≥ 12px, the serif only on short titles ≥ 20px; no tinted capsules) and `calm.spec.js` (on the mostly healthy day
each key screen's one-sentence answer comes first, in bold; the Overview's answer is its largest text; a colour budget
and no wall-of-text rows in the first viewport; the all-quiet Overview says so). `audit.js` now enforces the brand's
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
- `js/data.js` fake state (several dated addenda at the end: usage, logins, typed knowledge, env, workflow folders).
- `js/schema.js` plugin/connector/verb/event declarations. `js/store.js` collections, staging, the config
  pipeline (`S.stage`, `S.view`, `S.configText`, `S.envText`, `S.validate`, `S.apply`).
- `js/ui.js` router, forms (`UI.form`, `UI.fieldHTML`), drawers, guarded dismissal (`UI.dismissModal`), shared
  component helpers (`UI.choice`, `UI.chosen`, `UI.stepper`). `css/components.css` holds the shared components.
- `js/usage.js` usage display (off by default, per provider). `js/valuesource.js` the value-source field.
- `js/runrec.js` one record per run (steps, timing, inputs, outputs, logs, diffs), the run story and run-scoped
  audit. `js/glossary.js` terms (`UI.term`) and `#/glossary`. `js/palette.js` ⌘K and single-key shortcuts.
- `js/screens/botwizard.js` New bot (loaded before bots.js), `env.js` the Env tab and effective environment,
  `connect.js` the Slack/GitHub manifest flows. `assets/logo/` is a complete Daylight logo kit (Fermata, strengthened)
  plus `logo.js`, the one reference (`UI.logo()` inlines the kit's `mark-inline.html`); `manifest.webmanifest` PWA icons.
- `review/calm.html` the before/after pairs for the calm-by-default pass (shots in `review/shots/`).
- `index.html` head script picks the theme before paint (explicit wins, else the OS); `?sim=ios` is the
  test-only iPhone emulation (toolbar overlay + safe areas). Asset URLs carry `?v=<stamp>`; bump it on deploys.
- `js/create.js` create-in-place drawers (`UI.CREATE`). `js/wf.js` workflow engine + validator.
- Screens in `js/screens/`: overview, runs, repo (knowledge by kind), bots (teams, bots), folders (shared folder
  tree, folder pages), browser, logins, builder (workflows list + builder), config, secrets, system (packs,
  plugins, updates, connectors, runtimes, hosts, isolation, logs), catalog (catalog component; **not routed yet**),
  access (sign-in, enroll, passkeys).

## Done (commits on this branch, newest first)

- **From the iPhone review**: one stepper component everywhere, compact ("Step 2 of 5 · Watches" + a thin bar)
  whenever the labelled steps don't fit, never overlapping (`wizard.spec.js`, plus stepper checks in the layout and
  iPhone sweeps); the passkey glyph only on controls that sign (`passkey.spec.js`); Pause dispatch moved to the status
  menu and the System row (explained, confirmed, visible paused state, Resume); Run a workflow off the Overview (it's on
  each workflow and in ⌘K).

- **Calm by default** (from the first review: "all of it just looks so busy"): exception-first on every key screen. The Overview's
  answer line ("2 things need you." / "All quiet. Nothing needs you."), large needs-you rows, calm lanes (passing runs as
  a faint tick strip; failures and waits labelled where they are; running in ink), everything normal in quiet one-line
  rows; run history as grouped lists with finished work folded to one line; Needs you single-column; bots tree rolls up
  only what needs you; bot and team headers in one sentence; connectors, packs, workflows and cases one line per row with
  detail on hover; calm approval head. Demo days (calm default, quiet, everything at once). `calm.spec.js`; before/after
  pairs at `review/calm.html`.
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
| Teams; members → bots; delegation; Bots + folders; inline creation; knowledge authoring | done; teams move to the real schema in step D |
| Usage mode (off default, per provider, source-only limits) | done |
| Planned multi-user behind a toggle | deferred (toggle says so); issue numbers removed from copy |
| Add a login | done; folder principals removed in step D |
| Environment variables (scopes, effective env, import/export, restart via boot guard) | done (`#/secrets/env`) |
| Packs & plugins catalog (browse/detail/install wizard/sources/trust) | done (routed; lockfile hunk) |
| Connectors catalog (real builtin vs plugin split, custom git connector) | **not started** (SCHEMA still marks github builtin); facts in summary: builtins web, cron, webhook, rss, command, rest, graphql, kv, sql, memory, workflow, conductor (+ lan, tunnel); everything vendor is an official plugin |
| Value-source field | done; changes in step B (canonical vault syntax, no named secrets) |
| Knowledge kinds | done, on conductor's real kinds and collection locations |
| Visual QA bar + shared components | done: layout (both themes), iPhone WebKit sweep, dismiss, copy-number, Daylight and calm specs |
| Daylight adoption | done (this pass) |
| Calm by default (exception-first) | done (this pass): every key screen; `calm.spec.js`; `review/calm.html` |
| Passkey enroll cancel + every flow has a way out | done (dismiss spec) |
| Shared folders across workflows | done |

## Known gaps

- The phone breakpoint stays at 720px (Daylight's phone is < 600px): tablets in portrait between 600 and 720 get the
  phone layout, which keeps the tab bar and sheets. Revisit with real tablet use.
- The WebKit projects need Docker (the Playwright Ubuntu image); WebKit's Ubuntu build doesn't run on the Arch host.
- Running work is drawn in ink, not the brand's sky blue: the calm rule (colour only for what needs someone)
  outranks the brand book here; flagged for the brand designer.
- Deferred from the audits (why in AUDIT-RESPONSE): bulk on bots tree/packs/connectors/secrets/cases; pack dependency
  tree; visual AND/OR filter groups; new-trigger comment placement; multi-user previews.
