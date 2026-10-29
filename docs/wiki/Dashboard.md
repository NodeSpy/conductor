# Dashboard (local web UI)

Conductor serves a local web dashboard from inside the daemon. It reads the
daemon's live state — the loaded config, the run store and the event hub — and
streams run activity over Server-Sent Events. It is off by default.

With operators configured, every API call needs a signed-in operator: by
passkey, by signing a challenge with the operator's SSH key, or by password plus
an authenticator-app code (see [Signing in](#signing-in)). The sign-in methods
are described in
[`docs/design/dashboard-auth-methods.md`](../design/dashboard-auth-methods.md).

## Enable it

Add a `dashboard:` block to your config:

```yaml
dashboard:
  enabled: true          # off by default; a config without the block is unchanged
  listen: [localhost]    # keywords only: `localhost` and `tailscale`
  port: 7788             # default 7788; must not collide with an inbound listener
```

Reload the daemon and open `http://localhost:7788`.

- `listen` takes **keywords only**: `localhost` (127.0.0.1 and ::1) and
  `tailscale` (the node's tailnet addresses, HTTPS with the MagicDNS
  certificate). Other addresses go under `bind:` (below).
- The port must not collide with a webhook/callable inbound listener — that is a
  config error.
- The block is validated at load, so a typo is named at startup, not at runtime.
- Every listener checks the request's `Host` header against the names it
  serves. The localhost listener answers only to `localhost`, `127.0.0.1` and
  `[::1]`; a reverse proxy in front of it must pass one of those as `Host`, or
  use a `bind:` entry with its own hostnames.

### Other listeners: `tailscale.tls` and `bind`

```yaml
dashboard:
  tailscale:
    tls: off               # default on. off: plain HTTP on the tailnet addresses
  bind:
    - address: 0.0.0.0:7790
      hostnames: [conductor.home.arpa]
      cert_file: /etc/conductor/tls/dashboard.crt
      key_file: /etc/conductor/tls/dashboard.key
    - address: 192.168.1.20:7791
      hostnames: [192.168.1.20]
      allow_plain_http: true
```

- `tailscale.tls: off` serves the tailnet listener over plain HTTP. WireGuard
  still encrypts the hop and the peer is still checked with Tailscale WhoIs,
  but the browser sees plain HTTP, so passkeys are not offered there.
- A `bind` entry is an IP and port (not the dashboard port), and `hostnames` is
  required: requests with any other `Host` are refused.
- With `cert_file` and `key_file` the entry serves HTTPS, and each non-IP
  hostname is a passkey relying party.
- Without a certificate, an entry on a non-loopback address needs
  `allow_plain_http: true`, and the daemon logs a warning at boot. On plain HTTP
  the session cookie and everything on the page cross the network unencrypted.

## Signing in

| Connection | Passkey | SSH key | Password + code |
|---|---|---|---|
| `localhost` | yes | yes | yes |
| HTTPS (tailnet, `bind` with a certificate) | yes, on a hostname | yes | yes |
| Plain HTTP (`tailscale.tls: off`, `bind` without a certificate) | no | yes | yes |

Browsers only allow passkeys in a secure context (HTTPS, or localhost) on a
hostname. On plain HTTP the sign-in page and the app show an "Unencrypted
connection" notice, and the session cookie is a plain `HttpOnly`,
`SameSite=Strict` cookie instead of a `__Host-` `Secure` one. A session is only
accepted on the kind of connection it was created on.

### First sign-in from another machine: the SSH key

The SSH key in `operators.<name>.ssh_key` needs no setup in the dashboard.
Pick **SSH key**, enter the operator name, and either:

- **Sign on this computer** — run the command the page shows (it pipes the
  challenge into `ssh-keygen -Y sign -n conductor-dashboard-login`), or
  `conductor dashboard sign '<challenge>' --key <key>`, and paste the signature.
- **Approve from another device** — the page shows an 8-character login code.
  On any machine with the key and conductor installed, run:

  ```sh
  conductor dashboard approve-login ABCD-EFGH --url https://box.example:7790 --operator ops
  ```

  The command fetches the request, refuses it unless the challenge names the
  same origin as `--url` and the same operator, shows where the request came
  from, asks for confirmation (`--yes` skips the question), signs it and posts
  the signature. The waiting page signs in within a couple of seconds.

The challenge is bound to the operator, the page's origin and a nonce, and
expires after 5 minutes. A login code works once, allows 3 wrong signatures,
and is only accepted from the listener and host that created it. Pending
challenges are capped per client.

### Password and authenticator app

Set a password from **Access** (signed in by any method), or from the one-time
enrolment link (`conductor dashboard enroll`, then choose "A password +
authenticator"). In Access, **Authenticator app** shows a QR code and the key;
it becomes active once you enter a current code from the app.

- Passwords are 12 to 256 characters, stored as an argon2id hash.
- Codes are RFC 6238 TOTP (SHA-1, 30 seconds, 6 digits). A code from one step
  before or after the current one is accepted, and a code (or any earlier one)
  is accepted only once.
- By default a password signs in only together with a code.
  `auth.password_requires_totp: false` lets an operator with no authenticator
  sign in with the password alone; an operator who has one still enters a code.
- 5 wrong passwords or codes within 15 minutes lock that operator's password
  and code sign-in (and code confirmations) for 15 minutes, doubling up to 4
  hours, whichever client they come from. Passkey and SSH
  sign-in are not affected by that lock. Every method also counts failures per
  client.
- Lost the authenticator or the password: `conductor dashboard reset
  --operator <name>` (signed with the SSH key) removes the operator's passkeys,
  password, authenticator and sessions.

### Confirming sensitive actions (step-up)

Approvals, purchases, widening config changes, secrets, browser take-over and
credential changes ask you to confirm first. The dialog offers the methods
`auth.stepup` allows that you have on this connection: passkey, SSH key (paste
or approve-login, as above, in the `conductor-dashboard-stepup` namespace) or an
authenticator code. A password confirms only with
`password_requires_totp: false` and `password` listed in `stepup`. The
confirmation is single use, bound to the action, and the audit log records the
method.

Adding or removing a password, authenticator or passkey from Access is itself
confirmed this way.

### Configuration

```yaml
dashboard:
  auth:
    methods: [passkey, ssh, password]   # default
    password_requires_totp: true        # default
    stepup: [passkey, ssh, totp]        # default
```

A config without `auth:` uses these defaults. Setting
`password_requires_totp: false`, `allow_plain_http: true` or
`tailscale.tls: off` from the dashboard's config editor counts as a widening
change and needs a confirmation.

`conductor dashboard status` lists, per operator, passkeys, sessions, and
whether a password and an authenticator are set.

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
