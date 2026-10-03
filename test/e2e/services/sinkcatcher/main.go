// Command sinkcatcher captures notification posts for the e2e harness
// (test/e2e/). Conductor's notify sinks are pointed at it: Slack/Discord/ntfy via
// config URLs, Pushover/Notifiarr via the PC_PUSHOVER_URL / PC_NOTIFIARR_URL
// testability hooks. Each sink has its own path so group G can assert that every
// channel fired. Captures are queryable at GET /_captured and reset at POST
// /_reset.
//
// It also stands in for Slack's Socket Mode (group K's ack/on_done feedback
// case): /slackapi/apps.connections.open answers like the real endpoint,
// pointing the slack plugin's websocket at /slackapi/socket here; a harness
// POST to /_fire_slack_event relays one events_api envelope to every
// connected socket, so the daemon's `on: slack.<event>` triggers fire
// without a real Slack workspace.
//
// NOT part of the shipped product; harness-only.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

type post struct {
	Sink        string `json:"sink"`
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Body        string `json:"body"`
}

var (
	mu    sync.Mutex
	posts []post
)

// slackAPIMethodsUsed is every Slack Web API method the e2e suite's pinned
// slack plugin (test/e2e/Dockerfile's GITHUB_PLUGIN_VERSION, built from
// conductor-plugins) actually calls across every group K scenario this
// suite drives: chat.postMessage/chat.postEphemeral (the chat verb's post/
// ephemeral options), reactions.add (the feedback verb's react — K10's
// ack/on_done/on_fail), apps.connections.open (Socket Mode's own handshake
// — handled separately, above, but listed here too for completeness/
// documentation) and conversations.open (DM-style posts, `user:` instead
// of `channel:`). Anything else is refused (ok:false, unknown_method)
// rather than silently answered as if it worked — update this set (from
// real captured traffic, not guesswork: a plugin version bump can call a
// method this list hasn't seen yet) if the suite starts exercising one.
var slackAPIMethodsUsed = map[string]bool{
	"chat.postMessage":      true,
	"chat.postEphemeral":    true,
	"reactions.add":         true,
	"apps.connections.open": true,
	"conversations.open":    true,
}

// socket Mode: every currently-connected slack plugin socket, so
// /_fire_slack_event can broadcast to it. A real Slack workspace allows many
// connections; the harness only ever opens one at a time.
var (
	socketsMu sync.Mutex
	sockets   = map[*websocket.Conn]struct{}{}
)

func main() {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	http.HandleFunc("/", handle)
	log.Printf("sinkcatcher listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/_health":
		writeJSON(w, map[string]any{"ok": true})
		return
	case "/_captured":
		mu.Lock()
		defer mu.Unlock()
		if sink := r.URL.Query().Get("sink"); sink != "" {
			// Scoped to one sink's own captures (e.g. ?sink=slackapi) — the
			// harness's slack_sink_has relies on this rather than
			// substring-grepping the WHOLE combined buffer, which could
			// false-positive on another sink's (discord/ntfy/pushover/
			// notifiarr) captured body happening to contain the same text.
			var filtered []post
			for _, p := range posts {
				if p.Sink == sink {
					filtered = append(filtered, p)
				}
			}
			writeJSON(w, filtered)
			return
		}
		writeJSON(w, posts)
		return
	case "/_reset":
		mu.Lock()
		posts = nil
		mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
		return
	case "/_fire_slack_event":
		fireSlackEvent(w, r)
		return
	case "/slackapi/apps.connections.open":
		writeJSON(w, map[string]any{"ok": true, "url": socketURL(r)})
		return
	case "/slackapi/socket":
		acceptSocket(w, r)
		return
	}
	body, _ := io.ReadAll(r.Body)
	p := post{
		Sink:        sinkName(r.URL.Path),
		Path:        r.URL.Path,
		ContentType: r.Header.Get("Content-Type"),
		Body:        string(body),
	}
	mu.Lock()
	posts = append(posts, p)
	mu.Unlock()
	// The slack-shaped base (PC_SLACK_API_URL=http://sink-catcher:8080/slackapi)
	// must answer like the Slack Web API — the connector verb checks ok:true and
	// reads ts/channel — while still capturing the call above. Only for a
	// method the e2e suite's own slack plugin actually calls
	// (slackAPIMethodsUsed below): an unknown method answers like the real
	// API does to one it doesn't recognize (ok:false, error:unknown_method)
	// rather than blending in as a false "it worked" — a stub that answers
	// ok:true to anything can't tell "the plugin called the method we
	// expect" apart from "the plugin called the WRONG method and the mock
	// didn't notice".
	if strings.HasPrefix(r.URL.Path, "/slackapi/") {
		log.Printf("captured %s post: %s", p.Sink, truncate(p.Body, 120))
		method := strings.TrimPrefix(r.URL.Path, "/slackapi/")
		if !slackAPIMethodsUsed[method] {
			writeJSON(w, map[string]any{"ok": false, "error": "unknown_method"})
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "ts": "1700000000.000100",
			"channel": map[string]any{"id": "CDM"},
		})
		return
	}
	log.Printf("captured %s post: %s", p.Sink, truncate(p.Body, 120))
	writeJSON(w, map[string]any{"ok": true})
}

// socketURL builds the ws:// URL apps.connections.open hands back, on the
// same host:port the request itself arrived on (the compose network's own
// service name, however the daemon is configured to reach it).
func socketURL(r *http.Request) string {
	return fmt.Sprintf("ws://%s/slackapi/socket", r.Host)
}

// acceptSocket upgrades to a websocket, sends the Socket Mode "hello" frame,
// registers the connection so /_fire_slack_event can reach it, and blocks
// reading (discarding, but ACKing nothing of its own — the plugin's envelope
// acks are one-way) until the connection closes.
func acceptSocket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	ctx := r.Context()
	socketsMu.Lock()
	sockets[c] = struct{}{}
	socketsMu.Unlock()
	defer func() {
		socketsMu.Lock()
		delete(sockets, c)
		socketsMu.Unlock()
		c.Close(websocket.StatusNormalClosure, "")
	}()
	if err := c.Write(ctx, websocket.MessageText, []byte(`{"type":"hello"}`)); err != nil {
		return
	}
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
	}
}

// fireSlackEvent relays one events_api envelope (the POSTed body is the
// Slack "event" object: {"type":"app_mention", "text":…, …}) to every
// connected socket, wrapped exactly as Socket Mode delivers it.
func fireSlackEvent(w http.ResponseWriter, r *http.Request) {
	var event map[string]any
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	envelope, err := json.Marshal(map[string]any{
		"type":        "events_api",
		"envelope_id": "e2e-envelope",
		"payload":     map[string]any{"event": event},
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ctx := context.Background()
	socketsMu.Lock()
	n := 0
	for c := range sockets {
		if c.Write(ctx, websocket.MessageText, envelope) == nil {
			n++
		}
	}
	socketsMu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "delivered": n})
}

// sinkName derives the sink from the first path segment (e.g. /ntfy/topic → ntfy).
func sinkName(path string) string {
	seg := strings.Split(strings.Trim(path, "/"), "/")
	if len(seg) == 0 || seg[0] == "" {
		return "unknown"
	}
	// Notifiarr posts to /notifiarr/api/v1/notification/passthrough/<key>.
	return seg[0]
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
