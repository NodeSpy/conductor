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

## PASS — propagation into workers / memory / hand-off / tickets / audit

The only conductor-written persistent artifacts that carry page-derived text
are the purchase ledger and the audit. Proposals carry only agent-supplied
fields (`why`, item names) and principal metadata — never a resolved value. The
one page-derived field, the receipt's order number / charged amount, was the
CVC channel above and is now scrubbed fail-closed. The receipt URL is
`scrubAll`'d and `redactURL`'d (query/fragment/userinfo dropped). No profile,
credential or card value is written to the ledger, a hand-off note, a ticket,
or the audit.
