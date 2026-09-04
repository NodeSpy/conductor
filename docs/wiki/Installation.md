# Installation

conductor ships as a single Go binary with no database and no web UI. It installs to
`~/.local/bin`, keeps its config at `~/.config/conductor/`, and runs as a per-user
background service (`systemd --user` on Linux, `launchd` on macOS). This page covers
the released-binary installer, building from source, running the service, and the
built-in auto-update mechanism.

## Install (released binary, one-liner)

```sh
curl -fsSL https://raw.githubusercontent.com/NodeSpy/conductor/main/scripts/install-release.sh | bash
```

This fetches the release asset for your OS/arch (mac amd64/arm64, linux
amd64/arm64/386) directly from GitHub releases — no `gh`, no auth. Pin a version
with `... | bash -s -- v0.6.4`.

What it does:

1. Downloads the binary to `~/.local/bin/conductor` and runs `conductor version` to
   confirm it works.
2. Warns if `~/.local/bin` isn't on `PATH`.
3. Seeds `~/.config/conductor/` if it doesn't already have a `config.yaml`:
   - `config.yaml` — a starter config with the github integration present but
     `enabled: false`.
   - `conductor.env` — placeholder secrets (`GH_WEBHOOK_SECRET=`,
     `GH_SMEE_URL=https://smee.io/CHANGE_ME`), written `chmod 600`.
   - `config.example.yaml` — the full annotated reference config, dropped alongside
     for lookup; not loaded by conductor itself.
4. Offers to install the background service (see below).

After installing, fill in `~/.config/conductor/config.yaml` and
`~/.config/conductor/conductor.env`, then run `conductor validate` before starting
the service — see [[Configuration]] and [[GitHub-App-Setup]].

### Updating

```sh
conductor update                 # self-update to the latest release (uses gh)
conductor update --tag v0.2.0    # pin a version
conductor update --force         # reinstall even if already current
```

`conductor update` also regenerates the installed service unit if its template
changed, reloads it, and restarts an already-running service into the new binary —
you never need to reinstall the service after an upgrade.

Or let the daemon update itself:

```yaml
update:
  auto: true
  interval: 10m       # default; check cadence
  apply: true         # restart into the new binary after updating (default true)
```

## Install from source

```sh
git clone https://github.com/NodeSpy/conductor.git
cd conductor
./scripts/install.sh          # builds, seeds config, then prompts to install the service
```

Requires the local `paseo` CLI (authenticated to your daemon) and `gh` on `PATH`.

## Running as a service

The installer offers this during setup; the binary also manages it directly — it
generates the right unit for the host OS (`systemd --user` on Linux, a `launchd`
LaunchAgent on macOS):

```sh
conductor service install      # write the unit and start it (if the config validates)
conductor service sync         # rewrite the unit if its template changed, and reload
conductor service uninstall    # stop and remove it
```

`install` only *starts* the service once `conductor validate` passes against the
current config — a fresh install with a broken/disabled config gets the unit
written but left stopped, so it can't crash-loop.

Logs:

- Linux: `journalctl --user -u conductor -f`
- macOS: `tail -f ~/Library/Logs/conductor.log`

On Linux, `loginctl enable-linger "$USER"` keeps the service running across logout
and reboot; the installer runs this for you.

Secrets live in `~/.config/conductor/conductor.env`; the daemon loads them itself
at startup, so both systemd and launchd work without extra environment wiring in
the unit.

**PATH is baked into the unit.** A `--user` service otherwise inherits a minimal
PATH and can't find `paseo`, `gh`, `go`, or `claude` under `~/.local/bin`. The
generated unit sets `PATH` to `~/.local/bin` plus the standard bin dirs (including
Homebrew and Go) plus your install-time PATH. If a tool lives somewhere unusual,
add a systemd drop-in (`Environment=PATH=…`) or set `PATH` directly in
`conductor.env`.

## Behavior

### Auto-update

`update.auto: true` has the running daemon poll `gh api
repos/NodeSpy/conductor/releases/latest` on `interval` (default `10m`). Each check
is a **conditional request**: conductor remembers the release repo's `ETag` and
sends `If-None-Match`, so an unchanged repo answers `304 Not Modified` — a reply
GitHub doesn't bill against your rate limit. That makes a tight interval
effectively free, so a newly-published release is installed within one interval
rather than hours, with no webhook or per-operator setup required.

With `apply: true` (default), the daemon restarts into the new binary once it's
downloaded:

- Under a service manager, it exits cleanly so systemd (`Restart=always`) or
  launchd (`KeepAlive`) relaunches it — a real restart that re-reads the unit's
  environment.
- Run in the foreground (not as a service), it re-execs in place instead.

With `apply: false`, the new binary is downloaded and staged on disk, and the
daemon logs `restart to apply` instead of restarting itself — you decide when to
cycle the service.

See [[Configuration]] for the full `update:` schema and [[Commands]] for the
`conductor update` / `conductor service` command reference.
