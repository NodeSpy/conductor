package handoff

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// discordGatewayStub serves one Discord-gateway-shaped websocket session:
// HELLO with a tiny heartbeat interval, reads IDENTIFY (asserting the token),
// delivers a MESSAGE_CREATE, waits for a heartbeat frame, then requests a
// reconnect.
func discordGatewayStub(t *testing.T, identified, heartbeats *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		ctx := r.Context()
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"op":10,"d":{"heartbeat_interval":20}}`))
		// IDENTIFY
		if _, data, err := c.Read(ctx); err == nil {
			var f struct {
				Op int `json:"op"`
				D  struct {
					Token string `json:"token"`
				} `json:"d"`
			}
			if json.Unmarshal(data, &f) == nil && f.Op == 2 && f.D.Token == "bot-tok" {
				identified.Add(1)
			}
		}
		_ = c.Write(ctx, websocket.MessageText,
			[]byte(`{"op":0,"t":"MESSAGE_CREATE","s":7,"d":{"channel_id":"D1","content":"approve","author":{"id":"U9"}}}`))
		// Heartbeat (op 1, carrying the seq we just sent).
		if _, data, err := c.Read(ctx); err == nil {
			var f struct {
				Op int             `json:"op"`
				D  json.RawMessage `json:"d"`
			}
			if json.Unmarshal(data, &f) == nil && f.Op == 1 && strings.TrimSpace(string(f.D)) == "7" {
				heartbeats.Add(1)
			}
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"op":7}`))
		// Give the client a moment to read the reconnect frame.
		time.Sleep(50 * time.Millisecond)
	}))
}

func TestRunDiscordGatewaySession(t *testing.T) {
	var identified, heartbeats atomic.Int64
	ws := discordGatewayStub(t, &identified, &heartbeats)
	defer ws.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"url":%q}`, ws.URL)
	}))
	defer api.Close()
	defer setDiscordAPIURL(api.URL)()

	inbox := NewInbox()
	pending := inbox.register("D1", "")

	// One session: identify, deliver, heartbeat, reconnect request.
	err := runDiscordGatewayOnce(context.Background(), "bot-tok", inbox, t.Logf)
	if err == nil || !strings.Contains(err.Error(), "reconnect") {
		t.Fatalf("session should end on op 7, got %v", err)
	}
	if identified.Load() != 1 {
		t.Fatal("IDENTIFY not sent or wrong token")
	}
	if heartbeats.Load() != 1 {
		t.Fatal("heartbeat with the tracked sequence not sent")
	}
	select {
	case dec := <-pending.done:
		if dec.Action != ActionApprove {
			t.Fatalf("delivered decision: %+v", dec)
		}
	default:
		t.Fatal("MESSAGE_CREATE was not delivered to the inbox")
	}
}

// TestRunDiscordGatewayLoop: the persistent loop reconnects across sessions
// and stops on cancel.
func TestRunDiscordGatewayLoop(t *testing.T) {
	var identified, heartbeats, sessions atomic.Int64
	ws := discordGatewayStub(t, &identified, &heartbeats)
	defer ws.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions.Add(1)
		fmt.Fprintf(w, `{"url":%q}`, ws.URL)
	}))
	defer api.Close()
	defer setDiscordAPIURL(api.URL)()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { RunDiscordGateway(ctx, "bot-tok", NewInbox(), nil); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for sessions.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gateway loop did not stop on cancel")
	}
	if sessions.Load() < 2 {
		t.Fatal("gateway never reconnected")
	}
}

func TestParseNgrokTunnelsFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"tunnels":[{"public_url":""},{"public_url":"https://x.ngrok.app"}]}`)
	}))
	defer srv.Close()
	url, err := fetchNgrokTunnelURL(srv.URL)
	if err != nil || url != "https://x.ngrok.app" {
		t.Fatalf("fetch: %q %v", url, err)
	}
	if _, err := fetchNgrokTunnelURL("http://127.0.0.1:1/api"); err == nil {
		t.Fatal("unreachable API should error")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `nope`)
	}))
	defer bad.Close()
	if _, err := fetchNgrokTunnelURL(bad.URL); err == nil {
		t.Fatal("bad body should error")
	}
}

func TestNotWiredChannelPresent(t *testing.T) {
	_, err := notWiredChannel{name: "x"}.Present(context.Background(), Draft{})
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("notWired: %v", err)
	}
}
