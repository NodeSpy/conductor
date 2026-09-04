package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/core"
	"github.com/coder/websocket"
)

func TestNameAndActions(t *testing.T) {
	g := newTest(t, baseCfg())
	if g.Name() != "test" {
		t.Fatalf("Name = %q", g.Name())
	}
	refs := g.Actions()
	if len(refs) != 3 || !strings.Contains(refs[0].Where, "triggers[0] (app_mention)") {
		t.Fatalf("Actions: %+v", refs)
	}
}

func TestOpenSocket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer xapp-1" {
			t.Errorf("auth header: %q", got)
		}
		fmt.Fprint(w, `{"ok":true,"url":"wss://socket.example/x"}`)
	}))
	defer srv.Close()
	old := connectionsOpenURL
	connectionsOpenURL = srv.URL
	defer func() { connectionsOpenURL = old }()

	g := newTest(t, baseCfg())
	url, err := g.openSocket(context.Background())
	if err != nil || url != "wss://socket.example/x" {
		t.Fatalf("openSocket: %q %v", url, err)
	}

	// Slack-level error surfaces.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false,"error":"invalid_auth"}`)
	}))
	defer bad.Close()
	connectionsOpenURL = bad.URL
	if _, err := g.openSocket(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid_auth") {
		t.Fatalf("api error: %v", err)
	}

	// Unparseable body errors.
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `nope`)
	}))
	defer junk.Close()
	connectionsOpenURL = junk.URL
	if _, err := g.openSocket(context.Background()); err == nil {
		t.Fatal("bad json should error")
	}
}

// socketServer runs a Socket-Mode-shaped websocket endpoint: it sends hello,
// one app_mention events_api envelope (expecting the ACK back), then a
// disconnect frame.
func socketServer(t *testing.T, acked *atomic.Int64) *httptest.Server {
	t.Helper()
	payload := `{"event":{"type":"app_mention","text":"<@U0> fix it","user":"U1","channel":"C1","ts":"1.2"}}`
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"hello"}`))
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"events_api","envelope_id":"env-1","payload":`+payload+`}`))
		// Read the ACK for env-1.
		if _, data, err := c.Read(ctx); err == nil {
			var ack struct {
				EnvelopeID string `json:"envelope_id"`
			}
			if json.Unmarshal(data, &ack) == nil && ack.EnvelopeID == "env-1" {
				acked.Add(1)
			}
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"disconnect","reason":"refresh_requested"}`))
	}))
}

func TestRunOnceSocketSession(t *testing.T) {
	var acked atomic.Int64
	ws := socketServer(t, &acked)
	defer ws.Close()
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"ok":true,"url":%q}`, ws.URL)
	}))
	defer open.Close()
	old := connectionsOpenURL
	connectionsOpenURL = open.URL
	defer func() { connectionsOpenURL = old }()

	g := newTest(t, baseCfg())
	emit, got := collect()
	err := g.runOnce(context.Background(), emit)
	if err == nil || !strings.Contains(err.Error(), "disconnect: refresh_requested") {
		t.Fatalf("session should end on the disconnect frame, got %v", err)
	}
	if len(*got) != 1 || (*got)[0].Kind != "app_mention" {
		t.Fatalf("mention not emitted: %+v", *got)
	}
	if acked.Load() != 1 {
		t.Fatal("envelope was not ACKed")
	}
}

// TestStartReconnects: a failed connect backs off, reconnects, pumps a
// session, and cancel stops the loop.
func TestStartReconnects(t *testing.T) {
	var acked, opens atomic.Int64
	ws := socketServer(t, &acked)
	defer ws.Close()
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opens.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError) // first open fails
			return
		}
		fmt.Fprintf(w, `{"ok":true,"url":%q}`, ws.URL)
	}))
	defer open.Close()
	old := connectionsOpenURL
	connectionsOpenURL = open.URL
	defer func() { connectionsOpenURL = old }()

	g := newTest(t, baseCfg())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	emitted := make(chan core.Trigger, 4)
	done := make(chan error, 1)
	go func() {
		done <- g.Start(ctx, func(_ context.Context, tr core.Trigger) { emitted <- tr })
	}()
	select {
	case <-emitted:
	case <-time.After(10 * time.Second):
		t.Fatal("no trigger after reconnect")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start should return ctx.Err(), got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not stop")
	}
	if opens.Load() < 2 {
		t.Fatalf("expected a reconnect, opens=%d", opens.Load())
	}
}

func TestRenderSay(t *testing.T) {
	data := map[string]any{"user": "U1", "channel": "C1"}
	cases := []struct{ in, want string }{
		{"plain text", "plain text"},
		{"hi <@{{.user}}> in {{.channel}}", "hi <@U1> in C1"},
		{"broken {{.user", "broken {{.user"}, // parse error → literal
		{"{{fail .user}}", "{{fail .user}}"}, // exec error → literal
		{"missing {{.nope}} ok", "missing <no value> ok"},
	}
	for _, c := range cases {
		if got := renderSay(c.in, data); got != c.want {
			t.Errorf("renderSay(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStashPendingEviction(t *testing.T) {
	g := newTest(t, baseCfg())
	say := &Feedback{Say: "done"}
	for i := 0; i <= maxPendingFeedback; i++ {
		g.stashPending(fmt.Sprintf("k%d", i), say, nil, evt{channel: "C"}, nil, 1)
	}
	g.pendingMu.Lock()
	defer g.pendingMu.Unlock()
	if len(g.pending) != maxPendingFeedback {
		t.Fatalf("ring bound: %d", len(g.pending))
	}
	if _, ok := g.pending["k0"]; ok {
		t.Fatal("oldest entry should be evicted")
	}
}

func TestFirstLineAndFirstNonEmpty(t *testing.T) {
	if got := firstLine("  a title\nsecond"); got != "a title" {
		t.Fatalf("firstLine: %q", got)
	}
	if got := firstLine("single"); got != "single" {
		t.Fatalf("firstLine single: %q", got)
	}
	if got := firstNonEmpty("", "", "x", "y"); got != "x" {
		t.Fatalf("firstNonEmpty: %q", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Fatalf("firstNonEmpty empty: %q", got)
	}
}

func TestCallAPILogsSlackError(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, `{"ok":false,"error":"channel_not_found"}`)
	}))
	defer srv.Close()
	g := newTest(t, baseCfg())
	g.callAPI(context.Background(), srv.URL, map[string]string{"channel": "C"})
	if hits.Load() != 1 {
		t.Fatal("api not called")
	}
	// Unreachable endpoint: logged, never panics.
	g.callAPI(context.Background(), "http://127.0.0.1:1/x", map[string]string{})
}
