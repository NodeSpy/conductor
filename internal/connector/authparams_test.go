package connector

import (
	"net/url"
	"strings"
	"testing"
)

// TestConsentURLAuthParams verifies that a connector's baked-in AuthParams
// (e.g. Google's access_type=offline) are appended to the consent URL — without
// them Google never returns a refresh token and the managed token dies in 1h.
func TestConsentURLAuthParams(t *testing.T) {
	a := authConfig{
		Type:       "oauth2",
		Grant:      "authorization_code",
		AuthURL:    "https://accounts.google.com/o/oauth2/v2/auth",
		ClientID:   "cid",
		Scopes:     []string{"https://www.googleapis.com/auth/calendar"},
		AuthParams: map[string]string{"access_type": "offline", "prompt": "consent"},
	}
	raw := consentURL(a, "state123", "challengeXYZ")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("consentURL not parseable: %v", err)
	}
	q := u.Query()
	if q.Get("access_type") != "offline" {
		t.Errorf("access_type = %q, want offline (url: %s)", q.Get("access_type"), raw)
	}
	if q.Get("prompt") != "consent" {
		t.Errorf("prompt = %q, want consent", q.Get("prompt"))
	}
	// Sanity: the standard params are still present.
	if q.Get("client_id") != "cid" || q.Get("code_challenge") != "challengeXYZ" || q.Get("response_type") != "code" {
		t.Errorf("standard consent params missing: %s", raw)
	}
	if !strings.Contains(q.Get("scope"), "calendar") {
		t.Errorf("scope missing: %s", raw)
	}
}
