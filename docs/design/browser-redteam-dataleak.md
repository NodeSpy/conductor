# Browser agent — red-team: credential / PII / card data leaks

Adversarial review of the promise in `docs/wiki/Browser.md`: **the confined agent
never sees a value.** conductor fills credentials, profile fields and card data
into the browser; the agent only names *which* field. This document records the
attacks run against the in-page extraction and fill path, each with the exact
agent-visible artifact inspected and the verdict, and the patch for every leak.

All work was hermetic: a throwaway conductor, the in-repo fake login site
(`internal/browser/browsertest`) and fake shop, fake secrets with sentinels
(password, TOTP seed, card `4242 4242 4242 4242`, CVC `987`, region `IL`,
`jane.private@example.test`). No real vault, profile, card or site was touched.

Focus (attacker A): in-page extraction and the fill path — read-back,
DOM/snapshot/screenshot, shadow/iframe, origin/field TOCTOU, timing/clipboard,
error paths.

## Summary

| # | Attack | Verdict |
|---|--------|---------|
| 1 | Read-back of a filled value via `evaluate` (`.value`, `defaultValue`, `FormData`, `getComputedStyle`, selection, submit-and-read) | PASS — `evaluate` is off for the life of a browser where any value was filled |
| 2 | **Value in a `snapshot` (a11y tree)** | **LEAK (fixed)** — the browser mirrors a filled value into a `StaticText` descendant of the field; a short value (CVC `987`, region `IL`) was printed as `- text: 987`, masked only on the field's own node |
| 3 | **Value in a `screenshot`** | **LEAK (fixed)** — short values (CVC, region code) were not masked |
| 4 | Shadow DOM / iframe fill-and-read | PASS — fill refuses any node not in the main frame's own top document; open shadow roots masked in screenshots |
| 5 | Origin / field confusion + resolve→fill TOCTOU | PASS — exact origin, egress-narrowed login browser, atomic re-check in the setter |
| 6 | Timing / clipboard / keyboard / mutation observer | PASS within model — all require page script, and `evaluate` is off; a page on the exact bound origin already holds the value it was filled with |
| 7 | Error-path echo of a value | PASS — no error message embeds a filled value; `secret()` returns the source's error, never a value |
| 8 | JS injection to capture at fill time (agent hook, prototype pollution, service worker, main-world setter override) | PASS — `evaluate` is read-only and off after a fill; the fill runs in an isolated world's pristine setter; no agent data is concatenated into evaluated JS |
| 9 | Browser extension / raw CDP / profile or flag tampering | PASS — fixed minimal flag set, `--disable-extensions`, `--remote-debugging-pipe` (no port), namespace/Seatbelt sandbox (#179) |
| 10 | Keypress capture below the page | PASS — credentials are filled by the `.value` setter + `input`/`change` dispatch, never keystrokes; no `keydown`/`beforeinput`/`keypress` is emitted |

**Leaks found: 2. All patched: yes.** Both proven end-to-end on the real headless
Chromium conductor drives (not a mock): a sandboxed `chromium` process, driven
through the real `Do(...)` verbs, the sentinel actually filled into the real page,
and the leak read from the real `snapshot` / `screenshot` output.

---

## Attack 2 — short filled values were printed in a snapshot (LEAK → fixed)

### The hole

Chromium's accessibility tree represents a filled text field's value **twice**:
as the field node's `value`, and as a `StaticText` descendant node that mirrors
the displayed glyphs. conductor masked only the first — it replaced the filled
field node's AX `value` with the mark, keyed on that node's backend id. The
mirror `StaticText` is a *different* node (a different backend id, not in
`secretNodes`), and the snapshot renderer printed it as a plain `- text: …` line.

A value ≥ 4 chars was still caught by the whole-reply text scrub, but a value
under 4 — a card **CVC** or a region code, deliberately kept out of the scrub —
was neither scrubbed nor on the masked node. The agent just calls
`conductor browser snapshot` and reads it.

### Evidence (before the fix, on the real browser)

`TestRealCVCNotInSnapshotOrScreenshot` (a hostile merchant with an inert
place-order button keeps the filled checkout on screen) and
`TestRealAXTreeShortValueStructure`:

```
LEAK: the snapshot shows the CVC: "- text: 987"
LEAK: the rendered snapshot shows the region value: "- text: IL"
```

The parent chain confirms the mirror is a descendant of the masked field:
`StaticText "IL" (backend 92) → generic (57) → textbox (backend 33, the filled,
masked field)`.

### The fix

The snapshot renderer now renders a filled field's **whole subtree quiet**: when
it walks a node whose backend id is in `secretNodes`, it suppresses the
`StaticText` the browser mirrors the value into (the field's own value already
shows the mark). Value-agnostic — it needs no copy of the secret and works for
any length — and exactly aligned with the existing node-masking boundary.

`internal/browser/snapshot.go`: `snapshot` passes the secret-node set to
`renderAX`, which ORs `secret[n.BackendID]` into the subtree's `quiet` flag.

### Evidence (after the fix)

`TestRealCVCNotInSnapshotOrScreenshot` passes: the CVC is in neither the snapshot
(no `- text: 987`, field shows the mark) nor the screenshot pixels, while
`in.eval` confirms the page really holds `987` (the fill worked — the agent just
cannot see it). `TestRealAXTreeShortValueStructure` confirms the region is gone
from the rendered snapshot though still present in the raw AX tree.

---

## Attack 3 — short filled values were visible in a screenshot (LEAK → fixed)

### The hole

Snapshots hide a filled field two ways: a value ≥ 4 characters is scrubbed from
the whole reply by its text, and every filled field (any length) has its node's
value replaced with `«filled by conductor»` by **node id** — so a CVC or a
two-letter region code, too short to scrub from free text without shredding
unrelated output, is still hidden in a snapshot.

The screenshot masking did not have the second half. `maskSecrets`
(`internal/browser/credentials.go`) searched the page for the **text** of each
value in `secretValues()`, and `secretValues()` deliberately excludes values
under 4 characters (scrubbing "321" everywhere would be useless). Consequences:

- A filled CVC (`987`) or region (`IL`) in a visible `type=text` field — the
  normal markup for a card CVC and a state field — was rendered in the
  screenshot and **not masked**. The agent screenshots the page and OCRs the
  value. No page script and no control of the page is required; the field is
  simply on screen.
- Worse, in a browser where the **only** filled value was short,
  `secretValues()` was empty and `maskSecrets` returned the screenshot
  **untouched** — no masking at all.

### Evidence (before the fix)

`TestRedteamShortValueScreenshotMasked`: fill a profile form, screenshot, sample
the pixels of each filled field's box.

```
control OK: the email field is masked
LEAK: the filled region field is NOT masked in the screenshot — visible to OCR (box 516,105,192,23)
```

The `email` field (≥ 4) was a solid black box; the `state` field holding `IL`
showed its white background and glyphs.

### The fix

`maskSecrets` now masks **every filled field by its node box**, regardless of the
value's length — the same contract the snapshot already keeps ("hidden by its
field"). For each node in `secretNodes` it resolves the node in the frame's
isolated world and takes its `getBoundingClientRect` (document coordinates,
scroll included for full-page captures), and blacks that box out in addition to
the value-text boxes. The entry gate changed from "there is a scrubbable value"
to `in.filled()` ("any value was filled here"), so a short-value-only browser is
masked too. A node that no longer resolves in the document is skipped (it is not
in the capture); a node that resolves but cannot be measured fails the
screenshot closed, as before.

`internal/browser/credentials.go`: `maskSecrets` + new `secretNodeRects` /
`secretNodeBoxJS`.

### Evidence (after the fix)

`TestRedteamShortValueScreenshotMasked` and
`TestRedteamOnlyShortValueScreenshotMasked` both pass: the region field's box is
solid black in both a viewport and a full-page capture, including when it is the
only value filled.

### Residual / accepted limitation (unchanged by this fix)

A value under 4 characters that an **adversarial page on the exact bound origin**
copies, via its own script, into its own visible *text* (not the filled field)
is not scrubbed from snapshot text — the design accepts this because scrubbing a
3-character string from every reply shreds unrelated output, and because such a
page is same-origin with the field and therefore already holds the value it was
filled with. This fix closes the field-visibility leak (no page cooperation
needed), which is the realistic threat: the agent drives a fill on a legitimate
page it does not control and screenshots it.

---

## Attacks that held (PASS)

**1 — read-back via `evaluate`.** The defense is *not* V8's `throwOnSideEffect`
(reading is not a side effect) — it is a hard lockout: `evaluate` is refused for
the life of a browser where any value was filled, *before the expression runs*,
so no transform/encoding can smuggle a value past the scrub.
`TestRealEvaluateReadbackLocked` runs 21 read-back expressions on the real
browser after a real password fill — `.value`, `.defaultValue`,
`getAttribute('value')`, every `attributes` walk, `getComputedStyle`,
`validationMessage`, `document.activeElement.value`, `cloneNode().outerHTML`,
`XMLSerializer`, `new FormData(form)`, `form.elements['password'].value`,
`JSON.stringify`, unary `+`, `.length`, a `charCodeAt` oracle, `btoa(...)`,
reverse, `structuredClone`, `:valid` matching, `documentElement.outerHTML` — and
every one is refused with "credential was filled". `TestRealCardFillLocksEvaluate`
proves the same after a real PAN/CVC fill, even on the confirmation page.

**2 — snapshot / a11y value mirror — now fixed.** See *Attack 2* above: the
field node's value is masked and its mirror `StaticText` subtree is now quiet;
the whole reply (snapshot text, tab titles, dialogs, URLs) is additionally
scrubbed of every value ≥ 4 chars. No `logs` / console verb is exposed to the
agent (`Actions` has none), and the browser never enables `Runtime`/`Log`
console capture for the agent.

**4 — shadow DOM / iframe.** `fieldCheck`, `fieldSet` and `walletSet` each
refuse a node whose `ownerDocument` is not the frame's own document or whose
window is not the top window — so a field in an open or closed shadow root tree
that is a different document, a same-origin or sandboxed iframe, or a nested
browsing context is refused before any secret is fetched. The vault-fetch
counter confirms nothing is read on refusal (`TestCredentialOriginBinding`). Open
shadow roots are walked by the screenshot masking; closed shadow roots and
canvas remain documented screenshot limitations.

**5 — origin / field confusion and resolve→fill TOCTOU.** Origins are matched
exactly (scheme + lowercased host + port, optional path prefix) against the
browser's own `securityOrigin`, never the page's word. A lookalike host
(`127.0.0.1` vs `localhost`), a redirect off the bound host (dies at the
login browser's narrowed egress proxy), a path-bound credential off its path,
and a cross-origin framed login are all refused (`TestCredentialOriginBinding`).
A credential fills only in the separate, logged-in browser whose egress is
narrowed to the credential's own hosts — never beside the agent's page in the
anonymous browser. The setter (`fieldSet` / `walletSet`) re-verifies
`location.origin` in the isolated world **in the same call** that assigns the
value, so a navigation or frame swap racing between the earlier `checkPage` and
the assignment cannot land the value on another origin; the isolated-world
`location.origin` is the document's real origin and the page cannot spoof it.

**6 — timing / clipboard / keyboard / mutation.** Every one of these needs a
script in the page, and `evaluate` is off after a fill; there is no
clipboard-read verb. On the exact bound origin the page's own scripts can read
the field, but that is the same-origin boundary — the page is the party being
signed in and already receives the value.

**7 — error-path echo.** Action errors are returned without the value scrub, so
every error path that touches a secret was reviewed and fuzzed: `secret()`
returns the source's error (or "returned nothing"), never the value;
`fieldSet` / `walletSet` refusals name the field and the reason, never the value;
`selectOption` echoes only the agent's own argument.
`TestRedteamErrorPathsNeverEchoValue` forces a battery of failures — wrong field,
unknown ref/credential/field, lookalike origin, and an oversized (>640 KB)
unicode password actually set into a field — and asserts no error carries the
password, the TOTP seed or the sentinel. `TestRedteamOpaqueOriginFillRefused`
confirms a `data:` / `about:blank` page is never a fill target and fetches
nothing. A `wait` is likewise not an oracle: the body is scrubbed of values ≥ 4
chars before the text match (a short value reaches `innerText` only through
same-origin script, the documented short-value boundary, and the filled field's
own value is never in `innerText`).

**8 — JavaScript injection to capture the value at fill time.** The threat: the
agent installs an `input`/`change`/`keydown` listener, a `MutationObserver`, or
overrides the `value` setter / `setAttribute` / `execCommand` *before* conductor
fills, so the fill triggers its hook and hands it the plaintext.

- *The agent's own script is blocked at the source.* The only verb that runs the
  agent's JavaScript is `evaluate`, and it is read-only: V8's `throwOnSideEffect`
  refuses any expression that writes. `TestRedteamEvaluateNoSideEffect` fires a
  battery — `addEventListener`, `Object.defineProperty` / `__defineSetter__` on
  `HTMLInputElement.prototype`, `new MutationObserver(...).observe`,
  `Object.prototype` pollution, reassigning `window.fetch`, writing a global, a
  title, a `value`, an attribute, `localStorage`, `document.cookie`,
  `navigator.clipboard.readText`, `execCommand` — and every one is refused. And
  `evaluate` is off entirely once any value is filled, so even a hook that
  somehow landed could not be read back through it.
- *No agent data reaches evaluated JS.* Every script conductor runs in a page is
  fixed text; the agent's values are passed as `callFunctionOn` arguments, never
  concatenated into source. The one built selector (`markedNodeAttr`) uses only
  conductor's own field names and is double-escaped.
- *The fill resists main-world pollution.* The value is assigned in a fresh
  **isolated world** (`Page.createIsolatedWorld`) whose `HTMLInputElement.prototype`
  is pristine — a page that polluted the main world's prototype, or defined an own
  `value` setter on the element, is bypassed because the fill calls the isolated
  world's native setter with `set.call(this, v)`.
- *Residual (inherent, out of conductor's control):* a page on the exact bound
  origin that **already** runs attacker script (stored/reflected XSS, a hostile
  third-party widget, a compromised dependency) is same-origin with the field and
  can read its value after the fill, or siphon the form's own POST. conductor's
  guarantee is scoped to "the configured origin is trustworthy"; it stops the
  *agent* from injecting the capturing script and never types the value as
  keystrokes, but it cannot make a compromised login origin safe. This is the web
  same-origin boundary, documented, not a conductor defect.

**9 — browser extension, raw CDP, profile/flag tampering.** The browser is
launched with a fixed, minimal flag set the agent cannot extend (`ChromeArgs`):
`--disable-extensions`, `--disable-component-extensions-with-background-pages`,
no `--load-extension`, and `PasswordLeakDetection` disabled so Chrome itself
never sends a filled credential's hash prefix out. The DevTools protocol runs
over `--remote-debugging-pipe` — an inherited fd, not a TCP port — so nothing
can attach its own CDP client to call `Input` or `Network.getResponseBody`. The
profile lives under the browser's sandbox dir, and the browser runs in its own
user + mount + pid + network namespace (Linux) or under Seatbelt (macOS) with
only its dir and (read-only) install visible, its sole network conductor's proxy.
The agent is a separate process that speaks only conductor's mediated verbs; it
cannot drop an extension, edit `Preferences`, reach the pipe, or set
`--proxy-server` / `--host-rules` / `CHROME_*`. These process-boundary guarantees
are the agent sandbox's (#179); container mode (#183) only tightens them.

**10 — keypress capture below the page.** Credentials, profile fields and card
data are never typed: the fill assigns `.value` through the isolated world's
setter and dispatches only synthetic `input` and `change` events — no `keydown`,
`keypress` or `beforeinput`. A capture-phase key listener (even one the agent
could install, which it cannot) observes nothing. Lower still, an OS/X11
keylogger or a read of the browser's `/proc/<pid>/mem` would require the agent to
share the browser's process space, which the namespace/Seatbelt sandbox forbids.

## Running the checks

```
CONDUCTOR_TEST_CHROME=<chrome> go test ./internal/browser/ \
  -run 'TestRedteam|TestCredentialOriginBinding|TestCredentialRedaction|TestWalletPurchase'
```

Regression tests:
- `internal/browser/redteam_realbrowser_test.go` — the real-browser batteries:
  21-variant `evaluate` read-back lockout, the a11y value-mirror snapshot leak,
  the CVC in neither snapshot nor screenshot on a hostile sticky checkout, and
  the card-fill evaluate lockout.
- `internal/browser/redteam_dataleak_test.go` — the patched screenshot leak.
- `internal/browser/redteam_inject_test.go` — evaluate is read-only (the
  anti-injection linchpin).
- `internal/browser/redteam_edge_test.go` — error paths never echo a value;
  opaque origins refuse the fill.
