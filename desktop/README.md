# Conductor desktop client

A small Tauri v2 app for Linux and macOS, with its own Needs you, Chat and
Bots screens. It is a client of the conductor dashboard API and nothing more
(see `../docs/design/event-journal.md`, "The desktop client"): it signs in
with your SSH key, holds its own subscription to the attention/chat/ticket
event stream so it can notify you even while the window is closed, and shows
its own bundled UI in its main window — it never loads the daemon's web
dashboard or any other remote origin.

## Layout

```
desktop/
  app/                the local UI: Vite + Preact + TypeScript
    src/
      main.tsx          entry point
      app.tsx           top-level route switch + the "not configured yet" gate
      router.ts         a tiny hash router (#/needs, #/chat, #/bots, #/setup)
      state.ts          app-wide signals (attention, conversations, bots) + live updates
      api/
        client.ts         typed wrappers over the Tauri commands, one per endpoint
        invoke.ts          the real invoke, or the `?mock` fixture layer
        mock.ts            fixture data for `?mock` (dev + conformance screenshots)
        types.ts            wire types mirroring the Rust/Go shapes
      components/        Shell (rail + top bar), Approval, icons
      routes/             Needs, Chat, Bots, BotPage, Setup
      styles/             copied from design/dashboard-prototype (tokens.css verbatim)
  src-tauri/          the Rust/Tauri app
    src/
      main.rs          entry point
      lib.rs           Tauri setup: AppState (shared Client), commands, tray, windows
      settings.rs       settings persisted in the app config dir
      persist.rs        the event stream cursor, persisted in the app data dir
      client.rs          HTTP: cookie-jar session, SSH sign-in, every daemon call
      stream.rs           SSE parsing, reconnect backoff, dedup
      notify.rs            notification planning: coalescing, chat filtering
      subscribe.rs          the background task: client+stream+notify+tray, plus
                             emitting each event to the main window for live UI updates
      sign.rs               shells out to `ssh-keygen -Y sign` (sign-in + step-up)
      tray.rs               tray menu/count/tooltip
      types.rs              wire types (Item, events, chat)
    capabilities/default.json   ACL for the local main/settings windows (no remote origin)
  build/              Docker-based Linux build (the host has no webkit2gtk)
  ci/desktop.yml       a GitHub Actions workflow template (not wired in yet)
```

## Building

### Linux

The host this was built on does not have `webkit2gtk-4.1` installed, and
per policy we don't install system packages on it. Build inside Docker
instead:

```sh
desktop/build/linux.sh
```

This builds `desktop/build/linux.Dockerfile` (`rust:latest` +
`libwebkit2gtk-4.1-dev`, `libayatana-appindicator3-dev`, `librsvg2-dev`,
`libssl-dev`, Node.js, build tools), installs `desktop/app`'s npm
dependencies, and runs `cargo tauri build` inside it — which in turn runs
`desktop/app`'s `npm run build` (`tauri.conf.json`'s `beforeBuildCommand`) to
produce the bundled UI before packaging. Caches live on local disk
(`$HOME/.cache/conductor-design/desktop-{target,registry}` and
`desktop-app/node_modules` — never `~/Projects`, which is NFS), and the
resulting AppImage and `.deb` land in
`$HOME/.cache/conductor-design/desktop-out`.

Run the unit tests the same way:

```sh
desktop/build/linux.sh test   # or: docker run ... cargo test, see the script
```

(`linux.sh` forwards its arguments to `cargo tauri build`; for a plain
`cargo test` run, use the same `docker run` invocation substituting the
command — see the script, it's short.)

### The local UI on its own

```sh
cd desktop/app
npm install
npm run dev        # vite dev server
npm run build      # tsc --noEmit && vite build -> dist/
```

Append `?mock` (or `?mock=empty`) to the dev/preview URL to run the UI
against fixture data (`src/api/mock.ts`) with no Tauri runtime and no
daemon — used for local iteration and for conformance screenshots against
the design prototype.

### macOS

```sh
cargo install tauri-cli --version "^2" --locked   # or: npm i -g @tauri-apps/cli
cd desktop/src-tauri
cargo tauri build            # this Mac's own architecture; builds desktop/app first
# A universal (Intel + Apple Silicon) build also needs both targets:
#   rustup target add aarch64-apple-darwin x86_64-apple-darwin
#   cargo tauri build --target universal-apple-darwin
```

The `.app` lands in `target/release/bundle/macos/` and the `.dmg` in
`target/release/bundle/dmg/`. If the `.dmg` step fails with "Resource busy",
Finder or Spotlight was holding the temporary disk image; run it again.

The unsigned build produces a `.dmg` that Gatekeeper will refuse to open by
double-click. To run it anyway:

- Right-click the app → Open → Open (lets you bypass Gatekeeper once), or
- `xattr -dr com.apple.quarantine /Applications/Conductor.app` after installing.

Signing and notarization are not configured (no Apple Developer identity was
available here). `desktop/ci/desktop.yml` has a commented block showing where
`APPLE_CERTIFICATE`, `APPLE_CERTIFICATE_PASSWORD`, `APPLE_SIGNING_IDENTITY`,
`APPLE_ID`, `APPLE_PASSWORD` and `APPLE_TEAM_ID` plug in.

## CI

`desktop/ci/desktop.yml` is a template, deliberately **not** under
`.github/workflows/` yet — move it there to enable it. It builds Linux
(AppImage + deb) and macOS (universal dmg, unsigned) and runs `cargo test` on
both.

## Architecture

- **All daemon traffic goes through Rust.** One shared, signed-in `Client`
  lives in `AppState` (`lib.rs`) and is used both by the background
  subscription (`subscribe.rs`) and by every Tauri command the UI calls.
  Each command maps to exactly one daemon endpoint (`attention`, `dismiss`,
  `approval_view`, `decide`, `conversations`, `messages`, `send_message`,
  `mark_read`, `bots`, `bot`, `bot_proposals`, `open_in_browser`) — there is
  no generic proxy command. A `401` is handled once, in one place
  (`lib.rs::with_client`): sign in again and retry the same call.
- **The main window never loads a remote origin.** It only ever shows
  `desktop/app`'s bundled UI (`frontendDist` in `tauri.conf.json`). There is
  no `ssh_sign`/`desktop_operator` command exposed to a page, and no runtime
  "remote-daemon" capability: those existed only because an earlier design
  had the window navigate to the daemon's own dashboard. The settings window
  loads the same bundled UI at `#/setup`.
- **Decisions use SSH step-up, not a page-initiated signature.** Approving or
  denying in Needs you, a purchase, or a bot's proposals calls the `decide`
  command, which: GETs the approval view again (the daemon "describes" the
  action from a GET in the same session before it will mint a step-up),
  begins an SSH step-up for the decision's digest (`digests.approve` or
  `digests.deny` from that view — never computed client-side), signs the
  challenge with the configured key, finishes the step-up, then POSTs the
  decision with the resulting token in `X-Conductor-Stepup` and the cached
  CSRF token in `X-Conductor-CSRF`. The in-app Approve/Deny button is the
  confirmation; there's no extra native dialog on top of it (unlike the old
  page-initiated `ssh_sign`, which signed on behalf of a daemon page it had
  to confirm with the operator first).
- **Live updates.** `subscribe.rs` follows `attention,chat,ticket` and, on
  every parsed event (plus `reset`), emits a small `{topic, type}` payload to
  the main window as `conductor://event`. `src/state.ts`'s `wireLiveUpdates`
  listens for it and refetches the relevant snapshot — the UI doesn't know
  anything about SSE, reconnects or dedup; that all stays in Rust.
- **Notifications and the tray open in-app routes.** A notification's `link`
  is an in-app hash route (`#/needs/<key>`, `#/chat/<kind>/<name>`), not a
  daemon URL (see `notify.rs`); clicking it emits `conductor://navigate` to
  the main window instead of calling `WebviewWindow::navigate`. The tray's
  "Open dashboard in browser" uses the `opener` plugin to open the
  configured daemon URL in the system browser — the one place this app still
  points at the daemon's own web dashboard, and only ever through the
  system's browser, not this app's window.

## What's implemented vs. notable gaps

- Sign-in, snapshot, SSE subscription with reconnect/backoff/dedup, cursor
  persistence, notification coalescing and chat filtering: implemented and
  unit-tested (`stream.rs`, `notify.rs`, `persist.rs`, `sign.rs`).
- The `Client`'s decision flow (approval view → SSH step-up begin/sign/finish
  → decision POST with `X-Conductor-Stepup`), CSRF attachment, the
  form-encoded `mark_read` call, and daemon error decoding
  (`{"error":{"code","message","hint"}}`) are unit-tested against a local
  fake HTTP server (`tiny_http`, dev-only) in `client.rs`.
- Attention item lifecycle: `opened`/`resolved`/`updated` are all handled
  (`updated` replaces in place and never notifies); `source` (empty = raised,
  dismissable via the `dismiss` command; non-empty = closes on its own) is
  modeled on `Item`, and the Needs you screen dismisses a raised item inline.
- The `reset` event (`{"type":"reset","topic":"reset","id":"<cursor>"}`)
  triggers a fresh snapshot fetch and resumes from *its* cursor, not the
  reset's own, per spec, and is relayed to the UI so it refetches too.
- Clicking a notification shows the main window and, best-effort, navigates
  to its in-app route (stashed as notification `extra`); not every desktop
  notification backend round-trips `extra` back to the click handler, so on
  some platforms a click may just surface the app without the deep link. Not
  independently verified against each of Linux/macOS's native notification
  centers.
- Icons are placeholder art (generated, not designed) — swap
  `src-tauri/icons/*` before shipping.
- The screens cover what the plan scoped: Needs you (proposal/purchase
  approvals, raised-item dismiss, "open in browser" for anything else with a
  link), Chat (conversation list + thread + composer, attachments listed by
  name), Bots (teams as the page) and a bot page with its proposals approved
  inline. There is no settings/permission editing on a bot's page (the
  prototype's much larger config-editing surface) — out of scope here; the
  daemon has no endpoints for it from this client either.
- macOS build, code signing/notarization, and the dock badge
  (`WebviewWindow::set_badge_count`) are implemented but unverified: no macOS
  machine or Apple Developer identity was available in this environment.
- `cargo tauri build`'s AppImage/deb bundling was exercised in Docker; the
  resulting binary has not been run against a live conductor daemon
  end-to-end in this change (see the final report for what was and wasn't
  verified against a throwaway daemon).
