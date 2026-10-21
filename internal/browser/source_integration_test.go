//go:build linux || darwin

package browser

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

// TestBrowserSourceRedactsFilledValues runs a real headless Chromium: it fills
// a sentinel password and a sentinel card into a real page, then reads the
// page with `source` and proves neither value appears — while the raw DOM
// still held the password constant, so this proves redaction HAPPENED, not
// that the page lacked the value. It also proves the structural redaction
// runs BEFORE the scrub backstop (the backstop disabled), that a same-origin
// frame is included and a cross-origin one is not.
func TestBrowserSourceRedactsFilledValues(t *testing.T) {
	r := newRig(t)
	_, port, _ := strings.Cut(r.host, ":")
	r.st.Allow = []string{"localhost:" + port, "127.0.0.1:" + port}
	bound := "http://localhost:" + port
	alike := "http://127.0.0.1:" + port
	const cardSentinel = "4000123455559999" // only ever live-filled, never planted

	v := &fakeVault{m: map[string]string{
		"s/pw":   browsertest.Password, // also planted in the page's structural spots
		"s/card": cardSentinel,
	}, fetches: map[string]int{}}
	ref := func(k string) *SecretRef { return &SecretRef{Vault: "testvault", Key: k} }
	// Two credentials bound to the origin: one stands for a password, one for
	// a card number — both are filled secrets of this browser.
	r.st.Credentials = []Credential{
		{Name: "site", Origin: bound, Identities: []string{"agent:test@acme/app"}, Password: ref("s/pw")},
		{Name: "card", Origin: bound, Identities: []string{"agent:test@acme/app"}, Password: ref("s/card")},
	}
	r.m.Secrets = v.read
	c := r.caller("k1", RequesterOperator)

	r.do(c, "open", map[string]any{"url": bound + "/source-sentinels?cross=" + alike})
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	r.do(c, "fill-credential", map[string]any{"ref": refOf(t, snap, "textbox", "Password"), "field": "password", "credential": "site"})
	r.do(c, "fill-credential", map[string]any{"ref": refOf(t, snap, "textbox", "Card"), "field": "password", "credential": "card"})

	// Both are now filled secrets of this browser.
	in := r.m.sessions[c.Key].login
	if in == nil {
		t.Fatal("the credential fill should have opened the logged-in browser")
	}
	secrets := in.secretValues()
	if !has(secrets, browsertest.Password) || !has(secrets, cardSentinel) {
		t.Fatalf("both sentinels should be filled: %v", secrets)
	}

	// The raw DOM still holds the planted password constant: redaction has
	// something real to remove.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	tb, err := in.current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if raw := rawOuterHTML(t, in, tb); !strings.Contains(raw, browsertest.Password) {
		t.Fatalf("the raw DOM should contain the planted constant; it did not")
	}

	// `source` through the manager: neither value, structure visible.
	out := r.do(c, "source", map[string]any{"max": 200000})
	src, ok := out["source"].(string)
	if !ok || src == "" {
		t.Fatalf("no source: %v", out)
	}
	for _, s := range []string{browsertest.Password, cardSentinel} {
		if strings.Contains(src, s) {
			t.Fatalf("source leaked %q:\n%s", s, clip(src, 800))
		}
	}
	if !strings.Contains(src, "<form") || !strings.Contains(src, `value="`+Redacted+`"`) {
		t.Fatalf("source should show structure with redaction markers:\n%s", clip(src, 800))
	}
	if !strings.Contains(src, "SAMEFRAME_MARKER") {
		t.Fatalf("a same-origin frame's content should be included:\n%s", clip(src, 800))
	}
	if strings.Contains(src, "CROSSFRAME_SECRET") {
		t.Fatalf("a cross-origin frame's content must never appear:\n%s", clip(src, 800))
	}

	// Structural redaction runs BEFORE the scrub backstop: with the filled
	// values cleared (so scrub is a no-op), the planted password in its
	// structural spots and the live field values are STILL absent.
	in.mu.Lock()
	saved := in.secrets
	in.secrets = nil
	in.mu.Unlock()
	body, _, err := in.sourceHTML(ctx, tb, 200000)
	in.mu.Lock()
	in.secrets = saved
	in.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(in.scrub("x"), Redacted) {
		t.Fatal("scrub should be a no-op for this check")
	}
	for _, s := range []string{browsertest.Password, cardSentinel} {
		if strings.Contains(body, s) {
			t.Fatalf("structural redaction (scrub disabled) leaked %q — the value survived only because of the backstop:\n%s", s, clip(body, 800))
		}
	}
}

func has(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// rawOuterHTML reads the whole document's serialized HTML straight from the
// browser (no redaction) — the "raw DOM" the source artifact is checked
// against.
func rawOuterHTML(t *testing.T, in *Instance, tb *tab) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var doc struct {
		Root struct {
			NodeID int64 `json:"nodeId"`
		} `json:"root"`
	}
	if err := in.conn().Call(ctx, tb.session, "DOM.getDocument", map[string]any{"depth": 0}, &doc); err != nil {
		t.Fatal(err)
	}
	var oh struct {
		OuterHTML string `json:"outerHTML"`
	}
	if err := in.conn().Call(ctx, tb.session, "DOM.getOuterHTML", map[string]any{"nodeId": doc.Root.NodeID}, &oh); err != nil {
		t.Fatal(err)
	}
	return oh.OuterHTML
}
