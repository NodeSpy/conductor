# Dashboard sign-in methods — design

Status: **built** on `feat/dashboard-full`. Operator docs: `docs/wiki/Dashboard.md`
(Signing in).

The dashboard used to sign in with a passkey only. That excluded three common
cases:

- a browser on another machine, before any passkey is enrolled (enrolment is a
  localhost link);
- a connection that is not a secure context (plain HTTP on a LAN or on the
  tailnet with TLS off), where browsers refuse WebAuthn;
- a phone that cannot hold a passkey for the box's relying party.

This design adds two methods beside the passkey: signing a challenge with the
operator's SSH key, and a password with an authenticator-app code. It
generalizes step-up to all of them, and adds the listeners that need them.

## 1. Policy

```yaml
dashboard:
  auth:
    methods: [passkey, ssh, password]   # sign-in methods
    password_requires_totp: true
    stepup: [passkey, ssh, totp]        # methods that confirm sensitive actions
    config_stepup: all                  # which config applies need one: all | guarded
```

- Every field has a default, and a config without `auth:` gets the defaults:
  passkey, SSH and password sign-in are all on (§7 says why). Validation runs
  at load. These are config errors:
  - unknown methods, duplicates, or an empty list;
  - `password` under `stepup`, unless `password` is a sign-in method and
    `password_requires_totp` is false;
  - `password_requires_totp: false` without `password` in `methods`. `totp` is a step-up method
  only: it is never a sign-in method on its own.
- `password_requires_totp: false` is the only way to make a password alone a
  credential. Turning it off, `allow_plain_http: true` and `tailscale.tls: off`
  are classified as widening by the config editor.
- **Every config apply needs a step-up** when sign-in is on
  (`config_stepup: all`, the default), and so does every restore. Most of the
  config runs something on the box: a cron connector and a trigger step with
  `use: cli` is a shell, and that shell can rewrite an operator's `ssh_key` or
  read the sign-in store. A session cookie must not be enough for any of it.
- `config_stepup: guarded` is the one loosen. In that mode an apply needs a
  step-up when its candidate changes anything outside a short inert list
  (`pricing`, `usage.mode`) or loosens a guardrail. Everything else is guarded:
  `operators`, `imports`, the whole `dashboard` and `store` blocks, triggers,
  workflows, checks, connectors, runtimes, hosts, engines, models, packs,
  plugin and pack trust, secrets, vaults, the sandbox, the browser, callable,
  policy, bots, teams, update and `dry_run`. A key that is not on the inert
  list, including one added to the config later, is guarded. What this mode
  gives up: a stolen session can change cost estimates and the usage report
  mode. Setting it is itself a widening change.
- The config editor decides this from the whole candidate, not from the path
  that was staged:
  - it compares every leaf between the live file and the candidate, so staging
    a whole parent map or list is judged by what it ends up containing, and
    names each guarded section that changed;
  - it also loads both and compares the loaded configs (minus the inert
    paths), so a change that reaches a guarded setting through an import, a
    pack or a YAML reference counts too. A candidate that cannot be loaded
    counts as changed;
  - the apply asks for the step-up under the editor's lock, on the exact
    candidate it is about to write, so nothing staged after the review slips
    past.
- **If the sign-in store can't be opened**, or the auth layer can't be built,
  every listener refuses the API with a 503. The localhost listener says why;
  the others say only that sign-in is unavailable. The dashboard never serves
  as if no sign-in were needed because sign-in broke. With no operators and no
  state dir there is nothing to sign in to, and the API stays open on localhost
  as on a fresh install.
- The server is the authority on what a connection offers:
  - `GET /api/v1/auth/state` lists the sign-in methods for this listener and
    transport and, when signed in, the step-up methods this operator has here.
  - Passkeys are listed only when the listener has a relying party for the
    request's host and the transport is a secure context.
  - The client additionally hides passkeys when `window.isSecureContext` is
    false.

## 2. SSH sign-in

The operator's `ssh_key` already authenticates the CLI (enrolment, reset, bot
approvals). The dashboard reuses it.

**Challenge.** `POST /api/v1/auth/ssh/begin {operator}` returns a one-line text:

```
conductor-dashboard-login v1 operator=ops origin=https://box.example:7790 nonce=<base64, 16 random bytes> expires=<RFC 3339>
```

and a step-up challenge adds the purpose and the action digest:

```
conductor-dashboard-stepup v1 operator=ops origin=... purpose=approve digest=<sha256> nonce=... expires=...
```

- Signatures are SSHSIG (`ssh-keygen -Y sign`) in the namespaces
  `conductor-dashboard-login` and `conductor-dashboard-stepup`. These differ
  from the enrolment and bot-approval namespaces, so a signature made for one
  purpose cannot be used for another.
- The server keeps the text it issued and verifies the signature against that
  stored copy, never against text sent back by a client. Verification is
  `ssh-keygen -Y verify` against the operator's configured key.
- The origin is the scheme and `Host` of the request that began it. `Host` has
  already passed the listener's allowlist, and it is also matched against a
  conservative pattern so the text is safe inside the single-quoted shell
  command the page shows.
- An unknown operator still gets a challenge, so a client cannot tell whether
  an operator exists. Finishing it runs the same verification against a key
  whose private half was discarded, so it fails the same way, with the same
  error, as a wrong signature for a real operator.
- A challenge expires after 5 minutes and is consumed by its first successful
  use.
- It is bound to the listener, host and transport that created it and, for a
  step-up, to the session. A request from anywhere else finds no challenge.
  That includes its login code: `pending` and `approve` look a code up only on
  the listener, host and transport it was issued on.
- Pending sign-in challenges are capped at 5 per client, 32 per network (an
  IPv4 /24 or an IPv6 /48) and 4096 overall. Step-up challenges have their own
  cap of 5 per session, so a flood of sign-in challenges cannot stop a
  signed-in operator from confirming.

**Paste.** The page shows the `ssh-keygen` command (and the equivalent
`conductor dashboard sign '<challenge>'`). The operator pastes the armoured
signature into `POST /api/v1/auth/ssh/finish {challenge_id, signature}`.
`challenge_id` is a 256-bit secret known only to the browser that began the
challenge.

**Approve from another device.**

- Each challenge also has a login code: 8 characters from a 32-symbol alphabet
  (40 bits), shown as `ABCD-EFGH`. The code is not the challenge id, so knowing
  it is not enough to receive the session.
- `conductor dashboard approve-login <code> --url <origin> --operator <name>`
  does the following:
  1. Calls `POST /api/v1/auth/ssh/pending {code}` to get the challenge text, the
     requesting address and device, when the request started and when it
     expires, and for a step-up the action's digest and the dashboard's
     description of it.
  2. Refuses unless the challenge's origin equals the origin of `--url`, the
     operator matches and the namespace is the expected one. This stops a
     look-alike page from getting a challenge for its own origin approved. It
     does not stop a relay that starts the sign-in on the real dashboard (§6).
  3. Shows all of that — for a step-up the purpose, the description and the
     first 12 characters of the digest, which the waiting page also shows — and
     says that approving a code someone else sent signs them in. Then it asks
     for confirmation on the terminal (`--yes` skips the question).
  4. Signs the challenge and posts `POST /api/v1/auth/ssh/approve {code, signature}`.
- The waiting page polls `POST /api/v1/auth/ssh/poll {challenge_id}` every 2
  seconds, and the session is issued to it.
- `pending` and `approve` need no session and no CSRF token: the signature is
  the credential. They are still same-origin JSON endpoints (§4a). Unknown
  codes and bad signatures count toward the per-client lockout. Three wrong
  signatures against one code burn it.
- **Describing a step-up.** The digest is a hash, so the server cannot turn it
  back into an action. Instead, whenever it hands out a digest (a proposal's or
  a purchase's approval view, a config review), it notes what confirming that
  digest does, in words built from its own data. `pending` returns that note
  for a step-up over the same purpose and digest. A digest the server never
  handed out has no description, and the CLI says so.

## 3. Password and TOTP

- **Password.** Stored as argon2id (m=19 MiB, t=2, p=1, 32-byte key) in PHC
  string form; 12 to 256 characters.
  - Comparison is constant-time.
  - An unknown operator is checked against a dummy hash, so the timing does not
    reveal whether the operator exists.
  - At most two hashes run at once, which bounds the memory and CPU an
    unauthenticated client can consume.
- **TOTP.** RFC 6238 (HMAC-SHA-1, 30-second step, 6 digits), 160-bit secret.
  - The code for the current step or one step either side is accepted.
  - Each accepted step is spent: the same step is refused the next time, and so
    is any step older than two before the newest accepted one. The
    check-and-set is atomic, so two requests racing with one code get one
    success. Spending the next step's code early does not refuse the current
    step's code.
  - Enrolment shows the key as a QR code (an `otpauth://` URI) and as text, in
    4-character blocks (copying takes it without spaces). The authenticator
    becomes active only after a correct code from the app.
  - Replacing it works the same way: Access shows a new key, and the current
    authenticator keeps working until a code from the new one confirms it
    (under a `credential.change` step-up).
- **Sign-in.** `POST /api/v1/auth/password/login {operator, password, code}`.
  The password and code are checked together, and a failure does not say which
  one was wrong.
  - With `password_requires_totp: true`, an operator without an active
    authenticator cannot sign in by password at all.
  - With `false`, an operator without an authenticator signs in by password
    alone. One with an authenticator still needs a code.
- **Setting them.** A signed-in operator sets them from Access, or a new
  operator from the one-time localhost enrolment link. Recovery is `conductor
  dashboard reset`, which is signed with the SSH key. It clears passkeys,
  password, authenticator and sessions.

**Lockout.**

- **Per client.** Every method's failures count against the client key, as
  passkey failures did before: 5 failures in 15 minutes give a 15-minute lock,
  doubling up to 4 hours. The client key is the tailnet node on the tailnet,
  otherwise the address: an IPv4 address, or an IPv6 /64, because one host
  usually holds a whole /64.
- **Per operator.** Password and TOTP failures, including TOTP and password
  step-up attempts, also count against the operator, whichever client they come
  from. This bounds guessing spread over many addresses.
- **Reserved before evaluated.** A guess reserves its place in the client and
  operator buckets before the hash or code check runs, and the reservation is
  refused when the failures plus the guesses still running reach the limit.
  Taking the reservation is atomic, so a concurrent burst gets at most 5
  guesses evaluated per bucket, not one per request in flight. A success or an
  error that is not a wrong guess releases the place.
- Checking whether a client is locked creates no state, and idle entries are
  swept, so the lockout table stays bounded however many addresses an
  attacker uses.
- The operator lock covers only the guessable secrets. Passkey and SSH sign-in
  are not blocked by it, so an attacker who sends wrong passwords cannot lock
  the operator out of the methods that do not depend on a guessable secret.

## 4. Step-up for every method

- Callers still call `RequireStepup(session, purpose, digest, token)`. The
  token is single-use, expires after 2 minutes, and is bound to the session,
  the purpose and the digest. Callers do not know which method produced it.
- **Minting.** Every method mints through one function, which checks
  `auth.stepup`, records the method in the evidence and writes the audit
  entry:
  - **Passkey.** As before.
  - **SSH.** A step-up challenge (§2), by paste or by `approve-login` from
    another device.
  - **TOTP.** A fresh code.
  - **Password.** Only when the policy allows it.
- **Bot approvals.** A passkey step-up is presented to the approval gate as
  before (`webauthn` evidence). Any other method is presented as `dashboard`
  evidence that names the method, and the SSH signature when there is one. The
  approval's audit record carries the method.
- **Credential changes.** Setting or removing a password or authenticator, and
  adding a passkey from Access, use the purpose `credential.change`. Their
  digest names the operator and the action. Together with the config step-up
  (§1), a stolen session cookie cannot add a credential or change an
  operator's key without a confirmation.
- **Password step-up** needs the authenticator code as well when the operator
  has an authenticator, as at sign-in, and counts against the operator lock.
- **UI.** One dialog serves every caller. It offers the step-up methods the
  operator has on this connection, strongest first. It shows what is being
  confirmed inside the dialog: a summary, the action itself pretty-printed (a
  proposal's or purchase's action, the config change's effects) and the first
  12 characters of the digest. Cancelling the dialog rejects the caller's
  promise with a "cancelled" error, which pages do not show as a failure.

## 4a. Requests to the auth endpoints

Every auth endpoint, signed in or not (`/api/v1/auth/*`, `/api/v1/enroll/*`,
passkeys, sessions, Access):

- refuses a request whose `Origin` is not the request's own origin (scheme and
  `Host`, default ports ignored), with 403;
- refuses a `Sec-Fetch-Site` other than `same-origin` or `none`, with 403;
- refuses a request with a body that is not `Content-Type: application/json`,
  with 415. That keeps a cross-site form, which cannot send JSON without a
  preflight, from reaching them at all.

The CLI sends no `Origin` and the JSON content type, so it is unaffected.

## 5. Transports and listeners

| Listener | Transport | Passkey RP |
|---|---|---|
| `localhost` | localhost (secure context) | `localhost` |
| `tailscale`, `tls: on` (default) | https | the MagicDNS name |
| `tailscale`, `tls: off` | http (WireGuard-encrypted hop) | none |
| `bind` with `cert_file`/`key_file` | https | each non-IP hostname |
| `bind` on a loopback address, no certificate, only loopback hostnames | localhost | `localhost` |
| `bind` on a loopback address, no certificate, any other hostname | http | none |
| `bind` elsewhere, no certificate | http | none |

- **`bind` entries.**
  - `address` is an IP and port, not the dashboard port, and no address may
    repeat.
  - `hostnames` is required. `Host` must match one of them, which blocks DNS
    rebinding.
  - The certificate and key must be set together.
  - Plain HTTP on a non-loopback address needs `allow_plain_http: true`. That is
    checked at load and again when the listener starts, and a warning is logged
    at boot.
  - A loopback address is a secure context only under a loopback name: a
    browser treats `http://box.lan` as insecure even when it resolves to
    127.0.0.1. Such an entry is served as plain HTTP without passkeys; it needs
    no acknowledgment, since the traffic stays on the machine.
  - A TLS entry with `allow_plain_http` is a config error, so the
    acknowledgment cannot be left set where it no longer applies.
- **Every listener** applies the same chain: security headers, Host allowlist,
  then the session and CSRF checks. The localhost listener checks `Host`
  against the loopback names (`localhost`, `127.0.0.1`, `[::1]`). The tailnet
  listener accepts only its MagicDNS name and tailnet addresses, never a
  loopback name, so a tailnet peer cannot mint challenges carrying the
  localhost listener's origin.
- **Cookies.**
  - On https and localhost the session cookie is `__Host-conductor_session`
    with `Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/`.
  - On http it is `conductor_session` with `HttpOnly`, `SameSite=Strict`,
    `Path=/` and no `Secure` flag, since a browser drops a `Secure` cookie set
    over plain HTTP.
  - A `bind` listener's cookie name adds a suffix derived from its address.
    Cookies ignore the port, so without it a loopback `bind` named `localhost`
    and the localhost listener would overwrite each other's cookie.
- **Sessions record the transport and the method.** A cookie is accepted only
  on the transport it was issued for, so an http session cannot be presented
  on an https listener of the same name, nor the reverse.
- **UI on plain HTTP.**
  - The sign-in page and the app show an "Unencrypted connection" notice.
  - The UI does not use the Clipboard API or `crypto.subtle`, which browsers
    withhold from non-secure contexts. Copy falls back to selection, and digests
    come from the server.

## 6. Threat-model changes

- **Plain HTTP, passive.** Anyone on the network path can read and replay the
  session cookie. The configuration only allows this with
  `allow_plain_http: true` (or `tailscale.tls: off`, where WireGuard protects
  the hop). Step-up tokens are single-use, bound to an action and short-lived,
  so replaying an observed confirmation does not authorize a different action.
  A session stolen over plain HTTP can still be used until it expires or is
  signed out, but not to change who can sign in (§1, §4).
- **Plain HTTP, active.** An attacker who can modify traffic rewrites the page
  itself. Every code, password or signature the operator then enters can
  confirm an action of the attacker's choosing, and the page can show
  anything. The only check outside the page is `approve-login`, which prints
  the server's own description of a step-up. Plain HTTP means trusting
  everything on the path; use it only where that is true.
- **Password.** Phishable and guessable. It requires TOTP by default; guessing
  is bounded per client and per operator; and only the explicit knob makes a
  password a step-up method.
- **TOTP.** Phishable in real time. Replay protection stops reuse of an
  observed code, but not a relay that uses the code first. Passkeys remain the
  strongest method and are offered first wherever they work.
- **Real-time relay of SSH sign-in.** A look-alike page can start a sign-in on
  the real dashboard from its own server and ask the operator to approve that
  code, or to sign and paste that challenge. The challenge names the real
  origin, so the CLI's origin check passes. This cannot be prevented for a
  method whose proof the operator carries across by hand. The CLI narrows it by
  showing the requesting address, the device as the browser describes itself
  (the requester controls that text), when the request started, and for a
  step-up the action, and by saying that approving a code someone sent signs
  them in. Passkeys resist it: the browser binds the assertion to the origin it
  is actually on, so a look-alike page gets nothing usable.
- **Which action a step-up confirms.** A script running in the dashboard's own
  origin can ask for a step-up over any digest. The dialog shows the action the
  page says it is confirming, which such a script controls; a TOTP code or a
  passkey touch carries no independent display. `approve-login` shows the
  server's description instead, so an SSH confirmation through it is the one
  place the operator sees the action from outside the page.
- **SSH approve-login.**
  - A short code is guessable in principle. 40 bits, a 5-minute life, a
    per-client lockout and a 3-try burn per code make guessing impractical.
  - What a guessed code gives an attacker is the challenge text and device
    description, not a session. A session also needs the operator's signature.
  - The CLI's origin check refuses a challenge minted for a look-alike's own
    origin; a relay through the real dashboard passes it (above).
  - The remaining risk is an operator who approves a code they did not request
    (the relay above).
- **Host allowlist on localhost.** A reverse proxy that forwards a different
  `Host` to the localhost listener is now refused. Such setups should use a
  `bind` entry with the proxy's hostname.

## 7. Choices that differ from the narrowest reading

- **Defaults.** With no `dashboard.auth` block, SSH and password sign-in are on
  beside passkeys. Defaulting to passkey-only would protect an existing
  passkey-only install from gaining methods on upgrade, but the dashboard has
  not shipped, so there is no such install; the operator chose these defaults.
  Narrow them with `methods:`.
- **Operator lockout scope.** It applies to password and TOTP only (§3), not
  to passkey or SSH, to avoid a remote lock-out of the operator. Those methods
  are not guessable, so an attacker holding the operator lock cannot keep the
  real operator out.
- **IPv6 client bucket.** A /64, the usual allocation to one host or link; a
  per-address bucket would let one host rotate through 2^64 addresses.
- **Credential changes.** Setting the first password from Access also needs a
  step-up. On a fresh remote session that is an SSH or passkey confirmation.
- **Enrolment link.** A password enrolled through the localhost link does not
  require confirming with an existing passkey (a second passkey does). The link
  itself is SSH-signed and single-use.
- **Wording.** The existing pages' passkey-specific labels ("Approve with
  passkey" and similar) now name no method, because the dialog chooses it.
