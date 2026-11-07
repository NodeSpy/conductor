# Conductor desktop client

A small Tauri v2 tray app for Linux and macOS. It is a client of the conductor
dashboard API and nothing more (see `../docs/design/event-journal.md`, "The
desktop client"): it signs in with your SSH key, holds its own subscription to
the attention/chat event stream so it can notify you even while the window is
closed, and shows the daemon's own dashboard in its main window.

It does not bundle a frontend app. The only page it ships is a tiny local
setup/settings form (`ui/setup.html`); after setup it navigates its window to
the daemon's own URL and the daemon serves its normal SPA there.

## Layout

```
desktop/
  src-tauri/          the Rust/Tauri app
    src/
      main.rs          entry point
      lib.rs           Tauri setup: commands, tray, windows, capabilities
      settings.rs       settings persisted in the app config dir
      persist.rs        the event stream cursor, persisted in the app data dir
      client.rs          HTTP: cookie-jar session, SSH sign-in, snapshot, dismiss
      stream.rs           SSE parsing, reconnect backoff, dedup
      notify.rs            notification planning: coalescing, chat filtering
      subscribe.rs          the background task wiring client+stream+notify+tray
      sign.rs               ssh_sign: shells out to `ssh-keygen -Y sign`
      tray.rs               tray menu/count/tooltip
      types.rs              wire types (Item, events, chat)
    capabilities/default.json   static ACL for the local setup/settings windows
    permissions/ssh-sign.toml  the app-defined permission backing `ssh_sign`
  ui/                 the static setup/settings page
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
`libssl-dev`, build tools) and runs `cargo tauri build` inside it, with the
cargo target dir and registry cache on local disk
(`$HOME/.cache/conductor-design/desktop-{target,registry}` — never
`~/Projects`, which is NFS) and copies the resulting AppImage and `.deb` into
`$HOME/.cache/conductor-design/desktop-out`.

Run the unit tests the same way:

```sh
desktop/build/linux.sh test   # or: docker run ... cargo test, see the script
```

(`linux.sh` forwards its arguments to `cargo tauri build`; for a plain
`cargo test` run, use the same `docker run` invocation substituting the
command — see the script, it's short.)

### macOS

```sh
cargo install tauri-cli --version "^2" --locked   # or: npm i -g @tauri-apps/cli
cd desktop/src-tauri
cargo tauri build            # this Mac's own architecture
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

## The web-page contract

The daemon's dashboard page detects it is running inside this app via
`window.__TAURI_INTERNALS__` (or `window.__TAURI__`, which is present because
`tauri.conf.json` sets `app.withGlobalTauri: true`) and calls:

```js
const { invoke } = window.__TAURI__.core;
const signature = await invoke("ssh_sign", { namespace, message });
```

- `namespace` must be exactly `"conductor-dashboard-login"` or
  `"conductor-dashboard-stepup"` (matching
  `internal/dashboard/auth.LoginNamespace` / `StepupNamespace`) — anything else
  is rejected before a process is even spawned (`src-tauri/src/sign.rs`).
- `message` is the exact challenge text the daemon asked to be signed (the
  dashboard verifies its own stored copy, so the app does not need to, and
  must not, modify it).
- The app shows a native confirmation dialog naming the namespace and the
  full message text before signing, every time. There is no "don't ask again"
  — each sign-in and each step-up gets its own confirmation.
- On success it resolves to the armored SSHSIG text (the string
  `ssh-keygen -Y sign` writes to stdout when it reads the message from stdin
  with no file arguments — verified against the host's installed OpenSSH:
  `ssh-keygen -Y sign -f <pubkey> -n <namespace>`, message on stdin, signature
  on stdout).

`ssh_sign` is the **only** command granted to the daemon's page. The grant is
scoped to the exact configured daemon origin: `complete_setup` (called after
the settings form saves) builds a runtime-only capability with
`CapabilityBuilder::new("remote-daemon").remote(origin).window("main").permission("allow-ssh-sign")`
and calls `app.add_capability(...)` (Tauri v2's `dynamic-acl` feature, enabled
by default) — so a daemon on a different origin, or any other page the main
window might ever navigate to, gets nothing. The static
`capabilities/default.json` (used for the local setup/settings pages) does
*not* include `ssh_sign` at all.

### IPC under the daemon's CSP

The design doc notes the daemon's dashboard serves `connect-src 'self'`.
Tauri v2's default IPC on Linux and macOS goes over a custom
`https://tauri.localhost` / `ipc://` protocol request, not a same-origin
`fetch`/`XHR`, so it is not subject to the page's `connect-src` directive —
this is expected to work unmodified and is the mechanism the "window
navigates to the daemon's own URL" design relies on. **This has not been
exercised against a real running daemon in this change** (no daemon build was
available to test against); if it turns out the custom-protocol request *is*
blocked in practice on some platform/webview combination, Tauri v2 falls back
to `window.postMessage` for IPC on an unsupported origin, which `connect-src`
also does not govern (`postMessage` is a `script-src`-adjacent capability, not
a fetch). Either path should clear a `connect-src 'self'` CSP; flagging this
as the one piece of the design not independently verified end-to-end.

## Security model of `ssh_sign`

- The app never touches the private key. `ssh-keygen -Y sign -f <path to the
  **public** key>` either finds the matching private key loaded in
  `ssh-agent`, or (if `-f` happens to point at a private key file) uses it
  directly — the app always configures and passes the public key path, per
  the design.
- Only two namespaces are signable, checked before any process is spawned
  (`sign::namespace_allowed`). This matches the daemon's own namespace
  separation (`internal/dashboard/auth.LoginNamespace` /
  `StepupNamespace`): a signature made for one purpose can never be replayed
  as another, and this app cannot be used to sign, say, a git push or a bot
  approval.
- Every call shows a native dialog with the literal namespace and message
  text before signing — the same text the daemon will independently verify
  against its own stored challenge, so there is nothing to trust in what the
  page claims; the operator reads the real thing.
- Every app command (`get_settings`, `save_settings`, `default_ssh_key`,
  `complete_setup`, `ssh_sign`) is declared in `build.rs`
  (`AppManifest::new().commands(&[...])`), which autogenerates a deny-by-default
  `allow-<command>` permission for each. `capabilities/default.json` (the local
  setup/settings windows) grants the first four — **not** `allow-ssh-sign` —
  and the runtime "remote-daemon" capability (added by `complete_setup`,
  scoped to the configured origin) grants only `allow-ssh-sign` and nothing
  else (no `core:default`, no notification/dialog/opener). Without the
  `AppManifest` declaration, Tauri only ACL-checks a *local* window's app
  commands once the app has *any* app-manifest permission file at all, and
  always checks a *remote* origin's regardless — declaring every command
  explicitly removes that conditional and makes every command, local or
  remote, opt-in only (verified by reading
  `tauri::webview::Webview::on_message` in the vendored `tauri` 2.12.5
  source).
- `tauri::ipc::RuntimeAuthority::add_capability` (what `complete_setup` calls
  to scope `ssh_sign` to the configured origin) only ever **appends** —
  reconfiguring to a new daemon does not revoke the previous origin's grant
  for the life of the running app (verified by reading
  `add_capability_inner`, which does `entry(cmd_key).or_default().extend(...)`
  for both allowed and denied commands, never replacing). So `ssh_sign`
  itself re-checks the calling webview's *current* URL against the
  *current* settings on every call (`Settings::allows_origin`,
  unit-tested) — independent of however many stale capability grants have
  accumulated, only today's configured origin can ever succeed.
- The app's own background sign-in (`client::Client::sign_in`, used to keep
  the subscription authenticated) signs the same namespaces the same way,
  but without a confirmation dialog — it is the app acting as itself, not on
  behalf of a page, and it is logged.
- Requests to `/api/v1/auth/ssh/{begin,finish}` never send an `Origin` header
  (reqwest does not add one for non-browser requests; this is simply the
  default, stated in `client.rs` so it stays true on purpose).

## What's implemented vs. notable gaps

- Sign-in, snapshot, SSE subscription with reconnect/backoff/dedup, cursor
  persistence, notification coalescing and chat filtering: implemented and
  unit-tested (`stream.rs`, `notify.rs`, `persist.rs`, `sign.rs`).
- Attention item lifecycle: `opened`/`resolved`/`updated` are all handled
  (`updated` replaces in place and never notifies); `source` (empty = raised,
  dismissable via `POST /api/v1/attention/{key}/dismiss` with
  `X-Conductor-CSRF`; non-empty = closes on its own) is modeled on `Item` but
  there is no tray "Dismiss" action yet — not required by the design, and the
  client already tolerates the field either way.
- The `reset` event (`{"type":"reset","topic":"reset","id":"<cursor>"}`)
  triggers a fresh snapshot fetch and resumes from *its* cursor, not the
  reset's own, per spec.
- Clicking a notification shows the main window and, best-effort, navigates
  to the item's `link`/conversation (stashed as notification `extra`); not
  every desktop notification backend round-trips `extra` back to the click
  handler, so on some platforms a click may just surface the app without the
  deep link. Not independently verified against each of Linux/macOS's native
  notification centers.
- Icons are placeholder art (generated, not designed) — swap
  `src-tauri/icons/*` before shipping.
- macOS build, code signing/notarization, and the dock badge
  (`WebviewWindow::set_badge_count`) are implemented but unverified: no macOS
  machine or Apple Developer identity was available in this environment.
- `cargo tauri build`'s AppImage/deb bundling was exercised in Docker (see the
  final report); the resulting binary has not been run against a live
  conductor daemon end-to-end (no daemon build available concurrently with
  this change — the server side was being implemented in parallel).
