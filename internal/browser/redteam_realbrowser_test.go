//go:build linux || darwin

package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

// These run against the REAL headless Chromium conductor drives (newCredRig /
// newWalletRig launch a sandboxed chromium; r.m.Do is the real verb path; the
// evaluate expressions execute in real V8 over CDP). Nothing here is mocked.

// TestRealEvaluateReadbackLocked: on the real browser, after conductor fills a
// password, EVERY read-back expression an agent could try is refused — because
// evaluate is locked off for the life of a browser where a value was filled,
// BEFORE the expression runs (so no transform/encoding can smuggle the value
// past the scrub). The defense is the lockout, not throwOnSideEffect.
func TestRealEvaluateReadbackLocked(t *testing.T) {
	r := newCredRig(t)
	r.st.Evaluate = []string{"agent:test@*"}
	c := r.caller("op", RequesterOperator)
	ctx := context.Background()

	r.do(c, "open", map[string]any{"url": r.bound + "/login"})
	// Before any fill, a read works (the lockout is what changes).
	if _, err := r.m.Do(ctx, c, "evaluate", map[string]any{"expr": `document.querySelector('input[type=password]').value`}); err != nil {
		t.Fatalf("a pre-fill read was refused: %v", err)
	}
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	r.do(c, "fill-credential", map[string]any{"credential": "site", "field": "password", "ref": refOf(t, snap, "textbox", "Password")})

	reads := []string{
		`document.querySelector('input[type=password]').value`,
		`document.querySelector('input[type=password]').defaultValue`,
		`document.querySelector('input[type=password]').getAttribute('value')`,
		`document.querySelector('input[type=password]').attributes.length`,
		`[...document.querySelector('input[type=password]').attributes].map(a=>a.value).join()`,
		`getComputedStyle(document.querySelector('input[type=password]')).content`,
		`document.querySelector('input[type=password]').validationMessage`,
		`document.activeElement && document.activeElement.value`,
		`document.querySelector('input[type=password]').cloneNode(true).outerHTML`,
		`new XMLSerializer().serializeToString(document.querySelector('input[type=password]'))`,
		`document.querySelector('form') && [...new FormData(document.querySelector('form'))].map(e=>e.join('=')).join('&')`,
		`document.querySelector('form').elements['password'].value`,
		`JSON.stringify({v: document.querySelector('input[type=password]').value})`,
		`+document.querySelector('input[type=password]').value`,
		`document.querySelector('input[type=password]').value.length`,
		`document.querySelector('input[type=password]').value.split('').map(ch=>ch.charCodeAt(0)).join(',')`,
		`btoa(document.querySelector('input[type=password]').value)`,
		`document.querySelector('input[type=password]').value.split('').reverse().join('')`,
		`structuredClone(document.querySelector('input[type=password]').value)`,
		`document.querySelector('input[type=password]').matches(':valid')`,
		`document.documentElement.outerHTML`,
	}
	for _, expr := range reads {
		out, err := r.m.Do(ctx, c, "evaluate", map[string]any{"expr": expr})
		if err == nil {
			t.Errorf("LEAK: evaluate ran after a fill: %q -> %v", expr, out)
			continue
		}
		if !strings.Contains(err.Error(), "credential was filled") {
			t.Errorf("refused for the wrong reason (%q): %v", expr, err)
		}
	}
	r.noLeak()
}

// TestRealAXTreeShortValueStructure: fill a short value (a region code, under
// the 4-char scrub floor) on the real browser, read the RAW accessibility tree
// Chromium returns, and prove the value appears in no field the rendered
// snapshot would print — i.e. the node-id value masking is not the only thing
// standing between a short value and the a11y snapshot.
func TestRealAXTreeShortValueStructure(t *testing.T) {
	w := newWalletRig(t)
	op := w.caller("k-op", RequesterOperator)
	w.do(op, "open", map[string]any{"url": w.a + "/signup"})
	w.do(op, "fill-form", map[string]any{"profile": "personal"})
	s, _ := w.m.session(op)
	in := s.active
	ctx := context.Background()
	tb, _ := in.current(ctx)

	// The raw tree, exactly as Chromium reports it (before conductor masks).
	var raw struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := in.conn().Call(ctx, tb.session, "Accessibility.getFullAXTree", map[string]any{}, &raw); err != nil {
		t.Fatal(err)
	}
	// Where does "IL" live in the raw tree? (Documented: in the filled field's
	// value, which conductor masks by node id.) If it surfaces in a name or a
	// property the renderer prints, the rendered snapshot would leak it.
	region := "IL"
	for i := range raw.Nodes {
		n := &raw.Nodes[i]
		if n.Name != nil && strings.Contains(n.Name.str(), region) {
			t.Logf("region in a11y NAME of role %q (backend %d)", n.Role.str(), n.BackendID)
		}
		for _, p := range n.Properties {
			if strings.Contains(strings.Trim(string(p.Value.Value), `"`), region) {
				t.Logf("region in a11y property %q of role %q (backend %d)", p.Name, n.Role.str(), n.BackendID)
			}
		}
	}
	// The real rendered snapshot the agent would get: the browser mirrors the
	// filled value into a StaticText descendant of the field, which the renderer
	// would print as "- text: IL". After the fix the field's whole subtree is
	// quiet, so the region value appears nowhere — not in a text line, not in
	// the masked field.
	rendered := w.do(op, "snapshot", nil)["snapshot"].(string)
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "- text: IL") || strings.Contains(line, `value="IL"`) {
			t.Errorf("LEAK: the rendered snapshot shows the region value: %q", strings.TrimSpace(line))
		}
	}
	var stateSeen bool
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "textbox") && strings.Contains(strings.ToLower(line), "state") {
			stateSeen = true
			if !strings.Contains(line, FilledMark) {
				t.Errorf("the State field is not marked filled: %q", strings.TrimSpace(line))
			}
		}
	}
	if !stateSeen {
		t.Fatal("the State field was not in the snapshot at all")
	}
	_ = json.Marshal
	w.noLeak()
}

// TestRealCVCNotInSnapshotOrScreenshot: the sharp case. On the real browser,
// a hostile merchant keeps the checkout on screen after conductor fills the
// card (a place-order button that never navigates). The agent then snapshots
// and screenshots. The 3-digit CVC — below the scrub floor — must appear in
// neither: not as a "- text: 987" line (the a11y value mirror), not as the
// field's value, and not in the screenshot pixels.
func TestRealCVCNotInSnapshotOrScreenshot(t *testing.T) {
	w := newWalletRig(t)
	w.shop.Sticky = true
	op := w.caller("k-op", RequesterOperator)
	ctx := context.Background()

	w.do(op, "open", map[string]any{"url": w.a + "/cart/add?item=widget"})
	p := w.propose(op, "12.00", w.a)
	id := p["purchase"].(string)
	w.approve(id)
	w.do(op, "open", map[string]any{"url": w.a + "/checkout"})
	place := refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	// pay fills the card; the inert button means it does not navigate and the
	// fields are not cleared — they stay filled on the page.
	w.do(op, "pay", map[string]any{"purchase": id, "submit": place})

	s, _ := w.m.session(op)
	in := s.active
	if len(in.secretValues()) == 0 && !in.filled() {
		t.Fatal("the card was not filled")
	}
	// The CVC value really is in the field (the fill worked) — the agent must
	// still not be able to read it.
	tb, _ := in.current(ctx)
	if v, _ := in.eval(ctx, tb, `document.querySelector('[name=cvc]').value`); !strings.Contains(string(v), "987") {
		t.Fatalf("the page did not get the CVC: %s", v)
	}

	rendered := w.do(op, "snapshot", nil)["snapshot"].(string)
	if strings.Contains(rendered, "987") {
		for _, line := range strings.Split(rendered, "\n") {
			if strings.Contains(line, "987") {
				t.Errorf("LEAK: the snapshot shows the CVC: %q", strings.TrimSpace(line))
			}
		}
	}

	// The screenshot: the CVC field box must be masked (black), not showing 987.
	img := decodeShot(t, w, w.do(op, "screenshot", map[string]any{"path": "cvc.png"}))
	cx, cy, cw, ch, iw := boxOf(t, in, tb, `[name="cvc"]`)
	scale := float64(img.Bounds().Dx()) / iw
	if !masked(img, cx, cy, cw, ch, scale) {
		t.Errorf("LEAK: the CVC field is not masked in the screenshot")
	}
	w.noLeak()
}

// TestRealTransformReflectionBoundary: conductor scrubs a value a page reflects
// back verbatim (the realistic case), but a page whose OWN script transforms the
// value first (base64) beats a literal substring scrub. This documents the
// inherent boundary: it needs attacker script already on the credential's exact
// trusted origin (the agent cannot inject it — evaluate is read-only), and
// conductor cannot un-transform arbitrary encodings. Not a fill/redaction-path
// bug; the mitigation is the operator trusting the origin + the egress allowlist.
func TestRealTransformReflectionBoundary(t *testing.T) {
	r := newCredRig(t)
	c := r.caller("op", RequesterOperator)
	r.do(c, "open", map[string]any{"url": r.bound + "/codefield-xform"})
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	r.do(c, "fill-credential", map[string]any{"credential": "site", "field": "otp", "ref": refOf(t, snap, "textbox", "Code")})
	s, _ := r.m.session(c)
	code := s.active.secretValues()[0]
	after := r.do(c, "snapshot", nil)["snapshot"].(string)
	// What conductor DOES guarantee: the verbatim value never appears.
	if strings.Contains(after, code) {
		t.Errorf("the raw value was not scrubbed from the snapshot")
	}
	// What it cannot: a hostile script's base64 of the value. Documented, not
	// asserted away — proving the boundary is real, not hand-waved.
	b64 := base64.StdEncoding.EncodeToString([]byte(code))
	if strings.Contains(after, b64) {
		t.Logf("inherent boundary: a hostile same-origin script exfiltrated base64(value) (%s) via the page's own text; requires XSS on the trusted origin", b64)
	}
}

// TestRealSelectLabelNotInSnapshot: a filled <select> (a country here; a card
// expiry month/year on a real checkout) masks the combobox value, but the
// browser also exposes the selected <option> as its own a11y node whose name is
// the label ("United States"). That label — the value in human form, and not
// caught by the value scrub when the stored value is a short code like "US" —
// must not render. The whole subtree of a filled field is suppressed.
func TestRealSelectLabelNotInSnapshot(t *testing.T) {
	w := newWalletRig(t)
	op := w.caller("k-op", RequesterOperator)
	w.do(op, "open", map[string]any{"url": w.a + "/signup"})
	w.do(op, "fill-form", map[string]any{"profile": "personal"})
	rendered := w.do(op, "snapshot", nil)["snapshot"].(string)
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "United States") {
			t.Errorf("LEAK: the snapshot shows the selected country label: %q", strings.TrimSpace(line))
		}
	}
	// The country field itself is still present, marked filled.
	var seen bool
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, "Country") && (strings.Contains(line, "combobox") || strings.Contains(line, "textbox")) {
			seen = true
			if !strings.Contains(line, FilledMark) {
				t.Errorf("the country field is not marked filled: %q", strings.TrimSpace(line))
			}
		}
	}
	if !seen {
		t.Fatal("the country field vanished from the snapshot")
	}
	w.noLeak()
}

// TestRealWaitNotSelectOracle: a filled <select>'s option label reaches
// document.body.innerText (unlike a text input's value), so `wait` — which
// matches the agent's text against that body — could be brute-forced as an
// oracle for the filled country (or a card expiry month/year select). The wait
// comparison now blinds every filled value and displayed label, so the match
// cannot confirm one.
func TestRealWaitNotSelectOracle(t *testing.T) {
	w := newWalletRig(t)
	op := w.caller("k-op", RequesterOperator)
	w.do(op, "open", map[string]any{"url": w.a + "/signup"})
	w.do(op, "fill-form", map[string]any{"profile": "personal"})
	// The label really is on the page (the fill worked) ...
	s, _ := w.m.session(op)
	in := s.active
	tb, _ := in.current(context.Background())
	if v, _ := in.eval(context.Background(), tb, `(document.body && document.body.innerText) || ""`); !strings.Contains(string(v), "United States") {
		t.Fatal("the country was not filled")
	}
	// ... but the agent-facing wait cannot confirm it (it times out).
	if err := w.fail(op, "wait", map[string]any{"text": "United States", "timeout": 1.0}); !strings.Contains(err.Error(), "timed out") {
		t.Errorf("LEAK: wait confirmed the filled country label (an oracle): %v", err)
	}
	// A control: ordinary page text (never filled) still matches.
	w.do(op, "wait", map[string]any{"text": "Create an account", "timeout": 2.0})
	w.noLeak()
}

// TestRealCardFillLocksEvaluate: a full real purchase — propose, approve, pay
// (the PAN/CVC filled on the real checkout) — then every attempt to read the
// card back through evaluate is refused, even on the confirmation page.
func TestRealCardFillLocksEvaluate(t *testing.T) {
	w := newWalletRig(t)
	w.st.Evaluate = []string{"agent:test@*"}
	op := w.caller("k-op", RequesterOperator)
	ctx := context.Background()

	w.do(op, "open", map[string]any{"url": w.a + "/cart/add?item=widget"})
	p := w.propose(op, "12.00", w.a)
	id := p["purchase"].(string)
	w.approve(id)
	w.do(op, "open", map[string]any{"url": w.a + "/checkout"})
	place := refOf(t, w.do(op, "snapshot", nil)["snapshot"].(string), "Place order")
	done := w.do(op, "pay", map[string]any{"purchase": id, "submit": place})
	if done["status"] != PurchaseDone {
		t.Fatalf("pay: %v", done)
	}
	for _, expr := range []string{
		`document.documentElement.outerHTML`,
		`document.body.innerText`,
		`[...document.querySelectorAll('input')].map(i=>i.value).join('|')`,
		`document.title`,
	} {
		if _, err := w.m.Do(ctx, op, "evaluate", map[string]any{"expr": expr}); err == nil || !strings.Contains(err.Error(), "credential was filled") {
			t.Errorf("evaluate after a card fill (%q): %v", expr, err)
		}
	}
	_ = browsertest.ShopTestCard
	w.noLeak()
}
