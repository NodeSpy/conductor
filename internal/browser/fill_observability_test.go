//go:build linux || darwin

package browser

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestCredentialFillUnobservableByPageScript (red team, attacker B — the
// operator's added directions: in-page JS injection, prototype poisoning, and
// keypress capture below the page).
//
// Even on the correct, allowed origin, assume hostile script runs in the page
// (stored/reflected XSS, a third-party widget, a compromised dependency). It
// tries every in-page way to capture the credential at fill time:
//   - overrides HTMLInputElement.prototype.value's setter (prototype poisoning);
//   - adds capture-phase keydown and input listeners on window.
//
// Conductor must fill so that page script cannot intercept the value (the
// isolated world's pristine setter, never el.value = v in the page's main
// world) and must not emit observable keystrokes (no Input.dispatchKeyEvent for
// a fill), and the value must never reach the agent.
//
// What is inherent and documented (NOT a conductor break): an ordinary `input`
// event listener — like a real user's keystrokes would fire — still sees the
// value. That is "a site sees what is typed into it". The test asserts that
// happened (so it is a real test) but that nothing of it reached the agent.
func TestCredentialFillUnobservableByPageScript(t *testing.T) {
	r := newRig(t)
	const pw = "SENTINEL-PW-9f3a1b2c" // >= 4 chars, so the general scrub covers it
	const user = "sentinel-user@example.test"

	var mu sync.Mutex
	var setterRuns, keydowns, inputs int
	var captured []string
	report := func(what, v string) {
		mu.Lock()
		defer mu.Unlock()
		switch what {
		case "setter":
			setterRuns++
		case "keydown":
			keydowns++
		case "input":
			inputs++
		}
		if v != "" {
			captured = append(captured, v)
		}
	}

	// A login page carrying hostile script, and the real sign-in validator.
	const hostile = `<!doctype html><html><head><title>Sign in</title>
<script>
// Prototype poisoning: redefine the value setter in the page's MAIN world so
// any el.value = v hands us the plaintext. Conductor's isolated world has its
// own pristine prototypes, so this must NOT fire.
(function(){
  const d = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value');
  Object.defineProperty(HTMLInputElement.prototype, 'value', {
    configurable: true,
    get(){ return d.get.call(this); },
    set(v){ navigator.sendBeacon('/report?what=setter&v=' + encodeURIComponent(v)); return d.set.call(this, v); },
  });
  // Keypress capture below page JS: a keylogger would see these. A fill emits
  // none.
  window.addEventListener('keydown', function(){ navigator.sendBeacon('/report?what=keydown'); }, true);
  // An ordinary input listener (the inherent residual, like a real user).
  window.addEventListener('input', function(e){ navigator.sendBeacon('/report?what=input&v=' + encodeURIComponent(e.target.value)); }, true);
})();
</script></head><body><h1>Sign in</h1>
<form method="post" action="/login">
<label>Username <input name="username" autocomplete="username"></label>
<label>Password <input name="password" type="password" autocomplete="current-password"></label>
<button type="submit">Sign in</button></form></body></html>`

	mux := http.NewServeMux()
	mux.HandleFunc("/report", func(w http.ResponseWriter, req *http.Request) {
		report(req.URL.Query().Get("what"), req.URL.Query().Get("v"))
		w.WriteHeader(204)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			_ = req.ParseForm()
			if req.Form.Get("username") == user && req.Form.Get("password") == pw {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, `<!doctype html><title>Welcome</title><h1>Signed in</h1>`)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<!doctype html><title>Sign in</title><h1>Wrong</h1>`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, hostile)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	r.st.Allow = append(r.st.Allow, host)
	v := &fakeVault{m: map[string]string{"u": user, "p": pw}, fetches: map[string]int{}}
	r.m.Secrets = v.read
	ref := func(k string) *SecretRef { return &SecretRef{Vault: "testvault", Key: k} }
	r.st.Credentials = []Credential{{
		Name: "hostile", Origin: srv.URL, Identities: []string{"agent:test@acme/app"},
		LoginURL: srv.URL + "/login", Username: ref("u"), Password: ref("p"),
	}}

	c := r.caller("op", RequesterOperator)
	r.do(c, "open", map[string]any{"url": srv.URL + "/login"})
	out := r.do(c, "login", map[string]any{"credential": "hostile"})

	// 1. The sign-in worked: the pristine isolated-world setter set the right
	//    value despite the main-world override (if the override had captured or
	//    mangled it, the POST would not have matched and there'd be no
	//    "submitted"/the page would not move on).
	steps := fmt.Sprint(out["steps"])
	if !strings.Contains(steps, "password") || !strings.Contains(steps, "submitted") {
		t.Fatalf("the sign-in did not complete: %v", out)
	}

	mu.Lock()
	gotSetter, gotKey, gotInput, gotCap := setterRuns, keydowns, inputs, append([]string(nil), captured...)
	mu.Unlock()

	// 2. The page's overridden value setter was never invoked: conductor filled
	//    through the isolated world's pristine prototype, so prototype poisoning
	//    cannot intercept the fill.
	if gotSetter != 0 {
		t.Fatalf("LEAK: the page's overridden value setter saw the fill %d time(s): %v", gotSetter, gotCap)
	}
	// 3. No observable keystrokes: a keylogger (page-level or below) sees none.
	if gotKey != 0 {
		t.Fatalf("LEAK: the fill dispatched %d keydown event(s) a keylogger could read", gotKey)
	}
	// 4. The inherent residual is real: an ordinary input listener did fire and
	//    saw the value (a site sees what is typed into it) — this proves the
	//    test actually exercised the path.
	if gotInput == 0 {
		t.Fatalf("the input event should fire like a real user's (test is not exercising the fill)")
	}
	sawPW := false
	for _, s := range gotCap {
		if dec, _ := url.QueryUnescape(s); dec == pw {
			sawPW = true
		}
	}
	if !sawPW {
		t.Fatalf("expected the page's own input listener to observe the value (the documented residual): %v", gotCap)
	}

	// 5. The crux: none of that reached the AGENT. The password is in no reply,
	//    no audit entry, and no workspace file.
	var hay []string
	b, _ := json.Marshal(out)
	hay = append(hay, string(b))
	r.audit.mu.Lock()
	for _, e := range r.audit.es {
		eb, _ := json.Marshal(e)
		hay = append(hay, string(eb))
	}
	r.audit.mu.Unlock()
	filepath.Walk(r.ws, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			fb, _ := os.ReadFile(p)
			hay = append(hay, string(fb))
		}
		return nil
	})
	for _, h := range hay {
		if strings.Contains(h, pw) {
			t.Fatalf("LEAK: the credential reached an agent-visible artifact: %.200s", h)
		}
	}

	// 6. The agent cannot read back what the hostile listener captured:
	//    evaluate is refused (off after a fill, and not opted in regardless).
	if err := r.fail(c, "evaluate", map[string]any{"expr": "document.title"}); !strings.Contains(err.Error(), "evaluate") {
		t.Fatalf("evaluate should be refused after a credential fill: %v", err)
	}
}
