package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// captureNotifier returns a notifier whose log lines are collected.
func captureNotifier(on []string) (*Notifier, *[]string) {
	var lines []string
	n := New(config.Notify{On: on}, func(f string, a ...any) {
		lines = append(lines, fmt.Sprintf(f, a...))
	}, nil)
	return n, &lines
}

func TestEmitLogsNeverComments(t *testing.T) {
	n, lines := captureNotifier([]string{"escalate", "needs_input"})
	tr := core.Trigger{Kind: "review_requested", Target: core.Target{Repo: "acme/w", PR: 3, Number: 3}}

	// A non-listed event is silent.
	n.Emit(context.Background(), EventDispatch, tr, "x")
	if len(*lines) != 0 {
		t.Fatalf("dispatch not in policy; should be silent, got %v", *lines)
	}

	// Attention events log a private, actionable line (no PR comment path exists).
	n.Emit(context.Background(), EventNeedsInput, tr, "agent live")
	n.Emit(context.Background(), EventEscalate, tr, "cap reached")
	if len(*lines) != 2 {
		t.Fatalf("want 2 log lines, got %d: %v", len(*lines), *lines)
	}
	for _, l := range *lines {
		if !strings.Contains(l, "acme/w#3") || !strings.Contains(l, "open paseo") {
			t.Fatalf("log line missing ref/hint: %q", l)
		}
	}
}

func TestNotifyPolicyGate(t *testing.T) {
	n := config.Notify{On: []string{"escalate", "dispatch"}}
	if !n.Wants("dispatch") || !n.Wants("escalate") {
		t.Fatal("listed events should be wanted")
	}
	if n.Wants("complete") {
		t.Fatal("unlisted event should not be wanted")
	}
}

func TestDigestPostsAndAudits(t *testing.T) {
	posted := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posted <- string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var audited []map[string]any
	n := New(config.Notify{SlackWebhookURL: srv.URL}, func(string, ...any) {},
		func(e map[string]any) { audited = append(audited, e) })
	n.Digest(context.Background(), "last 24h — dispatched 5 (ok 4, failed 1)")

	select {
	case body := <-posted:
		if !strings.Contains(body, "digest") || !strings.Contains(body, "dispatched 5") {
			t.Fatalf("digest slack payload wrong: %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("digest was never posted to slack")
	}
	if len(audited) != 1 || audited[0]["event"] != "digest" {
		t.Fatalf("digest should write one audit row, got %+v", audited)
	}
}

func TestSlackSinkPosts(t *testing.T) {
	posted := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posted <- string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(config.Notify{On: []string{"escalate"}, SlackWebhookURL: srv.URL}, func(string, ...any) {}, nil)
	tr := core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/w", Number: 7}}
	n.Emit(context.Background(), EventEscalate, tr, "gave up")

	select {
	case body := <-posted:
		if !strings.Contains(body, "escalate") || !strings.Contains(body, "acme/w#7") {
			t.Fatalf("slack payload missing expected content: %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slack webhook was never posted")
	}

	// A non-wanted event posts nothing.
	n.Emit(context.Background(), EventComplete, tr, "done")
	select {
	case body := <-posted:
		t.Fatalf("unwanted event should not post, got: %s", body)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestDiscordSinkPosts(t *testing.T) {
	posted := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posted <- string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(config.Notify{On: []string{"escalate"}, DiscordWebhookURL: srv.URL}, func(string, ...any) {}, nil)
	tr := core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/w", Number: 9}}
	n.Emit(context.Background(), EventEscalate, tr, "gave up")

	select {
	case body := <-posted:
		if !strings.Contains(body, "escalate") || !strings.Contains(body, "acme/w#9") {
			t.Fatalf("discord payload missing expected content: %s", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("discord webhook was never posted")
	}
}

func TestNtfySinkPublishes(t *testing.T) {
	posted := make(chan struct {
		body  string
		title string
		path  string
	}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posted <- struct {
			body  string
			title string
			path  string
		}{string(b), r.Header.Get("Title"), r.URL.Path}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := New(config.Notify{On: []string{"escalate"}, Ntfy: config.NotifyNtfy{Server: srv.URL, Topic: "conductor"}},
		func(string, ...any) {}, nil)
	tr := core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/w", Number: 11}}
	n.Emit(context.Background(), EventEscalate, tr, "gave up")

	select {
	case got := <-posted:
		if got.path != "/conductor" {
			t.Fatalf("ntfy path = %q, want /conductor", got.path)
		}
		if got.title != "conductor" {
			t.Fatalf("ntfy title = %q", got.title)
		}
		if !strings.Contains(got.body, "acme/w#11") {
			t.Fatalf("ntfy body missing expected content: %s", got.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ntfy was never published")
	}
}

func TestPushoverSinkPosts(t *testing.T) {
	posted := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		posted <- r.Form.Get("message")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	orig := pushoverURL
	pushoverURL = srv.URL
	defer func() { pushoverURL = orig }()

	n := New(config.Notify{On: []string{"escalate"}, Pushover: config.NotifyPushover{Token: "tok", User: "usr"}},
		func(string, ...any) {}, nil)
	tr := core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/w", Number: 13}}
	n.Emit(context.Background(), EventEscalate, tr, "gave up")

	select {
	case msg := <-posted:
		if !strings.Contains(msg, "acme/w#13") {
			t.Fatalf("pushover message missing expected content: %s", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pushover was never posted")
	}

	// Missing user/token: no post attempted.
	n2 := New(config.Notify{On: []string{"escalate"}, Pushover: config.NotifyPushover{Token: "tok"}},
		func(string, ...any) {}, nil)
	n2.Emit(context.Background(), EventEscalate, tr, "gave up")
	select {
	case msg := <-posted:
		t.Fatalf("incomplete pushover config should not post, got: %s", msg)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestNotifiarrSinkPosts(t *testing.T) {
	posted := make(chan struct {
		body   string
		apiKey string
		path   string
	}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posted <- struct {
			body   string
			apiKey string
			path   string
		}{string(b), r.Header.Get("X-Api-Key"), r.URL.Path}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	orig := notifiarrURL
	notifiarrURL = srv.URL + "/%s"
	defer func() { notifiarrURL = orig }()

	n := New(config.Notify{On: []string{"escalate"}, Notifiarr: config.NotifyNotifiarr{APIKey: "abc123", ChannelID: "42"}},
		func(string, ...any) {}, nil)
	tr := core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/w", Number: 17}}
	n.Emit(context.Background(), EventEscalate, tr, "gave up")

	select {
	case got := <-posted:
		if got.path != "/abc123" {
			t.Fatalf("notifiarr path = %q, want /abc123", got.path)
		}
		if got.apiKey != "abc123" {
			t.Fatalf("notifiarr X-Api-Key header = %q", got.apiKey)
		}
		if !strings.Contains(got.body, "acme/w#17") || !strings.Contains(got.body, "\"channel\":\"42\"") {
			t.Fatalf("notifiarr payload missing expected content: %s", got.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notifiarr was never posted")
	}
}
