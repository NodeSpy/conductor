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
- screenshots black out every field and text showing one;
- `wait --text` matches only the scrubbed text, so it is never a guessing
  oracle;
- a download containing a filled value is refused;
- **`evaluate` is off for the rest of that browser's life**, even read-only:
  a side-effect-free expression could read a filled field and transform it
  past any scrub.

Page JavaScript **on the credential's own origin** can read a field it is
given; that is how browsers work, and why the origin binding matters.

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
  result shows in the inventory. By default, success means the page left the
  sign-in page and asks for no password or code. A login can name what
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
  exact origin.
- **SMS codes** are not supported: they need a forwarding service.
- **Your own passkeys can't be used**, because 1Password doesn't export their
  private keys. Register a separate passkey for the agent instead.
- **No live view of an agent's browser.** You get a `peek` screenshot. The
  interactive view belongs to the planned dashboard.

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
