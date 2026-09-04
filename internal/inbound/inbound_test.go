package inbound

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
)

func hexSig(secret, body string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func TestVerifyHMAC(t *testing.T) {
	secret, body := "s3cr3t", `{"a":1}`
	h := hexSig(secret, body)
	if !VerifyHMAC(secret, []byte(body), h, "") {
		t.Fatal("bare hex should verify")
	}
	if !VerifyHMAC(secret, []byte(body), "sha256="+h, "") {
		t.Fatal("sha256= prefixed hex should verify")
	}
	b64 := base64.StdEncoding.EncodeToString(mustHex(t, h))
	if !VerifyHMAC(secret, []byte(body), b64, "base64") {
		t.Fatal("base64 scheme should verify")
	}
	if VerifyHMAC(secret, []byte(body), hexSig("wrong", body), "") {
		t.Fatal("wrong secret must not verify")
	}
	if !VerifyHMAC("", []byte(body), "anything", "") {
		t.Fatal("empty secret means no verification (true)")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseSmeeFrame(t *testing.T) {
	// Object body: headers come from scalar keys, body is the re-serialized object.
	f, ok := parseSmeeFrame(`{"x-source":"cloudwatch","content-type":"application/json","body":{"alarm":"cpu"}}`)
	if !ok {
		t.Fatal("object-body frame should parse")
	}
	if f.Header("X-Source") != "cloudwatch" {
		t.Fatalf("header lookup case-insensitive failed: %+v", f.Headers)
	}
	if string(f.Body) != `{"alarm":"cpu"}` {
		t.Fatalf("body not preserved: %s", f.Body)
	}
	// String body.
	f2, ok := parseSmeeFrame(`{"body":"raw text"}`)
	if !ok || string(f2.Body) != "raw text" {
		t.Fatalf("string body should unwrap, got %q ok=%v", f2.Body, ok)
	}
	// Control frame with no body is skipped.
	if _, ok := parseSmeeFrame(`{"message":"ready"}`); ok {
		t.Fatal("bodyless control frame should be skipped")
	}
}

func TestSyntheticTargetStable(t *testing.T) {
	a := SyntheticTarget("rss:changelog", "guid-123")
	b := SyntheticTarget("rss:changelog", "guid-123")
	if a.Repo != "rss:changelog" || a.Number == 0 {
		t.Fatalf("unexpected target: %+v", a)
	}
	if a.Number != b.Number {
		t.Fatal("numID must be stable for the same dedup string")
	}
	if SyntheticTarget("rss:changelog", "guid-456").Number == a.Number {
		t.Fatal("different dedup strings should (almost always) differ")
	}
}

func TestForceNoCheckout(t *testing.T) {
	if got := ForceNoCheckout(config.Action{}); got.Checkout != "none" {
		t.Fatalf("empty checkout should default to none, got %q", got.Checkout)
	}
	if got := ForceNoCheckout(config.Action{Checkout: "branch-off"}); got.Checkout != "branch-off" {
		t.Fatalf("explicit checkout should be respected, got %q", got.Checkout)
	}
}

func TestListenerRegisterAndDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const addr = "127.0.0.1:38251" // fixed high port so the test client has a known URL
	got := make(chan string, 1)
	Register(ctx, addr, "/hook/a", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
		w.WriteHeader(http.StatusAccepted)
	}), t.Logf)

	// Give ListenAndServe a moment to bind.
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	var err error
	for time.Now().Before(deadline) {
		resp, err = http.Post("http://"+addr+"/hook/a", "application/json", stringReader(`{"ok":true}`))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("post never succeeded: %v", err)
	}
	resp.Body.Close()
	select {
	case b := <-got:
		if b != `{"ok":true}` {
			t.Fatalf("handler got wrong body: %s", b)
		}
	case <-time.After(time.Second):
		t.Fatal("handler never fired")
	}
	// Unregistered path 404s.
	r2, err := http.Post("http://"+addr+"/nope", "application/json", stringReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("unregistered path should 404, got %d", r2.StatusCode)
	}
}

type sr struct{ s string }

func (r *sr) Read(p []byte) (int, error) {
	n := copy(p, r.s)
	r.s = r.s[n:]
	if len(r.s) == 0 {
		return n, io.EOF
	}
	return n, nil
}
func stringReader(s string) io.Reader { return &sr{s} }
