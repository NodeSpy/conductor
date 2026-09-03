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

// setDiscordAPIURL points RESTPoster's endpoint at base (an httptest.Server
// URL) for the duration of a test, mirroring setSlackAPIURL (PC_DISCORD_API_URL
// is read once at init(), too late for a per-test env override). Returns a
// func restoring the real endpoint.
func setDiscordAPIURL(base string) func() {
	orig := discordAPIBase
	discordAPIBase = strings.TrimRight(base, "/")
	return func() { discordAPIBase = orig }
}

func TestRESTPosterPost(t *testing.T) {
	var gotAuth, gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		fmt.Fprint(w, `{"id":"123456"}`)
	}))
	defer srv.Close()
	defer setDiscordAPIURL(srv.URL)()

	p := NewRESTPoster("bot-secret")
	id, err := p.Post(context.Background(), "C123", "", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if id != "123456" {
		t.Fatalf("unexpected id %q", id)
	}
	if gotPath != "/channels/C123/messages" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if gotAuth != "Bot bot-secret" {
		t.Fatalf("unexpected Authorization header %q", gotAuth)
	}
	if !strings.Contains(gotBody, `"content":"hello"`) {
		t.Fatalf("expected content in body, got %q", gotBody)
	}
}

func TestRESTPosterPostError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Missing Access","code":50001}`)
	}))
	defer srv.Close()
	defer setDiscordAPIURL(srv.URL)()

	p := NewRESTPoster("bot-secret")
	if _, err := p.Post(context.Background(), "Cbogus", "", "hi"); err == nil || !strings.Contains(err.Error(), "Missing Access") {
		t.Fatalf("expected a Missing Access error, got %v", err)
	}
}

func TestRESTPosterOpenDM(t *testing.T) {
	var gotBody, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		buf, _ := io.ReadAll(r.Body)
		gotBody = string(buf)
		fmt.Fprint(w, `{"id":"D42"}`)
	}))
	defer srv.Close()
	defer setDiscordAPIURL(srv.URL)()

	p := NewRESTPoster("bot-secret")
	ch, err := p.OpenDM(context.Background(), "U123")
	if err != nil {
		t.Fatal(err)
	}
	if ch != "D42" {
		t.Fatalf("unexpected channel %q", ch)
	}
	if gotPath != "/users/@me/channels" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if !strings.Contains(gotBody, `"recipient_id":"U123"`) {
		t.Fatalf("expected recipient_id in body, got %q", gotBody)
	}
}

func TestRESTPosterOpenDMError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message":"Unknown User","code":10013}`)
	}))
	defer srv.Close()
	defer setDiscordAPIURL(srv.URL)()

	p := NewRESTPoster("bot-secret")
	if _, err := p.OpenDM(context.Background(), "Ubogus"); err == nil || !strings.Contains(err.Error(), "Unknown User") {
		t.Fatalf("expected an Unknown User error, got %v", err)
	}
}
