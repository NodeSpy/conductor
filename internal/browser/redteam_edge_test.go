//go:build linux || darwin

package browser

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/browser/browsertest"
)

// TestRedteamErrorPathsNeverEchoValue: action errors are returned to the agent
// without the value scrub, so no error on any fill path may carry a filled
// value. A battery of forced failures must never echo the password or the TOTP
// seed, including an oversized unicode value stuffed into a maxlength field.
func TestRedteamErrorPathsNeverEchoValue(t *testing.T) {
	r := newCredRig(t)
	c := r.caller("op", RequesterOperator)
	ctx := context.Background()

	r.do(c, "open", map[string]any{"url": r.bound + "/login"})
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	pwRef := refOf(t, snap, "textbox", "Password")
	userRef := refOf(t, snap, "textbox", "Username")

	var errs []string
	try := func(action string, args map[string]any) {
		if _, err := r.m.Do(ctx, c, action, args); err != nil {
			errs = append(errs, err.Error())
		}
	}
	// The password into the username field, and vice versa.
	try("fill-credential", map[string]any{"credential": "site", "field": "password", "ref": userRef})
	try("fill-credential", map[string]any{"credential": "site", "field": "username", "ref": pwRef})
	// A field that is not an input.
	try("fill-credential", map[string]any{"credential": "site", "field": "otp", "ref": refOf(t, snap, "button", "Sign in")})
	// An unknown ref, an unknown credential, an unknown field.
	try("fill-credential", map[string]any{"credential": "site", "field": "password", "ref": "e999999"})
	try("fill-credential", map[string]any{"credential": "nope", "field": "password", "ref": pwRef})
	try("fill-credential", map[string]any{"credential": "site", "field": "bogus", "ref": pwRef})
	// On the lookalike origin (refused for origin).
	r.do(c, "open", map[string]any{"url": r.alike + "/login"})
	asnap := r.do(c, "snapshot", nil)["snapshot"].(string)
	try("fill-credential", map[string]any{"credential": "site", "field": "password", "ref": refOf(t, asnap, "textbox", "Password")})
	try("login", map[string]any{"credential": "site"})

	// An oversized unicode password routed through a real fill: the value is
	// set, the submit fails to move on, but no error may carry it.
	r.vault.mu.Lock()
	big := "SENTINEL-PW-" + strings.Repeat("é🔑", 40000)
	r.vault.m["site/password"] = big
	r.vault.mu.Unlock()
	r.do(c, "open", map[string]any{"url": r.bound + "/login"})
	snap = r.do(c, "snapshot", nil)["snapshot"].(string)
	try("fill-credential", map[string]any{"credential": "site", "field": "password", "ref": refOf(t, snap, "textbox", "Password")})

	for _, e := range errs {
		for _, secret := range []string{browsertest.Password, testSeed, big, "SENTINEL-PW-"} {
			if strings.Contains(e, secret) {
				t.Fatalf("LEAK: an error echoed a secret: %.200s", e)
			}
		}
	}
}

// TestRedteamOpaqueOriginFillRefused: a page with no real origin — a data: URL,
// about:blank — is never a fill target for a credential (its origin can match
// no credential, and the opaque origin fails the exact check).
func TestRedteamOpaqueOriginFillRefused(t *testing.T) {
	r := newCredRig(t)
	c := r.caller("op", RequesterOperator)
	ctx := context.Background()

	for _, u := range []string{
		"data:text/html,<input type=password id=p>",
		"about:blank",
	} {
		// Navigating there may itself be refused; either way, a credential must
		// not fill on it.
		_, _ = r.m.Do(ctx, c, "open", map[string]any{"url": u})
		if _, err := r.m.Do(ctx, c, "login", map[string]any{"credential": "site"}); err == nil {
			t.Fatalf("login succeeded on %q", u)
		}
		before := r.vault.count()
		if _, err := r.m.Do(ctx, c, "fill-credential", map[string]any{"credential": "site", "field": "password", "ref": "e1"}); err == nil {
			t.Fatalf("fill-credential succeeded on %q", u)
		}
		if r.vault.count() != before {
			t.Fatalf("a fill on %q fetched the secret", u)
		}
	}
	_ = time.Second
}
