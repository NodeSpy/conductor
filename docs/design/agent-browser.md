# Agent browser — design

Status: **built** on `feat/agent-browser` (stacked on `feat/agent-sandbox`).
Operator docs: `docs/wiki/Browser.md`.

Any agent conductor launches can drive a real browser. The model stays the
sandbox's (`docs/design/agent-sandbox.md`): **agents are read-only, conductor
performs every write.** A browser is a write channel (one click in a logged-in
session can delete or buy something), so the browser is conductor's, not the
agent's.

## 1. Where the browser runs: conductor's, per session, sandboxed

Two shapes were possible:

- **(A) Inside the agent's sandbox (or container).** The agent would hold the
  browser process and its DevTools endpoint. Every control below would then be
  advisory:
  - evaluate gating: the agent can speak CDP itself;
  - audit: actions bypass conductor;
  - upload/download confinement: the browser shares the agent's filesystem;
  - logged-in gating: the cookies sit in a profile the agent can read, then
    replay with `curl` past any audit, or send to any allowlisted host.

  It would also put Chromium's large attack surface beside the agent's own
  files.
- **(B) Conductor launches it, sandboxed, and drives it.** Chosen.

What (B) looks like as built:

- **The agent's side.** The agent's only interface is `conductor browser …`
  over the skill socket it already has, authorized by its token like every
  `conductor call`.
- **One browser per session.** Conductor runs one headless Chromium per agent
  session (per token), launched with conductor's own builtin sandbox:
  - **Linux:** user, mount, pid and net namespaces with `pivot_root`. The jail
    shows only the browser's throwaway directory, Chromium's install
    (read-only) and the base system. Chromium's own sandbox stays on: its
    namespace sandbox nests inside ours (no `--no-sandbox`).
  - **macOS, with a container engine (the default whenever one is
    present):** a container (`internal/browser/container.go`). Section 1a.
  - **macOS without one (the fallback):** Seatbelt, with the same file
    allow-list and the network limited to one proxy port.
    - macOS refuses a Seatbelt sandbox inside another
      (`forbidden-sandbox-reinit`), so Chromium's own per-process sandbox
      cannot nest there. It runs with `--no-sandbox`, and the outer profile
      confines every Chromium process alike. A renderer exploit holds what
      the browser process holds inside the jail (its profile, including a
      logged-in browser's cookies, and its proxy), and nothing beyond.
    - **Mach and IOKit are named, never blanket** (red team C). Headless
      Chromium looks up the agent's service list plus
      `opendirectoryd.api`, and registers and looks up only its own
      child-process rendezvous (`<bundle>.MachPortRendezvousServer.<pid>`)
      and crashpad's handshake port, by regex. IOKit is limited to
      `IOSurfaceRootUserClient` and `RootDomainUserClient`. The list came
      from the sandbox's own denial log on macos-latest. A test asserts that
      the profile has no blanket `mach-lookup`, `mach-register` or
      `iokit-open`. The Keychain's services are denied by name too, along
      with `--use-mock-keychain` and `--password-store=basic`.
    - The operator's own headed takeover window (`Window`) also gets the
      window server's services, by name.
    - **No metadata outside its paths.** Seatbelt counts a directory listing
      as `file-read-metadata`. The browser profile drops the agent profile's
      global metadata allowance and allows it only on its own paths, the
      system's, and the literal components leading to them. A `file://`
      listing of the workspace, `/Users` or the browser's parent fails (CI).
    - Its run directories sit in `/private/tmp/cbr-…`, never under the home,
      so no ancestor of a bound path reveals the home's layout.
    - Chromium's temp dir (its process-singleton socket) is redirected
      (`MAC_CHROMIUM_TMPDIR`) to a private short dir, so it never touches the
      per-user temp dir where the operator's own Chrome keeps its singleton
      socket. CoreFoundation's home (`CFFIXED_USER_HOME`) is the browser's own.
    - **The trusted proxy endpoint is a TCP loopback port.** Chrome takes
      its proxy as host:port. On Linux an in-jail forwarder bridges that to a
      unix socket inside a private network namespace, but macOS has no
      network namespace, so the port sits on the shared loopback. Any local
      user could connect to it and reach the allowlist, but nothing beyond it
      (red team A). Chrome sends no proxy credentials, so nothing only the
      browser holds could gate it. This mode is therefore documented as **single-user Macs
      only**. Conductor logs it at boot and with every session, and adds the
      number of local users (`dscl`, uid ≥ 501) when there's more than one.
      The container path closes it.
- **The protocol.** CDP runs over `--remote-debugging-pipe` (fds 3/4, passed
  through the in-sandbox forwarder by a new `--pass-fds`). There is **no
  debugging port**: nothing but conductor can drive it, not even another local
  user (a TCP port on macOS would be reachable by all). There is no new
  dependency: the CDP client is ~200 lines over `encoding/json`.
- **The network.** The jail has none. Its only way out is the egress proxy
  with the browser's allowlist, through a per-browser **credential-less**
  endpoint, because Chrome cannot send proxy credentials:
  - Linux, and the container on any OS: a unix socket in its own 0700
    directory, shown only to that browser's forwarder (or relayed by
    conductor over the container's stdio).
  - The macOS fallback: a loopback port its Seatbelt profile alone allows.

  Loopback is proxied too (`--proxy-bypass-list=<-loopback>`); QUIC and
  non-proxied WebRTC UDP are off.
- **Lightest.** No browser in the agent's image or sandbox, no Node, no
  Playwright, no new Go module.
- **Container backend.** It works unchanged: the agent only needs its skill
  socket and the `conductor` binary, which both backends already provide, and
  the browser stays host-side under conductor's sandbox.

## 1a. The container (macOS's default; `browser.container: on` anywhere)

Decided at boot (`cmd/conductor/browseriso.go`):
- `auto` (the default) uses the container on macOS whenever an engine is
  present and answers (`docker`, then `podman`; `docker version` must
  succeed, so Docker Desktop installed but stopped counts as none), and the
  host's namespaces on Linux;
  `on` requires it; `off` never uses it.
- **Never quietly weaker.** While the image builds and proves itself, browser
  actions are refused as "being prepared". If it can't sandbox Chromium,
  `auto` falls back to Seatbelt with the warning, and `on` refuses every
  browser action.

The container:
- **Image.** `conductor-browser:<hash>` is built from an embedded Dockerfile:
  the shim built from embedded Go source, plus Alpine's `chromium`, run as
  uid 1000. It's built with `docker build -` and a tar context, from no file
  on the host.
- **Run flags.** `--network none`, `--cap-drop ALL`,
  `--security-opt no-new-privileges`, `--read-only` with tmpfs home and
  `/tmp`, memory and pid limits, and no bind mount at all.
- **Seccomp.** The engine's default profile (moby's, embedded) plus exactly
  what Chromium's namespace sandbox needs: `clone`/`unshare` with namespace
  flags, `setns`, `chroot`. `clone3` returns `ENOSYS`, so glibc and musl
  fall back to `clone`, whose flags the profile can see.
- **Chromium's own sandbox is on.** Its namespace layer and seccomp-BPF run
  inside the container, which is the point: a renderer exploit then faces
  Chromium's sandbox, then the container, then the engine's VM on macOS.
  Before first use conductor opens `chrome://sandbox` in it and requires
  "You are adequately sandboxed" (namespace layer, PID and network
  namespaces, seccomp-BPF).
- **One stdio channel.** The engine's stdin/stdout carry everything
  (`internal/browser/mux.go` on the host, `containershim` inside):
  - channel 0 is the DevTools pipe (fds 3/4 of Chromium);
  - channel 1 is file transfers, confined to the in-container transfer dir
    (no symlink, `O_NOFOLLOW`, `O_EXCL`);
  - channels ≥ 2 are proxy connections. Chromium dials the shim's
    in-container loopback listener; each connection is relayed to that
    browser's unix-socket trusted endpoint on the host.

  No TCP port is opened on the host or the Mac. This works the same through
  Docker Desktop's or colima's VM.
- **Files.** Downloads and uploads move through the file channel into the
  same host transfer directory the native browser uses, so section 5's
  confinement is unchanged.
- **Audit.** Every session's isolation (`container`, `seatbelt`,
  `namespace`) is logged and recorded with each action, and `status` shows
  it, before a browser starts too.
- **Proven where.** On Linux, locally and in CI: the whole browser and
  credential suite runs with every browser in a container, and
  `chrome://sandbox` passes. The live proofs (a real agent through paseo,
  pass 2 and the login-setup pass) ran with `container: on`.
- **Not yet on a Mac.**
  - GitHub's macOS runners have no engine.
  - The real-desktop Mac now has Docker Desktop installed, but stopped.
    It wasn't started for a run, because every test run that touches
    processes, containers or the sandbox must run inside a PID namespace
    (after a mutant's `kill(-1)` took the build box down), and macOS has
    none.
  - The code path is the same on both, and the engine is reached through
    its CLI either way. What is unproven is Docker Desktop's VM itself:
    its stdio relay and its seccomp profile handling.
- **The container backend** (`feat/agent-container`) finds its engine the
  same way. `FindEngine` here mirrors its `agentsandbox.ResolveEngine` and
  ping, and becomes a call to it once both branches have landed.

## 2. The allowlist is enforced by the proxy

- `browser.allow` (plus `sandbox.egress` and the identity's registry egress)
  is the browser's allowlist. The agent's own process never gets it: it cannot
  reach those sites past the audit.
- `checkURL` refuses an off-list URL (and any scheme but http(s)/about:blank)
  up front. That is the readable error; the **proxy** is the boundary.
- A redirect, a subresource or a page script that leaves the list dies at the
  proxy (403 or a tunnel failure). It is audited with the identity (each
  browser gets its own proxy manager), and the SSRF guard applies.
- **Exact host binding** (`specialAllowed`). A loopback, private or
  link-local address is reached only through an entry that names it:
  - the same IP literal, listed as a literal (not inside a glob), with a
    matching port;
  - or `localhost` listed and asked for. `localhost` is resolved by conductor
    to `127.0.0.1`/`::1`, never by the resolver.

  `localhost:3000`, `127.0.0.1:3000` and `[::1]:3000` are three entries, and
  none implies another. An allowlisted name that resolves (or rebinds) to an
  internal address is refused even when that address is listed.

## 3. Profiles: anonymous by default, logged in by the operator's opt-in

- **Anonymous** means an ephemeral `--user-data-dir`, deleted with the
  browser. Nothing is imported from any real browser profile, and none is ever
  read.
- **A logged-in site** is a `browser.logins` entry (site and identities). It
  is separate from the allowlist and needs a saved session, seeded by the
  operator in one of two ways:
  - a headed takeover (`conductor browser seed`), from which only the site's
    cookies are kept;
  - `cookies import` for that domain only.

  Jars live in `<state>/browser/logins/` (0700/0600).
- **A separate browser.** A logged-in session goes only into a **separate
  browser** whose allowlist is narrowed to the logged-in sites (plus their
  `also:` hosts). An untrusted allowlisted page never shares a browser, a
  cookie jar or a network with the operator's session, so a cross-site request
  into it is structurally impossible. A navigation routes by host: a granted
  site goes to the logged-in browser, anything else to the anonymous one.
- **Write-back is update-only.** Cookie values and expiry rotate; no cookie is
  ever added or removed. An agent cannot plant a cookie in the operator's
  session or clear one from it.
- **Provenance gating** is structural: a logged-in browser exists only while
  `Provenance(principal, site)` allows it, and that is checked at **every
  action**.
  - The default rule, `OperatorOnly`, admits only requester `operator`. The
    token carries the requester: `operator` for a local `conductor run`
    (`core.Trigger.Operator`, not the MCP callable face) and for a session
    started in a directory of the operator's own (`DefaultEntry`); `event` for
    any trigger; `""` (least trust) for a launcher's utility process.
  - Bots (#178) replace `requesterFor` with the case's requester class, read from
    the ledger at every action, so a home session whose open cases include a
    third party's falls to anonymous.
  - When the answer changes, the logged-in browser is saved and closed before
    the action runs.

## 4. Credentials the agent never sees

A `browser.credentials` entry binds a sign-in to identities and an exact
origin, by vault reference only. The sources are conductor's existing
`vaults:` framework (1Password through `op`, the conductor vault, pass, file,
HashiCorp), so no vendor is hardcoded. A new uncached `vaults.ReadFresh`
fetches one value per fill and taints it, so it is redacted from logs.

A fill runs these checks, in order, and **fetches nothing until all pass**:

1. **The credential.** It is bound to the identity, and provenance is the
   operator's (the same rule as logged-in sites).
2. **The origin.** The page's main-frame `securityOrigin` comes from
   `Page.getFrameTree` (the browser's view, never the page's). It must equal
   the credential's scheme, host and port exactly, and its URL must sit under
   the bound path. A lookalike host, a redirect's landing page or an off-path
   page is refused.
3. **The allowlist.** The page is on the browser's allowlist.
4. **The field.**
   - It is resolved into a fresh **isolated world** of the main frame, and
     `this.ownerDocument === document && window === window.top` is checked
     there, with that world's pristine prototypes. An embedded frame's field
     (same-origin or not) is refused. A test found that `DOM.resolveNode` into
     the main world does *not* refuse a same-origin frame's node by itself.
   - The field fits the value: password only into `type=password`, never a
     username or code into one.

The value is then set through the isolated world's own `value` setter, and
`input`/`change` are dispatched. Page scripts on the bound origin can read the
field, as browsers work; nothing else can.

**A usable credential's host is a logged-in site.** Found live: the first cut
signed the agent in inside the anonymous browser, beside every other
allowlisted page. Now:
- `grant()` adds each credential host whose identity binding and provenance
  hold;
- that host's pages always open in the separate, narrowed browser;
- a provenance change closes that browser at the next action.

`login` opens `login_url` when the current page has nothing to fill (found by
the e2e).

**A sign-in runs only in that separate browser.** Found live, with a real
agent: a `login` with no page open ran in the anonymous browser. `pick` had
no target to route by, and `login` then navigated to the sign-in page inside
whatever browser was open. Now:
- a `login` from no page (or from the anonymous browser) is routed by the
  credential's own sign-in page;
- `login` and `fill-credential` refuse in any browser but the logged-in one,
  so an anonymous browser that reached the credential's origin (say, by an
  open redirect on another allowlisted site) gets no fill.

What happens after a fill, in that browser:
- **Replies are scrubbed.** Every filled password and code is replaced in
  every reply (snapshots, titles, URLs, tab lists, downloads, dialogs) and in
  audit URLs.
- **`wait` matches the scrubbed text** in conductor, never in the page, so it
  is not a guessing oracle.
- **Screenshots are masked.** Every input holding a filled value, and every
  element whose text shows one, is blacked out. A browser with filled values
  whose boxes cannot be found gives **no** screenshot (fail closed).
- **Downloads containing a filled value are refused.**
- **`evaluate` is off for the rest of that browser's life**, even read-only.
  A side-effect-free expression can still read a field and transform it
  (reverse, char codes) past any string scrub; only refusing it is a
  guarantee.
- **`source` stays on, because it cannot carry a value out.** The `source`
  artifact (below) never emits a field's value and strips the markup a value
  hides in, structurally, as it serialises — so it is the read path that
  survives a fill, and the reason `evaluate` can stay off for most work.

### `source`: a read-only, structurally-redacted HTML view

`snapshot` flattens a page to its a11y tree; `source` gives the HTML structure
for the work that needs it (a table's shape, hidden fields, a widget's
markup), without ever becoming a way to read a filled value back.

- **Conductor builds the string, node by node.** It sources the DOM over CDP
  (`DOM.getDocument`, depth −1, pierced) and serialises each node itself. It
  never returns the browser's raw `outerHTML` and scrubs afterward: the
  redaction is at the point of serialisation.
- **Structural redaction, as each node is written:**
  - a node in `secretNodes` (where a value was filled) has its value, checked
    state and text dropped, with a `[redacted]` placeholder;
  - every `<input>`/`<textarea>`/`<select>` has its value dropped whatever its
    origin (`value="[redacted]"` marks that one exists) — broader than
    `secretNodes`, so a value the agent typed is consistent and a mis-tracked
    secret cannot slip through;
  - `<script>`/`<style>` bodies, HTML comments, `<template>` contents, `data-*`
    values (key kept), `style` and `on*` handler attributes are stripped — the
    places a value hides, useless to the agent;
  - tag names, structure, ids/classes/roles/aria, link `href`s (scrubbed) and
    visible text (scrubbed) survive;
  - a same-origin frame and an open shadow root are included with the same
    redaction; a cross-origin frame's contents never are.
- **A whole-string scrub is a backstop only**, for a value that reached visible
  text or a link; the structural redaction is the guarantee, and a test proves
  the value is absent with that backstop disabled.
- **Size-capped** like `snapshot` (`--max`, default 25k, max 200k).
- It needs **no `evaluate` opt-in** and **works after a fill**.

The second factors:
- **TOTP.** `OTPCode` (RFC 6238; SHA1/256/512, digits, period) computes the
  code from an `otpauth://` URI or a base32 seed, or passes through a code the
  source computed (1Password's `?attribute=otp`, where the seed never leaves
  1Password).
- **Push.** With `approval: true`, conductor emits `needs_input` (the
  operator's `conductor.*` triggers alert their phone) and waits for the page
  to move on (bounded; `await-approval` continues).
- **Emailed codes** (opt-in, `internal/browser/imap.go`). A credential's
  `email_code` names a `browser.mailboxes` entry (IMAP; the username and app
  password are vault references; no provider is hardcoded) and the sender,
  subject and code pattern. After the step that sends the code, conductor:
  1. connects over TLS (implicit or STARTTLS, optional `ca_file`).
     Plaintext is allowed only to a loopback address, and refused before any
     dial.
  2. `EXAMINE`s the folder (read-only, so no flag changes);
  3. `UID SEARCH SINCE … FROM … SUBJECT …` on the server;
  4. fetches only those messages' `INTERNALDATE` and From/Subject headers
     (`BODY.PEEK`). It keeps a message only if the From address equals the
     configured one (or its `@domain`), and only if it arrived after the step
     (30 s clock skew);
  5. reads the body of the newest such message, decoding multipart,
     quoted-printable and base64;
  6. extracts the code with the pattern and fills it like a TOTP code.

  A message is used once per identity and credential. It polls every 3 s up
  to `wait` (≤ 90 s). No error carries message text, and the code is
  scrubbed like every filled value.
- **SMS codes** are not supported (they need a forwarding service).

**Passkeys** use CDP's WebAuthn virtual authenticator:
- **Registration.** At a one-time operator takeover (`passkey register`), an
  empty virtual authenticator is armed. The operator adds a passkey on the
  site, and conductor takes the one credential whose RP covers the bound host
  and writes it (private key included) to the credential's writable vault.
- **Use.** For one `login --passkey`, conductor:
  1. re-checks the origin;
  2. attaches a virtual authenticator holding only that passkey to that tab;
  3. re-checks the origin before the click;
  4. waits for the page to move on;
  5. persists the new sign count;
  6. removes the authenticator.

  WebAuthn's own RP-ID/origin binding applies on top.
- **Binding.** A passkey credential names exactly one identity
  (validation-enforced).
- **Platforms.** It works headless on Linux and macOS, and in the container
  (CI proves all three), and in the operator's headed window (Linux on Xvfb
  and macOS on the runner's window server, in CI, and on a real desktop
  Mac). On
  `localhost` the test site is a secure context; an IP literal cannot be an RP
  ID, which is why the tests bind `localhost` and use `127.0.0.1` as the
  lookalike.

## 4a. Login setup: the dashboard's backend

The dashboard offers three ways to add a login. Each is a control-socket
operation (`browser-login-*`, `browser-vault-*` in
`cmd/conductor/browserlogins.go`) and a `conductor browser credential …`
command. The store is `internal/browser/loginstore.go`; its logins join the
config's `browser.credentials` and `browser.logins` (`WithStore`, and the
config's names win).

- **Typed** (`password`). A username, a password, and optionally a TOTP
  secret or `otpauth://` URI.
  - The values go into the store's own vault: conductor's sealed vault
    format, keyed by a random key in its own 0600 file, and not a `vaults:`
    entry. So no `vault get`, template, verb or broker grant names it.
  - **Write-only:** set, replace, remove. The one read path is unexported
    and used only by a fill (and the resolve check, which returns nothing of
    it). A reflection test pins the store's exported methods.
- **From a vault** (`vault`). The picker gets metadata only
  (`vaults.ListItems` / `ItemDetail`).
  - **1Password.** `op item list` / `op item get --format json` are decoded
    into types with no value field: a value (or an OTP field's current code)
    is dropped by the parse itself, and the buffer is zeroed after. A test
    feeds fixture values and asserts that none crosses any listing or any
    control response, and a reflection test pins the types' fields.
  - **The mapping.** Username, password and code come from 1Password's
    purpose, then type, then label. The reference is canonical:
    `{{ vault "op" "<op's own reference without op://>" }}`, plus
    `?attribute=otp` for an OTP field, so the seed stays in 1Password.
  - **Other vaults.** pass lists names from the store's files (never
    `pass show`); HashiCorp `LIST`s metadata paths (a field is named by
    hand); conductor and file list entry names.
- **Sign in yourself** (`session`). A login site from the store, seeded by
  the existing takeover. The inventory reads its expiry from the saved
  cookies: the earliest persistent cookie's, or unknown when one is a
  session cookie.

**Any value source per field** (`secrets.ParseSource`, at the shared
resolver, so every config field gets it too):
- `${env.NAME}` and `{{secret "<vault>/<key>"}}` resolve wherever
  `env:`/`{{ vault }}` did;
- a typed value is stored write-only;
- `${settings.…}` is refused as a secret source: at the shared parser, at
  set time, and at load on the raw body (`checkSecretSources`, before either
  substitution), because a substituted setting is indistinguishable from a
  literal afterwards;
- the loader's `${NAME}` is refused in a secret field for the same reason;
- everything resolves at fill time, outside the sandbox, and is tainted.

**Bindings** are identities (exact or prefix), `bot:<name>` or
`team:<name>`. A bot or team binding matches a session whose
`Principal.Groups` names it (the `groupsFor` seam, wired by bots, #178).
**Origins** are exact, and several are allowed: a fill checks the one the
page is on (`pickOrigin`: the browser's security origin, else the login
URL's, else the first). The fill's own origin check is unchanged.

**Classification.** A new login, binding, origin, `also` host or site is
`widening` (the dashboard's passkey gate). Removals are `narrowing`, and a
field's value or source is `neutral`. `dry_run` answers without changing
anything.

**Resolve check** (`CheckSources`). It reads each field fresh, as a fill
would, and returns per field only `ok` or the error. A code's secret must
parse. A password or code from `${env.…}` gets a warning (plain text; named
when `conductor.env` sets it).

**Test login** (`TestLogin`). It is the agents' own pipeline (`doWith`)
with a settings view narrowed to that one credential:
- the identity is `operator:test-login`, the requester the operator;
- the allowlist is the allowlist's entries for the credential's hosts;
- there is no login site and no evaluate;
- a throwaway workspace and session are removed after.

Signed in means the check's URL prefix or text when given; else the page
is on none of the pages a sign-in step was submitted from (the instance
records them), nor the start page, and asks for no password or code. Found
by its test: a site that answers a wrong code on the code page itself, with
no form, read as signed in under the first rule ("no field left"). The answer is
`ok`, `failed` (why), or `needs-you` (an approval still waiting). No
value, no screenshot and no cookie are kept. It is audited and recorded as
the login's last test.

**Inventory and usage.** For each login: method, origins or site,
bindings, field sources (`(stored, write-only)` for a typed value), item,
added and via, last used, last test, and session expiry. Every
`login`/`fill-credential` appends identity, requester, dispatch, action,
outcome and origin to `credential-usage.jsonl` (bounded). **Revoking**
removes the record and its stored values, and a session login's saved
sessions. The next use is refused ("no credential").

## 4b. Personal details and payment cards

Bots fill forms, sign up for things and buy things. The login model
carries over: values the agent never sees, conductor fills them, a
per-site opt-in by the operator, provenance gating, and values in vaults or
stored write-only. Code: `internal/browser/wallet.go` (types, the store),
`purchases.go` (the ledger), `walletfill.go` (the agent's actions), and
`cmd/conductor/browserwallet.go` (the operator's ops and CLI).

**Profiles.**
- **Fields.** Each canonical field (name, email, phone, company, birthday,
  `address.*`, `billing.*`, `custom.*`) takes any value source. Typed values
  go into the login store's sealed vault, write-only.
- **Who.** Identities, `bot:` and `team:` bindings; `OperatorOnly`
  provenance, as for logins.
- **Where.** A list of sites, each an exact origin with the fields allowed
  there. `address`/`billing` allow every part of that address.
- **The fill.**
  - `fill-profile` and `fill-form` (a finder maps a form by `autocomplete`
    tokens, with billing sections, then by labels) resolve the field in the
    main frame's isolated world.
  - The fill re-checks the origin atomically with the set (`walletSet`, for
    inputs, textareas and selects), and refuses password, hidden and file
    inputs.
  - The value is remembered for the scrub (4 characters and up) and by its
    node.
- **Snapshots** show `«filled by conductor»` in place of any filled field's
  value, so a CVC or a region code (too short to scrub from text) is still
  hidden.
- **Visible facts.** For a field the operator marks: `exists`; `fact`
  (`profileFact`: an email's domain, a country, a phone's country code, 18 or
  older); or `value`. Facts too need the operator's provenance.

**Cards and purchases.**
- **A card** holds:
  - its sources (number, expiry, CVC, name), Luhn- and MM/YY-checked when
    typed;
  - its exact merchants (none by default);
  - its bindings and `limits` (per purchase, day, month), plus
    `identity_limits`;
  - a freeze (in the store, so it covers config cards too).
- **The flow.**
  1. **Propose.** The card is bound and unfrozen, the merchant listed, the
     limits hold; the purchase is recorded pending, with a digest over its
     terms.
  2. **Approve.** An operator in `PaymentApprovers` signs
     `ApprovalPayload("browser-purchase", id, digest, decision, …)` with
     their SSH key: #178's `SignApproval`/`VerifyApproval`, the same path as
     a bot's proposal. `Approve` is once-only under the ledger's lock, and
     the approval expires (`approval_ttl`, default 1h).
  3. **Pay**, all in one browser action, so no agent action can interleave:
     - the page's main-frame security origin must equal the merchant;
     - the total re-check reads every total-labelled amount in the main
       document (script, style and template excluded; hidden elements
       included, since an extra total can only cause a refusal) and the
       agent's `--total-ref`. No total, another currency, or a maximum above
       the approval is refused, and a superseding purchase is proposed;
     - the card fields are found (in the page's own document only);
     - `Consume` spends the approval and re-checks the limits under the
       ledger's lock (the period boundary at the moment of spending: a
       spend counts while strictly inside the window, as the bot ledger
       counts);
     - the card is filled;
     - the total is re-read, and conductor clicks place-order itself. A
       change after the fill clears the card, nothing is submitted, and the
       spend is released (`Unspend`; the approval stays used).
  4. **Receipt.** The confirmation's order number and charged total are
     recorded, and a charge above the approval is flagged and notified.
- **Scrubbing.** The card number in every form (bare, groups of four by space
  or dash), the expiry forms, and the holder's name are scrubbed from every
  reply. The CVC is hidden by its node. Screenshots mask filled fields, and
  `evaluate` is off.
- **One spend-cap rule.** `config.SpendCap.Check` is the rule a bot's
  `commit_money` uses: the bot gate now calls it, and card and identity
  limits are expressed as spend caps (a day and a 30-day month).
- **Kill switches.** A card freeze, and the engine's pause file ("pause all
  automation"), stop proposals and fills.

**Bots.**
- **The gate.** A bot session's purchase goes to its gate as the verb
  `browser.purchase` (`commit_money`, amount `total`, under `spend_cap`).
  The proposal's execution (`conductor bot approve`) runs through a
  `RunVerb` interceptor. It re-checks the gate in the approved context, then
  approves the browser's purchase (`ApproveBotPurchase`, matched by the
  executing proposal: `bot.ApprovedProposal`).
- **Unattended** is a new gate branch for `commit_money` (`Gate.unattended`).
  Every condition must hold:
  - an operator's `autonomy.unattended` entry for the verb;
  - not shadow, sandboxed;
  - requester class operator or bot;
  - not recurring;
  - a named merchant;
  - the unattended cap (counting only unattended spends) and `spend_cap`
    (all spends), both re-checked inside the ledger write that records the
    spend.

  Config refuses an entry that isn't `commit_money`, or doesn't fit inside
  `spend_cap`. A team's bot needs the team's new `autonomy.ceiling:
  commit_money`, and the entry is a template widening.
- **Provenance.** `requesterFor` reads the gate's class for a bot session
  (`Gate.RequesterClass`), and `groupsFor` gives `bot:<name>` and
  `team:<team>`.

**Spend limits stack; the lowest wins.**
- **The layers:**
  - the card's `limits`;
  - its `identity_limits` for the caller;
  - the bot's `purchases:` and its team's `budget.purchases:` (each
    optionally per card);
  - the ticket's `spend_limit`.

  Each is evaluated as windows (per purchase, a day, 30 days; a ticket's
  whole life) by one function, `evaluate`, over the ledger. A spend entry
  records the bot and team it was made for, so a bot's window counts only
  its own spend and a team's counts its bots'.
- **Narrow, never widen.**
  - Config refuses a bot's caps above its team's: overall, per card, and a
    bot's per-card caps against the team's overall ones.
  - A bot template can't set `purchases:`.
  - A ticket's budget above the lowest 30-day cap of the layers over it is
    clamped there at every check. The `browser-case-budget` op warns when
    one is set.
- **The ticket's budget** is read through a small interface (`CaseBudgets`:
  a case reference to amount, currency, optional card and merchants, and
  the key its spend counts under). The default reads the case ledger's
  `spend_limit`, and a ticket store can supply its own.
  - **What counts:** every purchase proposed under the case, while pending,
    approved, being paid or paid.
  - **What frees it:** a purchase that is refused, denied or expired.
  - Unspent budget is never transferable.
  - A home session acts for the case the gate holds it to.
- **Check points.** Propose (`ProposeWithin`), approve (`ApproveWithin`,
  also on the bot path) and the fill (`Consume`) each evaluate every layer
  inside the ledger's locked write: two proposals can't jointly overrun a
  ticket. Each records the layers, the stage and the binding layer (the
  refusing one, or else the one with the least room left).
  - A refusal names the layer and what is left.
  - The approval view carries `remaining_by_layer` and `would_refuse`.
  - The operator API serves these read-only; a budget is set only over the
    control socket.

**Classification for the dashboard.**
- **Ticket budgets:** a new, larger, re-currencied, card-freed or
  merchant-widened budget is a widening; a smaller, removed or newly
  restricted one is a narrowing.
- **Widening:** adding a profile or card, a binding, a site or field, a
  merchant, a higher limit or a looser identity limit, a visibility raised,
  an unfreeze.
- **Narrowing:** removals, a lower limit, a new identity limit, a freeze.
- `dry_run` answers without changing anything.

**The operator's threat model** (also in the wiki):
- **What the agent can learn:**
  - the profile and card names it's bound to, their sites, merchants and
    limits;
  - the visible facts;
  - a purchase's status;
  - after it, the order number and the charged total.
- **What it can do:**
  - fill an allowed field on an allowed site;
  - propose a purchase;
  - pay an approved one once, on its merchant, at or under its total.
- **What it cannot do:**
  - read a value;
  - approve, enlarge, redirect or reuse a purchase;
  - fill a card without one;
  - use a profile or card for a third party's request.
- **What a merchant sees:** the values filled into its page (that is the
  point). Exact origins and limited cards bound the damage.

## 5. Files: only through the workspace, by conductor

- The browser never sees the workspace:
  - **Uploads.** Conductor copies a workspace file into the browser's private
    transfer directory first.
  - **Downloads.** Conductor moves them out of it, into
    `<workspace>/.browser/downloads/`.
  - **Screenshots.** Conductor writes them.
- Every path goes through an `os.Root` on the workspace (no absolute path out,
  no `..`, no symlink escape). On top of that:
  - no `.git` component;
  - none of the workspace's read-only or hidden carve-outs;
  - `O_EXCL`, never an overwrite;
  - size limits.

## 5a. `evaluate`: per identity, and off after a fill

- **Opt-in.** `browser.evaluate` lists identities (`*` prefixes allowed), and
  defaults to none. The old global `read-only` value is refused at validation
  with a hint.
- **Read-only.** For an opted-in identity, an expression runs only with V8's
  `throwOnSideEffect`: no DOM write, no network, no storage.
- **Off after any credential fill.** Once a credential has been filled in a
  browser, `evaluate` stays off for that browser's whole life, opt-in or not
  (section 4).
- **Audited.** Every evaluate's audit entry records `evaluate_opt_in` and the
  expression's length.

## 6. Audit

Every action is audited (`event: browser`). An entry has:
- the action and its outcome (`ok`, `refused` or `error`, with a reason);
- the identity, agent, repo, dispatch and requester;
- the page URL (query, fragment and userinfo dropped);
- the element ref;
- `logged_in` and its sites;
- for a credential: its name, the origin, the field and the steps.

Fills and evaluate record a length only. The other audited events are
downloads (path, source URL), proxy refusals (`egress`), and the operator's
session saves, deletes, takeovers, passkey saves and peeks.

## 7. The token stands for its sandbox

The browser keys on the token's `skill.Identity.Sandbox` binding: the
identity, the workspace, its egress, and its carve-outs.
- **Paseo / `sandbox exec`.** The resolver stamps the binding at mint.
- **cli/acp launches.** The controller mints with it too (`SkillEnvFor`).

Found live: looking the workspace up by dispatch id failed for a second
dispatch into a resumed workspace, because the registry keeps the first
dispatch's entry so a resume finds its own state. A token with no binding gets
no browser.

## 8. Paseo, bots, and the container backend

- **Paseo.** It works through the `<provider>-sandboxed` entries:
  `conductor sandbox exec` mints a token bound (`skill.Identity.Sandbox`) to
  the session's sandbox identity and workspace. Paseo's own browser MCP tools
  are blocked by the sandbox, by design.
- **Bots** (#178, formerly members). They wire two seams in
  `cmd/conductor/browser.go`: `requesterFor` (the case's requester class:
  `operator`, `bot:<name>`, …; or `Manager.Provenance`), and `groupsFor`
  (the bots and teams a session acts for, which `bot:`/`team:` bindings
  match). A logged-in site or a credential is a mutating
  capability, so it is allowed for an operator-requested case, and an event's,
  a third party's, a proactive case's, or a child of a non-operator case gets
  the anonymous profile. A bot's tier for `browser.*` can narrow it further
  there.
- **Container backend.** Nothing changes: the agent keeps its socket and
  token, and the browser stays conductor's.

## 9. What the red team should attack (browser mode)

1. **Allowlist escape.**
   - DNS rebinding of an allowlisted name onto loopback or metadata.
   - Proxy auth confusion on the credential-less endpoint (any same-uid
     process can reach it; is anything more reachable through it than the
     allowlist?).
   - The macOS fallback's trusted loopback port (any local user; documented
     single-user only): anything beyond the allowlist through it?
   - Exact binding: reach `127.0.0.1` through a `localhost` entry or the
     reverse; `[::1]`, `::ffff:127.0.0.1`, `0.0.0.0`, decimal/octal IPs, a
     trailing dot, `localhost.` and `*.localhost`.
   - WebRTC, QUIC, DNS prefetch, service workers, `chrome://` / `file://` /
     `view-source:` / `data:` / `blob:` navigations started by a page.
2. **Logged-in session theft.**
   - Read the jar from inside any sandbox.
   - Get cookies into an agent reply (`snapshot`, titles, `document.cookie`
     via `evaluate` — HttpOnly or not).
   - Make the logged-in browser load an untrusted page (an open redirect on
     the logged-in site; an `also:` host).
   - Cross-site requests from the anonymous browser.
   - Session fixation through write-back.
3. **Provenance.**
   - Make an event's dispatch look like the operator's: `Trigger.Operator`
     paths, `DefaultEntry` reached through an agent, a token re-minted for
     another workspace, a durable hand-off session.
   - Race a member's case change against an action.
4. **Credential exfiltration.**
   - A frame or lookalike field: `about:blank`/`srcdoc` frames,
     `document.domain`, a same-origin frame swapped in between the check and
     the set, a navigation racing the fill.
   - Field-type confusion (a `password` input relabelled by page script).
   - Reading a filled value back: `select` into another field and snapshot,
     clipboard, drag, form `GET` putting it in the URL, `history`, page
     titles, `wait` as an oracle, screenshots of transformed renderings
     (CSS `content`, canvas), downloads, a page echoing the value into a
     cross-origin request.
   - The TOTP seed or code through vault errors.
5. **Passkey abuse.**
   - Using it on a lookalike (RP ID suffix rules).
   - Keeping the authenticator attached after login.
   - Registering a passkey for another RP during the takeover.
   - Reading the private key back through `WebAuthn.getCredentials`: is it
     reachable from any agent-facing path?
6. **Workspace confinement.** Symlink races between `checkRel` and
   `os.Root`; hard links; a download name with control or RTL characters; a
   screenshot written into a carve-out; uploads from `/proc/self/…`.
7. **Browser-process escape.**
   - The jail: what does Chromium see?
   - Its own sandbox nesting, `/dev/shm`.
   - The `--pass-fds` path in `sandbox-net`: can a payload inherit more than
     fds 3/4?
   - The macOS fallback: the named Mach services (`opendirectoryd`, the
     rendezvous and crashpad regexes: can another process's name match?),
     IOKit's two classes, the headed window's extra services, and no
     per-process Chromium sandbox (a renderer holds the jail's reach). Find
     a directory listing or `stat` that still reveals a name outside the
     allowed paths.
8. **The container.**
   - Escape from Chromium's renderer through the widened syscalls (`clone`
     or `unshare` with namespace flags, `setns`, `chroot`) and then from the
     container.
   - The stdio mux: forge a frame from inside (the shim is in the container,
     past Chromium's sandbox; a compromised browser process can write
     anything to it). Can a frame open a channel conductor did not expect,
     make the host read or write outside the transfer directory, or reach a
     host socket other than its own proxy endpoint?
   - The shim's file confinement: symlinks, `..`, hard links inside the
     tmpfs transfer dir.
   - Make conductor run the weaker mode: break the image build or the
     `chrome://sandbox` self-test so `auto` falls back, quietly or not.
   - Image provenance: the build pulls `golang:…-alpine` and `alpine:…` by
     tag.
9. **Emailed codes.**
   - Get a code from a different message: a spoofed From, a lookalike
     domain, an older message, a reply that quotes one, a subject trick.
   - Read anything back through errors or timing.
   - Make conductor write to the mailbox (flags, `\Seen`).
   - Make it talk plaintext to a non-loopback server (STARTTLS stripping, a
     `localhost` that isn't).
10. **Login setup.**
   - Read a typed value back through any operation, the inventory, the
     usage history, an error, the audit, or the store's files.
   - Make a 1Password listing carry a value (an item whose label, title or
     URL holds one is the item owner's choice; a value field must never
     cross).
   - Widen a login without `change: widening` (a binding, an origin, a site,
     a bot or team), or make a dry run change something.
   - Make a test login leak (its result, the audit, a kept cookie or
     screenshot) or run beyond the credential's hosts.
   - Use a `${settings.…}` as a secret source past the raw-body check
     (imports, packs, flow mappings, anchors).
   - Use a revoked login (a live browser, a racing write-back).
11. **Personal details and cards.**
   - Read a profile or card value back:
     - a page echoing it in another form (spaced, reversed, split across
       elements, in an attribute, a title, a URL);
     - a CVC or 2-letter code by its text;
     - screenshots of a transformed rendering;
     - the receipt text;
     - errors, the audit, the ledger.
   - **The total re-check:**
     - show a low "Total" and charge a higher one (server side, or a total
       that isn't labelled "total");
     - a currency trick (`$` that is CAD);
     - change the total between the check and the click (the click follows
       the re-read without a page action in between, but a page timer could
       race it);
     - a total split across nodes;
     - a total only in an image.
   - **The merchant check:** a same-origin page that isn't the checkout; an
     open redirect on the merchant; a card form in a frame of another origin
     (refused); a merchant on `localhost` vs `127.0.0.1`.
   - **The approval:**
     - reuse it, race two pays (`Consume` is under the ledger's lock);
     - approve with a signature for other terms (the digest covers card,
       merchant, items, total, currency, recurring, identity, bot);
     - approve a bot's purchase on the browser path (refused);
     - an expired one; a refusal that leaves the approval usable.
   - **The caps:** the period boundary, an identity limit through a group, a
     purchase in another currency, two unattended purchases racing the bot
     ledger.
   - **The stack:**
     - overrun a ticket's budget with two proposals racing (both evaluated
       under the ledger's lock);
     - free budget by letting an approval expire and then paying it;
     - reuse a refused purchase's share;
     - move a ticket's unspent budget to another case;
     - widen a bot past its team, overall or per card, through config or a
       template;
     - set a ticket above the bot's 30-day cap and spend past the cap;
     - have a home session name a richer case than the gate holds it to;
     - spend another bot's daily room.
   - **Autonomy:** make a bot buy unattended without the opt-in, in shadow,
     unsandboxed, for an event or a third party, recurring, at an unnamed or
     lookalike merchant; or a template or team that widens it.
   - **The kill switches:** fill after a freeze or a pause; a pay already
     past its checks when the freeze lands (the freeze is read at the start
     of the pay).
12. **Resource abuse.** Unbounded tabs, downloads, or wait loops; the
   `max_browsers` cap; idle reaping during a held action.

## 9a. What remains (plain language)

- **macOS without a container engine.** Chromium runs without its own
  sandbox, so a renderer exploit holds the browser jail's reach (its profile,
  a logged-in browser's cookies, its proxy). Any other local user can relay
  through its proxy port to the allowlist. Conductor warns at boot and per
  session, with the local user count. Single-user Macs only, or install an
  engine.
- **The container's Chromium is as current as its image**, which is rebuilt
  only when its definition changes. Removing the image forces a rebuild.
- **Page scripts on a credential's own origin can read the field they're
  given.** That's inherent to the web, and why the binding is exact.
- **SMS codes** are a follow-up.
- **Your own passkeys can't be used** (1Password doesn't export their private
  keys). Register a dedicated one.
- **There's no live view of an agent's browser**, only `peek`.
- **The container path is unproven on a real Mac** (section 1a).
- **A test sign-in's default success check is a heuristic**: off every page
  a step was submitted from, and no password or code field left. A site
  that answers a failure elsewhere needs the login's own `check` (a URL
  prefix or text).
- **The login store's key sits beside it** (`credential-values/vault.key`,
  0600, in conductor's state dir). A copied or committed store is sealed
  without it, but anything running as the operator's user can read both:
  the same trust as the conductor vault's default key file.
- **HashiCorp field names are entered by hand** (`path#field`): listing them
  would mean reading the secret's data.
- **Hosted card fields aren't filled.** Stripe Elements, Braintree and Adyen
  put the card inputs in a frame of the provider's origin, and `pay` fills
  only the page's own document. It refuses such a checkout.
- **The total is the page's word.** A merchant that charges more on its
  server than its page shows is caught after the fact (the receipt check). A
  limited card is the layer that stops it.
- **A total check can be raced by a page timer** in the milliseconds between
  the last re-read and the click.
- **A bot's home session can't use the browser, and nothing buys unattended,
  until #178's sandbox seam is wired** (`sandboxSeam` still answers "no
  sandbox"). A bot's sandboxed workers can use profiles and propose
  purchases.
- **SMS codes and receipts by email** are follow-ups.
- **A ticket's budget is a bot case's** (`bot:<name>/<case>`). A ticket
  store plugs in through `CaseBudgets` and can give a stable key, so a
  ticket handed between bots keeps one count. Until then a hand-off starts
  a new count under the new case. An operator's own sessions have no ticket
  layer.
- **Limits don't convert currencies.** A layer in USD refuses a purchase in
  EUR rather than guess a rate.

## 10. Found on the way (outside the browser)

- **`agentsandbox`**: a fresh identity home's `~/.claude.json` was created
  empty when the operator has none, and current Claude Code exits on an empty
  one ("corrupted"). It is now seeded as `{}`. This is #179's code, fixed
  here because the live proof needed it.
- **Chrome's background services** (search preconnect, GCM, password leak
  detection, field trials) are switched off. None would pass the proxy, but
  leak detection would try to send a filled credential's hash prefix out, and
  the refusals flooded the audit. Each refused target is now audited once per
  browser.

## 11. Not in this PR (follow-ups)

- **A live takeover view**: watch and drive an agent's browser interactively.
  This is the dashboard follow-on; here, `peek` (a screenshot) and the
  operator's own headed windows (`seed`, `passkey register`).
- **SMS codes**: they need a forwarding service (Twilio or a phone bridge)
  that conductor could read like the mailbox.
- **An MCP face** for the browser (the CLI is the contract).

