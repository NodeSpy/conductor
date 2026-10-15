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
  - **macOS:** Seatbelt, with the same file allow-list and the network limited
    to one proxy port. Mach rules are broad, which Chromium's child-process
    rendezvous needs, but the Keychain's services are denied by name; `--use-mock-keychain`
    and `--password-store=basic` too.
    - macOS refuses a Seatbelt sandbox inside another
      (`forbidden-sandbox-reinit`), so Chromium's own per-process sandbox
      cannot nest there. It runs with `--no-sandbox`, and the outer profile
      confines every Chromium process alike.
    - Consequence: on macOS a renderer exploit holds what the browser process
      holds inside the jail (its profile, including a logged-in browser's
      cookies, and its proxy), and nothing beyond.
    - Seatbelt classes a directory listing as metadata, which every
      sandboxed process may read: names anywhere, contents only on the
      allow-list. A `file://` listing would show them, but no agent or page
      can navigate there (conductor refuses `file:`; Chrome refuses it from
      a web page).
    - Chromium's temp dir (its process-singleton socket) is redirected
      (`MAC_CHROMIUM_TMPDIR`) to a private short dir, so it never touches the
      per-user temp dir where the operator's own Chrome keeps its singleton
      socket. CoreFoundation's home (`CFFIXED_USER_HOME`) is the browser's own.
- **The protocol.** CDP runs over `--remote-debugging-pipe` (fds 3/4, passed
  through the in-sandbox forwarder by a new `--pass-fds`). There is **no
  debugging port**: nothing but conductor can drive it, not even another local
  user (a TCP port on macOS would be reachable by all). There is no new
  dependency: the CDP client is ~200 lines over `encoding/json`.
- **The network.** The jail has none. Its only way out is the egress proxy
  with the browser's allowlist, through a per-browser **credential-less**
  endpoint, because Chrome cannot send proxy credentials:
  - Linux: a unix socket in its own 0700 directory, shown only to that
    browser's forwarder.
  - macOS: a loopback port its Seatbelt profile alone allows.

  Loopback is proxied too (`--proxy-bypass-list=<-loopback>`); QUIC and
  non-proxied WebRTC UDP are off.
- **Lightest.** No browser in the agent's image or sandbox, no Node, no
  Playwright, no new Go module.
- **Container backend.** It works unchanged: the agent only needs its skill
  socket and the `conductor` binary, which both backends already provide, and
  the browser stays host-side under conductor's sandbox.

## 2. The allowlist is enforced by the proxy

- `browser.allow` (plus `sandbox.egress` and the identity's registry egress)
  is the browser's allowlist. The agent's own process never gets it: it cannot
  reach those sites past the audit.
- `checkURL` refuses an off-list URL (and any scheme but http(s)/about:blank)
  up front. That is the readable error; the **proxy** is the boundary.
- A redirect, a subresource or a page script that leaves the list dies at the
  proxy (403 or a tunnel failure). It is audited with the identity (each
  browser gets its own proxy manager), and the SSRF guard applies: an
  allowlisted name resolving to a private or loopback address is refused
  unless that literal IP is listed.

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
  - Members replace `requesterFor` with the case's requester class, read from
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

The second factors:
- **TOTP.** `OTPCode` (RFC 6238; SHA1/256/512, digits, period) computes the
  code from an `otpauth://` URI or a base32 seed, or passes through a code the
  source computed (1Password's `?attribute=otp`, where the seed never leaves
  1Password).
- **Push.** With `approval: true`, conductor emits `needs_input` (the
  operator's `conductor.*` triggers alert their phone) and waits for the page
  to move on (bounded; `await-approval` continues).
- **SMS and email codes** are not supported.

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
- **Platforms.** It works headless on Linux and macOS (CI proves both). On
  `localhost` the test site is a secure context; an IP literal cannot be an RP
  ID, which is why the tests bind `localhost` and use `127.0.0.1` as the
  lookalike.

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

## 7. Paseo, members, and the container backend

- **Paseo.** It works through the `<provider>-sandboxed` entries:
  `conductor sandbox exec` mints a token bound (`skill.Identity.Sandbox`) to
  the session's sandbox identity and workspace. Paseo's own browser MCP tools
  are blocked by the sandbox, by design.
- **Members.** They wire one seam: `requesterFor` in `cmd/conductor/browser.go`
  (or `Manager.Provenance`). A logged-in site or a credential is a mutating
  capability, so it is allowed for an operator-requested case, and an event's,
  a third party's, a proactive case's, or a child of a non-operator case gets
  the anonymous profile. A member's tier for `browser.*` can narrow it further
  there.
- **Container backend.** Nothing changes: the agent keeps its socket and
  token, and the browser stays conductor's.

## 8. What the red team should attack (browser mode)

1. **Allowlist escape.**
   - DNS rebinding of an allowlisted name onto loopback or metadata.
   - Proxy auth confusion on the credential-less endpoint (any same-uid
     process can reach it; is anything more reachable through it than the
     allowlist?).
   - On macOS, any local user can connect to the trusted loopback port.
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
   - The macOS Seatbelt browser variant: broad Mach lookups (which services
     beyond the Keychain matter?), IOKit, and no per-process Chromium sandbox
     (a renderer holds the jail's reach). Directory names are readable
     (metadata): find any way to make the browser load a `file:` URL.
8. **Resource abuse.** Unbounded tabs, downloads, or wait loops; the
   `max_browsers` cap; idle reaping during a held action.

## 9. Not in this PR (follow-ups)

- **A live takeover view**: watch and drive an agent's browser interactively.
  This is the dashboard follow-on; here, `peek` (a screenshot) and the
  operator's own headed windows (`seed`, `passkey register`).
- **SMS and email codes**: an opt-in connector that reads a code server-side
  and fills it like TOTP.
- **An MCP face** for the browser (the CLI is the contract).
- **Per-identity `browser.evaluate`**; it is global today, and off once a
  credential is filled regardless.
