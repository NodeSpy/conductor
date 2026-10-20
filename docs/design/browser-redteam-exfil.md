# Agent browser — red team: exfiltration & propagation (attacker B)

Scope (attacks 6–9 of `agent-browser.md` §9): egress/proxy/beacon channels,
2FA/passkey relay, profiles & cards into proposals/receipts/screenshots, and
leakage via spawned workers, memory, hand-off, tickets and the audit. Plus the
operator's added direction: **trick conductor into filling a real credential /
profile / card into a page the agent controls** (origin/field confusion).

Threat model: the agent is fully malicious and controls any page it can reach,
including pages it hosts on an allowlisted origin and the checkout/merchant
pages a card or profile is filled into. A **leak** is any path where a filled
value (a sentinel) reaches an agent-visible artifact: a verb reply, the audit
log, the persisted ledger, a screenshot, the operator API, a file the agent can
read, or an egress request it can make.

Harness: hermetic, local. The `internal/browser` integration rigs stand up a
throwaway conductor manager, a headless Chromium under conductor's namespace
sandbox, and local fake sites (`browsertest.Site`, `browsertest.Shop`). Fake
secrets with sentinels: password `hunter2…`, TOTP seed, card `4242…`, **CVC
`987`**, profile values (`jane.private@example.test`, …). Every rig greps every
agent-visible artifact for every sentinel (`noLeak`).

Run (PID namespace mandatory for process tests; scratch on local disk; decoy
HOME; secret envs unset):

```
SCRATCH=~/.cache/conductor-redteam-exfil
export GOCACHE=$SCRATCH/gocache GOTMPDIR=$SCRATCH/gotmp TMPDIR=$SCRATCH/gotmp HOME=$SCRATCH/home
unset PASEO_HOME CONDUCTOR_HOME CLAUDE_CONFIG_DIR XDG_CONFIG_HOME XDG_STATE_HOME XDG_DATA_HOME
export CONDUCTOR_TEST_CHROME=.../chrome CONDUCTOR_REQUIRE_BROWSER=1
unshare --user --map-current-user --pid --fork --kill-child --mount --mount-proc \
  go test -race ./internal/browser/ -run 'TestWallet|TestCredential' -count=1
```

---

## LEAK 1 — the card's CVC is smuggled back to the agent through the receipt

**Severity: high.** Breaks the wiki's absolute promise: *"The card never reaches
the agent. Not its number, CVC or expiry; not in a reply, a log, the run record
or a screenshot."*

### The hole

Filled values are protected two ways:
- **by node** — the filled input's DOM node is marked secret; snapshots show
  `«filled by conductor»` and screenshots black the box out;
- **by text** — the value string is added to `in.secrets` and `scrub()`/
  `scrubAny()` strips it from every reply.

A value **under 4 characters** is deliberately kept out of the text scrub
(`setWallet`: `if len(s) >= 4`), because scrubbing `"987"` or `"CA"` from every
reply would shred the page. Those short values were protected **by node only**.
The **CVC went further**: it was filled with *no* scrub list at all
(`walletfill.go`: `cvc := … return []string{v}, nil`), so its value never
entered any set.

`readReceipt` reads the confirmation page's raw `innerText` (not the a11y tree),
so the **by-node** protection does not apply there, and it scrubbed that text
with `in.scrub` — which does not know the CVC. It then extracts an "order
number" (`orderRE`, `[A-Z0-9][A-Z0-9-]{3,40}`) and a charged amount from that
text.

A malicious merchant (a page the agent controls on its own allowlisted origin,
which legitimately receives the card during `pay`) writes the CVC into its
order-confirmation text. Conductor reads it, fails to scrub it, and the value
reaches the agent through **four** channels:

1. the `pay` reply (`purchaseView` → `order_number`);
2. `purchase-status` (the agent can re-query any time);
3. the **persisted ledger** `purchases.json` (`Receipt.OrderNumber`), served
   read-only by the operator API `GET /v1/purchases/{id}`;
4. the **audit log** (`recordCardUse` → `order_number`, `charged`).

### Proof

`TestWalletCVCReceiptExfil` (new). The fake shop gets a `EchoCVCInOrder` toggle:
on checkout it sets the confirmation's order number to `FS<cvc>X`. Before the
fix:

```
wallet_exfil_test.go:…: LEAK: the CVC reached the agent in the pay reply's
    order_number: "FS987X"
```

The existing `TestWalletPurchase` never caught this because its leak set
(`walletSecrets`) **explicitly excludes the CVC** — the suite treated CVC text
leakage as acceptable, which is exactly the blind spot.

### The fix (`internal/browser`, fill/redaction path only)

Fail-closed, minimal, and without re-shredding every reply:

- A new per-browser set `secretsShort` holds filled values too short for the
  general scrub (`instance.go`, `rememberShort` in `credentials.go`).
- `setWallet` routes a short scrub value into `secretsShort` instead of
  dropping it (`walletfill.go`).
- The CVC is now given its value as a scrub entry
  (`cvc := … return []string{v}, []string{v}`), so it lands in `secretsShort`.
- A new `scrubAll` (`credentials.go`) strips `secrets` **and** `secretsShort`.
  It is used **only** where conductor extracts a value out of a page's own text
  — `readReceipt`'s body text and URL — never on a whole reply (that would
  shred the page). `scrub`/`scrubAny` are unchanged, so snapshots and other
  replies keep their behaviour.

Because the CVC string is now redacted from the receipt text **before** the
order number and amounts are parsed, the exact-substring channel (order number)
*and* the exact-amount channel (`"Total charged: $987.00"` → `$[redacted].00`,
which `parseMoney` then can't read) are both closed.

### Re-proof

After the fix: `TestWalletCVCReceiptExfil` passes (no sentinel in any of the
four channels; the merchant still receives the card, which is inherent and
expected). `TestWalletBenignOrderNumberSurvives` confirms a normal order number
(`FS-1001`) and `charged` still reach the agent — the fix is fail-closed, not a
blanket blackout. `TestScrubAllShort` pins the `scrub` vs `scrubAll` split.
`TestWalletPurchase` and the credential suite stay green under `-race`.

Residual (documented, not a regression): a merchant can still *guess-encode* a
short value into a **transformed** rendering it controls (e.g. the CVC as the
decimal `9.87`, or across canvas/CSS content) — the same inherent limit the
design already lists for readback of transformed renderings. Exact copies of a
filled value no longer survive.

Commit: see `git log` for `redteam: the CVC is scrubbed from a receipt's own text`.

---

## LEAK 2 — a filled value, transformed by page script, is read back through `snapshot`

**Severity: medium–high. Demonstrated end-to-end on the real headless browser.**
Breaks the wiki's claim that a filled value is *"scrubbed from … snapshots, page
text, titles, URLs"* and never *"enters the agent's … snapshots."*

### The hole

The scrub is a **literal string replace**. The design itself notes this is not a
guarantee against transforms — which is exactly why `evaluate` is refused after a
fill ("a side-effect-free expression could read a filled field and transform it
past any string scrub; only refusing it is a guarantee"). But the **same
transform-past-scrub weakness applies to `snapshot`, `tabs` and the title**, fed
not by the agent's `evaluate` (which is off) but by **page script on the filled
value's own origin** — which the design already acknowledges can read the field.

Proof (`TestTransformExfilViaSnapshot`, real browser): a login page carries
script that, the instant conductor fills the password, reads the field, reverses
it and base64-encodes it, and writes both into a sibling text node and an
`aria-label`. `snapshot` then returned:

```
- textbox "Password" [ref=e11] [focused, value="«filled by conductor»"]
  - text: ••••••••••••••••••••
- text: REV:c2b1a3f9-WP-LENITNES B64:U0VOVElORUwtUFctOWYzYTFiMmM=
```

The field node is masked, but the transformed copies sail through the literal
scrub. A malicious agent reads the password out of its own `snapshot` reply —
**even though the egress narrowing blocks that hostile script from phoning the
value out itself.** The snapshot read-back defeats the egress containment.

### The fix (defense-in-depth) and the honest residual

`scrub` now also redacts the **common reversible encodings** of each filled
value (reverse, base64 std/url/raw, hex upper/lower, percent) for values ≥ 6
chars (`secretForms`, `credentials.go`). This closes the demonstrated vectors
(`TestTransformExfilViaSnapshot` passes after the fix) and the ones an attacker
reaches for first, across every page-text reply (`snapshot`, `tabs`, title,
downloads, `wait`), and — via `scrubAll` — the receipt.

**This is defense-in-depth, not a guarantee, and I will not pretend otherwise.**
Confirmed still leaking on the real browser (characterization run, not
committed): a **char-code** transform `83.69.78.84.…` is read straight out of
`snapshot`. XOR, a rotation, or a custom split are equally uncatchable by any
scrub. `snapshot` cannot be refused the way `evaluate` is, so the literal/encoded
scrub is best-effort redaction of echoes, **not** a boundary. The real controls
that bound a hostile script on a filled origin are:

- **Origin trust** — a credential fills only on its exact bound origin; binding
  it to an origin that runs hostile script is the operator's trust decision (the
  wiki's "bind to exact origins you trust / prefer agent-specific credentials").
- **Egress narrowing** — a *non-colluding* compromised third-party script on the
  origin cannot send the value anywhere (the logged-in browser reaches only the
  credential's hosts). The snapshot read-back is the gap this leak exposes; the
  fix closes the common encodings and documents the rest.
- **Redundancy for agent-controlled origins** — if the agent controls the
  origin's content, that origin's own server already received the value; snapshot
  adds nothing there.

**Recommendation for the design:** state plainly (as it already does for
`evaluate`) that `snapshot`/page-text verbs carry the same transform caveat —
the scrub redacts literal and common-encoded echoes, and a hostile script on a
*trusted* origin can still surface a value through an arbitrary transform. The
guarantee is origin trust + egress, not the scrub.

Commit: see `git log` for `redteam: scrub common transforms of a filled value from page text`.

---

## PASS — evaluate read-back after a fill (the operator's prime suspicion)

Proven on the real browser (`TestEvaluateReadbackBlockedAfterFill`): with the
identity **opted in** to `evaluate`, after `login` fills the password every
read-back expression is refused — `.value`, `.defaultValue`,
`.getAttribute('value')`, a `FormData` dump, `document.activeElement.value`,
`getSelection().toString()`, `.outerHTML`, `btoa(value)`, `.length`, and a
whole-form `new FormData(form)` serialization. None run, because `evaluate` is
**off for the life of a browser once any value is filled** (`in.filled()`, set by
`remember` on every credential/profile/card fill, *before* the value is set —
fail-closed). The guarantee does not rest on `throwOnSideEffect` (reading is not
a side effect); it rests on refusing `evaluate` outright. The transform read-back
the operator suspected therefore cannot run through `evaluate` — but it can
through page script + `snapshot`, which is LEAK 2 above.

## PASS — credential / profile / card fill into an agent-controlled page

The operator's priority angle. Attacked every listed vector; all fail closed.

- **Exact origin binding.** `parseOrigin` rejects userinfo (`user@host`), query
  and fragment, and non-http(s) schemes. `origin.check` compares the browser's
  own `securityOrigin` (from `Page.getFrameTree`, never the page's word)
  exactly on scheme + host + port, host lowercased, default port normalised,
  and requires the URL under the bound path. Lookalikes
  (`127.0.0.1` vs `localhost`, a port variant, case, a trailing dot, a
  punycode/IDN homoglyph) are all a different host string → refused. Covered by
  `TestCredentialOriginBinding`, `TestCredentialPasskey` (lookalike passkey
  refused), and profile/card exact-origin matching
  (`Profile.allowed`, `Card.merchantAllowed`, `pay`'s `checkPage`).
- **Atomic re-check at the instant of fill.** `fieldSet`/`walletSet` run in one
  CDP call in a fresh **isolated world** of the **main** frame and re-verify
  `this.ownerDocument === document && window === window.top` and
  `location.origin === expected` before the pristine `value` setter fires. A
  navigation, SPA route change, `history.pushState`, meta-refresh, or frame
  swap racing between resolve and set lands on a different `location.origin`
  and is refused. `location` is unforgeable and the isolated world uses its own
  prototypes, so the page cannot spoof `location.origin`.
- **Frames / nested contexts.** The isolated world is the main frame's; a node
  in a child frame (same-origin, cross-origin, `srcdoc`, sandboxed) does not
  resolve there (`DOM.resolveNode` with the main world's context id), and
  `fieldCheck`/`fieldSet` re-assert `ownerDocument === document`. Naming a
  frame's node straight to the fill is refused before any secret is fetched
  (`TestCredentialOriginBinding`).
- **`data:` / `blob:` / `about:blank`.** `checkURL` refuses any scheme but
  http(s)/`about:blank` up front, and an opaque/`null` `securityOrigin` fails
  `parseOrigin`, so `origin.check` refuses the fill.
- **Wrong browser.** `login`/`fill-credential` refuse unless the action runs in
  the separate, narrowed logged-in browser (`in == s.login`); an anonymous
  browser that reached the credential's host (e.g. via an open redirect on
  another allowlisted site) gets no fill.
- **Redirect to a lookalike / off-host.** The logged-in browser's egress is
  narrowed to the credential's own hosts, so a redirect to the lookalike dies
  at its proxy and is audited (`TestCredentialOriginBinding`).
- **Credential/profile/card fetch is last.** Every check passes before any
  value is read; a refused attempt fetches nothing (`credRig.refused` asserts
  the vault fetch count is unchanged).

Filling into a page the agent controls **on the exact bound origin** is not a
conductor break — it is the inherent "a site sees what is typed into it", which
the design documents and bounds with exact origins and limited cards.

## PASS — in-page JS injection / prototype poisoning / keypress capture

Operator's added direction: even on the correct allowed origin, assume hostile
script runs in the page (stored/reflected XSS, a third-party widget, a
compromised dependency, or the agent's own `evaluate`), and assume the agent
wants to sniff keystrokes below the page. Proven by
`TestCredentialFillUnobservableByPageScript`: a login page carries hostile
script that (a) redefines `HTMLInputElement.prototype.value`'s setter in the
main world, (b) adds capture-phase `keydown` and `input` window listeners, and
beacons everything it sees to its own server. Conductor signs in; then:

- **Prototype poisoning does not intercept the fill.** The overridden setter
  was invoked **0 times** — conductor sets the value through the *isolated
  world's* own pristine `HTMLInputElement.prototype` descriptor
  (`fieldSet`/`walletSet`), which the page's main-world redefinition cannot
  reach. The sign-in still succeeded, proving the correct value was set.
- **No observable keystrokes.** The fill emitted **0** `keydown` events. A fill
  sets the value and dispatches only `input`/`change`; it never uses
  `Input.dispatchKeyEvent`. Form submit uses `form.requestSubmit()`, not a
  synthesised Enter. A page-level or below-page keylogger sees nothing. (The
  agent-directed `type`/`press` verbs do use CDP key events, but those carry no
  secret.)
- **The value never reached the agent.** The password sentinel is in no reply,
  no audit entry and no workspace file; `evaluate` is refused after the fill, so
  the agent cannot read back what the hostile listener stashed.
- **The agent cannot inject the hook itself.** `evaluate` is opt-in per
  identity, runs with V8 `throwOnSideEffect` (adding a listener, overriding a
  setter, registering a service worker and polluting a global are all side
  effects and are rejected), and is off entirely once any value is filled in
  that browser. Conductor's fill code runs in an isolated world with its own
  globals/prototypes, so main-world pollution of `Object.prototype`, `fetch`,
  `JSON`, etc. does not affect it.

**Residual — and where it stops being inherent (see LEAK 2):** an *ordinary*
`input` listener still fires and sees the value — exactly as it would for a real
user's keystrokes ("a site sees what is typed into it"). The hostile page then
sending it to **its own origin** is inherent. But the hostile page surfacing a
*transform* of it in the DOM so the **agent** reads it through `snapshot` is a
real leak (LEAK 2) — the literal scrub missed it; the fix redacts common
encodings and the residual (arbitrary transforms) is bounded by origin trust +
egress, not the scrub. No fill method that lets the site function can hide the
value from the site's own event listeners (CDP `Input.insertText` and trusted
events fire listeners too); only setter-override interception is blocked, which
conductor already does.

## PASS — browser extensions, CDP endpoint, launch flags, process isolation

Operator's added direction (attacks the browser process, not a page):

- **Extensions are off and the flag set is fixed.** `ChromeArgs` is a hardcoded
  list the agent has no input into: `--disable-extensions`,
  `--disable-component-extensions-with-background-pages`,
  `--disable-default-apps`, and **no** `--load-extension`. The agent cannot pass
  flags, and the browser inherits conductor's (sandbox-wrapped) environment, not
  the agent's, so `CHROME_*`/`CHROMIUM_FLAGS`/a PAC URL/`--proxy-server`/
  `--host-rules`/`--auto-open-devtools` cannot be injected.
- **No CDP endpoint the agent can reach.** CDP runs over
  `--remote-debugging-pipe` (fds 3/4), **not** a port — there is no
  `--remote-debugging-port`. The agent holds only the skill socket and
  conductor's mediated verbs; it cannot attach its own CDP client to call
  `Input`, `Network.getResponseBody` or `Page.captureSnapshot`.
- **The profile is ephemeral and conductor's, not the agent's.** The
  `--user-data-dir` lives under conductor's per-browser dir (0700), is created
  fresh and deleted on close; the agent has no filesystem path to it, so it
  cannot drop an extension or edit `Preferences`/`Secure Preferences` between
  launches.
- **Process/memory isolation is the #179 sandbox layer.** Conductor launches the
  browser in its own user/mount/pid/net namespaces (Linux) or Seatbelt/container
  (macOS), separate from the agent's sandbox. The agent cannot run processes in
  the browser's space, read `/proc/<pid>/mem`, its stdin/ptys, or a core dump.
  Container mode (#183) only strengthens this (no network, `--cap-drop ALL`,
  read-only root, no host bind). The egress proxy and fixed flags are the
  browser-layer controls; the process/memory boundary is the sandbox layer.

## PASS — 2FA / passkey relay

- **TOTP / emailed codes** are filled through `fillField`, which `remember`s the
  computed code (≥ 6 digits) into `in.secrets`, so it is scrubbed from replies,
  snapshots, titles, URLs and the audit; no error carries the code or the
  mailbox message (`imap.go`; `TestCredentialEmailCode`).
- **Passkey private key** lives in the vault and is injected into the virtual
  authenticator over CDP (`WebAuthn.addCredential`), which the agent cannot
  speak; the authenticator holds only that one credential, is attached only
  while the page is on the bound origin, and is removed in a `defer` after the
  sign-in. The agent gets no assertion material it could relay.

## Flagged for reconciliation (attacker A's readback domain)

The CVC's root cause — short filled values are kept out of the general text
scrub (`scrub`/`scrubAny`), only node-masked — is now closed for the path
conductor itself extracts a value from (the receipt, via `scrubAll`). The same
root cause may admit a **snapshot** readback that belongs to attacker A
(attacks 1–2): hostile page script (on an agent-controlled checkout) could copy
the CVC out of the masked card input into a *different* element or input, which
the a11y snapshot renders and `scrubAny` (which uses `scrub`, not `scrubAll`)
would not redact because the CVC is short. This was not independently confirmed
here and overlaps A's area; the `secretsShort` set added for the receipt fix is
the hook a snapshot-side fix would reuse (with the documented short-value
over-redaction trade-off that the profile section already accepts). Left for A /
reconciliation rather than patched here to avoid conflicting edits.

## PASS — propagation into workers / memory / hand-off / tickets / audit

The only conductor-written persistent artifacts that carry page-derived text
are the purchase ledger and the audit. Proposals carry only agent-supplied
fields (`why`, item names) and principal metadata — never a resolved value. The
one page-derived field, the receipt's order number / charged amount, was the
CVC channel above and is now scrubbed fail-closed. The receipt URL is
`scrubAll`'d and `redactURL`'d (query/fragment/userinfo dropped). No profile,
credential or card value is written to the ledger, a hand-off note, a ticket,
or the audit.
