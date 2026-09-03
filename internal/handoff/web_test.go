package handoff

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestWebChannelReviseFlow drives the web channel end-to-end over httptest: a
// draft is presented, the page renders its body, a POST revise resolves the
// Await, and the returned Decision carries the edited text.
func TestWebChannelReviseFlow(t *testing.T) {
	c := NewWebChannel("http://example.test", 0, nil)
	srv := httptest.NewServer(c)
	defer srv.Close()
	// Point the link base at the test server so Ref is fetchable.
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pres, err := c.Present(ctx, Draft{Title: "Review o/r#1", Body: "proposed review text", Repo: "o/r", Number: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pres.Ref(), srv.URL+"/handoff?id=") {
		t.Fatalf("Ref should be a page link, got %q", pres.Ref())
	}

	// GET the page: the draft body must be rendered into the textarea.
	resp, err := http.Get(pres.Ref())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET draft page: status %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "proposed review text") {
		t.Fatalf("draft page missing body:\n%s", body)
	}

	// Await in the background; a POST revise should resolve it.
	got := make(chan Decision, 1)
	go func() {
		d, aerr := pres.Await(ctx)
		if aerr != nil {
			t.Errorf("Await: %v", aerr)
		}
		got <- d
	}()

	// Give Await a moment to start selecting.
	time.Sleep(20 * time.Millisecond)
	form := url.Values{"action": {"revise"}, "text": {"tighten the second paragraph"}}
	pr, err := http.PostForm(pres.Ref(), form)
	if err != nil {
		t.Fatal(err)
	}
	pr.Body.Close()
	if pr.StatusCode != http.StatusOK {
		t.Fatalf("POST decision: status %d", pr.StatusCode)
	}

	select {
	case d := <-got:
		if d.Action != ActionRevise || d.Text != "tighten the second paragraph" {
			t.Fatalf("unexpected decision: %+v", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Await never resolved after POST")
	}

	// The draft is consumed: a second GET 404s.
	resp2, err := http.Get(pres.Ref())
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("decided draft should 404, got %d", resp2.StatusCode)
	}
}

func TestWebChannelApproveAndDiscard(t *testing.T) {
	c := NewWebChannel("http://example.test", 0, nil)
	srv := httptest.NewServer(c)
	defer srv.Close()
	c.baseURL = srv.URL
	ctx := context.Background()

	for _, action := range []string{ActionApprove, ActionDiscard} {
		pres, err := c.Present(ctx, Draft{Title: "t", Body: "b"})
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan Decision, 1)
		go func() { d, _ := pres.Await(ctx); done <- d }()
		time.Sleep(10 * time.Millisecond)
		r, err := http.PostForm(pres.Ref(), url.Values{"action": {action}})
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		select {
		case d := <-done:
			if d.Action != action {
				t.Fatalf("want %s, got %s", action, d.Action)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s never resolved", action)
		}
	}
}

func TestWebChannelBadRequests(t *testing.T) {
	c := NewWebChannel("http://example.test", 0, nil)
	srv := httptest.NewServer(c)
	defer srv.Close()

	// Missing id.
	r1, _ := http.Get(srv.URL + "/handoff")
	if r1.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing id should be 400, got %d", r1.StatusCode)
	}
	r1.Body.Close()

	// Unknown id.
	r2, _ := http.Get(srv.URL + "/handoff?id=nope")
	if r2.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id should be 404, got %d", r2.StatusCode)
	}
	r2.Body.Close()
}

// TestWebChannelTokenIsCryptoRandomAndUnique asserts the draft id is no longer a
// guessable sequential counter: many ids in a row must be unique, URL-safe
// (base64url — no "+"/"/"), and not follow any predictable sequence like "h1",
// "h2", ...
func TestWebChannelTokenIsCryptoRandomAndUnique(t *testing.T) {
	c := NewWebChannel("http://example.test", 0, nil)
	ctx := context.Background()

	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		pres, err := c.Present(ctx, Draft{Title: "t", Body: "b"})
		if err != nil {
			t.Fatal(err)
		}
		id := strings.TrimPrefix(pres.Ref(), "http://example.test/handoff?id=")
		if id == "" {
			t.Fatal("empty id")
		}
		if seen[id] {
			t.Fatalf("duplicate id generated: %q", id)
		}
		seen[id] = true
		if strings.HasPrefix(id, "h") && !strings.ContainsAny(id, "+/") {
			// "h<n>" was the old sequential format; a 24-byte base64url token is
			// ~32 chars and starting with "h" by chance is fine, but it must not
			// match the old exact counter shape.
			if id == fmt.Sprintf("h%d", i+1) {
				t.Fatalf("id looks like the old sequential counter: %q", id)
			}
		}
		if strings.ContainsAny(id, "+/=") {
			t.Fatalf("id must be URL-safe (base64url, no padding), got %q", id)
		}
		// 24 raw bytes base64url (no padding) encodes to 32 characters.
		if len(id) != 32 {
			t.Fatalf("expected a 32-char token (24 raw bytes, base64url), got %d chars: %q", len(id), id)
		}
	}
}

// TestWebChannelExplicitIDKept confirms a caller-supplied Draft.ID is honored
// verbatim (only an empty ID triggers token generation).
func TestWebChannelExplicitIDKept(t *testing.T) {
	c := NewWebChannel("http://example.test", 0, nil)
	pres, err := c.Present(context.Background(), Draft{ID: "custom-id", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if pres.Ref() != "http://example.test/handoff?id=custom-id" {
		t.Fatalf("explicit id should be kept verbatim, got ref %q", pres.Ref())
	}
}

// TestWebChannelTTLExpiryUnblocksAwaitAnd404s uses a short explicit ttl (rather
// than the 30m default) so the test is fast and deterministic: past the
// deadline, the page 404s and a blocked Await returns rather than hanging.
func TestWebChannelTTLExpiryUnblocksAwaitAnd404s(t *testing.T) {
	ttl := 40 * time.Millisecond
	c := NewWebChannel("http://example.test", ttl, nil)
	srv := httptest.NewServer(c)
	defer srv.Close()
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pres, err := c.Present(ctx, Draft{Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan error, 1)
	go func() {
		_, aerr := pres.Await(ctx)
		got <- aerr
	}()

	select {
	case err := <-got:
		if err == nil {
			t.Fatal("Await should return an error once the draft expires")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Await never unblocked on TTL expiry")
	}

	// The page itself must also report the draft gone (404), not linger.
	resp, err := http.Get(pres.Ref())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expired draft should 404, got %d", resp.StatusCode)
	}
}

// TestWebChannelDefaultTTLApplied confirms a zero/negative ttl argument falls
// back to the package default rather than a zero (never-expiring) deadline.
func TestWebChannelDefaultTTLApplied(t *testing.T) {
	c := NewWebChannel("http://example.test", 0, nil)
	if c.ttl != defaultTTL {
		t.Fatalf("zero ttl should default to %s, got %s", defaultTTL, c.ttl)
	}
	c2 := NewWebChannel("http://example.test", -time.Second, nil)
	if c2.ttl != defaultTTL {
		t.Fatalf("negative ttl should default to %s, got %s", defaultTTL, c2.ttl)
	}
	c3 := NewWebChannel("http://example.test", 5*time.Minute, nil)
	if c3.ttl != 5*time.Minute {
		t.Fatalf("explicit ttl should be kept, got %s", c3.ttl)
	}
}
