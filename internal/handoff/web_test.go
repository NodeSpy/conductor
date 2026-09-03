package handoff

import (
	"context"
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
	c := NewWebChannel("http://example.test", nil)
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
	c := NewWebChannel("http://example.test", nil)
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
	c := NewWebChannel("http://example.test", nil)
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
