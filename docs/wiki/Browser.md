# Browser

Any agent conductor launches sandboxed (a `cli`/`acp` worker, an opencode or
agent-deck session, a paseo session on a `<provider>-sandboxed` entry, a
hand-off, a bot's worker) can drive a real browser to do work on websites:
`conductor browser …`. Conductor runs the browser. The agent asks; conductor
decides, acts, and audits.

**A browser is a write channel.** One click in a logged-in session can delete
or buy something. So the browser is anonymous unless you opt a site in, it
reaches only the sites you list, and a logged-in site is used only for your
own requests.

## Turn it on

```yaml
browser:
  allow:                      # the sites an agent's browser may reach (sandbox.egress joins them)
    - docs.example.com        # a bare host is :443
    - "*.example.org"
    - "127.0.0.1:8080"        # a local site: host:port
  # chrome: /usr/bin/chromium # default: the first Chrome/Chromium installed (host mode)
  # container: auto           # auto (default) | on | off — see "Where it runs"
  # container_engine: docker  # docker | podman | a path (default: docker, then podman, on macOS)
  # evaluate: ["agent:triage@acme/app"]   # identities opted in to read-only evaluate (default: none)
  # idle_timeout: 10m
  # max_browsers: 4
  # max_download_mb: 100
  # max_upload_mb: 25
```

It needs Chrome or Chromium: on Linux `google-chrome`/`chromium` on `PATH`, on
macOS Google Chrome or Chromium in `/Applications`. The agent sandbox must be on
(`sandbox.agents`): the browser serves sandboxed agents only. `"*"` (the whole web)
is refused: list the sites.

## What an agent does

```sh
conductor browser open https://docs.example.com/
conductor browser snapshot                 # the page as text; each control has a ref
#   - heading "Install" [level=1]
#   - textbox "Search" [ref=e41]
#   - link "Download" [ref=e57]
conductor browser source                   # the page's HTML source, read-only, every value redacted
conductor browser fill e41 "proxy settings" --submit
conductor browser click e57
conductor browser downloads                # → .browser/downloads/<name> in the workspace
conductor browser screenshot --path shots/page.png
conductor browser upload e63 report.pdf    # a file from the workspace
```

The other actions are `type`, `select`, `press KEY`, `scroll`,
`wait --text|--gone|--url`, `back`, `forward`, `reload`, `tabs`,
`tab new|select|close`, `evaluate` (if enabled), `status` and `close`. See
`conductor browser --help`.

Each launch's token gets its own browser, and an idle one closes after
`idle_timeout`.

### Reading the page: `snapshot` and `source`

An agent reads a page only through outputs conductor builds — never a raw DOM
dump, never its own script by default:

- **`snapshot`** is the page as a screen reader sees it: one line per
  meaningful node, each control with a ref the other actions take. It is what
  most work needs.
- **`source`** is the page's **HTML source**, read-only and point-in-time,
  serialised by conductor node by node so it can **redact structurally** as it
  writes: a field's value is never emitted (`value="[redacted]"` marks that one
  exists), a node a secret was filled into is dropped, and the places a value
  hides — `<script>` and `<style>` bodies, HTML comments, `<template>`
  contents, `data-*` values, `style` and event-handler attributes — are
  stripped. Tag names, ids, classes, roles, aria, link targets and visible
  text survive; a same-origin frame or open shadow root is included with the
  same redaction, a cross-origin frame's contents never. It is size-capped
  (`--max`, default 25k, max 200k).

`source` is for reading structure a flat snapshot flattens — a table's shape,
a form's hidden fields, a widget's markup. It needs **no `evaluate` opt-in**
and works **after a credential is filled**, so it is the way to read a page you
have signed into without reaching for a live script. Prefer `snapshot` and
`source` over `evaluate`, which stays off for most work and entirely once a
value is filled.

## The boundary

- **Where it runs.** Conductor launches a headless Chromium per agent session,
  in one of two ways. Either way it sees only its own throwaway profile
  directory and Chromium's install, and never your home, your real browser
  profiles, conductor's state, or the workspace.
  - **Linux:** in user, mount, pid and network namespaces with `pivot_root`,
    with Chromium's own sandbox (namespaces + seccomp-BPF) nested inside for
    every renderer.
  - **macOS, with Docker Desktop, colima or podman installed and running
    (the default whenever an engine answers):** in a container.
    - Linux Chromium runs **with its own sandbox on** inside a container with
      no network, no capability, `no-new-privileges`, a read-only root and no
      mount of your Mac.
    - The container's seccomp profile is the engine's default plus exactly
      what Chromium's sandbox needs (`clone`/`unshare` with namespace flags,
      `setns`, `chroot`).
    - Conductor reaches it over the engine's stdin/stdout alone: the DevTools
      pipe, each proxy connection, and file transfers. That works the same
      through Docker Desktop's or colima's VM.
    - The first start builds its image (a few minutes). Until Chromium proves
      in it that it is sandboxed (`chrome://sandbox`: "adequately sandboxed"),
      browser actions are refused as "being prepared", never quietly run in
      the weaker mode. `browser.container: on` requires a container on any OS.
  - **macOS without a container engine (the fallback):** Seatbelt alone.
    - macOS cannot nest Chromium's own Seatbelt sandbox, so Chromium runs
      with `--no-sandbox` and a renderer exploit holds what the browser jail
      holds.
    - Its Mach and IOKit access is narrowed to named services.
    - Its egress proxy is a loopback port any local user could relay through
      to the allowlist. Use this mode on a **single-user Mac only**.
    - Conductor logs this at boot and on every session, with the number of
      local users when there is more than one. Install an engine to leave this
      mode.
    - A directory listing outside the browser's own paths fails: it sees no
      file names of yours.
  - **Control.** Conductor drives the browser over the DevTools protocol on a
    **pipe**, so there is no debugging port for anything else to reach. The
    agent never holds the browser.
- **Network.** The browser has no network of its own. Its only way out is
  conductor's egress proxy with `browser.allow` (plus `sandbox.egress` and the
  identity's own allowlist), loopback included.
  - Hosts are taken **exactly as bound**: `localhost:3000`, `127.0.0.1:3000`
    and `[::1]:3000` are three entries, and none implies another.
  - An allowlisted name that resolves (or rebinds) to an internal address is
    refused, even if that address is listed.
  - A URL off the list is refused up front, and a redirect or a page's own
    request off it dies at the proxy and is audited. The agent's own process gets nothing from `browser.allow`: it
  cannot `curl` those sites past the audit.
- **Profile.** It is ephemeral and anonymous: no cookies, no saved passwords,
  nothing from your browser, gone when the browser closes.
- **Files.**
  - Downloads land in the workspace's `.browser/downloads/` and screenshots
    where the agent says, inside the workspace. Uploads come only from the
    workspace.
  - Conductor moves the files, not the browser, through a confined path walk:
    no absolute path, no `..`, no symlink out, nothing under `.git`, nothing in
    a read-only or hidden part of the workspace, and never an overwrite.
- **Scripts.** `evaluate` is **off** by default, opted in **per identity**
  (`evaluate: [identities]`). For those, an expression runs only when V8 can
  rule out every side effect: no DOM write, no network, no storage. Whether
  the identity was opted in is in every evaluate's audit entry. In a browser
  where a credential was filled, evaluate is off whatever the opt-in says.
- **Status.** `conductor browser status` shows what the browser runs in
  (`container`, `seatbelt` or `namespace`, or why none can start yet) and
  whether `evaluate` is on for this browser right now.
- **Audit.** Every action is audited with:
  - the identity, the agent, repo and dispatch;
  - who asked (`requester`);
  - the page URL (its query and fragment dropped), the action and element ref;
  - whether it was logged in, and the outcome.

  A fill records the text's length, never the text.

## Logged-in sites (opt in, per identity and per site)

**Opting a site in gives that identity's agent your session's power on that
site** whenever the work is your own request. Prefer an agent-specific account
where the site allows one.

```yaml
browser:
  allow: [app.example.com, static.example-cdn.net]
  logins:
    - site: app.example.com              # a host, or *.domain
      identities: ["dir:/home/me/projects/app", "agent:triage@acme/app"]
      also: [static.example-cdn.net]     # further hosts the logged-in browser may reach
```

Then seed a session for an identity, in one of two ways:

```sh
# 1. Log in yourself, in a browser window conductor opens (sandboxed,
#    allowlisted, throwaway); press Enter when done. Only app.example.com's
#    cookies are kept.
conductor browser seed --identity agent:triage@acme/app --site app.example.com
# 2. Import cookies for that one domain (a JSON / Playwright storage state /
#    cookies.txt export). Everything for other domains is dropped.
conductor browser cookies import --identity agent:triage@acme/app --site app.example.com cookies.json

conductor browser logins     # what is saved (never values)
conductor browser logout --identity agent:triage@acme/app --site app.example.com
```

A whole browser profile is never imported. Saved sessions live in
`<state>/browser/logins/` (0700, each 0600), outside every sandbox.

How it is enforced:
- **A separate browser.** A logged-in session goes into a separate browser
  that reaches only the logged-in sites (and their `also:` hosts). An untrusted
  page never shares a browser with your session, so it cannot forge a request
  into it.
- **Routing.** A navigation to a logged-in site goes to that browser, anything
  else to the anonymous one.
- **What the agent can change.** As the site rotates the session, conductor
  updates the cookies it saved, and only those: an agent cannot add a cookie to
  your saved session or clear one from it.
- **Provenance, checked at every action.** A logged-in site is used **only for
  the operator's own request**:
  - an agent you started in a directory of your own (a paseo session on a
    sandboxed entry);
  - a local `conductor run` of a manual trigger;
  - for bots, an operator-requested case.

  An event, a third party's request, a bot's own initiative (proactive
  work), a case spawned from a non-operator case, or an unknown requester gets
  the **anonymous** profile, with a note saying why. If a session's provenance
  changes (a bot's home session takes on a third party's case), its
  logged-in browser closes at the next action.

## Credentials: sign-ins the agent never sees

Conductor can sign an agent in from your vaults: 1Password, the conductor
vault, `pass`, a file, HashiCorp. **The agent never sees a value.** It asks,
and conductor fills the field from outside the sandbox. The value never enters
the agent's process, context, transcript, workspace, screenshots, snapshots or
logs.

```yaml
vaults:
  op: { type: onepassword }
  house: { type: conductor }            # writable: where passkeys are kept
browser:
  allow: [app.example.com]
  credentials:
    - name: app
      origin: https://app.example.com    # EXACT scheme://host[:port], optional /path prefix
      identities: ["agent:triage@acme/app"]
      login_url: https://app.example.com/login
      username: '{{ vault "op" "Agents/App/username" }}'
      password: '{{ vault "op" "Agents/App/password" }}'
      otp:      '{{ vault "op" "Agents/App/one-time password?attribute=otp" }}'
      passkey:  '{{ vault "house" "passkeys/app-triage" }}'
      # approval: true                   # the site asks you to approve on your phone
```

Each field names **where its value comes from**, and the sources can be mixed
per field. They are read only when the field is filled, by conductor, outside
every sandbox:

| source | example |
|---|---|
| a vault entry | `'{{ vault "op" "Agents/App/password" }}'` (or the old map `{vault: op, key: …}`) |
| a named secret | `'{{secret "house/app_pw"}}'` |
| an environment variable | `'${env.APP_PW}'` (conductor.env included) |
| a typed value | a username only: `username: agent@example.com` |

A typed password, code or passkey never sits in the config: add it write-only
(`conductor browser credential`, below). `${settings.…}` is refused in a secret
field (a setting is plain config), and so is the loader's `${APP_PW}`, which
would put the value into the config at load: write `${env.APP_PW}`.

Nothing is shared by default: a credential is usable only by the identities it
names, on its origin, and only for the operator's own request (the same
provenance rule as logged-in sites).

The agent's side:

```sh
conductor browser login --credential app                 # fills what the page asks for, submits each step
conductor browser fill-credential e12 password --credential app   # one field, by ref
conductor browser login --credential app --passkey --ref e7       # sign in with the passkey
conductor browser await-approval --credential app        # waiting on your phone
```

A usable credential's host counts as a logged-in site: its pages always open
in the separate, narrowed browser, so whatever conductor signs the agent in to
never shares a browser with an untrusted page. `login` starts at
`login_url` when the current page has nothing to fill.

Every fill checks, before any value is fetched:
1. **The credential.** It is bound to this identity, and the request is the
   operator's own.
2. **The origin.** The page's origin is exactly the credential's origin
   (scheme, host and port), as the browser reports it for the main frame. A
   lookalike host, the page a redirect lands on, or an origin outside the bound
   path is refused.
3. **The field.** It is in the page's own document, never an embedded frame,
   checked from an isolated JavaScript world the page's scripts cannot reach.
   It fits the value: a password only into a password field, a code or a
   username never into one.
4. **The allowlist.** The page is on it.

Then conductor fetches the value **fresh** (uncached, for this one fill), sets
it from the isolated world, and audits the identity, origin, credential name,
field and outcome, never the value. Vault reads are tainted, so a value that
somehow reached a log line is redacted there too.

After a fill, in that browser:
- password and one-time-code values are scrubbed from snapshots, page text,
  titles and URLs;
- `source` never emits a field's value and strips the markup a value hides in
  (its structural redaction runs before any scrub), so it stays available
  after a fill;
- screenshots black out every field and text showing one;
- `wait --text` matches only the scrubbed text, so it is never a guessing
  oracle;
- a download containing a filled value is refused;
- **`evaluate` is off for the rest of that browser's life**, even read-only:
  a side-effect-free expression could read a filled field and transform it
  past any scrub.

### What this guarantees, and the one thing it cannot

conductor makes the agent **blind to the verbatim value** everywhere conductor
controls: the fill runs in an isolated world the page's scripts cannot reach,
the filled field is masked in every snapshot and screenshot (by its node, so
even a value too short to scrub — a CVC, a region code — is hidden), the value
is scrubbed from page text, titles, URLs and anything conductor reads back out
of a page (a receipt's order number included), `evaluate` is off for the life of
the browser, and the agent cannot inject script of its own.

What conductor **cannot** do is make a page that is *already running hostile
script on the credential's own origin* safe. Such a script is same-origin with
the field: it can read the value the moment it is filled and, because it can
transform the value first (base64, reversed, split — anything), it can defeat a
text scrub, which can only match the value verbatim. This is the web's
same-origin boundary, not something the redaction layer can close — a site you
let conductor sign into is a site you trust with that credential.

That is exactly why the two things around the fill matter:

- **Origins are exact and operator-chosen.** A credential, profile or card fills
  only on the specific scheme + host + port the operator configured (a card only
  on its listed merchants). A lookalike host, a redirect, a subdomain, an
  embedded frame or any page the agent itself stands up is refused. So the only
  script that could read a filled value is one already on a site the operator
  decided to trust.
- **Egress is allowlisted.** Even a hostile first-party script can only send what
  it reads to hosts on the browser's allowlist, through conductor's proxy —
  there is no open path off the box.

In short: conductor stops the *agent* from ever seeing a filled value, and binds
every fill to operator-trusted origins with bounded egress. It does not, and
cannot, defend a credential against a site that is itself already compromised.

### Adding logins (what the dashboard drives)

Besides `browser.credentials`, you can add logins at runtime: from the
dashboard, or `conductor browser credential …` (both use the same daemon
operations). There are three ways:

1. **Type a username and password**, and optionally a TOTP secret or an
   `otpauth://` URI. Typed values are stored **write-only** in conductor's own
   encrypted store (`<state>/browser/credential-values/`, its own key, not a
   `vaults:` entry). You can set, replace or remove one, but nothing reads one
   back, through any command or API. Only a fill uses it.

   ```sh
   conductor browser credential add app --origin https://app.example.com \
     --bind agent:triage@acme/app --username agent@example.com --password-stdin
   ```

2. **Pick from a vault.** The picker lists **metadata only**: vault, item
   title, websites, field labels and types, never a value.
   - **1Password:** `op item list` / `op item get`, read into types that have
     no value field, so a value never leaves the daemon. The username,
     password and one-time code fields are matched automatically. The
     reference is stored in conductor's syntax
     (`{{ vault "op" "Vault/Item/field" }}`; a one-time code is
     `…?attribute=otp`, so its seed stays in 1Password).
   - **conductor and `file` vaults:** entry names.
   - **`pass`:** entry names, from the store's directory.
   - **HashiCorp:** secret paths. Name a field by hand (`path#field`).

   ```sh
   conductor browser vault-items op --query example     # pick an item
   conductor browser credential add app --vault op --item <id> --bind agent:triage@acme/app
   ```

   Each field can also take any source by hand: `--password-ref '${env.APP_PW}'`.

3. **Sign in yourself.** Add a session login, then seed it:

   ```sh
   conductor browser credential add app-session --session --site app.example.com --bind agent:triage@acme/app
   conductor browser seed --identity agent:triage@acme/app --site app.example.com
   ```

   The inventory shows when the saved session expires, where its cookies say
   so. A session cookie has no expiry, so then it says it's unknown.

A login binds to **identities** (exact, or a prefix ending in `*`),
**`bot:<name>`** or **`team:<name>`**, and to exact origins (`--origin` repeats).
An origin must be reachable through `browser.allow`.

Then:

```sh
conductor browser credential ls          # method, origins, bindings, last used, last test, expiry: no values
conductor browser credential usage app   # who used it (identity, dispatch), when
conductor browser credential check app   # does each field resolve? ok or why, never the value
conductor browser credential test app    # sign in, in a sandboxed browser
conductor browser credential bind app --bind bot:helper --dry-run   # → "widening"
conductor browser credential rm app      # revoke: the login and every value it stored
```

- **`check`** resolves every field fresh and reports `ok` or the error.
  - It warns when a password or code comes from an environment variable,
    which is plain text, and says when `conductor.env` sets it.
  - It refuses a `${settings.…}` source.
- **`test`** signs in exactly as an agent would (the same checks, in a fresh
  sandboxed browser narrowed to the login's hosts). It answers `ok`, `failed`
  with the reason, or `needs-you` when the site waits for your approval on
  your phone. It returns no value and no screenshot, keeps no cookie, and the
  result shows in the inventory. By default, success means the page is on none
  of the pages a sign-in step was submitted from, and asks for no password or
  code. A login can name what
  signed-in looks like instead (`--check-url`, `--check-text`).
- **Widening.** Every change that widens who may use a login, or where,
  answers `change: widening`: a new login, a new binding, a new origin. The
  dashboard asks for a passkey for those. Removals are `narrowing`, and a
  field's new value is `neutral`. `--dry-run` reports the classification and
  changes nothing.
- **Revoking.** `rm` removes the login and its stored values, and a session
  login's saved sessions. The next use is refused, and a browser still
  holding it closes at its next action.

### Two-factor codes

- **TOTP.** Conductor computes the current code outside the sandbox.
  - 1Password: point `otp` at `"<vault>/<item>/one-time password?attribute=otp"`.
    `op` returns the code, and the seed never leaves 1Password.
  - Other vaults: store an `otpauth://` URI or the base32 seed.

  Neither the seed nor the code reaches the agent.
- **Push approvals.** With `approval: true`, conductor notifies you after it
  submits. This is a `needs_input` event, so your configured `conductor.*`
  triggers alert you (ntfy, Pushover, Slack, …). The agent then waits, and
  `login` returns once the page moves on, or with "still waiting"; the agent
  continues with `await-approval`.
- **Emailed codes** (opt-in). A credential names a mailbox and the message
  that carries its code:

  ```yaml
  browser:
    mailboxes:
      agent-inbox:
        imap: imap.example.com:993            # 993: TLS; or starttls: true on 143
        username: { vault: op, key: "Agents/Mail/username" }
        password: { vault: op, key: "Agents/Mail/app-password" }   # an app password
    credentials:
      - name: app
        # … origin, identities, username, password as above …
        email_code:
          mailbox: agent-inbox
          from: no-reply@app.example.com      # exact sender, or @app.example.com
          subject: "sign-in code"             # text the subject contains
          code: "code is ([0-9]{6})"          # the code: the first group
          # wait: 60s                         # up to 90s
  ```

  What conductor reads:
  - It opens the mailbox **read-only** (`EXAMINE`): nothing is marked read,
    moved or deleted.
  - The server searches by sender and subject. Conductor reads only those
    messages' headers, holds the From address to the configured one exactly,
    and reads the body of the **one newest match that arrived after the sign-in
    step**.
  - It extracts the code with your pattern, fills it like a TOTP code, and
    never uses a message twice.

  The agent never sees the mailbox, the message or the code, and no error
  carries message text. The connection is TLS (implicit or STARTTLS, with
  `ca_file` for a private CA), with plaintext allowed only to a loopback
  address (`plaintext_loopback: true`, for a local bridge). Prefer a mailbox
  that only receives the agent's codes.
- **SMS codes** are not supported: they need a forwarding service. That's a
  follow-up.

### Passkeys

Conductor uses Chromium's WebAuthn **virtual authenticator**. 1Password does
not export passkey private keys, so your own passkeys are never used. Instead:

1. Name one identity on the credential (a passkey is dedicated to one identity
   and one site) and a writable vault for `passkey:`.
2. Register it once, yourself:

   ```sh
   conductor browser passkey register app --url https://app.example.com/settings/security
   ```

   A browser window opens with an empty virtual authenticator (sign in first,
   or seed a session for the identity), and you add a passkey on the site.
   Conductor keeps exactly that credential's private key in the vault. On the
   site it is an **additional, separately revocable** passkey; yours are
   untouched.
3. The agent signs in with `login --credential app --passkey --ref <button>`.
   For that one sign-in, conductor attaches a virtual authenticator holding
   only that passkey to that tab, only while the page is on the bound origin
   (WebAuthn's own RP-ID binding applies too), and removes it after.

This works headless on Linux and macOS (the DevTools WebAuthn domain).

**Prefer agent-specific credentials**: a separate passkey, or a separate
account where the site allows one. They are revocable without touching yours.

## Personal details (profiles)

Agents filling in forms and signing up for things need your name, email,
address and the like. A **profile** is a named set of those details. Several
are allowed: a personal one, a work one. **The agent never receives a value.**
It asks conductor to fill a field by its meaning, and conductor types the value
into the page in the confined browser, the way it fills a password.

```yaml
browser:
  profiles:
    work:
      identities: ["bot:shopper"]                 # who may use it; nobody by default
      fields:                                      # each field: any value source
        name:          '{{ vault "op" "Me/Work/name" }}'
        email:         '{{ vault "op" "Me/Work/email" }}'
        phone:         '${env.WORK_PHONE}'
        company:       '{{ vault "house" "work/company" }}'
        address.line1: '{{ vault "op" "Me/Work/street" }}'
        address.city:  '{{ vault "op" "Me/Work/city" }}'
        # also: given_name, family_name, birthday, address.line2/region/postal_code/country,
        # billing.* (the same parts), custom.<anything>
      sites:                                       # where it may be filled, and which fields there
        - { origin: "https://shop.example.com", fields: [name, email, address] }
      visible: { email: fact }                     # hidden (default) | exists | fact | value
```

- **Values.** They come from a vault (1Password, the conductor vault, pass,
  HashiCorp, a file), `${env.NAME}`, or a named secret. A typed value goes in
  write-only (`conductor browser profile set work email --stdin`), never in
  the config.
- **Opt-in per site and per field.** A field is filled only on an exact origin
  that lists it. `address` and `billing` allow every part of that address.
- **Who.** Only the identities, bots (`bot:<name>`) and teams (`team:<name>`)
  it names. Only for the operator's own request: the same provenance rule as
  logins, so a bot's case requested by a third party gets nothing.
- **Visible facts.** The agent may learn a non-sensitive fact it needs to
  reason with, only for a field you mark:
  - `exists`: the field is set;
  - `fact`: an email's domain, an address's country, a phone's country code,
    or "18 or older" for a birthday;
  - `value`: the value itself.

  Everything else is hidden.

The agent's side:

```sh
conductor browser profiles                          # names, sites and fields allowed there, visible facts
conductor browser fill-form --profile work          # fills every field of the form it may; says which, never what
conductor browser fill-profile e12 email --profile work
```

Every fill is audited with the profile, the field, the site, the identity and
the case: never the value. A value the page echoes back ("Welcome, Jane!") is
scrubbed from what the agent sees, as a password is. A field conductor filled
shows `«filled by conductor»` in a snapshot. A value under 4 characters (a
region code, say) is hidden by its field, not scrubbed from the page's text.

## Payment cards and purchases

Agents can buy things, with protections that are strong by default: **every
purchase is an approval.**

```yaml
operators:
  daniel: { ssh_key: "ssh-ed25519 AAAA…" }       # who may approve (signs with this key)
browser:
  cards:
    shopping:
      kind: virtual                              # virtual | single-use | physical
      number: '{{ vault "op" "Cards/Shopping/number" }}'
      expiry: '{{ vault "op" "Cards/Shopping/expiry" }}'      # MM/YY
      cvc:    '{{ vault "op" "Cards/Shopping/cvc" }}'
      name:   '{{ vault "op" "Cards/Shopping/name" }}'
      merchants: ["https://shop.example.com"]    # none by default: nothing can be bought
      identities: ["bot:shopper"]
      limits: { per_purchase: 50, per_day: 100, per_month: 300, currency: USD }
      identity_limits:
        "bot:shopper": { per_purchase: 20, per_day: 40, per_month: 100 }
  payments:
    approvers: [daniel]                          # default: every operator with a key
    approval_ttl: 1h                             # an approval is single-use and expires
```

**How a purchase goes:**

1. **The agent proposes it:** the card, the merchant (an exact origin on the
   card's list), the items, the total and currency, recurring or one-off, and
   why.

   ```sh
   conductor browser purchase --card shopping --merchant https://shop.example.com \
     --total 24.00 --currency USD --items '[{"name":"Widget","qty":2,"amount":12}]' \
     --why "the case asks for two widgets"
   ```

   Conductor checks the card's merchants and limits, and the identity's,
   before anything else.
2. **You approve it.** It waits in Needs you (a `needs_input` notice). Your
   approval is signed with your operator key, as a bot's proposal is:

   ```sh
   conductor browser purchases --status pending
   conductor browser purchase approve pu7          # shows merchant, total, recurring, who, why; then signs
   ```

   The signature covers the purchase's terms (card, merchant, items, total,
   currency, recurring, identity).
3. **The agent pays, on the checkout page:**

   ```sh
   conductor browser pay pu7 e41                  # e41: the place-order button
   ```

   Conductor then:
   - checks the page is the approved merchant's, exactly;
   - **re-reads the total on the page**. A higher total, another currency, or
     another merchant is refused, and a new purchase for what the page shows
     is proposed for your approval;
   - spends the approval (single-use) and re-checks the limits at that
     moment;
   - fills the card;
   - re-reads the total once more, and places the order itself. If the page
     raised its price under the card, the card is cleared and nothing is
     submitted.
4. **The receipt.** Conductor reads the order confirmation (order number and
   the total charged) into the purchase record and the audit. The agent may
   see the order number and the total. A confirmation charging more than you
   approved is flagged and notified.

**What protects you:**

- **The card never reaches the agent.** Not its number, CVC or expiry; not in
  a reply, a log, the run record or a screenshot. A page echoing the number
  (in full, in groups of four, with dashes) is scrubbed, and the card's fields
  are masked in screenshots. `evaluate` stays off in that browser.
- **Limits.** Per purchase, per day and per 30 days, for the card and for
  each identity, bot or team you list. A bot's or team's own caps and a
  ticket's budget stack on top, and the lowest wins (see
  [Spend limits stack](#spend-limits-stack)). They are checked when a
  purchase is proposed, approved and paid.
- **Recurring charges** are flagged. A subscription needs your explicit
  approval for its recurring nature, and never runs unattended.
- **Merchants.** Each card has its own allowlist of exact origins; none by
  default.
- **Kill switch.**
  - `conductor browser card freeze shopping` stops every fill at once (config
    cards included).
  - `conductor pause` (pause all automation) also blocks every payment.
- **Defense in depth: use a limited card.** Conductor's checks are the first
  layer. A virtual or single-use card with its own limits is the second:
  1Password's masked cards, Privacy.com, Stripe Issuing, or a card number in a
  vault. `conductor validate` reminds you when a card isn't one.

**Unattended purchases (opt-in, narrow).**
- **Bots only, and never the default.** A bot may buy without a per-purchase
  approval only under a small cap, at named merchants. It's #178's
  `commit_money` tier with its spend cap, so a bot's every purchase also
  counts against its `autonomy.spend_cap`:

  ```yaml
  bots:
    shopper:
      autonomy:
        tiers:   { commit_money: [browser.purchase] }
        amounts: { browser.purchase: total }
        spend_cap: { per_action: 50, per_period: 200 }
        unattended:                                  # a widening: never the default
          browser.purchase: { per_action: 10, per_period: 30, period: 24h, merchants: ["https://shop.example.com"] }
  ```

- **It must fit.** The unattended cap must fit inside `spend_cap`. A bot in a
  team needs the team's `autonomy.ceiling: commit_money`.
- **When it applies.** Only when all of these hold:
  - the bot is out of shadow mode and its sessions are sandboxed;
  - the request is yours or the bot's own (never a third party's, an event's
    or a sweep's);
  - the charge isn't recurring;
  - the merchant is named.

  Anything else is a proposal you approve with `conductor bot approve`.
- **The card still applies.** Its own merchant list and limits hold either
  way.

### Spend limits stack

Every purchase is held to every layer that applies, and **the lowest wins**.
No layer can widen another:

1. **The card's limits**: `limits:` (per purchase, per day, per 30 days).
2. **The card's limits for the caller**: `identity_limits:` for its
   identity, bot or team.
3. **The bot's and its team's caps**: `purchases:` on the bot,
   `budget.purchases:` on its team. Optionally tighter per card. A team's
   are ceilings for its bots: `conductor validate` refuses a bot cap above
   its team's.
4. **The ticket's budget**: a `spend_limit` on the case the purchase is made
   for. It is spent only by the bot an operator confirmed it for, on an open
   ticket. A limit that reached another bot without an operator confirming
   it, a closed ticket's, or the copy a moved ticket left behind allows
   nothing: the purchase is refused with the reason (see
   [Tickets → Spend limit](Tickets#spend-limit)).

```yaml
teams:
  ops:
    budget:
      purchases: { per_purchase: 100, per_day: 200, per_month: 500, currency: USD }
bots:
  shopper:
    team: ops
    purchases:                                   # each cap ≤ the team's
      per_purchase: 40
      per_day: 100
      per_month: 300
      cards:
        shopping: { per_purchase: 25, per_day: 50, per_month: 150 }
```

All three caps are required wherever caps are set. A bot without
`purchases:` is held to its team's.

**A ticket's budget** is set on a bot's case, for one task: "buy a hard
drive, up to $120".

```sh
conductor browser budget shopper c3 --amount 120.00 --currency USD   [--card shopping] [--merchant https://shop.example.com]…
conductor browser budget shopper c3              # what it holds and what is left
conductor browser budget shopper c3 --clear
```

- **What counts.** Every purchase proposed under the case counts against
  its budget for its whole life: pending, approved, being paid, paid.
- **What frees budget.** A purchase you deny, or one that is refused or
  expires, frees its share.
- **No carry-over.** Unspent budget is never moved anywhere else.
- **Optional restrictions.** `--card` and `--merchant` hold the budget to one
  card and to those exact origins.
- **No budget.** A case without one falls back to the other layers.
- **One bot's.** Setting a case's budget confirms it for that case's bot. A
  budget that reached a bot unconfirmed (see
  [Tickets → Spend limit](Tickets#spend-limit)), a closed case's, or a moved
  copy's allows nothing, and `case-budget --show` says why (`held`).
  Confirming a held budget counts as widening, whatever the amount.
- **Clamping.** A ticket's budget can never exceed the layers above it. A
  budget above the lowest 30-day cap of the bot, its team or the card is
  clamped there at every check, and setting one warns you.

On a bot's case the record is
`spend_limit: { amount: "120.00", currency: USD, card: …, merchants: […] }`.
A bot's home session acts for the case the gate holds it to (its
least-trusted open case), so that case's budget applies. A worker acts for
its own.

**Every check names the layer that decided.** Proposing, approving and the
fill each evaluate every layer in one locked write, so two purchases can't
jointly overrun a budget. Each records the binding layer: the one that
refused, or else the one the purchase leaves the least room in. A refusal
says which layer and what is left:

```text
refused: over this ticket's 120.00 USD limit: 35.00 USD left
refused: over bot shopper's 40.00 USD per-purchase limit
refused: over team ops's 200.00 USD daily limit: 15.00 USD left
```

`conductor browser purchase show pu7` and the approval prompt list the
remaining amount per layer, and whether the purchase would be refused now.
`conductor browser spend` adds per-bot and per-team rows.

**On the operator API (read-only):** `GET /v1/purchases`,
`GET /v1/purchases/{id}` (with `remaining_by_layer`), `GET /v1/spend`,
`GET /v1/bots/{bot}/cases/{case}/budget`. A decision, and setting a budget,
stay control-socket operations.

## What an agent can and can't learn or do

| | the agent can | the agent can't |
|---|---|---|
| **Logins** | ask conductor to sign it in, on the bound origin | read a password, code, passkey or session cookie |
| **Personal details** | ask conductor to fill a field, on an opted-in site; learn the facts you made visible | read a value; fill a field on a site or of a kind you didn't allow; use a profile on a third party's request |
| **Cards** | propose a purchase; pay an approved one, once, on its merchant; see the order number and total | read the number, CVC or expiry; pay without your signed approval (or outside its unattended cap); pay more, elsewhere, or twice; approve its own purchase |
| **Pages** | see what the page shows (`snapshot`) and its redacted HTML source (`source`), scrubbed of every value conductor filled | read a filled value out of the source; run `evaluate` in a browser where a value was filled |

What a site can do is what any site can: a page on an origin you opted in
sees the values filled into it. Bind profiles and cards to exact origins you
trust, and prefer a limited card.

## Watching and taking over

- `conductor browser sessions`: the live agent browsers.
- `conductor browser peek <session> --out shot.png`: a screenshot of one
  (audited).
- `conductor browser seed …` / `passkey register …`: your own window
  (`--headless --wait-url` for a scripted site). It's your installed Chrome,
  under the same sandbox as an agent's browser: on Linux with your display's
  socket shown read-only, and on macOS with the window server's services
  allowed by name.

A live, interactive view of an agent's browser belongs to the planned
dashboard.

## Paseo, bots

**Paseo.** A paseo session started on a `<provider>-sandboxed` entry already
has `conductor` and its token. Tell the agent to use `conductor browser …`.
Paseo's own browser tools are in paseo's MCP server, which the sandbox blocks by
design, so conductor's browser is the supported path. A session you start in a
directory of your own is your own request: logged-in sites and credentials
opted in for its identity (`dir:<the directory>`) are available to it.

**Bots** use it from their home session and workers like any agent. A
logged-in site or a credential counts as a mutating capability, so it follows
the case's requester: an operator-requested case may sign in, while a third
party's, an event's, or the bot's own initiative gets the anonymous
profile.

## What remains

These are the limits that still hold:

- **macOS without Docker Desktop, colima or podman.** Chromium runs without
  its own sandbox, so a malicious page that exploits the renderer gets
  everything the browser's jail allows (its profile and its proxy, nothing of
  your Mac's files). Any other local user could also relay through its proxy
  port to the allowlisted sites. Conductor warns about both. Use this mode on
  a single-user Mac, or install an engine.
- **The container's Chromium is as current as its image.** Conductor builds
  the image once, with Alpine's Chromium at that time, and rebuilds it only
  when conductor's image definition changes. To pick up a newer Chromium,
  remove the image (`docker rmi` the `conductor-browser:…` tag); conductor
  rebuilds it at its next start.
- **Page scripts on a credential's own origin can read the field they were
  given.** That's how browsers work, and it's why a credential is bound to an
  exact origin. `source` is **structurally redacted** — a field's value is
  never serialised and the markup a value hides in is stripped — so the same
  residual holds and no more: a hostile script already on the trusted origin
  could pre-encode a value it was given into ordinary visible text, which
  `source` keeps (scrubbed as a backstop). Trusted-origin binding and the
  egress allowlist are what bound that.
- **SMS codes** are not supported: they need a forwarding service.
- **Your own passkeys can't be used**, because 1Password doesn't export their
  private keys. Register a separate passkey for the agent instead.
- **No live view of an agent's browser.** You get a `peek` screenshot. The
  interactive view belongs to the planned dashboard.
- **A test sign-in guesses success by default.** It looks for having left the
  pages it submitted from, with no password or code field left. If a site
  shows its failures elsewhere, give the login `--check-url` or
  `--check-text`.
- **Typed values are sealed with a key file beside them**
  (`<state>/browser/credential-values/vault.key`). A copy of the store alone
  reveals nothing, but anything running as your user can read both. For more,
  keep the value in 1Password or another vault and add it by reference.
- **HashiCorp:** pick the secret's path, then type the field (`path#field`).
  Listing field names would mean reading the secret.
- **The macOS container path hasn't run on a real Mac yet.** It's the same
  code as on Linux, through Docker Desktop's VM.
- **A card form inside an embedded frame isn't filled.** Hosted payment fields
  (Stripe Elements, Braintree, Adyen) live in a frame of the payment
  provider's origin, so `pay` refuses them for now. The checkout must have the
  card fields in its own page.
- **Conductor reads the total the page shows.** A merchant that charges more
  on its server than its page shows is caught only after the fact, by the
  receipt check. A limited card is the layer that stops it.
- **A bot's home session can't use the browser yet on this branch, and no bot
  buys unattended yet.** A bot's workers launch sandboxed, so they can use
  profiles and propose purchases, each approved by you. The home session runs
  through paseo, which is sandboxed only once #178's sandbox seam is wired.
  Unattended purchases need that same seam to report the sandbox; until then
  every bot purchase is a proposal. The gate, its approvals and the
  unattended path are in place and tested.

## Identities

A login or credential names agent sandbox identities:

| who | identity |
|---|---|
| a dispatch on a configured repo | `agent:<profile>@<owner/repo>` |
| a dispatch whose target the sender chose | `agent:<profile>@dispatch-<id>` (never opt one in) |
| an agent you started in a directory | `dir:<the directory>` |
| a bot | the bot's registered identity |
| any session acting for a bot or a team | `bot:<name>`, `team:<name>` (a login added at runtime) |

A trailing `*` matches a prefix (`agent:triage@*`). A passkey credential names
exactly one identity.
