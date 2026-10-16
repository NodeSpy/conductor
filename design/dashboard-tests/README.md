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
| `knowledge.spec.js` | Operator-authored knowledge: create, edit, preview, save as one operator commit, rename, delete, revert from history; approve with changes; deny with a reason (sent back to the bot); bulk approve; upload a markdown file; edit and pin bot memory, blame-lite; a simulated bot commit causing a conflict, rebased explicitly; the unsaved-changes guard; search across team knowledge and bot memory. |
| `teams.spec.js` | Old `#/members` links redirecting to `#/bots`; solo bots listed; delegation edges with status and result on cases; closing a delegated case reporting back; delegating to a teammate under the original requester; cross-team delegation only where linked (and none for solo bots); the team's ask & delegate graph and delegation reach; a trigger handing work to a team (`bot: { team: … }`) and testing the routing. Define a team through the diff; assign a bot; a team ceiling refusing a wider bot; inherit/narrow defaults; routing rules, "try a rule", routed work with provenance, operator reassignment; knowledge promotion approval; links and lead changes. |

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
