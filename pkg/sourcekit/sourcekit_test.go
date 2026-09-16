package sourcekit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

func TestParseRelayPayload(t *testing.T) {
	t.Run("body as string", func(t *testing.T) {
		r, ok := parseRelayPayload([]byte(`{"body":"hello","X-Token":"abc"}`))
		if !ok {
			t.Fatal("ok=false for string body")
		}
		if string(r.Body) != "hello" {
			t.Fatalf("body = %q", r.Body)
		}
		if r.Header.Get("x-token") != "abc" {
			t.Fatalf("header lookup (case-insensitive) failed: %v", r.Header)
		}
	})

	t.Run("body as object is re-marshaled", func(t *testing.T) {
		r, ok := parseRelayPayload([]byte(`{"body":{"n":1}}`))
		if !ok {
			t.Fatal("ok=false for object body")
		}
		if string(r.Body) != `{"n":1}` {
			t.Fatalf("body = %q", r.Body)
		}
	})

	t.Run("query passthrough", func(t *testing.T) {
		r, ok := parseRelayPayload([]byte(`{"body":"x","query":{"token":"t","tag":["a","b"]}}`))
		if !ok {
			t.Fatal("ok=false")
		}
		if r.Query.Get("token") != "t" {
			t.Fatalf("query token = %q", r.Query.Get("token"))
		}
		if got := r.Query["tag"]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
			t.Fatalf("query tag = %v", got)
		}
		// query keys must not leak into headers
		if r.Header.Get("query") != "" || r.Header.Get("body") != "" {
			t.Fatalf("structural keys leaked into headers: %v", r.Header)
		}
	})

	t.Run("missing body means keep-alive, not a delivery", func(t *testing.T) {
		if _, ok := parseRelayPayload([]byte(`{"timestamp":123}`)); ok {
			t.Fatal("ok=true for body-less envelope")
		}
		if _, ok := parseRelayPayload([]byte(`{"body":null}`)); ok {
			t.Fatal("ok=true for null body")
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		if _, ok := parseRelayPayload([]byte(`not json`)); ok {
			t.Fatal("ok=true for invalid json")
		}
	})
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

func TestServeReqRelay(t *testing.T) {
	// A fake smee-style SSE relay that emits one keep-alive then one delivery.
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("relay response not flushable")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// keep-alive (no body) — must be ignored
		fmt.Fprint(w, "data: {\"timestamp\":1}\n\n")
		// real delivery with body + query
		fmt.Fprint(w, "data: {\"body\":\"{\\\"ok\\\":true}\",\"query\":{\"token\":\"t\"},\"X-Src\":\"relay\"}\n\n")
		fl.Flush()
		<-r.Context().Done()
	}))
	defer relay.Close()

	got := make(chan *Request, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln := Listener{Relay: relay.URL} // relay-only, no Addr
	go func() { _ = ln.ServeReq(ctx, func(r *Request) { got <- r }) }()

	select {
	case r := <-got:
		if string(r.Body) != `{"ok":true}` {
			t.Fatalf("relayed body = %q", r.Body)
		}
		if r.Query.Get("token") != "t" {
			t.Fatalf("relayed query = %v", r.Query)
		}
		if r.Header.Get("X-Src") != "relay" {
			t.Fatalf("relayed header = %v", r.Header)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay delivery never dispatched")
	}
}

func TestServeReqNeedsAddrOrRelay(t *testing.T) {
	if err := (Listener{}).ServeReq(context.Background(), func(*Request) {}); err == nil {
		t.Fatal("expected error when neither Addr nor Relay is set")
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
