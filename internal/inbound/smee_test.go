package inbound

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// sseBody is one smee-shaped SSE stream: a ready control frame (no body, must
// be skipped), an object-body delivery, and a string-body delivery.
const sseBody = `event: ready
data: {"message":"ready"}

data: {"x-github-event":"push","content-type":"application/json","body":{"zen":"ok"}}

data: {"x-github-event":"ping","body":"raw-string-body","query":{"q":"1"}}

: comment line
id: 5
`

func TestStreamSmeeParsesFrames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("missing SSE accept header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte(sseBody))
	}))
	defer srv.Close()

	var frames []Frame
	err := streamSmee(context.Background(), srv.URL, func(f Frame) { frames = append(frames, f) })
	if err == nil || !strings.Contains(err.Error(), "stream closed") {
		t.Fatalf("clean EOF should surface as stream closed, got %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("want 2 frames (control frame skipped), got %d", len(frames))
	}
	if got := frames[0].Header("X-GitHub-Event"); got != "push" {
		t.Fatalf("case-insensitive header: %q", got)
	}
	if string(frames[0].Body) != `{"zen":"ok"}` {
		t.Fatalf("object body kept verbatim: %s", frames[0].Body)
	}
	if string(frames[1].Body) != "raw-string-body" {
		t.Fatalf("string body unwrapped: %s", frames[1].Body)
	}
	if _, has := frames[1].Headers["query"]; has {
		t.Fatal("query must not become a header")
	}
}

func TestStreamSmeeErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	if err := streamSmee(context.Background(), srv.URL, nil); err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("non-200 connect: %v", err)
	}
	if err := streamSmee(context.Background(), "://bad", nil); err == nil {
		t.Fatal("bad URL should error")
	}
	if err := streamSmee(context.Background(), "http://127.0.0.1:1/x", nil); err == nil {
		t.Fatal("unreachable host should error")
	}
}

// TestSmeeReconnectsUntilCancel: the loop survives a bad connect (backoff),
// reconnects, delivers a frame, and exits with ctx.Err() on cancel.
func TestSmeeReconnectsUntilCancel(t *testing.T) {
	var conns atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if conns.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError) // first connect fails
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"body\":{\"n\":1}}\n\n")
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan Frame, 8)
	var logged atomic.Int64
	done := make(chan error, 1)
	go func() {
		done <- Smee(ctx, srv.URL, func(string, ...any) { logged.Add(1) }, func(f Frame) { got <- f })
	}()

	select {
	case <-got: // a frame arrived after the reconnect
	case <-time.After(10 * time.Second):
		t.Fatal("no frame after reconnect")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Smee should return ctx.Err(), got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Smee did not stop on cancel")
	}
	if conns.Load() < 2 || logged.Load() == 0 {
		t.Fatalf("want a failed connect + reconnect logged, conns=%d logged=%d", conns.Load(), logged.Load())
	}
}

func TestSmeeCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// nil logf takes the no-op default.
	if err := Smee(ctx, "http://127.0.0.1:1/x", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("want ctx err, got %v", err)
	}
}

func TestDeliveryDedup(t *testing.T) {
	d := NewDeliveryDedup(2)
	if !d.Add("") || !d.Add("") {
		t.Fatal("empty ids are always new (nothing to dedup on)")
	}
	if !d.Add("a") || d.Add("a") {
		t.Fatal("first add new, repeat suppressed")
	}
	if !d.Add("b") || !d.Add("c") {
		t.Fatal("distinct ids are new")
	}
	// Ring bound 2: "a" was evicted, so it reads as new again.
	if !d.Add("a") {
		t.Fatal("evicted id should be new again")
	}
	// Default sizing.
	if NewDeliveryDedup(0).max != 2048 {
		t.Fatal("non-positive max takes the default")
	}
}
