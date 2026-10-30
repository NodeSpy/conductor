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
  hostname is a passkey relying party. It also sends
  `Strict-Transport-Security` (one year, without `includeSubDomains`), so a
  browser that has opened it once won't follow a link to plain HTTP on the same
  hostname. Browsers apply this to every port on the hostname, not to IP
  addresses, and not on a first visit. `hsts: false` on the entry turns it off,
  for a hostname where another plain-HTTP service must stay reachable. That
  counts as loosening a guardrail when changed from the editor.
- Without a certificate, an entry on a non-loopback address needs
  `allow_plain_http: true`, and the daemon logs a warning at boot. On plain HTTP
  the session cookie and everything on the page cross the network unencrypted.
- An entry on a loopback address without a certificate is a secure context only
  when its hostnames are loopback names (`localhost`, `127.0.0.1`, `::1`). Under
  any other name (`box.lan` pointing at 127.0.0.1) browsers treat it as plain
  HTTP, so passkeys are not offered there.

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

- **Sign on this device** — run the command the page shows (it pipes the
  challenge into `ssh-keygen -Y sign -n conductor-dashboard-login`), or
  `conductor dashboard sign '<challenge>' --key <key>`, and paste the signature.
- **Approve from another device** — the page shows an 8-character login code.
  On any machine with the key and conductor installed, run:

  ```sh
  conductor dashboard approve-login ABCD-EFGH --url https://box.example:7790 --operator ops
  ```

  The command fetches the request and refuses it unless the challenge names the
  same origin as `--url` and the same operator. It then shows the address the
  request came from, the device it says it is, when it started and when it
  expires — and for a confirmation (step-up) what is being confirmed and the
  first 12 characters of its digest, which the waiting page shows too. It asks
  before signing (`--yes` skips the question), signs it and posts the
  signature. The waiting page signs in within a couple of seconds. A
  confirmation the dashboard didn't describe is refused outright.

  Against a `bind` listener with a certificate your machine doesn't trust (one
  you made yourself), point `SSL_CERT_FILE` at that certificate rather than
  using a plain-HTTP listener:

  ```sh
  SSL_CERT_FILE=/etc/conductor/tls/dashboard.crt \
    conductor dashboard approve-login ABCD-EFGH --url https://conductor.home.arpa:7790 --operator ops
  ```

  **Approve only a request you started yourself, just now.** A look-alike page
  can start a sign-in on your real dashboard and show you its code; the command
  can't tell that apart from your own browser, and approving it signs the other
  side in as you. The same goes for pasting a signature the page asks for.
  Passkeys don't have this weakness: the browser ties a passkey to the site
  it's actually on.

The challenge is bound to the operator, the page's origin and a nonce, and
expires after 5 minutes. A login code works once, allows 3 wrong signatures,
and is only accepted on the listener, host and kind of connection that created
it. Pending challenges are capped per client, per network and overall;
confirmations have their own cap per session.

### Password and authenticator app

Set a password from **Access** (signed in by any method), or from the one-time
enrolment link (`conductor dashboard enroll`, then choose "A password +
authenticator"). In Access, **Authenticator app** shows a QR code and the key
(in 4-character blocks; **Copy** copies it without spaces); it becomes active
once you enter a current code from the app. **Replace…** does the same with a
new key: your current app keeps working until a code from the new one confirms
it.

- Passwords are 12 to 256 characters, stored as an argon2id hash.
- Codes are RFC 6238 TOTP (SHA-1, 30 seconds, 6 digits). A code from one step
  before or after the current one is accepted, and each code is accepted only
  once.
- By default a password signs in only together with a code.
  `auth.password_requires_totp: false` lets an operator with no authenticator
  sign in with the password alone; an operator who has one still enters a code.
- 5 wrong passwords or codes within 15 minutes lock that operator's password
  and code sign-in and confirmations for 15 minutes, doubling up to 4
  hours, whichever client they come from. The lock survives a restart. Passkey
  and SSH sign-in are not affected by that lock. Every method also counts
  failures per address (IPv4 or IPv6), and password and login-code guesses
  are throttled per network (an IPv4 /24 or IPv6 /64, 50 in 15 minutes), so a
  neighbour on the same LAN can't lock you out of SSH or passkeys. On
  localhost only the operator lock and the throttle apply. Guesses sent at the
  same moment count before they are checked, so a burst gets no more tries
  than a sequence. The error says which of these refused you.
- Lost the authenticator or the password: `conductor dashboard reset
  --operator <name>` (signed with the SSH key) removes the operator's passkeys,
  password, authenticator and sessions.

### Confirming sensitive actions (step-up)

Approvals, purchases, config changes and restores, secrets, browser take-over
and credential changes ask you to confirm first.
The dialog shows the dashboard's own description of what you are confirming,
the same text `approve-login` prints, and the start of its digest. For a
proposal or a purchase that is the canonical action (a proposal's verb and
options, a purchase's merchant, items, total and card), pretty-printed, with
the requester's own summary after it on a line marked as theirs. For a config
change or a restore it is every guarded section and every widening change,
then each value that changes, with its old and new value: every one outside
`pricing` and `usage.mode`, and every widening one, in full, however it was
staged. Only the other changes are shortened (past 20 or 200 characters, and
marked when they are). Control and
bidi characters show as escapes such as `\x1b` or `\u202e`, in the dialog and
in the terminal. The dialog offers the methods
`auth.stepup` allows that you have on this connection: passkey, SSH key (paste
or approve-login, as above, in the `conductor-dashboard-stepup` namespace) or an
authenticator code. A password confirms only with
`password_requires_totp: false` and `password` listed in `stepup`, and needs
the authenticator code too when you have one. The
confirmation is single use, bound to the action, and the audit log records the
method.

Sign-ins, confirmations, `approve-login` approvals, config applies and
restores, approval and purchase decisions, and messages and ticket changes made
from the dashboard are written to the audit log with the operator, the session,
how it signed in, the remote address, the listener and the digest that was
confirmed. An apply also records whether it needed a
confirmation, and an `approve-login` entry records both the approving and the
requesting address.

Adding or removing a password, authenticator or passkey from Access is itself
confirmed this way.

### Configuration

```yaml
dashboard:
  auth:
    methods: [passkey, ssh, password]   # default
    password_requires_totp: true        # default
    stepup: [passkey, ssh, totp]        # default
    config_stepup: all                  # default
```

A config without `auth:` uses these defaults. With sign-in on, every apply
from the dashboard's config editor, and every restore, needs a confirmation
(`config_stepup: all`). Most of the config runs something on this machine:
a trigger or workflow step can be a shell command, and the dashboard and store
blocks decide where the sign-in state lives. A session cookie on its own must
not be enough to change any of that.

`config_stepup: guarded` is the one way to loosen this. It lets two kinds of
edit apply on your session alone: raising a price that is already in
`pricing`, and setting `usage.mode`. Lowering, removing or adding a price
needs a confirmation, because every `max_cost_usd` budget cap is counted in
those prices. Every other path still needs a confirmation: `operators`,
`imports`, the whole `dashboard` and `store` blocks, triggers, workflows,
checks, connectors, runtimes, hosts, packs, plugins, secrets, and any key the
editor doesn't know. A widening change such as a lowered price,
`password_requires_totp: false`, `allow_plain_http: true`, `tailscale.tls: off`
or `config_stepup: guarded` itself is named first in the review. The editor
judges the whole new config, including what it loads through imports, packs
and YAML references, not only the value you edited. Applies that skip the
confirmation are limited to five in ten minutes; after that they ask for one.
What you give up with `guarded`: someone holding a stolen session can raise
prices and change the usage report mode, five times in ten minutes, without
your key.

Every sign-in endpoint accepts only same-origin JSON requests: a request from
another site, or a body that isn't `application/json`, is refused before it is
looked at.

**On plain HTTP, anyone who can change traffic on the way can change the page
itself**, and with it what a code, password or signature you enter confirms,
and the shell command the page offers to copy for SSH signing. On plain HTTP,
sign with `conductor dashboard approve-login <code>` instead of pasting a
command from the page: it reads the request from the dashboard itself and
prints what it confirms. Use plain HTTP only on a network you trust.

A session rests on the credential it signed in with. Changing an operator's
`ssh_key` ends the sessions signed in with the old key; setting a new password
or authenticator ends the password sessions, except the one that made the
change. Removing an operator ends all of theirs. Lowering `session.max` applies
to sessions that already exist. The session list on **Access** shows live
sessions only, each with the latest it can last.

`conductor dashboard status` lists, per operator, passkeys, live sessions, and
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

1. **stage** — the edit is held server-side (`internal/configedit`), per
   signed-in session: another session's edits never show in your review or
   ride into your apply. Last write to a path wins.
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
5. **history / restore** — every apply keeps a backup of the config it
   replaced: the newest 50, and the oldest of each of the last 30 days. Restore
   runs the same validate → apply gate in reverse.

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
| `POST /api/v1/config/stage` · `/unstage` · `/clear` | manage your session's staged edit set |
| `GET /api/v1/config/review` | diff + effects + widening + candidate sha + the step-up confirm token |
| `GET /api/v1/config/validate` | SSE: validate the candidate on a copy |
| `POST /api/v1/config/apply` | apply the staged set (requires the confirm token); backs up, writes, reloads |
| `GET /api/v1/config/history` · `POST /config/restore/review` · `/restore` | backups and restore |
| `GET /api/v1/{secrets,packs,plugins,connections,system,audit}` | per-screen read models (secret values never returned) |

The bots' operator API (the same routes `operator-api.sock` serves, see
[Bots](Bots.md#the-operator-api)) is mounted beside it:

| Method & path | Does |
|---|---|
| `GET /api/v1/bots` · `/bots/{bot}` · `/bots/{bot}/proposals` · `/proposals` | bot roster, detail and proposals |
| `GET /api/v1/bots/{bot}/attachments[/{id}[/content\|/thumbnail]]` | a bot's attachments |
| `POST /api/v1/bots/{bot}/messages` | send a bot a message (multipart, with files) |
| `GET /api/v1/teams` · `/teams/{team}` | teams |
| `GET /api/v1/conversations` · `/conversations/{id}/messages` · `/conversations/stream` | conversations, messages, live updates |
| `POST /api/v1/conversations/{id}/messages` | send to a bot or team conversation |
| `POST /api/v1/conversations/{id}/read` · `POST`/`DELETE /conversations/{id}/pin` · `PUT /conversations/pins` | your read marks and pins |
| `GET /api/v1/tickets` · `/tickets/{id}` · `/tickets/stream` | tickets |
| `POST /api/v1/tickets` · `PATCH /tickets/{id}` · `POST /tickets/{id}/comments` | create, edit, assign, move and comment on tickets |
| `POST /api/v1/{bots,teams}/{name}/avatar/plan` · `/avatar/apply` | plan and stage an avatar change |
| `GET /api/v1/bots/{bot}/cases/{case}/budget` | a case's spend limit and what is left |

On the dashboard you are the operator: a request that names another operator
is refused with 403, and a write on a bot, team or ticket needs you to be one of
its operators (`bots.<name>.operators`, `teams.<name>.operators`; none listed:
every operator). Reads are open to every operator, as on the socket.

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
- no horizontal overflow at desktop and phone widths, and dark mode renders;
- the sign-in methods, through a plain-HTTP listener on the container's
  non-loopback address: no passkey offered and the unencrypted notice shown,
  SSH sign-in by pasted signature, setting a password and authenticator from
  Access (confirmed with the SSH key), replacing the authenticator (confirmed
  with the old one), password + code sign-in (codes computed in the test),
  sign-in approved with `conductor dashboard approve-login`, and a proposal
  approved with an authenticator-code step-up whose dialog shows the action;
- on the iPhone, every route opens on its content with the nav folded behind
  the menu button.

The passkey suites (enrolment, passkey sign-in and step-up) use Chromium's
virtual authenticator and run on Chromium only.

Override the image with `PLAYWRIGHT_IMAGE=…` and the daemon port with
`DASH_E2E_PORT=…`.
