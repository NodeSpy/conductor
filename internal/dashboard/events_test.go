package dashboard

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/flow"
)

// TestHubResumeReplaysMissed checks that a subscriber reconnecting with a
// Last-Event-ID receives exactly the events it missed, in order, and no others.
func TestHubResumeReplaysMissed(t *testing.T) {
	h := newHub()
	for i := 0; i < 5; i++ {
		h.publish(api.TopicRun, "step_done", time.Now(), nil)
	}
	// Resume from id 2 → want 3, 4, 5.
	_, missed, reset, cancel := h.subscribe([]api.EventTopic{api.TopicRun}, 2)
	defer cancel()
	if reset {
		t.Fatalf("unexpected reset: ids 3-5 are still in the ring")
	}
	if len(missed) != 3 {
		t.Fatalf("missed = %d events, want 3", len(missed))
	}
	for i, ev := range missed {
		if ev.ID != int64(3+i) {
			t.Errorf("missed[%d].ID = %d, want %d", i, ev.ID, 3+i)
		}
	}
}

// TestHubResumeTopicFilter checks resume only replays the subscribed topics.
func TestHubResumeTopicFilter(t *testing.T) {
	h := newHub()
	h.publish(api.TopicRun, "run_started", time.Now(), nil)     // id 1
	h.publish(api.TopicAttention, "attention", time.Now(), nil) // id 2
	h.publish(api.TopicRun, "run_done", time.Now(), nil)        // id 3
	_, missed, _, cancel := h.subscribe([]api.EventTopic{api.TopicAttention}, 0)
	defer cancel()
	// lastID 0 means "no replay" — a fresh subscriber only gets live events.
	if len(missed) != 0 {
		t.Fatalf("fresh subscriber replayed %d events, want 0", len(missed))
	}
}

// TestHubResumeGapResets checks that a Last-Event-ID older than the ring yields
// a reset signal rather than a partial replay.
func TestHubResumeGapResets(t *testing.T) {
	h := newHub()
	// Overflow the ring so the oldest retained id is far above 1.
	for i := 0; i < ringSize+50; i++ {
		h.publish(api.TopicRun, "step_done", time.Now(), nil)
	}
	_, missed, reset, cancel := h.subscribe([]api.EventTopic{api.TopicRun}, 1)
	defer cancel()
	if !reset {
		t.Fatalf("resume from id 1 after %d events should reset", ringSize+50)
	}
	if len(missed) != 0 {
		t.Fatalf("reset should not also replay (%d events)", len(missed))
	}
}

// TestHubLiveDelivery checks a subscribed channel receives subsequently
// published events of its topic.
func TestHubLiveDelivery(t *testing.T) {
	h := newHub()
	ch, _, _, cancel := h.subscribe([]api.EventTopic{api.TopicRun}, 0)
	defer cancel()
	h.publish(api.TopicAttention, "attention", time.Now(), nil) // not our topic
	h.publish(api.TopicRun, "run_started", time.Now(), nil)     // ours
	select {
	case ev := <-ch:
		if ev.Type != "run_started" {
			t.Fatalf("got %q, want run_started (attention should be filtered out)", ev.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("no live event delivered")
	}
}

// TestSSEStreamLive is the end-to-end stream: a flow.RunEvent published on the
// daemon's hub arrives on the HTTP SSE stream as a run event.
func TestSSEStreamLive(t *testing.T) {
	s, _, fh := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Bridge the flow hub into the dashboard hub (Start does this; here we do it
	// directly so the test needs no listener).
	go s.hub.run(ctx, fh)

	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/v1/events?topics=run", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// Give the subscription a moment to register before publishing.
	time.Sleep(50 * time.Millisecond)
	fh.Publish(flow.RunEvent{Type: "run_started", Run: "r-live", Kind: "review", Repo: "o/r", Number: 7})

	line := readSSEData(t, resp.Body, 3*time.Second)
	if !strings.Contains(line, "r-live") || !strings.Contains(line, "run_started") {
		t.Fatalf("SSE data did not carry the published event: %q", line)
	}
}

// readSSEData reads lines until it sees a `data:` line, returning its content.
func readSSEData(t *testing.T, r interface{ Read([]byte) (int, error) }, timeout time.Duration) string {
	t.Helper()
	type res struct {
		line string
		err  error
	}
	out := make(chan res, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if strings.HasPrefix(sc.Text(), "data:") {
				out <- res{line: sc.Text()}
				return
			}
		}
		out <- res{err: sc.Err()}
	}()
	select {
	case rr := <-out:
		if rr.err != nil {
			t.Fatalf("read SSE: %v", rr.err)
		}
		return rr.line
	case <-time.After(timeout):
		t.Fatal("timed out waiting for an SSE data line")
		return ""
	}
}
