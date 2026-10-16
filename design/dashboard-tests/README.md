# Dashboard prototype: interaction tests

Playwright tests for `design/dashboard-prototype/`. They exist because the
previous pass only checked that pages render, and dead controls got through.

| spec | proves |
|---|---|
| `crawl.spec.js` | Every route, at 1440×900 and 390×844: every interactive element on the page is clicked. A click must change the DOM (outside toasts, tooltips and regions marked `data-volatile`), the URL, saved state, or start a download. Otherwise the control must be disabled (`aria-disabled`/`disabled`) **with a reason** (`data-reason` or `title`). Controls revealed by a click (modals, menus, expanded steps) are crawled too, to depth 2; revealed controls repeating one template (same `data-act`) are sampled, 3 per handler per route. A self-test plants a dead button and an unexplained disabled one and expects both reported. |
| `flows.spec.js` | Create / edit / delete for connectors (plugin-declared settings), runtimes, hosts, isolation, members (definition, access, credential bindings, ledger operations), secrets and vars (write-only values), vaults, browser identities and logins, packs and plugins, updates, restore, raw YAML, access; each ends in diff → passkey → apply, and checks the resulting state. Validation refusals (a host in use, a credential outside the team scope) are asserted too. |
| `builder.spec.js` | Build a workflow from scratch through the picker; insert between steps; set a field from the reference picker and by autocomplete; Test a step against a past event and see its output in the next step's picker; add a parallel group and a branch; edit YAML and see the list follow; undo/redo; keyboard and drag reorder; inline validation blocking Deploy; Deploy through the diff with comments kept; pack-owned overrides. |
| `teams.spec.js` | Define a team through the diff; assign a member; a team ceiling refusing a wider member; inherit/narrow defaults; routing rules, "try a rule", routed work with provenance, operator reassignment; knowledge promotion approval; links and lead changes. |

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
