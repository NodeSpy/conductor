# Dashboard (local web UI)

Conductor serves a local web dashboard from inside the daemon. It reads the
daemon's live state — the loaded config, the run store and the event hub — and
streams run activity over Server-Sent Events. It is off by default.

> **Phase 1 (foundation).** Today the dashboard binds `localhost` only, has no
> authentication, and ships one screen: Overview/Activity. The tailnet listener,
> TLS and WebAuthn sign-in, and the remaining screens are built on top of this
> foundation. See [`docs/design/dashboard.md`](../design/dashboard.md) for the
> full design.

## Enable it

Add a `dashboard:` block to your config:

```yaml
dashboard:
  enabled: true          # off by default; a config without the block is unchanged
  listen: [localhost]    # keyword-only: ONLY `localhost` and `tailscale`
                         # (no bind addresses, no 0.0.0.0, no expose:)
  port: 7788             # default 7788; must not collide with an inbound listener
```

Reload the daemon and open `http://localhost:7788`.

- `listen` takes **keywords only**. Phase 1 serves `localhost`; `tailscale` is
  accepted by config but not yet bound.
- The port must not collide with a webhook/callable inbound listener — that is a
  config error.
- The block is validated at load, so a typo is named at startup, not at runtime.

## What it shows

The **Overview/Activity** screen answers, at a glance: is it healthy, what is it
doing, and what needs you.

- **At a glance** tiles (running, runs in the last 12 hours, needs you, version).
- **Needs you** — failed runs, newest first.
- **Last 12 hours** — a runs-per-hour strip.
- **Activity** — the run history, grouped (Needs attention / Running / Finished),
  live: new runs and status changes stream in over SSE without a reload.
- **Machine** — version, pid, uptime, config sha, dispatch state.

The **Config**, **Secrets**, **Packs**, **Plugins & connections**, **System**
(Settings / Safety / Updates) and **Logs & audit** screens read the live config
and, where editable, drive the config-edit pipeline below.

## Editing the config

The dashboard edits the live config file **one value at a time**, spliced into
the file's text so every comment, blank line, anchor and section it did not touch
stays byte-for-byte as it was written — a no-op edit produces an empty diff, and
a one-value edit a one-line diff. Each change flows through one pipeline:

1. **stage** — the edit is held server-side (`internal/configedit`); last write
   to a path wins.
2. **review** — the diff (line by line), the effects (each change in words), and
   the **widening** list surfaced loudly (any least-privilege guardrail the
   change loosens: `isolation: none`, `sandbox.agents: off`, a wildcard egress, a
   `callable:` trigger, …).
3. **validate on a copy** — the candidate is written beside the config and run
   through the daemon's own load + semantic pass, so a bad edit never reaches the
   live file.
4. **apply** — a two-step confirm (step-up): apply echoes a token bound to the
   exact reviewed candidate, so a change to the staged set after review is
   rejected as stale. The live file is backed up, replaced atomically, and the
   daemon reloads (SIGHUP).
5. **history / restore** — every apply keeps a backup; restore runs the same
   validate → apply gate in reverse.

Packs (hold/remove) and System settings/updates are edited the same way — they
stage config edits and apply through the identical gate.

## The API

Same-origin JSON under `/api/v1` (no CORS).

| Method & path | Returns |
|---|---|
| `GET /api/v1/status` | version, pid, uptime, paused, config sha |
| `GET /api/v1/runs?since&limit&cursor` | run history, newest first, cursor-paginated |
| `GET /api/v1/events?topics=run,attention` | SSE stream; `Last-Event-ID` resumes from the hub's ring |
| `GET /api/v1/config` | the live config: text, sha, generic tree, flattened scalar leaves, staged edits |
| `POST /api/v1/config/stage` · `/unstage` · `/clear` | manage the staged edit set |
| `GET /api/v1/config/review` | diff + effects + widening + candidate sha + the step-up confirm token |
| `GET /api/v1/config/validate` | SSE: validate the candidate on a copy |
| `POST /api/v1/config/apply` | apply the staged set (requires the confirm token); backs up, writes, reloads |
| `GET /api/v1/config/history` · `POST /config/restore/review` · `/restore` | backups and restore |
| `GET /api/v1/{secrets,packs,plugins,connections,system,audit}` | per-screen read models (secret values never returned) |

## Building the UI

The SPA lives in `web/` (Preact + Vite). It builds into
`internal/dashboard/dist/`, which is **committed**, so `go build` and `go install`
never need Node. Node is a build-time dependency only.

```sh
make web         # npm ci && vite build → internal/dashboard/dist
make web-types   # regenerate web/src/api/types.ts from the Go API types (tygo)
```

## End-to-end tests

`web/e2e/` holds a Playwright suite that drives the **real** dashboard — the
embedded SPA served by a throwaway daemon — in a browser. It runs inside the
pinned Playwright Docker image and is hermetic: a global setup boots a daemon
with its own temp state dir, HOME and port (no live daemon, config or secrets
touched), seeds real run history by dispatching a trivial `command` trigger, and
tears the daemon down afterwards.

```sh
make dashboard-e2e   # static daemon build + Playwright in Docker
```

It covers, on **Chromium desktop and WebKit iPhone 15 (OS dark)**:

- the Overview renders the daemon's real run history (rows/counts asserted
  against the API);
- a live run dispatched mid-test updates the Overview over SSE, no reload;
- `/api/v1/status` is reflected in the UI (version, pid, config sha, dispatch);
- the app shell, nav and theme render with zero console errors and zero failed
  network requests on load;
- no horizontal overflow at desktop and phone widths, and dark mode renders.

Override the image with `PLAYWRIGHT_IMAGE=…` and the daemon port with
`DASH_E2E_PORT=…`.
