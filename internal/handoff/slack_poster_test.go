package handoff

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// setSlackAPIURL points WebAPIPoster's endpoints at base (an httptest.Server
// URL) for the duration of a test, mirroring internal/notify's
// pushoverURL-swap pattern (PC_SLACK_API_URL is read once at init(), too late
// for a per-test env override). Returns a func restoring the real endpoints.
func setSlackAPIURL(base string) func() {
	origPost, origOpen := slackPostMessageURL, slackConversationsOpenURL
	base = strings.TrimRight(base, "/")
	slackPostMessageURL = base + "/chat.postMessage"
	slackConversationsOpenURL = base + "/conversations.open"
	return func() {
		slackPostMessageURL, slackConversationsOpenURL = origPost, origOpen
	}
}

func TestWebAPIPosterPost(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"ok":true,"ts":"123.456"}`)
	}))
	defer srv.Close()
	defer setSlackAPIURL(srv.URL)()

	p := NewWebAPIPoster("xoxb-secret")
	ts, err := p.Post(context.Background(), "C123", "999.1", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if ts != "123.456" {
		t.Fatalf("unexpected ts %q", ts)
	}
	if gotAuth != "Bearer xoxb-secret" {
		t.Fatalf("unexpected Authorization header %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"thread_ts":"999.1"`) {
		t.Fatalf("expected thread_ts in body, got %q", gotBody)
	}
}

func TestWebAPIPosterPostNoThread(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		if strings.Contains(string(buf), "thread_ts") {
			t.Fatalf("a DM/non-threaded post must not include thread_ts, got %q", string(buf))
		}
		fmt.Fprint(w, `{"ok":true,"ts":"1.1"}`)
	}))
	defer srv.Close()
	defer setSlackAPIURL(srv.URL)()

	p := NewWebAPIPoster("xoxb-secret")
	if _, err := p.Post(context.Background(), "D999", "", "hi"); err != nil {
		t.Fatal(err)
	}
}

func TestWebAPIPosterPostError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"error":"channel_not_found"}`)
	}))
	defer srv.Close()
	defer setSlackAPIURL(srv.URL)()

	p := NewWebAPIPoster("xoxb-secret")
	if _, err := p.Post(context.Background(), "Cbogus", "", "hi"); err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("expected a channel_not_found error, got %v", err)
	}
}

func TestWebAPIPosterOpenDM(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/conversations.open" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		fmt.Fprint(w, `{"ok":true,"channel":{"id":"D42"}}`)
	}))
	defer srv.Close()
	defer setSlackAPIURL(srv.URL)()

	p := NewWebAPIPoster("xoxb-secret")
	ch, err := p.OpenDM(context.Background(), "U123")
	if err != nil {
		t.Fatal(err)
	}
	if ch != "D42" {
		t.Fatalf("unexpected channel %q", ch)
	}
	if !strings.Contains(gotBody, `"users":"U123"`) {
		t.Fatalf("expected users in body, got %q", gotBody)
	}
}

func TestWebAPIPosterOpenDMError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"error":"user_not_found"}`)
	}))
	defer srv.Close()
	defer setSlackAPIURL(srv.URL)()

	p := NewWebAPIPoster("xoxb-secret")
	if _, err := p.OpenDM(context.Background(), "Ubogus"); err == nil || !strings.Contains(err.Error(), "user_not_found") {
		t.Fatalf("expected a user_not_found error, got %v", err)
	}
}
