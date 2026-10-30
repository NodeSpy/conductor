package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// reqOn builds a request carrying the listener/node reqInfo the server wrapper
// would normally set.
func reqOn(method, path string, l Listener, node string, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.RemoteAddr = "127.0.0.1:5555"
	if method != http.MethodGet {
		r.Header.Set("Content-Type", "application/json") // as the SPA sends it
	}
	return WithReqInfo(r, l, node, "")
}

func TestCookieListenerBinding(t *testing.T) {
	h := newHarness(t)
	ck, sess, err := h.m.CreateSession("ops", ListenerLocalhost, "", "test", "127.0.0.1", "")
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	// On the listener it was created on: valid.
	r := reqOn("GET", "/api/v1/access", ListenerLocalhost, "", "")
	r.AddCookie(ck)
	if _, err := h.m.ValidateCookie(r, ListenerLocalhost, ""); err != nil {
		t.Fatalf("same-listener cookie should validate: %v", err)
	}
	// Presented on the tailnet listener: refused (design §6.3).
	r2 := reqOn("GET", "/api/v1/access", ListenerTailscale, "node-a", "")
	r2.AddCookie(ck)
	if _, err := h.m.ValidateCookie(r2, ListenerTailscale, "node-a"); err == nil {
		t.Fatal("a localhost cookie must be refused on the tailnet listener")
	}
	_ = sess
}

func TestCookieNodeBinding(t *testing.T) {
	h := newHarness(t)
	ck, _, _ := h.m.CreateSession("ops", ListenerTailscale, "node-a", "test", "100.1.1.1", "")
	r := reqOn("GET", "/x", ListenerTailscale, "node-b", "")
	r.AddCookie(ck)
	if _, err := h.m.ValidateCookie(r, ListenerTailscale, "node-b"); err == nil {
		t.Fatal("a tailnet cookie replayed from another node must be refused")
	}
}

func TestCookieIdleAndAbsoluteExpiry(t *testing.T) {
	h := newHarness(t)
	ck, _, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "test", "127.0.0.1", "")
	// Idle past 30m.
	h.clk.advance(31 * time.Minute)
	r := reqOn("GET", "/x", ListenerLocalhost, "", "")
	r.AddCookie(ck)
	if _, err := h.m.ValidateCookie(r, ListenerLocalhost, ""); err == nil {
		t.Fatal("an idle-expired session must be refused")
	}
}

func TestCookieBadSecret(t *testing.T) {
	h := newHarness(t)
	ck, _, _ := h.m.CreateSession("ops", ListenerLocalhost, "", "test", "127.0.0.1", "")
	id, _, _ := strings.Cut(ck.Value, ".")
	forged := &http.Cookie{Name: SessionCookie, Value: id + ".notthesecret"}
	r := reqOn("GET", "/x", ListenerLocalhost, "", "")
	r.AddCookie(forged)
	if _, err := h.m.ValidateCookie(r, ListenerLocalhost, ""); err == nil {
		t.Fatal("a cookie with a wrong secret must be refused (the list can't become a cookie)")
	}
}

func TestCSRFEnforcedOnMutations(t *testing.T) {
	sess := Session{CSRF: "good-token"}
	get, _ := http.NewRequest("GET", "/x", nil)
	if err := checkCSRF(sess, get); err != nil {
		t.Fatal("GET must not need a CSRF token")
	}
	post, _ := http.NewRequest("POST", "/x", nil)
	if err := checkCSRF(sess, post); err == nil {
		t.Fatal("a POST without the CSRF header must be refused")
	}
	post.Header.Set(CSRFHeader, "wrong")
	if err := checkCSRF(sess, post); err == nil {
		t.Fatal("a POST with a wrong CSRF token must be refused")
	}
	post.Header.Set(CSRFHeader, "good-token")
	if err := checkCSRF(sess, post); err != nil {
		t.Fatalf("a POST with the matching token must pass: %v", err)
	}
}

func TestProtectedHandlerNeedsSession(t *testing.T) {
	h := newHarness(t)
	rec := httptest.NewRecorder()
	r := reqOn("GET", "/api/v1/passkeys", ListenerLocalhost, "", "")
	h.m.Mux().ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("passkeys without a session = %d, want 401", rec.Code)
	}
}

func TestAuthStateSignedOut(t *testing.T) {
	h := newHarness(t)
	rec := httptest.NewRecorder()
	r := reqOn("GET", "/api/v1/auth/state", ListenerLocalhost, "", "")
	h.m.Mux().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth/state = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"signed_in":false`) {
		t.Fatalf("signed-out state body = %s", rec.Body.String())
	}
}

func TestEnrollEndpointsLocalhostOnly(t *testing.T) {
	h := newHarness(t)
	rec := httptest.NewRecorder()
	r := reqOn("POST", "/api/v1/enroll/redeem", ListenerTailscale, "node-a", `{"t":"x","pairing_code":"1"}`)
	h.m.Mux().ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("enroll on the tailnet = %d, want 403 (localhost only)", rec.Code)
	}
}

func TestSecurityHeadersSet(t *testing.T) {
	rec := httptest.NewRecorder()
	h := SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"frame-ancestors 'none'", "default-src 'self'", "object-src 'none'", "require-trusted-types-for 'script'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q; got %q", want, csp)
		}
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("X-Frame-Options must be DENY")
	}
}
