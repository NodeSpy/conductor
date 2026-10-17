# Dashboard prototype — handoff

Branch `design/dashboard` (worktree `~/Projects/paseo-home/worktrees/2ccqu2oy/design-dashboard`).
Prototype: `design/dashboard-prototype/` (static HTML/CSS/JS, hash routes, fake state in
localStorage key `cd-proto-state-v4`, "Reset demo data" in the user menu). Served live by the
user service `dashboard-prototype` at http://100.119.192.38:8767/ (don't stop or replace it).
Design doc: `docs/design/dashboard.md`. Tests: `design/dashboard-tests/`.

Guardrails (from Daniel): `df -h /` before heavy work (stop under 25 GB); **all scratch, caches,
node_modules, browsers, screenshots and test output on LOCAL disk under `~/.cache/conductor-design/`,
never under `~/Projects` (NFS; heavy churn there was killing the machine); clean up after yourself; run
suites sequentially, few browsers at once**; sequential work, no parallel subagents; commit and push early and often as
Daniel, no AI attribution; prototype + docs only (no conductor product code, don't touch the live
conductor/paseo services or configs); neutral fake data only (`platform-sre`, `acme/api`).
Report once, when everything passes.

## How to run the tests

```
cd design/dashboard-tests
BASE_URL=http://100.119.192.38:8767 WORKERS=3 ./run.sh                    # everything, desktop + phone
BASE_URL=http://100.119.192.38:8767 ./run.sh knowledge.spec.js --project=desktop --reporter=line
```

Deps live in `~/.cache/conductor-design/dashboard-tests-node` (Playwright 1.58.2); browsers in
`~/.cache/conductor-design/ms-playwright` (chromium-1208 symlinked from `~/.cache/ms-playwright`, webkit-2248
unpacked there); output in `~/.cache/conductor-design/out/`. WebKit iPhone projects: `./run-webkit.sh` (runs
inside the local `mcr.microsoft.com/playwright:v1.62.0-noble` image, since WebKit needs Ubuntu libraries).
Every test fails on any console error. `UI.act` logs an error on a duplicate action name, so name
clashes fail tests. Screenshot helpers for the visual review: `~/.cache/conductor-design/shots/shot.js`
(`node shot.js '<route>' out.png <usageMode|''> <w> <h>`, with NODE_PATH and BASE set) and
`overflow.js` (lists panels whose content overflows).

## Code map (prototype)

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
  `connect.js` the Slack/GitHub manifest flows. `assets/logo/` + `logo.js` the logo (via `UI.logo()`).
- `index.html` head script picks the theme before paint (explicit wins, else the OS); `?sim=ios` is the
  test-only iPhone emulation (toolbar overlay + safe areas). Asset URLs carry `?v=<stamp>`; bump it on deploys.
- `js/create.js` create-in-place drawers (`UI.CREATE`). `js/wf.js` workflow engine + validator.
- Screens in `js/screens/`: overview, runs, repo (knowledge by kind), bots (teams, bots), folders (shared folder
  tree, folder pages), browser, logins, builder (workflows list + builder), config, secrets, system (packs,
  plugins, updates, connectors, runtimes, hosts, isolation, logs), catalog (catalog component; **not routed yet**),
  access (sign-in, enroll, passkeys).

## Done (commits on this branch, newest first)

- Audit P1 and minors: builder hooks lane / keys only in YAML / pasted test payloads / canonical key order;
  visual QA bar specs (layout both themes, dismiss/Esc/Back, copy numbers); history revert-one + restore
  warning; phone tab fade + folded header actions; Connect with Slack/GitHub; scale (cursor paging, Compact
  density); bulk select (workflows, runs); packs install wizard routed + lockfile hunk + trust badges; Env tab +
  effective environment; ⌘K command line + keyboard shortcuts; passkey only for widening changes; display
  preferences local; glossary + plain definitions; New bot wizard; Get started; run detail on one run record
  + compare; iPhone fixes (dvh shell, safe areas, collapsed strip, one theme before paint) + WebKit iPhone
  projects; swappable logo assets; knowledge on conductor's real kinds. See `git log` for commit ids.
- 67130be teams and bots on conductor's schema (step D). e5461c6 one vault syntax (step B). 0502566 approvals (C).
- 9be090d shared folders; 1f117d7 / d808107 knowledge by kind; 628b6a7 enrollment cancel + guarded dismissal;
  48bb6d9 value-source field; bf4b585 Add a login; ba263ef usage off by default.

## In progress

**Audit fixes: complete pending the final full runs.** `design/AUDIT-RESPONSE.md` maps every finding (Sam B1–B3,
M1–M11, m1–m14, P1–P5, polish; Riley B1–B4, M1–M14, minors, polish) to fixed / changed differently / deferred.
Remaining: the final full Chromium suite (desktop + phone, sequential) and the WebKit iPhone sweep, then the
report ending with the line `AUDIT FIXES COMPLETE`.

Deferred (why in AUDIT-RESPONSE): bulk on bots tree/packs/connectors/secrets/cases; pack dependency tree; visual
AND/OR filter groups; agent card regrouping (waits for Daylight); new-trigger comment placement; multi-user
previews.

## Visual direction: Daylight chosen, restyle on hold

Daniel chose **Daylight** (warm, light-first; Atkinson Hyperlegible UI, Fraunces headings; colour only as
signal; summary-first; mono only for literal code/IDs/YAML; see http://100.119.192.38:8768/directions-v2.html and
`origin/design/brand:design/brand/v2/`). The brand designer is rebuilding tokens and the brand book; adopt the
finished package **in one pass after** the audit fixes. Until then: no restyle; new UI follows Daylight's
information design (summary sentence first, plain headline over technical detail, human times, colour only for
attention). The logo is one swappable asset (`dashboard-prototype/assets/logo/` + `logo.js`, used only via
`UI.logo()`); swapping it is a file drop. The final report ends with a line containing only
`AUDIT FIXES COMPLETE`.

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
| Visual QA bar + shared components | done: layout (both themes), iPhone WebKit sweep, dismiss, copy-number specs |
| Passkey enroll cancel + every flow has a way out | done (dismiss spec) |
| Shared folders across workflows | done |

## Known gaps

- The `dashboard-prototype` user service no longer exists on this machine (gone after the crash); it was not
  recreated (Daniel's service). Tests serve the prototype themselves (`./run.sh` with no BASE_URL, or a temporary
  `python3 -m http.server 8799` for `run-webkit.sh`).
- The WebKit projects need Docker (the Playwright Ubuntu image); WebKit's Ubuntu build doesn't run on the Arch host.
- Docs describe the audit additions in §4.12; the multi-user preview isn't designed in detail.
