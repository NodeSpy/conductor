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

## The API

Same-origin JSON under `/api/v1` (no CORS). Phase 1 endpoints:

| Method & path | Returns |
|---|---|
| `GET /api/v1/status` | version, pid, uptime, paused, config sha |
| `GET /api/v1/runs?since&limit&cursor` | run history, newest first, cursor-paginated |
| `GET /api/v1/events?topics=run,attention` | SSE stream; `Last-Event-ID` resumes from the hub's ring |

## Building the UI

The SPA lives in `web/` (Preact + Vite). It builds into
`internal/dashboard/dist/`, which is **committed**, so `go build` and `go install`
never need Node. Node is a build-time dependency only.

```sh
make web         # npm ci && vite build → internal/dashboard/dist
make web-types   # regenerate web/src/api/types.ts from the Go API types (tygo)
```
