# Dashboard prototype — handoff

Branch `design/dashboard` (worktree `~/Projects/paseo-home/worktrees/2ccqu2oy/design-dashboard`).
Prototype: `design/dashboard-prototype/` (static HTML/CSS/JS, hash routes, fake state in
localStorage key `cd-proto-state-v4`, "Reset demo data" in the user menu). Served live by the
user service `dashboard-prototype` at http://100.119.192.38:8767/ (don't stop or replace it).
Design doc: `docs/design/dashboard.md`. Tests: `design/dashboard-tests/`.

Guardrails (from Daniel): `df -h /` before heavy work (stop under 25 GB); npm cache/temp under
`~/Projects/.cache/`; sequential work, no parallel subagents; commit and push early and often as
Daniel, no AI attribution; prototype + docs only (no conductor product code, don't touch the live
conductor/paseo services or configs); neutral fake data only (`platform-sre`, `acme/api`).
Report once, when everything passes.

## How to run the tests

```
cd design/dashboard-tests
BASE_URL=http://100.119.192.38:8767 WORKERS=3 ./run.sh                    # everything, desktop + phone
BASE_URL=http://100.119.192.38:8767 ./run.sh knowledge.spec.js --project=desktop --reporter=line
```

Deps live in `~/Projects/.cache/dashboard-tests-node` (Playwright 1.58.2, chromium-1208 cached).
Every test fails on any console error. `UI.act` logs an error on a duplicate action name, so name
clashes fail tests. Screenshot helpers for the visual review: `~/Projects/.cache/shots/shot.js`
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
- `js/create.js` create-in-place drawers (`UI.CREATE`). `js/wf.js` workflow engine + validator.
- Screens in `js/screens/`: overview, runs, repo (knowledge by kind), bots (teams, bots), folders (shared folder
  tree, folder pages), browser, logins, builder (workflows list + builder), config, secrets, system (packs,
  plugins, updates, connectors, runtimes, hosts, isolation, logs), catalog (catalog component; **not routed yet**),
  access (sign-in, enroll, passkeys).

## Done (commits on this branch, newest first)

- 9be090d one shared folder tree (bots, teams, workflows), folder pages with tabs, workflows folder tree/moves.
- 1f117d7 / d808107 knowledge by kind (built-in kinds + collections, derived paths, runbook steps, schema-checked
  proposals and imports, Config → Collections).
- 628b6a7 enrollment cancel + confirm-with-existing-passkey; guarded dismissal and Esc; Cancel focus on destructive.
- 48bb6d9 value-source field (typed/env/secret/vault/setting) everywhere; shared components; Add a login restyle.
- bf4b585 Add a login (password, vault, sign in yourself), Test login, Logins tab.
- ba263ef usage display off by default; per provider only.
- earlier: builder, teams/bots, delegation, folders, create-in-place, knowledge editor, crawler suite.

## In progress

**The UX audit fix list** (highest priority; reports at
`~/Projects/danielcbaldwin/.scratch/ui-audit/{newcomer,power}/REPORT.md`). Write `design/AUDIT-RESPONSE.md`
mapping every finding ID (Sam B1–B3, M1–M11, m1–m14, P1–P5, polish; Riley B1–B4, M1–M14, minors, polish) to
fixed / changed-differently / deferred. Finish with the line `AUDIT FIXES COMPLETE` in the final report.

Plan (commit after each step):
- **A. Strict validation and builder fixes** (DONE, see commit after 9be090d; `js/realschema.js`, `semantics.spec.js`): real keys from `origin/main:internal/config`
  (trigger keys on/name/extends/abstract/enabled/filter/group/steps/hooks/policy/options/repo/shadow/gate/callable;
  `policy.budget` = window/max_cost_usd/max_tokens; isolation modes user|namespace|container, container needs
  `image`, not on a paseo runtime; filter grammar string=expr, map=AND, list=OR, `expr:`/`not_<key>`, typed
  `not_draft: true`; agent `prompt` optional; `use: cli` + `command:` label; verbs `<instance>.<verb>`; unknown
  keys are errors; Checks panel shows `conductor validate`-style output). Notes in `.scratch/*.go` (git show copies).
- **B. One secret reference syntax** (DONE e5461c6): `{{ vault "<vault>" "<key>" }}` everywhere (op:// retired); drop the named
  `secrets:` layer (alias label at most); value-source sources become Typed / Env (`${env.NAME}`) / Vault /
  Setting; broker grants `<vault>/<key>` with `secrets_via` shown.
- **C. Approvals** (DONE): full payload untruncated, Why + Impact on top, untrusted-requester banner, hashes under a
  disclosure, seal modal in words, Deny reason, real authenticator recorded, no blank fade; phone sheet with a
  sticky Approve/Deny bar.
- **D. Teams/bots on the real schema** (`origin/feat/members:internal/config/{teams,bots}.go`): team
  description/folder/collections/operators/peers/delegation.max_depth/autonomy{ceiling: reversible|observe,
  shadow, tiers}/budget{spend_cap, proactive}/isolation{sandbox optional|required, handoff_workers,
  skip_permission_prompts}/credentials{secrets_via, allow_secrets}/browser{logins, credentials}/knowledge{repo,
  seed_dir, promotion}/routing{rules[{name,match,to}], lead, fallback, timeout}; bot autonomy{shadow, tiers,
  promote, amounts, spend_cap{per_action,per_period,period}, approval_ttl}; `bot_links` top-level. Enforce the
  ceiling (promotion above it disabled + refused); one widening classifier; split Team-ceilings/Packs checks;
  folders never principals (drop `folder:` from login allow); fix "binds … undefined". Real kinds: knowledge
  {title, applies_to, evidence, confidence} + body; runbook {title, when_to_use, prerequisites, steps (list of
  strings), verification, rollback, owner}; watch is `watch/<slug>.md` {what, signal, threshold, why}; log
  {title, case, sweep, flagged_wrong} (annotate/flag append a "## Operator note" section); handoff {session};
  collections live under `teams.<t>.collections` / `bots.<b>.collections` (fields map {type, required, values},
  writers team operator|proposal, bot operator|bots, `from:` for pack-shipped).
- **E.** builder supports map-form `skill.verbs` with resource scopes; any internal error locks the list.
- **F.** run detail: per-step selection/log/diff, run-scoped audit, time axis, compare runs, consistent data,
  "What happened / What you can do".
- **P1 8–22** from Daniel's list (Get started, New bot wizard, Safety view, glossary + no issue numbers, Connect
  with Slack/GitHub, neutral new workflow, display prefs local, bulk select + one diff, ⌘K command line + keys,
  density/virtualization/paging, builder completeness, routed pack install wizard, diff fixes + revert-one,
  env tab + effective env, phone sheet/tabs/menus/cards).
- **Visual QA bar**: Playwright layout checks (overlap, overflow/clipping, abutting text, alignment, touch target
  ≥24px on phone, WCAG AA contrast both themes) on every route + dialog/drawer/wizard step; dismiss checks
  (visible close, Esc, Back for full-page flows); copy check for numbers contradicting data; screenshot review
  of every route/dialog at both viewports and themes.

## After the audit: adopt the brand/design system (queued)

Source: `origin/design/brand` (worktree `~/Projects/paseo-home/worktrees/2ccqu2oy/design-brand`, folder `design/brand/`):
live book http://100.119.192.38:8768/system.html, `BRAND.md`, `tokens/tokens.json` + generated `tokens/tokens.css`,
a compatibility file mapping prototype vars → tokens, logo assets in `assets/` (working mark **Fermata**; keep
the asset swap in one place). One clean pass: copy tokens.css + fonts, map everything onto tokens, then **remove
the compatibility layer**. Colours semantic only (no raw hex in screens; accent-as-text → text-safe accent token;
status = glyph + word with the 9 status glyphs; Needs you = accent + diamond). Type: Instrument Serif/Sans/
Martian Mono at the system scale, body 14px, 11px floor (mono 11.5); **italic serif only for human titles ≤5
words at ≥20px**, generated titles (proposals, runs, cases) in sans with a mono detail line. Density
Comfortable/Compact → `[data-density]`. Radii/borders/elevation/z-index/motion from tokens;
prefers-reduced-motion everywhere. Lucide + status glyphs at system sizes. Components match every state. Copy per
the voice guide + naming table (plain word first, term second, risk plainly, no issue numbers). Logo Fermata +
favicon. Default theme follows the OS, manual choice persisted. Then contrast check, full suite, visual QA (both
themes, both densities, desktop + phone), update the doc, AUDIT-RESPONSE.md, HANDOFF.md.

**Final report ends with a line containing only `DESIGN SYSTEM ADOPTED`** (replaces `AUDIT FIXES COMPLETE`);
the reviewers re-review once both the audit fixes and the adoption are done.

## Queued directions (status)

| Direction | Status |
|---|---|
| Original brief (no dead controls, CRUD via diff, builder, phone, Playwright) | done; re-verify in final run |
| Pipedream-style builder | done; audit adds hooks/isolation/filter/test-events (P1 18) |
| Teams; members → bots; delegation; Bots + folders; inline creation; knowledge authoring | done; teams move to the real schema in step D |
| Usage mode (off default, per provider, source-only limits) | done |
| Planned multi-user (#185) behind a toggle | **not started** (only toggle + data seeds); remove issue numbers from copy |
| Add a login | done; folder principals removed in step D |
| Environment variables ("Secrets, vars & env", scopes, effective env, import/export, restart via boot guard) | **not started** beyond `D.env` + `C.env` + conductor.env diff; audit P1 21 |
| Packs & plugins catalog (browse/detail/install wizard/sources/trust) | catalog.js written, **not routed**; audit P1 19 |
| Connectors catalog (real builtin vs plugin split, custom git connector) | **not started** (SCHEMA still marks github builtin); facts in summary: builtins web, cron, webhook, rss, command, rest, graphql, kv, sql, memory, workflow, conductor (+ lan, tunnel); everything vendor is an official plugin |
| Value-source field | done; changes in step B (canonical vault syntax, no named secrets) |
| Knowledge kinds | done; align field names to the real kinds in step D |
| Visual QA bar + shared components | components started (choice, stepper, wizard, catalog cards, chips); checks not written |
| Passkey enroll cancel + every flow has a way out | enroll done; Esc/guard done; dismiss tests not written |
| Shared folders across workflows | done |

## Known gaps

- `#/secrets/env` route doesn't exist yet (C.env points at it).
- `catalog.js` routes (`extensions/browse|item|install`) not registered; Browse still the old REG list.
- Team/bot config shapes are the prototype's own, not the real schema (step D).
- `op://` references still in data/value-source output (step B).
- Docs: usage, logins, value source, kinds, folders documented; env, catalog, multi-user, audit changes not yet.
