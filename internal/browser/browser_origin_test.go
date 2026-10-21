package browser

import "testing"

// TestBrowserOrigin pins the location.origin form used by the atomic
// origin re-check in fieldSet (default port omitted; non-default kept; IPv6
// bracketed) — so the re-check compares against the exact string a browser
// reports, and a filled value cannot be set after a cross-origin navigation.
func TestBrowserOrigin(t *testing.T) {
	cases := map[string]string{
		"https://example.com":        "https://example.com",
		"https://example.com:443":    "https://example.com",
		"http://example.com":         "http://example.com",
		"http://example.com:80":      "http://example.com",
		"https://example.com:8443":   "https://example.com:8443",
		"http://example.com:8080":    "http://example.com:8080",
		"https://example.com/signin": "https://example.com", // path dropped
		"http://[::1]:8443":          "http://[::1]:8443",
	}
	for in, want := range cases {
		o, err := parseOrigin(in)
		if err != nil {
			t.Errorf("parseOrigin(%q): %v", in, err)
			continue
		}
		if got := o.browserOrigin(); got != want {
			t.Errorf("browserOrigin(%q) = %q, want %q", in, got, want)
		}
	}
}
