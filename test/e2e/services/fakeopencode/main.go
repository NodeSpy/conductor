// Command fakeopencode is a hermetic stand-in for `opencode serve` — the HTTP API
// the opencode native-transport controller (issue #9, M4/T4.1) will drive. It is a
// SCAFFOLD: conductor's opencode controller isn't wired to run yet (M4 not landed;
// the M1 registry registers it as ErrNotRunnable), so the e2e runner does not
// assert against it. It exists so the harness is complete the moment M4 lands.
//
// It answers the minimal opencode HTTP surface (session create / prompt / event
// stream) with canned responses and no LLM.
//
// NOT part of the shipped product; harness-only.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync/atomic"
)

var seq int64

func main() {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/_health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})
	// POST /session → create a session.
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		id := atomic.AddInt64(&seq, 1)
		writeJSON(w, map[string]any{"id": id, "title": "fake-opencode-session"})
	})
	// POST /session/{id}/message → a prompt turn; canned completion.
	mux.HandleFunc("/session/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"info":  map[string]any{"role": "assistant", "cost": 0},
			"parts": []any{map[string]any{"type": "text", "text": "fake-opencode: done"}},
		})
	})
	log.Printf("fakeopencode listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
