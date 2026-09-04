package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/handoff"
)

func newSlackTestImpl(t *testing.T, apiBase string) *slackImpl {
	t.Helper()
	t.Setenv("PC_SLACK_API_URL", apiBase)
	impl := &slackImpl{
		name:  "sl",
		conn:  slackConn{AppToken: "xapp-x", BotToken: "xoxb-x"},
		deps:  Deps{},
		api:   newSlackAPI("xoxb-x"),
		inbox: handoff.NewInbox(),
	}
	return impl
}

func TestSlackVerbPostChannel(t *testing.T) {
	var gotMethod string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "111.222"})
	}))
	defer srv.Close()
	impl := newSlackTestImpl(t, srv.URL)
	out, err := impl.Invoke(context.Background(), "post", map[string]any{"channel": "C1", "text": "hi"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if gotMethod != "/chat.postMessage" {
		t.Fatalf("method path = %q", gotMethod)
	}
	if gotBody["channel"] != "C1" || gotBody["text"] != "hi" {
		t.Fatalf("body = %v", gotBody)
	}
	if out["ts"] != "111.222" || out["channel"] != "C1" {
		t.Fatalf("out = %v", out)
	}
}

func TestSlackVerbPostThread(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "222.333"})
	}))
	defer srv.Close()
	impl := newSlackTestImpl(t, srv.URL)
	_, err := impl.Invoke(context.Background(), "post", map[string]any{"channel": "C1", "text": "hi", "thread_ts": "111.222"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if gotBody["thread_ts"] != "111.222" {
		t.Fatalf("body = %v", gotBody)
	}
}

func TestSlackVerbPostDM(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/conversations.open":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": map[string]any{"id": "D1"}})
		case "/chat.postMessage":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "333.444"})
		}
	}))
	defer srv.Close()
	impl := newSlackTestImpl(t, srv.URL)
	out, err := impl.Invoke(context.Background(), "post", map[string]any{"user": "U1", "text": "hi"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out["channel"] != "D1" {
		t.Fatalf("out = %v", out)
	}
	if len(paths) != 2 || paths[0] != "/conversations.open" || paths[1] != "/chat.postMessage" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestSlackVerbPostEphemeral(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()
	impl := newSlackTestImpl(t, srv.URL)
	out, err := impl.Invoke(context.Background(), "post", map[string]any{
		"channel": "C1", "user": "U1", "text": "hi", "ephemeral": true,
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if gotPath != "/chat.postEphemeral" {
		t.Fatalf("path = %q", gotPath)
	}
	if out["channel"] != "C1" {
		t.Fatalf("out = %v", out)
	}
}

func TestSlackVerbReact(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()
	impl := newSlackTestImpl(t, srv.URL)
	out, err := impl.Invoke(context.Background(), "react", map[string]any{"channel": "C1", "ts": "1.1", "emoji": ":+1:"})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if gotBody["name"] != "+1" {
		t.Fatalf("emoji not trimmed of colons: %v", gotBody)
	}
	if out["ok"] != true {
		t.Fatalf("out = %v", out)
	}
}

func TestSlackVerbErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "channel_not_found"})
	}))
	defer srv.Close()
	impl := newSlackTestImpl(t, srv.URL)
	_, err := impl.Invoke(context.Background(), "post", map[string]any{"channel": "bogus", "text": "hi"})
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("got %v, want error mentioning channel_not_found", err)
	}
}

func TestSlackFilterTable(t *testing.T) {
	cases := []struct {
		name    string
		event   string
		filters map[string]any
		ctx     map[string]any
		want    bool
	}{
		{
			name: "channel match", event: "app_mention",
			filters: map[string]any{"channel": "C1"},
			ctx:     map[string]any{"slack": map[string]any{"channel": "C1"}},
			want:    true,
		},
		{
			name: "channel mismatch", event: "app_mention",
			filters: map[string]any{"channel": "C1"},
			ctx:     map[string]any{"slack": map[string]any{"channel": "C2"}},
			want:    false,
		},
		{
			name: "users match case-insensitive", event: "app_mention",
			filters: map[string]any{"users": []any{"U1"}},
			ctx:     map[string]any{"slack": map[string]any{"user": "u1"}},
			want:    true,
		},
		{
			name: "users no match", event: "app_mention",
			filters: map[string]any{"users": []any{"U1"}},
			ctx:     map[string]any{"slack": map[string]any{"user": "U2"}},
			want:    false,
		},
		{
			name: "reaction match", event: "reaction_added",
			filters: map[string]any{"reaction": "tada"},
			ctx:     map[string]any{"slack": map[string]any{"reaction": "tada"}},
			want:    true,
		},
		{
			name: "reaction mismatch", event: "reaction_added",
			filters: map[string]any{"reaction": "tada"},
			ctx:     map[string]any{"slack": map[string]any{"reaction": "eyes"}},
			want:    false,
		},
		{
			name: "command match", event: "slash_command",
			filters: map[string]any{"command": "/fix"},
			ctx:     map[string]any{"slack": map[string]any{"command": "/fix"}},
			want:    true,
		},
		{
			name: "command mismatch", event: "slash_command",
			filters: map[string]any{"command": "/fix"},
			ctx:     map[string]any{"slack": map[string]any{"command": "/other"}},
			want:    false,
		},
		{
			name: "no filters passes", event: "app_mention",
			filters: map[string]any{},
			ctx:     map[string]any{"slack": map[string]any{"channel": "anything"}},
			want:    true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := slackFilter(c.event, c.filters, c.ctx)
			if err != nil {
				t.Fatalf("slackFilter: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestSlackAskThread(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/chat.postMessage":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "999.111"})
		}
	}))
	defer srv.Close()
	impl := newSlackTestImpl(t, srv.URL)

	type result struct {
		out map[string]any
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := impl.Invoke(context.Background(), "ask", map[string]any{
			"to": "thread", "channel": "C1", "prompt": "ok?", "timeout": "5s",
		})
		done <- result{out, err}
	}()

	// wait for the draft to post and register in the inbox before delivering.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if impl.inbox.Deliver("C1", "999.111", "approve") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the ask to register on the inbox")
		}
		time.Sleep(5 * time.Millisecond)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("ask: %v", r.err)
	}
	if r.out["action"] != "approve" {
		t.Fatalf("out = %v", r.out)
	}
}

func TestSlackAskDM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.open":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": map[string]any{"id": "D1"}})
		case "/chat.postMessage":
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": "1.1"})
		}
	}))
	defer srv.Close()
	impl := newSlackTestImpl(t, srv.URL)

	type result struct {
		out map[string]any
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := impl.Invoke(context.Background(), "ask", map[string]any{
			"to": "dm", "user": "U1", "prompt": "ok?", "timeout": "5s",
		})
		done <- result{out, err}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if impl.inbox.Deliver("D1", "", "approve") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the ask to register on the inbox")
		}
		time.Sleep(5 * time.Millisecond)
	}

	r := <-done
	if r.err != nil {
		t.Fatalf("ask: %v", r.err)
	}
	if r.out["action"] != "approve" {
		t.Fatalf("out = %v", r.out)
	}
}

func TestSlackAskMissingToErrors(t *testing.T) {
	impl := newSlackTestImpl(t, "http://unused")
	_, err := impl.Invoke(context.Background(), "ask", map[string]any{"prompt": "ok?"})
	if err == nil || !strings.Contains(err.Error(), "must be dm|thread") {
		t.Fatalf("got %v", err)
	}
}
