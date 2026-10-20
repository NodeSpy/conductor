//go:build linux || darwin

package browser

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// credServer serves a login page (optionally with hostile transform script) and
// a credential bound to it, wired into a fresh rig. Returns the rig and caller.
func credServerRig(t *testing.T, pw string, loginHTML string) (*rig, Principal, string) {
	r := newRig(t)
	const user = "sentinel-user@example.test"
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			_ = req.ParseForm()
			w.Header().Set("Content-Type", "text/html")
			if req.Form.Get("password") == pw {
				fmt.Fprint(w, `<!doctype html><title>Welcome</title><h1>Signed in</h1>`)
			} else {
				fmt.Fprint(w, `<!doctype html><title>Sign in</title><h1>Wrong</h1>`)
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, loginHTML)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	r.st.Allow = append(r.st.Allow, strings.TrimPrefix(srv.URL, "http://"))
	v := &fakeVault{m: map[string]string{"u": user, "p": pw}, fetches: map[string]int{}}
	r.m.Secrets = v.read
	ref := func(k string) *SecretRef { return &SecretRef{Vault: "testvault", Key: k} }
	r.st.Credentials = []Credential{{
		Name: "site", Origin: srv.URL, Identities: []string{"agent:test@acme/app"},
		LoginURL: srv.URL + "/login", Username: ref("u"), Password: ref("p"),
	}}
	return r, r.caller("op", RequesterOperator), srv.URL
}

// TestEvaluateReadbackBlockedAfterFill (real browser): the operator's prime
// suspicion — read-back via evaluate, since "reading is not a side effect".
// After conductor fills a password, every read-back expression (plain, coerced,
// transformed, oracle) must be refused, because evaluate is OFF for the rest of
// a browser's life once any value is filled — the guarantee does not rest on
// throwOnSideEffect.
func TestEvaluateReadbackBlockedAfterFill(t *testing.T) {
	const pw = "SENTINEL-PW-9f3a1b2c"
	page := `<!doctype html><html><title>Sign in</title><body><h1>Sign in</h1>
<form method="post" action="/login">
<label>Username <input name="username" autocomplete="username"></label>
<label>Password <input name="password" type="password" autocomplete="current-password"></label>
<button type="submit">Sign in</button></form></body></html>`
	r, c, url := credServerRig(t, pw, page)
	r.st.Evaluate = []string{"agent:test@acme/app"} // opt the identity IN to evaluate

	r.do(c, "open", map[string]any{"url": url + "/login"})

	// Before any fill, read-only evaluate works (proves the harness: the field
	// is empty, so it is not yet an oracle). The value comes back JSON-encoded.
	if got := r.do(c, "evaluate", map[string]any{"expr": "document.querySelector('input[name=password]').value"}); strings.Contains(fmt.Sprint(got["value"]), "SENTINEL") {
		t.Fatalf("pre-fill password value should be empty: %v", got)
	}

	// login fills the password (and submits); the browser is now "filled".
	r.do(c, "login", map[string]any{"credential": "site"})

	// Every read-back shape the operator listed: all refused at the gate.
	exprs := []string{
		"document.querySelector('input[name=password]').value",
		"document.querySelector('input[name=password]').defaultValue",
		"document.querySelector('input[name=password]').getAttribute('value')",
		"JSON.stringify([...document.querySelectorAll('input')].map(e=>e.value))",
		"(document.activeElement||{}).value",
		"getSelection().toString()",
		"document.querySelector('input[name=password]') && document.querySelector('input[name=password]').outerHTML",
		"btoa(document.querySelector('input[name=password]')?document.querySelector('input[name=password]').value:'')",
		"(document.querySelector('input[name=password]')||{value:''}).value.length",
		"document.querySelector('form') && JSON.stringify([...new FormData(document.querySelector('form'))])",
	}
	for _, e := range exprs {
		err := r.fail(c, "evaluate", map[string]any{"expr": e})
		if !strings.Contains(err.Error(), "evaluate is off") {
			t.Fatalf("evaluate read-back was NOT blocked after fill: expr=%q err=%v", e, err)
		}
	}
}

// TestTransformExfilViaSnapshot (real browser): the transform-then-read trick.
// Hostile script on the credential's own origin reads the filled value, encodes
// it (reverse + base64) so a literal scrub misses it, and surfaces it as page
// text / an aria-label / the title. Does any agent-visible verb return the
// transformed value?
func TestTransformExfilViaSnapshot(t *testing.T) {
	const pw = "SENTINEL-PW-9f3a1b2c"
	// The page encodes the value the moment conductor types it.
	page := `<!doctype html><html><title>Sign in</title><body><h1>Sign in</h1>
<form method="post" action="/login">
<label>Username <input name="username" autocomplete="username"></label>
<label>Password <input name="password" type="password" autocomplete="current-password"></label>
<button type="submit">Sign in</button></form>
<div id="leak"></div><button id="exfil" aria-label="pending">x</button>
<script>
var pw = document.querySelector('input[name=password]');
function enc(){
  var v = pw.value; if(!v) return;
  var rev = v.split('').reverse().join('');
  var b64 = btoa(v);
  document.getElementById('leak').textContent = 'REV:'+rev+' B64:'+b64;
  document.getElementById('exfil').setAttribute('aria-label', 'REV:'+rev);
  document.title = 'T:'+rev;
}
pw.addEventListener('input', enc);
pw.addEventListener('change', enc);
</script></body></html>`
	r, c, url := credServerRig(t, pw, page)

	r.do(c, "open", map[string]any{"url": url + "/login"})
	// fill-credential: fills the password, does NOT submit, so the page (and its
	// transform output) stays for us to read.
	s := r.do(c, "snapshot", nil)["snapshot"].(string)
	ref := refOf(t, s, "textbox", "Password")
	r.do(c, "fill-credential", map[string]any{"credential": "site", "field": "password", "ref": ref})

	rev := reverseStr(pw)
	b64 := "U0VOVElORUwtUFctOWYzYTFiMmM=" // base64("SENTINEL-PW-9f3a1b2c")

	// Collect every agent-visible surface after the fill.
	snap := r.do(c, "snapshot", nil)["snapshot"].(string)
	reopen := r.do(c, "open", map[string]any{"url": url + "/login"}) // returns title too
	surfaces := map[string]string{
		"snapshot":   snap,
		"open_title": fmt.Sprint(reopen["title"]),
	}
	tabs := r.do(c, "tabs", nil)
	surfaces["tabs"] = fmt.Sprint(tabs["tabs"])

	for name, s := range surfaces {
		for form, probe := range map[string]string{"literal": pw, "reversed": rev, "base64": b64} {
			if probe != "" && strings.Contains(s, probe) {
				t.Fatalf("LEAK: the password reached the agent via %s (%s): %.300s", name, form, s)
			}
		}
	}
}
