package connector

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/secrets"
)

// buildSinkRegistry parses a connectors: YAML block and builds the registry.
func buildSinkRegistry(t *testing.T, y string) *Registry {
	t.Helper()
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatal(err)
	}
	reg, err := Build(&cfg, Deps{Secrets: secrets.New(), Config: &cfg})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

type sinkCapture struct {
	mu     sync.Mutex
	path   string
	body   string
	title  string
	apiKey string
	ctype  string
}

func sinkServer(c *sinkCapture) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.path, c.body = r.URL.Path, string(b)
		c.title = r.Header.Get("Title")
		c.apiKey = r.Header.Get("X-Api-Key")
		c.ctype = r.Header.Get("Content-Type")
		c.mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
}

func TestNtfyPublish(t *testing.T) {
	var cap sinkCapture
	srv := sinkServer(&cap)
	defer srv.Close()
	reg := buildSinkRegistry(t, "connectors:\n  n: { type: ntfy, server: "+srv.URL+", topic: alerts }\n")
	in, _ := reg.Get("n")
	out, err := in.Invoke(context.Background(), "publish", map[string]any{"title": "conductor", "message": "hello"})
	if err != nil || out["ok"] != true {
		t.Fatalf("publish: %v %v", out, err)
	}
	if cap.path != "/alerts" || cap.body != "hello" || cap.title != "conductor" {
		t.Fatalf("capture: path=%s body=%s title=%s", cap.path, cap.body, cap.title)
	}
	// Per-call topic override.
	if _, err := in.Invoke(context.Background(), "publish", map[string]any{"topic": "other", "message": "x"}); err != nil {
		t.Fatal(err)
	}
	if cap.path != "/other" {
		t.Fatalf("topic override: %s", cap.path)
	}
	// No topic anywhere: a clear error.
	reg2 := buildSinkRegistry(t, "connectors:\n  n: { type: ntfy, server: "+srv.URL+" }\n")
	in2, _ := reg2.Get("n")
	if _, err := in2.Invoke(context.Background(), "publish", map[string]any{"message": "x"}); err == nil || !strings.Contains(err.Error(), "no topic") {
		t.Fatalf("want no-topic error, got %v", err)
	}
}

func TestPushoverNotify(t *testing.T) {
	var cap sinkCapture
	srv := sinkServer(&cap)
	defer srv.Close()
	t.Setenv("PC_PUSHOVER_URL", srv.URL+"/msg")
	reg := buildSinkRegistry(t, "connectors:\n  p: { type: pushover, token: tok1, user: usr1 }\n")
	in, _ := reg.Get("p")
	if _, err := in.Invoke(context.Background(), "notify", map[string]any{"message": "hi there"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.body, "token=tok1") || !strings.Contains(cap.body, "user=usr1") || !strings.Contains(cap.body, "message=hi+there") {
		t.Fatalf("form: %s", cap.body)
	}
	if cap.ctype != "application/x-www-form-urlencoded" {
		t.Fatalf("content type: %s", cap.ctype)
	}
	// Missing creds → disabled connector via Validate.
	reg2 := buildSinkRegistry(t, "connectors:\n  p: { type: pushover }\n")
	in2, _ := reg2.Get("p")
	if in2.DisabledReason == "" {
		t.Fatal("token/user-less pushover should be disabled")
	}
}

func TestNotifiarrNotify(t *testing.T) {
	var cap sinkCapture
	srv := sinkServer(&cap)
	defer srv.Close()
	t.Setenv("PC_NOTIFIARR_URL", srv.URL)
	reg := buildSinkRegistry(t, "connectors:\n  nf: { type: notifiarr, api_key: key9, channel_id: \"42\" }\n")
	in, _ := reg.Get("nf")
	if _, err := in.Invoke(context.Background(), "notify", map[string]any{"text": "alert body"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cap.path, "/passthrough/key9") || cap.apiKey != "key9" {
		t.Fatalf("endpoint/key: %s %s", cap.path, cap.apiKey)
	}
	for _, want := range []string{`"name":"conductor"`, `"description":"alert body"`, `"channel":"42"`} {
		if !strings.Contains(cap.body, want) {
			t.Fatalf("payload missing %s: %s", want, cap.body)
		}
	}
}

func TestSlackWebhookOnlyPost(t *testing.T) {
	var cap sinkCapture
	srv := sinkServer(&cap)
	defer srv.Close()
	reg := buildSinkRegistry(t, "connectors:\n  s: { type: slack, webhook_url: "+srv.URL+"/hook }\n")
	in, _ := reg.Get("s")
	if in.DisabledReason != "" {
		t.Fatalf("webhook-only slack should validate: %s", in.DisabledReason)
	}
	if _, err := in.Invoke(context.Background(), "post", map[string]any{"text": "conductor [escalate] x"}); err != nil {
		t.Fatal(err)
	}
	if cap.body != `{"text":"conductor [escalate] x"}` {
		t.Fatalf("webhook payload: %s", cap.body)
	}
	// react/ask need the bot token.
	if _, err := in.Invoke(context.Background(), "react", map[string]any{"channel": "C", "ts": "1", "emoji": "x"}); err == nil || !strings.Contains(err.Error(), "bot_token") {
		t.Fatalf("react without bot_token: %v", err)
	}
	if _, err := in.Invoke(context.Background(), "ask", map[string]any{"prompt": "p", "to": "dm", "user": "U"}); err == nil || !strings.Contains(err.Error(), "bot_token") {
		t.Fatalf("ask without bot_token: %v", err)
	}
}

func TestDiscordWebhookOnlyPost(t *testing.T) {
	var cap sinkCapture
	srv := sinkServer(&cap)
	defer srv.Close()
	reg := buildSinkRegistry(t, "connectors:\n  d: { type: discord, webhook_url: "+srv.URL+"/hook }\n")
	in, _ := reg.Get("d")
	if in.DisabledReason != "" {
		t.Fatalf("webhook-only discord should validate: %s", in.DisabledReason)
	}
	if _, err := in.Invoke(context.Background(), "post", map[string]any{"text": "conductor msg"}); err != nil {
		t.Fatal(err)
	}
	if cap.body != `{"content":"conductor msg"}` {
		t.Fatalf("webhook payload: %s", cap.body)
	}
}
