package browser

import "testing"

// TestRedteamLookalikeOriginsRefused: the origin check compares the browser's
// own reported security origin to the credential's exact scheme+host+port.
// Every lookalike of https://grafana.example.com must be refused; only the
// exact origin (case-insensitive host) is accepted. check() is what gates every
// fill, so this pins the rule a page cannot talk its way past.
func TestRedteamLookalikeOriginsRefused(t *testing.T) {
	o, err := parseOrigin("https://grafana.example.com")
	if err != nil {
		t.Fatal(err)
	}
	refuse := []string{
		"https://grafana.example.com.evil.test",  // suffix host
		"https://grafana-example.com",            // hyphen, different host
		"https://grafana.example.com.",           // trailing dot
		"https://grafana.example.com:8443",       // port variant
		"http://grafana.example.com",             // scheme downgrade
		"https://evil.grafana.example.com",       // subdomain (not the exact host)
		"https://xn--grafan-........example.com", // punycode homoglyph (different host)
		"https://grafana.example.com@evil.test",  // userinfo trick (no host match / rejected)
		"https://evil.test",                      // unrelated
		"null",                                   // opaque origin
		"",                                       // empty
	}
	for _, so := range refuse {
		if err := o.check(so, so+"/login"); err == nil {
			t.Errorf("check accepted lookalike %q", so)
		}
	}
	accept := []string{
		"https://grafana.example.com",
		"https://GRAFANA.EXAMPLE.COM", // host is matched case-insensitively
		"https://grafana.example.com:443",
	}
	for _, so := range accept {
		if err := o.check(so, so+"/login"); err != nil {
			t.Errorf("check refused the real origin %q: %v", so, err)
		}
	}
	// A port-bound credential: only that port.
	p, _ := parseOrigin("https://grafana.example.com:8443")
	if p.check("https://grafana.example.com", "https://grafana.example.com/") == nil {
		t.Error("a :8443 credential accepted the default port")
	}
}
