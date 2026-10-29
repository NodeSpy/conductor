package auth

// Regression tests for the adversarial review of the sign-in methods: each one
// is the review's repro turned round to assert the defence holds.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// call sends one request through the auth mux as a listener wrapper would tag
// it. A body is sent as JSON with the matching Content-Type unless hdr says
// otherwise.
func call(m *Manager, method, path, host, addr string, c ConnInfo, body any, hdr map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, strings.NewReader(string(b)))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
		if method != http.MethodGet {
			r.Header.Set("Content-Type", "application/json")
		}
	}
	r.Host, r.RemoteAddr = host, addr
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	r = WithConn(r, c)
	rr := httptest.NewRecorder()
	m.Mux().ServeHTTP(rr, r)
	return rr
}

func errMessage(rr *httptest.ResponseRecorder) string {
	var e struct {
		Error struct{ Code, Message string }
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &e)
	return e.Error.Message
}

// An SSH sign-in attempt must not tell an unknown operator from a real one:
// the same error, and a real verification either way.
func TestSSHFinishDoesNotRevealOperators(t *testing.T) {
	h := newMethodsHarness(t, nil)
	var verified atomic.Int32
	real := h.m.deps.VerifySSHNS
	h.m.deps.VerifySSHNS = func(ctx context.Context, ns, op, pub string, payload []byte, sig string) error {
		verified.Add(1)
		return real(ctx, ns, op, pub, payload, sig)
	}
	probe := func(op string) string {
		rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "localhost:7788", "192.0.2.1:1", onLocalhost, map[string]string{"operator": op}, nil)
		var v SSHChallengeView
		_ = json.Unmarshal(rr.Body.Bytes(), &v)
		rr = call(h.m, "POST", "/api/v1/auth/ssh/finish", "localhost:7788", "192.0.2.1:1", onLocalhost,
			map[string]string{"challenge_id": v.ID, "signature": "-----BEGIN SSH SIGNATURE-----\nAAAA\n-----END SSH SIGNATURE-----\n"}, nil)
		return strings.TrimSpace(rr.Body.String())
	}
	a := probe("ops")
	b := probe("nosuchoperator")
	if a != b {
		t.Fatalf("an unknown operator answers differently:\n  known:   %s\n  unknown: %s", a, b)
	}
	if verified.Load() != 2 {
		t.Fatalf("both attempts must run a verification (got %d)", verified.Load())
	}
}

// Distinct IPv6 clients get distinct buckets; one /64 is one client.
func TestClientKeyIPv6(t *testing.T) {
	mk := func(addr string) string {
		r := httptest.NewRequest("POST", "/", nil)
		r.RemoteAddr = addr
		return clientKey(WithConn(r, onPlainHTTP))
	}
	if a, b := mk("[2001:db8::1]:40000"), mk("[2001:db8:ffff::99]:50000"); a == b {
		t.Fatalf("two IPv6 networks share the bucket %q", a)
	}
	if a, b := mk("[2001:db8:0:1::1]:1"), mk("[2001:db8:0:1:ffff::2]:2"); a != b {
		t.Fatalf("one /64 should be one client: %q vs %q", a, b)
	}
	if a, b := mk("192.0.2.1:1"), mk("192.0.2.2:1"); a == b {
		t.Fatalf("two IPv4 addresses share the bucket %q", a)
	}

	// One IPv6 attacker's bad codes don't lock another IPv6 client out.
	h := newMethodsHarness(t, nil)
	for i := 0; i < 5; i++ {
		call(h.m, "POST", "/api/v1/auth/ssh/pending", "box.lan", "[2001:db8::bad]:1", onPlainHTTP, map[string]string{"code": "AAAA-AAAA"}, nil)
	}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "box.lan", "[2001:db8:1234::7]:2", onPlainHTTP, map[string]string{"operator": "ops"}, nil); rr.Code != http.StatusOK {
		t.Fatalf("another IPv6 client was locked out: %d %s", rr.Code, rr.Body)
	}
}

// Unauthenticated begin requests can't fill the pending-challenge table for
// everyone: one network gets a bounded share, and a signed-in operator's SSH
// step-up never competes with sign-in requests.
func TestSSHPendingCapResistsFlood(t *testing.T) {
	h := newMethodsHarness(t, nil)
	accepted := 0
	for ip := 1; ip <= 250; ip++ {
		for j := 0; j < maxSSHPendingClient; j++ {
			rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "box.example", fmt.Sprintf("198.51.100.%d:1", ip), onHTTPS, map[string]string{"operator": "x"}, nil)
			if rr.Code == http.StatusOK {
				accepted++
			}
		}
	}
	if accepted > maxSSHPendingNetwork {
		t.Fatalf("one /24 holds %d pending challenges (cap %d)", accepted, maxSSHPendingNetwork)
	}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "box.example", "203.0.113.5:1", onHTTPS, map[string]string{"operator": "ops"}, nil); rr.Code != http.StatusOK {
		t.Fatalf("a client on another network can't begin: %d %s", rr.Code, rr.Body)
	}

	// Even with the sign-in table full, a signed-in operator's step-up begins.
	for n := 0; ; n++ {
		addr := fmt.Sprintf("10.%d.%d.1:1", n/250, n%250)
		rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "box.example", addr, onHTTPS, map[string]string{"operator": "x"}, nil)
		if rr.Code != http.StatusOK {
			break
		}
		if n > maxSSHPending+10 {
			t.Fatal("the sign-in table has no global cap")
		}
	}
	sess := Session{ID: "s1", Operator: "ops"}
	if _, err := h.m.BeginSSHStepup(req("POST", "/x", "box.example", onHTTPS, nil), sess, PurposeApprove, "d"); err != nil {
		t.Fatalf("a signed-in step-up was blocked by sign-in requests: %v", err)
	}
}

// A cross-site page can't drive the auth endpoints: no text/plain "simple"
// requests, no foreign Origin, no cross-site fetch — so it can't spend the
// operator's lockout budget either.
func TestAuthEndpointsRefuseCrossSite(t *testing.T) {
	h := newMethodsHarness(t, nil)
	for name, hdr := range map[string]map[string]string{
		"text/plain":     {"Content-Type": "text/plain;charset=UTF-8"},
		"form":           {"Content-Type": "application/x-www-form-urlencoded"},
		"foreign origin": {"Origin": "https://evil.example"},
		"null origin":    {"Origin": "null"},
		"cross-site":     {"Sec-Fetch-Site": "cross-site"},
		"same-site":      {"Sec-Fetch-Site": "same-site"},
	} {
		for i := 0; i < 6; i++ {
			rr := call(h.m, "POST", "/api/v1/auth/ssh/pending", "localhost:7788", "127.0.0.1:50000", onLocalhost, map[string]string{"code": "ZZZZ-ZZZZ"}, hdr)
			if rr.Code != http.StatusForbidden && rr.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("%s: %d %s", name, rr.Code, rr.Body)
			}
		}
		for i := 0; i < 6; i++ {
			call(h.m, "POST", "/api/v1/auth/password/login", "localhost:7788", fmt.Sprintf("198.51.100.%d:1", i+1), onLocalhost,
				map[string]string{"operator": "ops", "password": "wrong-wrong-wrong", "code": "000000"}, hdr)
		}
	}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "localhost:7788", "127.0.0.1:50002", onLocalhost, map[string]string{"operator": "ops"}, nil); rr.Code != http.StatusOK {
		t.Fatalf("the operator's own sign-in was locked by cross-site requests: %d %s", rr.Code, rr.Body)
	}
	if locked, _ := h.m.lock.Locked(operatorLockKey("ops")); locked {
		t.Fatal("cross-site password posts locked the operator")
	}

	// The page's own requests pass: same Origin, same-origin fetch, JSON.
	own := map[string]string{"Origin": "http://localhost:7788", "Sec-Fetch-Site": "same-origin"}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "localhost:7788", "127.0.0.1:50003", onLocalhost, map[string]string{"operator": "ops"}, own); rr.Code != http.StatusOK {
		t.Fatalf("a same-origin request was refused: %d %s", rr.Code, rr.Body)
	}
	// A DELETE without a JSON Content-Type is refused too.
	r := httptest.NewRequest("DELETE", "/api/v1/sessions", nil)
	r.Host, r.RemoteAddr = "localhost:7788", "127.0.0.1:1"
	rr := httptest.NewRecorder()
	h.m.Mux().ServeHTTP(rr, WithConn(r, onLocalhost))
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("DELETE without Content-Type: %d", rr.Code)
	}
}

// A password step-up needs the authenticator code when the operator has one,
// even with the password-only knob on.
func TestPasswordStepupNeedsEnrolledCode(t *testing.T) {
	pol := &Policy{Methods: []Method{MethodPasskey, MethodSSH, MethodPassword}, PasswordRequiresTOTP: false,
		Stepup: []Method{MethodPasskey, MethodSSH, MethodTOTP, MethodPassword}}
	h := newMethodsHarness(t, pol)
	if err := h.m.SetPassword("ops", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	secret := setTOTP(t, h.m, "ops")
	sess := Session{ID: "s-stolen", Operator: "ops"}
	dig := CredentialDigest("ops", "totp.remove")
	if _, err := h.m.PasswordStepup(sess, PurposeCredential, dig, "correct horse battery", ""); err == nil {
		t.Fatal("a password alone stepped up for an operator with an authenticator")
	}
	h.clk.advance(60 * time.Second)
	if _, err := h.m.PasswordStepup(sess, PurposeCredential, dig, "correct horse battery", codeAt(t, secret, h.clk.now())); err != nil {
		t.Fatalf("password + code: %v", err)
	}
}

// An operator lock held by an attacker doesn't spend the real operator's own
// client budget, so their passkey and SSH sign-in keep working.
func TestOperatorLockDoesNotSpillToClient(t *testing.T) {
	h := newMethodsHarness(t, nil)
	for i := 0; i < 5; i++ {
		call(h.m, "POST", "/api/v1/auth/password/login", "box.example", fmt.Sprintf("198.51.100.%d:1", i+1), onHTTPS, map[string]string{"operator": "ops", "password": "wrong-wrong-wrong"}, nil)
	}
	for i := 0; i < 8; i++ {
		rr := call(h.m, "POST", "/api/v1/auth/password/login", "box.example", "203.0.113.77:1", onHTTPS, map[string]string{"operator": "ops", "password": "the-right-one?", "code": "123456"}, nil)
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("while the operator is locked a password attempt answers %d", rr.Code)
		}
	}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "box.example", "203.0.113.77:1", onHTTPS, map[string]string{"operator": "ops"}, nil); rr.Code != http.StatusOK {
		t.Fatalf("the operator's device was locked out of SSH: %d %s", rr.Code, rr.Body)
	}
}

// A login code is only good on the listener and host it was issued on.
func TestLoginCodeBoundToListener(t *testing.T) {
	h := newMethodsHarness(t, nil)
	rr := call(h.m, "POST", "/api/v1/auth/ssh/begin", "localhost:7788", "127.0.0.1:1", onLocalhost, map[string]string{"operator": "ops"}, nil)
	var v SSHChallengeView
	_ = json.Unmarshal(rr.Body.Bytes(), &v)
	sig := h.key.sign(t, v.Namespace, v.Text)
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/pending", "10.0.0.5:7791", "192.0.2.99:1", onPlainHTTP, map[string]string{"code": v.Code}, nil); rr.Code == http.StatusOK {
		t.Fatalf("pending on another listener: %d %s", rr.Code, rr.Body)
	}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/approve", "10.0.0.5:7791", "192.0.2.99:1", onPlainHTTP, map[string]string{"code": v.Code, "signature": sig}, nil); rr.Code == http.StatusOK {
		t.Fatalf("approve on another listener: %d %s", rr.Code, rr.Body)
	}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/approve", "127.0.0.1:7788", "127.0.0.1:2", onLocalhost, map[string]string{"code": v.Code, "signature": sig}, nil); rr.Code == http.StatusOK {
		t.Fatalf("approve under another Host: %d %s", rr.Code, rr.Body)
	}
	if rr := call(h.m, "POST", "/api/v1/auth/ssh/approve", "localhost:7788", "127.0.0.1:2", onLocalhost, map[string]string{"code": v.Code, "signature": sig}, nil); rr.Code != http.StatusOK {
		t.Fatalf("approve where it was issued: %d %s", rr.Code, rr.Body)
	}
}

// burstPolicy allows a password alone, the worst case for guessing.
func burstPolicy() *Policy {
	return &Policy{Methods: []Method{MethodPasskey, MethodSSH, MethodPassword}, PasswordRequiresTOTP: false,
		Stepup: []Method{MethodPasskey, MethodSSH, MethodTOTP}}
}

// A concurrent burst from one client is held to the lockout bound: at most
// loginMaxFails guesses are evaluated, however many arrive at once.
func TestLockoutBoundsConcurrentBurst(t *testing.T) {
	h := newMethodsHarness(t, burstPolicy())
	if err := h.m.SetPassword("ops", "guessable-pass-0150"); err != nil {
		t.Fatal(err)
	}
	const n = 200
	res := make(chan int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func(i int) {
			<-start
			rr := call(h.m, "POST", "/api/v1/auth/password/login", "box.example", "198.51.100.7:1", onHTTPS,
				map[string]string{"operator": "ops", "password": fmt.Sprintf("guessable-pass-%04d", i)}, nil)
			res <- rr.Code
		}(i)
	}
	close(start)
	tally := map[int]int{}
	for i := 0; i < n; i++ {
		tally[<-res]++
	}
	if evaluated := tally[http.StatusUnauthorized] + tally[http.StatusOK]; evaluated > loginMaxFails {
		t.Fatalf("%d of %d concurrent guesses were evaluated (bound %d): %v", evaluated, n, loginMaxFails, tally)
	}
}

// The same bound over real TCP connections opened ahead of the burst.
func TestLockoutBoundsBurstOverTCP(t *testing.T) {
	h := newMethodsHarness(t, burstPolicy())
	if err := h.m.SetPassword("ops", "guessable-pass-0150"); err != nil {
		t.Fatal(err)
	}
	mux := h.m.Mux()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, WithConn(r, onHTTPS))
	}))
	defer srv.Close()
	const n = 300
	hc := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: n, MaxConnsPerHost: n}}
	warm := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			if resp, err := hc.Get(srv.URL + "/api/v1/auth/state"); err == nil {
				resp.Body.Close()
			}
			warm <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-warm
	}
	start := make(chan struct{})
	res := make(chan int, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			<-start
			body := fmt.Sprintf(`{"operator":"ops","password":"guessable-pass-%04d"}`, i)
			resp, err := hc.Post(srv.URL+"/api/v1/auth/password/login", "application/json", strings.NewReader(body))
			if err != nil {
				res <- 0
				return
			}
			resp.Body.Close()
			res <- resp.StatusCode
		}(i)
	}
	close(start)
	tally := map[int]int{}
	for i := 0; i < n; i++ {
		tally[<-res]++
	}
	if evaluated := tally[http.StatusUnauthorized] + tally[http.StatusOK]; evaluated > loginMaxFails {
		t.Fatalf("%d of %d guesses over TCP were evaluated (bound %d): %v", evaluated, n, loginMaxFails, tally)
	}
}

// Lockout state stays small: oversized or malformed operator names make no
// operator bucket, and expired entries are collected.
func TestLockoutStateIsBounded(t *testing.T) {
	h := newMethodsHarness(t, nil)
	const n = 40
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			name := fmt.Sprintf("%d-%s", i, strings.Repeat("A", 100*1000))
			call(h.m, "POST", "/api/v1/auth/password/login", "box.example", fmt.Sprintf("198.51.100.%d:1", i), onHTTPS,
				map[string]string{"operator": name, "password": "x"}, nil)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
	for i := 0; i < 50; i++ { // lookups alone create nothing
		h.m.lock.Locked(fmt.Sprintf("addr:192.0.2.%d", i))
	}
	h.m.lock.mu.Lock()
	big := 0
	for k := range h.m.lock.m {
		big = max(big, len(k))
	}
	before := len(h.m.lock.m)
	h.m.lock.mu.Unlock()
	if big > 200 {
		t.Fatalf("a lockout key of %d bytes was kept", big)
	}
	if before > n {
		t.Fatalf("%d lockout entries after %d attempts (lookups must not create entries)", before, n)
	}
	h.clk.advance(24 * time.Hour)
	call(h.m, "POST", "/api/v1/auth/password/login", "box.example", "203.0.113.1:1", onHTTPS, map[string]string{"operator": "ops", "password": "x"}, nil)
	h.m.lock.mu.Lock()
	after := len(h.m.lock.m)
	h.m.lock.mu.Unlock()
	if after > 2 {
		t.Fatalf("%d lockout entries a day later; expired ones must be collected", after)
	}
}

// A code accepted one step early doesn't refuse the operator's genuine
// current code; each step is still spent once.
func TestTOTPEarlyStepDoesNotBlockCurrent(t *testing.T) {
	h := newMethodsHarness(t, nil)
	secret := setTOTP(t, h.m, "ops")
	h.clk.advance(5 * time.Minute)
	sess := Session{ID: "s1", Operator: "ops"}
	next := codeAt(t, secret, h.clk.now().Add(30*time.Second))
	cur := codeAt(t, secret, h.clk.now())
	if _, err := h.m.TOTPStepup(sess, PurposeApprove, "d", next); err != nil {
		t.Fatalf("next step's code: %v", err)
	}
	if _, err := h.m.TOTPStepup(sess, PurposeApprove, "d", cur); err != nil {
		t.Fatalf("the current code after the next step's: %v", err)
	}
	if _, err := h.m.TOTPStepup(sess, PurposeApprove, "d", cur); err == nil {
		t.Fatal("the current code twice")
	}
	if _, err := h.m.TOTPStepup(sess, PurposeApprove, "d", next); err == nil {
		t.Fatal("the next step's code twice")
	}
	prev := codeAt(t, secret, h.clk.now().Add(-30*time.Second))
	h.clk.advance(30 * time.Second)
	if _, err := h.m.TOTPStepup(sess, PurposeApprove, "d", prev); err == nil {
		t.Fatal("a code from outside the window")
	}
}

// What the review tried to break and couldn't: replay, cross-kind,
// cross-operator, cross-session, cross-purpose, TOTP cross-use.
func TestSSHAndStepupClaimsHold(t *testing.T) {
	ctx := t.Context()
	h := newMethodsHarness(t, nil)
	other := newSSHKey(t)
	h.m.deps.Operators = func() map[string]string { return map[string]string{"ops": h.key.pub, "other": other.pub} }
	begin := func(op string) SSHChallengeView {
		v, err := h.m.BeginSSHLogin(localReq(), op)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	v1 := begin("ops")
	sig1 := h.key.sign(t, LoginNamespace, v1.Text)
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v1.ID, sig1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v1.ID, sig1); err == nil {
		t.Error("same challenge twice")
	}
	v2 := begin("ops")
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v2.ID, sig1); err == nil {
		t.Error("old signature on new challenge")
	}
	v3 := begin("ops")
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v3.ID, other.sign(t, LoginNamespace, v3.Text)); err == nil {
		t.Error("other's key signed in as ops")
	}
	v4 := begin("other")
	txt := strings.Replace(v4.Text, "operator=other", "operator=ops", 1)
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), v4.ID, h.key.sign(t, LoginNamespace, txt)); err == nil {
		t.Error("client-altered text accepted")
	}
	sess := Session{ID: "s1", Operator: "ops"}
	sv, err := h.m.BeginSSHStepup(localReq(), sess, PurposeApprove, "dig")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.FinishSSHLogin(ctx, localReq(), sv.ID, h.key.sign(t, StepupNamespace, sv.Text)); err == nil {
		t.Error("step-up challenge finished as login")
	}
	if err := h.m.ApproveSSH(ctx, localReq(), sv.Code, h.key.sign(t, StepupNamespace, sv.Text)); err != nil {
		t.Fatal(err)
	}
	if _, done, _ := h.m.PollSSHLogin(localReq(), sv.ID); done {
		t.Error("approved step-up polled as login")
	}
	if _, _, err := h.m.PollSSHStepup(localReq(), Session{ID: "s2", Operator: "ops"}, sv.ID); err == nil {
		t.Error("approved step-up polled by another session")
	}
	tok, done, err := h.m.PollSSHStepup(localReq(), sess, sv.ID)
	if !done || err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.RequireStepup(sess, PurposePurchase, "dig", tok); err == nil {
		t.Error("token for approve accepted for purchase")
	}
	_ = h.m.SetPassword("ops", "correct horse battery")
	secret := setTOTP(t, h.m, "ops")
	h.clk.advance(90 * time.Second)
	code := codeAt(t, secret, h.clk.now())
	if err := h.m.PasswordLogin("ops", "correct horse battery", code); err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.TOTPStepup(sess, PurposeApprove, "dig", code); err == nil {
		t.Error("sign-in TOTP code reused for step-up")
	}
}
