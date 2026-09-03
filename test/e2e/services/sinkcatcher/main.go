// Command sinkcatcher captures notification posts for the e2e harness
// (test/e2e/). Conductor's notify sinks are pointed at it: Slack/Discord/ntfy via
// config URLs, Pushover/Notifiarr via the PC_PUSHOVER_URL / PC_NOTIFIARR_URL
// testability hooks. Each sink has its own path so group G can assert that every
// channel fired. Captures are queryable at GET /_captured and reset at POST
// /_reset.
//
// NOT part of the shipped product; harness-only.
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
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
		writeJSON(w, posts)
		return
	case "/_reset":
		mu.Lock()
		posts = nil
		mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
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
	log.Printf("captured %s post: %s", p.Sink, truncate(p.Body, 120))
	writeJSON(w, map[string]any{"ok": true})
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
