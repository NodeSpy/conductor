# Dashboard prototype: interaction tests

Playwright tests for `design/dashboard-prototype/`. They exist because the
previous pass only checked that pages render, and dead controls got through.

| spec | proves |
|---|---|
| `crawl.spec.js` | Every route, at 1440×900 and 390×844: every interactive element on the page is clicked. A click must change the DOM (outside toasts, tooltips and regions marked `data-volatile`), the URL, saved state, or start a download. Otherwise the control must be disabled (`aria-disabled`/`disabled`) **with a reason** (`data-reason` or `title`). Controls revealed by a click (modals, menus, expanded steps) are crawled too, to depth 2; revealed controls repeating one template (same `data-act`) are sampled, 3 per handler per route. A self-test plants a dead button and an unexplained disabled one and expects both reported. |
| `flows.spec.js` | Create / edit / delete for connectors (plugin-declared settings), runtimes, hosts, isolation, bots (definition, access, credential bindings, ledger operations), secrets and vars (write-only values), vaults, browser identities and logins, packs and plugins, updates, restore, raw YAML, access; each ends in diff → passkey → apply, and checks the resulting state. Validation refusals (a host in use, a credential outside the team scope) are asserted too. |
| `builder.spec.js` | Build a workflow from scratch through the picker; insert between steps; set a field from the reference picker and by autocomplete; Test a step against a past event and see its output in the next step's picker; add a parallel group and a branch; edit YAML and see the list follow; undo/redo; keyboard and drag reorder; inline validation blocking Deploy; Deploy through the diff with comments kept; pack-owned overrides. |
| `folders.spec.js` | The Bots tree (folders → teams → bots, solo bots, roll-ups, New bot/team/folder); create a folder; Move to…; drag a team onto a folder; rename and delete folders, all through the diff; search; breadcrumbs; the phone drill-down; folder filters on Needs you, approvals, run history and browsers. |
| `create.spec.js` | Create in place, one combined diff: a new bot from a team page (landing back on the team), a connector from inside a workflow step, a secret from inside a credential binding (value to the vault, never in state), a folder from a move dialog, a team from a bot's definition; and an audit that **every entity picker has a create option** (and that every kind of picker was seen). |
| `knowledge.spec.js` | Knowledge by kind: New… creates knowledge, watch items (derived paths, rename by title, schema refusal); the runbook step editor (command and verb per step, reorder, remove) and citations; no free path outside a collection; declaring a collection in Config → Collections through the diff and creating in it with a free path; a malformed runbook proposal's schema errors, fixed by approving with changes; approve with changes, deny with a reason, bulk; import mapping (runbook detected, knowledge needing fields); bot memory edit, pin, blame, log flagged but never rewritten; a bot-commit conflict rebased; the unsaved guard, tag filters and search. |
| `teams.spec.js` | Old `#/members` links redirecting to `#/bots`; solo bots listed; delegation edges with status and result on cases; closing a delegated case reporting back; delegating to a teammate under the original requester; cross-team delegation only where linked (and none for solo bots); the team's ask & delegate graph and delegation reach; a trigger handing work to a team (`bot: { team: … }`) and testing the routing. Define a team through the diff; assign a bot; a team ceiling refusing a wider bot; inherit/narrow defaults; routing rules, "try a rule", routed work with provenance, operator reassignment; knowledge promotion approval; links and lead changes. |
| `usage.spec.js` | `usage.mode`: off by default with no usage or cost UI on any screen (cloud spend still shown, budget forms keep run/turn/token caps); subscription without a source shows per-provider counts observed by conductor and no limits or tokens; adding a usage source through Config → General (validated, diffed, applied) adds plan limits and tokens per provider labelled with the source; api shows dollars per provider and dollar budget fields; every usage figure sits inside one provider, with no totals across providers; switching back to off removes every trace. |
| `logins.spec.js` | Add a login: username & password with the TOTP preview, stored write-only behind a passkey and shown as "set"; from a vault with the 1Password item browser, search and field mapping, stored as `op://` references; sign in yourself goes to the takeover (disabled with a reason on the phone); Test login success, refusal and a push that needs you; usage history and revoke through the diff; "+ Add a login" inside a credential picker; no secret value in the DOM, the state or localStorage. |
| `valuesource.spec.js` | The value-source field: each source on a login form (typed, env var, setting, secret, vault with the label-only browser); a mixed-source login (typed username, password and TOTP from the same 1Password item via the shortcut); resolve states (resolves, does not resolve with the reason, unreachable vault, plain-text env warning); inline "+ Add env var" (into `conductor.env`, same review) and "+ New secret"; a sensitive field blocked from a setting; the same component in a connector form and a workflow step input; a typed password never renders after save. |
| `semantics.spec.js` | Config semantics match the real loader: strict decode (unknown keys), budget keys (window / max_cost_usd / max_tokens), typed filter values and the filter grammar (map/list/expression, expr:), isolation rules (not on paseo, container needs an image, no "none" mode), optional prompt, `<instance>.<verb>` grants, broker grants need `secrets_via: broker`, the bot step shape; the builder emits real keys; the isolation editor; map-form `skill.verbs` with resource scopes; pack overlay keys that match nothing fail, and the Checks panel shows the `conductor validate` output. |

Every test starts from fresh demo data and fails on any console error or
uncaught exception (`fixtures.js`). `window.__PROTO_FAST` makes the
simulated authenticator and reload take milliseconds.

## Running

```sh
design/dashboard-tests/run.sh                                # everything, both viewports
design/dashboard-tests/run.sh crawl.spec.js --project=phone  # one spec, one viewport
BASE_URL=http://127.0.0.1:8767 design/dashboard-tests/run.sh # against a running prototype server
```

`run.sh` installs `@playwright/test` (pinned to 1.58.2, which matches the
cached `~/.cache/ms-playwright/chromium-1208`) into a temp project at
`$DEPS_DIR` (default `~/Projects/.cache/dashboard-tests-node`), never into
the repo or globally, and runs from there. Without `BASE_URL` the suite
serves `design/dashboard-prototype/` itself with `python3 -m http.server` on
`127.0.0.1:$PORT` (default 8799). Results and failure screenshots go to
`$DEPS_DIR/out/`. `WORKERS` sets parallelism (default 2).
