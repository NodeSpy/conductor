package sourcekit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVerifyHMAC(t *testing.T) {
	secret, body := "s3cret", []byte(`{"a":1}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	good := hex.EncodeToString(mac.Sum(nil))

	if !VerifyHMAC(secret, body, good) {
		t.Fatal("valid hex signature rejected")
	}
	if !VerifyHMAC(secret, body, "sha256="+good) {
		t.Fatal("valid sha256=-prefixed signature rejected")
	}
	if VerifyHMAC(secret, body, "deadbeef") {
		t.Fatal("bad signature accepted")
	}
	if VerifyHMAC(secret, body, "not-hex!!") {
		t.Fatal("non-hex signature accepted")
	}
	if !VerifyHMAC("", body, "anything") {
		t.Fatal("empty secret should disable verification")
	}
}

func TestDedup(t *testing.T) {
	d := NewDedup(2)
	if !d.Add("a") || !d.Add("b") {
		t.Fatal("new keys should be accepted")
	}
	if d.Add("a") {
		t.Fatal("duplicate key should be rejected")
	}
	// Adding a third evicts the oldest ("a"), so "a" is new again.
	d.Add("c")
	if !d.Add("a") {
		t.Fatal("evicted key should be new again")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	// Bind :0 to grab a free port, then hand the address to the Listener.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()
	return addr
}

func TestServeReqHTTP(t *testing.T) {
	addr := freeAddr(t)
	got := make(chan *Request, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln := Listener{Addr: addr, Path: "/hook"}
	go func() { _ = ln.ServeReq(ctx, func(r *Request) { got <- r }) }()
	waitReady(t, addr)

	url := "http://" + addr + "/hook?token=sekret&tag=x"
	if !postOK(t, url, `{"event":"ping"}`, nil) {
		t.Fatal("POST not accepted")
	}
	select {
	case r := <-got:
		if string(r.Body) != `{"event":"ping"}` {
			t.Fatalf("body = %q", r.Body)
		}
		if r.Query.Get("token") != "sekret" || r.Query.Get("tag") != "x" {
			t.Fatalf("query not delivered: %v", r.Query)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never fired")
	}
}

func TestServeReqHTTPRejectsBadSignature(t *testing.T) {
	addr := freeAddr(t)
	fired := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln := Listener{Addr: addr, Path: "/hook", Secret: "s3cret", SigHeader: "X-Sig"}
	go func() { _ = ln.ServeReq(ctx, func(r *Request) { fired <- struct{}{} }) }()
	waitReady(t, addr)

	// Wrong signature → 401, handler must not fire.
	if postOK(t, "http://"+addr+"/hook", `{}`, map[string]string{"X-Sig": "deadbeef"}) {
		t.Fatal("bad signature was accepted (expected non-2xx)")
	}
	select {
	case <-fired:
		t.Fatal("handler fired on bad signature")
	case <-time.After(300 * time.Millisecond):
	}
}

func TestServeBackCompat(t *testing.T) {
	addr := freeAddr(t)
	type hb struct {
		h http.Header
		b []byte
	}
	got := make(chan hb, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln := Listener{Addr: addr, Path: "/x"}
	// Old (header, body) callback must still work.
	go func() { _ = ln.Serve(ctx, func(h http.Header, b []byte) { got <- hb{h, b} }) }()
	waitReady(t, addr)

	postOK(t, "http://"+addr+"/x", `hi`, map[string]string{"X-Marker": "m"})
	select {
	case v := <-got:
		if string(v.b) != "hi" || v.h.Get("X-Marker") != "m" {
			t.Fatalf("back-compat callback got %q / %v", v.b, v.h)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("back-compat callback never fired")
	}
}

func TestServeReqNeedsAddr(t *testing.T) {
	if err := (Listener{}).ServeReq(context.Background(), func(*Request) {}); err == nil {
		t.Fatal("expected error when Addr is not set")
	}
}

// --- helpers ---

var httpClient = &http.Client{Timeout: 2 * time.Second}

func postOK(t *testing.T, url, body string, headers map[string]string) bool {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

// waitReady blocks until the listener at addr accepts TCP connections, so a test
// doesn't race the server goroutine's startup. It uses a bare dial (not a POST)
// so it never invokes the request handler and thus can't perturb test state.
func waitReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			c.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("listener at %s never became ready", addr)
}
