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
```

- Every field has a default, and a config without `auth:` gets the defaults. A
  passkey operator sees no change. Validation runs at load. These are config
  errors:
  - unknown methods, duplicates, or an empty list;
  - `password` under `stepup`, unless `password` is a sign-in method and
    `password_requires_totp` is false;
  - `password_requires_totp: false` without `password` in `methods`. `totp` is a step-up method
  only: it is never a sign-in method on its own.
- `password_requires_totp: false` is the only way to make a password alone a
  credential. Turning it off, `allow_plain_http: true` and `tailscale.tls: off`
  are classified as widening by the config editor, so changing them from the
  dashboard needs a step-up.
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
  an operator exists. Such a challenge cannot be signed, because there is no key
  to verify it against.
- A challenge expires after 5 minutes and is consumed by its first successful
  use.
- It is bound to the listener, host and transport that created it and, for a
  step-up, to the session. A request from anywhere else finds no challenge.
- Pending challenges are capped at 5 per client and 256 overall.

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
     requesting device and the expiry.
  2. Refuses unless the challenge's origin equals the origin of `--url`, the
     operator matches and the namespace is the expected one. This stops a
     look-alike page from relaying its own challenge through the operator's
     approval.
  3. Shows the request and asks for confirmation on the terminal (`--yes`
     skips the question).
  4. Signs the challenge and posts `POST /api/v1/auth/ssh/approve {code, signature}`.
- The waiting page polls `POST /api/v1/auth/ssh/poll {challenge_id}` every 2
  seconds, and the session is issued to it.
- `pending` and `approve` need no session and no CSRF token: the signature is
  the credential. Unknown codes and bad signatures count toward the per-client
  lockout. Three wrong signatures against one code burn it.

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
  - Each operator records the last accepted step, and a code for that step or
    any earlier step is refused. This blocks replaying an observed code,
    including two requests racing with the same code (the check-and-set is
    atomic).
  - Enrolment shows the key as a QR code (an `otpauth://` URI) and as text. The
    authenticator becomes active only after a correct code from the app.
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
  doubling up to 4 hours.
- **Per operator.** Password and TOTP failures, including TOTP and password
  step-up attempts, also count against the operator, whichever client they come
  from. This bounds guessing spread over many addresses.
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
  digest names the operator and the action, so a stolen session cookie cannot
  silently add a credential.
- **UI.** One dialog serves every caller. It offers the step-up methods the
  operator has on this connection, strongest first. Cancelling the dialog
  rejects the caller's promise with a "cancelled" error, which pages do not
  show as a failure.

## 5. Transports and listeners

| Listener | Transport | Passkey RP |
|---|---|---|
| `localhost` | localhost (secure context) | `localhost` |
| `tailscale`, `tls: on` (default) | https | the MagicDNS name |
| `tailscale`, `tls: off` | http (WireGuard-encrypted hop) | none |
| `bind` with `cert_file`/`key_file` | https | each non-IP hostname |
| `bind` on a loopback address, no certificate | localhost | each non-IP hostname |
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
  - A TLS entry with `allow_plain_http` is a config error, so the
    acknowledgment cannot be left set where it no longer applies.
- **Every listener** applies the same chain: security headers, Host allowlist,
  then the session and CSRF checks. The localhost listener now also checks
  `Host` (`localhost`, `127.0.0.1`, `[::1]`).
- **Cookies.**
  - On https and localhost the session cookie is `__Host-conductor_session`
    with `Secure`, `HttpOnly`, `SameSite=Strict`, `Path=/`.
  - On http it is `conductor_session` with `HttpOnly`, `SameSite=Strict`,
    `Path=/` and no `Secure` flag, since a browser drops a `Secure` cookie set
    over plain HTTP.
- **Sessions record the transport and the method.** A cookie is accepted only
  on the transport it was issued for, so an http session cannot be presented
  on an https listener of the same name, nor the reverse.
- **UI on plain HTTP.**
  - The sign-in page and the app show an "Unencrypted connection" notice.
  - The UI does not use the Clipboard API or `crypto.subtle`, which browsers
    withhold from non-secure contexts. Copy falls back to selection, and digests
    come from the server.

## 6. Threat-model changes

- **Plain HTTP.** Anyone on the network path can read and replay the session
  cookie. The configuration only allows this with `allow_plain_http: true` (or
  `tailscale.tls: off`, where WireGuard protects the hop). Step-up tokens are
  single-use, bound to an action and short-lived, so replaying an observed
  confirmation does not authorize a different action. A session stolen over
  plain HTTP can still be used until it expires or is signed out.
- **Password.** Phishable and guessable. It requires TOTP by default; guessing
  is bounded per client and per operator; and only the explicit knob makes a
  password a step-up method.
- **TOTP.** Phishable in real time. Replay protection stops reuse of an
  observed code, but not a relay that uses the code first. Passkeys remain the
  strongest method and are offered first wherever they work.
- **SSH approve-login.**
  - A short code is guessable in principle. 40 bits, a 5-minute life, a
    per-client lockout and a 3-try burn per code make guessing impractical.
  - What a guessed code gives an attacker is the challenge text and device
    description, not a session. A session also needs the operator's signature.
  - The CLI's origin check stops relaying a look-alike page's challenge.
  - The remaining risk is an operator who approves a code they did not request.
    The CLI shows the requesting device and asks before signing.
- **Host allowlist on localhost.** A reverse proxy that forwards a different
  `Host` to the localhost listener is now refused. Such setups should use a
  `bind` entry with the proxy's hostname.

## 7. Choices that differ from the narrowest reading

- **Operator lockout scope.** It applies to password and TOTP only (§3), not
  to passkey or SSH, to avoid a remote lock-out of the operator.
- **Credential changes.** Setting the first password from Access also needs a
  step-up. On a fresh remote session that is an SSH or passkey confirmation.
- **Enrolment link.** A password enrolled through the localhost link does not
  require confirming with an existing passkey (a second passkey does). The link
  itself is SSH-signed and single-use.
- **Wording.** The existing pages' passkey-specific labels ("Approve with
  passkey" and similar) now name no method, because the dialog chooses it.
