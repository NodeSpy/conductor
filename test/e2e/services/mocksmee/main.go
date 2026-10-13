// Command mocksmee is a hermetic stand-in for smee.io used by the e2e
// harness (test/e2e/, group Y — the `listeners` connection semantic proven
// against the REAL github and smee plugins). It accepts a POST to any
// channel path and re-broadcasts it, in smee's own wire format, as a
// Server-Sent Event to every GET subscriber of that same channel — the
// outbound-SSE-in, HTTP-POST-relay-out shape the real smee.io service and
// conductor-plugins' smee connector (connectors/smee) both speak.
//
// Unlike real smee.io, this mock never re-parses or re-serializes the
// POSTed body: it is embedded verbatim (as a raw JSON value) in the SSE
// frame, so a signature computed over the original bytes (X-Hub-Signature-256)
// still verifies once the smee plugin's relay replays it — unlike the real
// service's documented re-serialization quirk, deliberately not reproduced
// here since the suite needs a deterministic signature round-trip, not a
// faithful reproduction of that one wart.
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

type subscriber chan []byte

var (
	mu   sync.Mutex
	subs = map[string][]subscriber{} // channel path -> its live SSE subscribers
)

func main() {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	http.HandleFunc("/", handle)
	log.Printf("mocksmee listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/_health" {
		writeJSON(w, map[string]any{"ok": true})
		return
	}
	channel := strings.Trim(r.URL.Path, "/")
	if channel == "" {
		http.Error(w, "a smee channel path is required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		serveSSE(w, r, channel)
	case http.MethodPost:
		deliver(w, r, channel)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// deliver turns one POSTed request into a smee-shaped frame — every header,
// lowercased, as a flat string field, plus the raw body under "body" — and
// fans it out to the channel's subscribers. A channel with nobody listening
// yet (the relay hasn't connected) silently drops it, same as real smee.io
// would for a delivery nobody was subscribed to receive.
func deliver(w http.ResponseWriter, r *http.Request, channel string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	frame := map[string]any{}
	for k, vs := range r.Header {
		if len(vs) > 0 {
			frame[strings.ToLower(k)] = vs[0]
		}
	}
	// Embedded as a JSON STRING, not a raw json.RawMessage value: when a
	// json.RawMessage sits inside a larger Marshal call, encoding/json
	// compacts it — stripping the original document's whitespace/indentation
	// — which byte-for-byte changes a pretty-printed payload and breaks a
	// signature computed over the original bytes. A plain Go string is
	// escaped instead of compacted, a lossless round trip for arbitrary text,
	// and is exactly what parseSmeeFrame's primary path (the smee plugin,
	// connectors/smee) expects: unmarshal `body` as a string first.
	frame["body"] = string(body)
	b, err := json.Marshal(frame)
	if err != nil {
		http.Error(w, "encode frame: "+err.Error(), http.StatusInternalServerError)
		return
	}
	n := broadcast(channel, b)
	log.Printf("mocksmee: delivered to %q (%d subscriber(s), %d bytes)", channel, n, len(body))
	writeJSON(w, map[string]any{"ok": true})
}

// serveSSE streams every frame delivered to channel, in smee's event-stream
// shape, until the client disconnects.
func serveSSE(w http.ResponseWriter, r *http.Request, channel string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch := subscribe(channel)
	defer unsubscribe(channel, ch)
	log.Printf("mocksmee: subscriber joined %q", channel)

	// smee.io's own first frame on connect ("ready") — harmless to mimic and
	// a cheap confirmation in the logs that a relay actually connected.
	_, _ = w.Write([]byte("event: ready\ndata: {}\n\n"))
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-ch:
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(frame)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

func subscribe(channel string) subscriber {
	ch := make(subscriber, 16)
	mu.Lock()
	subs[channel] = append(subs[channel], ch)
	mu.Unlock()
	return ch
}

func unsubscribe(channel string, ch subscriber) {
	mu.Lock()
	defer mu.Unlock()
	list := subs[channel]
	for i, c := range list {
		if c == ch {
			subs[channel] = append(list[:i], list[i+1:]...)
			break
		}
	}
}

func broadcast(channel string, frame []byte) int {
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, ch := range subs[channel] {
		select {
		case ch <- frame:
			n++
		default:
			// A slow/stuck subscriber never blocks a delivery; it just
			// misses this one frame.
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
