package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/flow"
)

// sseEvent is one parsed server-sent event.
type sseEvent struct {
	id, event string
	env       api.Event
}

// readSSE parses events off a stream until n arrive or timeout.
func readSSE(t *testing.T, r io.Reader, n int, timeout time.Duration) []sseEvent {
	t.Helper()
	out := make(chan []sseEvent, 1)
	go func() {
		var got []sseEvent
		var cur sseEvent
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				cur.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				cur.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &cur.env)
			case line == "" && cur.event != "":
				got = append(got, cur)
				cur = sseEvent{}
				if len(got) == n {
					out <- got
					return
				}
			}
		}
		out <- got
	}()
	select {
	case got := <-out:
		if len(got) < n {
			t.Fatalf("stream ended after %d of %d events", len(got), n)
		}
		return got
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %d SSE events", n)
		return nil
	}
}

func openStream(t *testing.T, ctx context.Context, url, lastID string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	return resp
}

// TestSSEStreamLive is the end-to-end stream: a flow.RunEvent published on the
// daemon's hub arrives on the HTTP SSE stream as a run event with a cursor id.
func TestSSEStreamLive(t *testing.T) {
	s, _, fh := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // before ts.Close: ends the stream's request
	go s.bridgeRuns(ctx, fh)

	resp := openStream(t, ctx, ts.URL+"/api/v1/events?topics=run", "")
	time.Sleep(50 * time.Millisecond) // let the bridge subscribe
	fh.Publish(flow.RunEvent{Type: "run_started", Run: "r-live", Kind: "review", Repo: "o/r", Number: 7})

	ev := readSSE(t, resp.Body, 1, 3*time.Second)[0]
	if ev.event != "run" || ev.env.Type != "run_started" || ev.env.Subject != "r-live" {
		t.Fatalf("event = %+v", ev)
	}
	if ev.id == "" || ev.id != ev.env.ID {
		t.Fatalf("SSE id %q vs envelope id %q", ev.id, ev.env.ID)
	}
}

// TestSSEResumeReplaysMissedAndFiltersTopics: a client reconnecting with
// Last-Event-ID gets exactly what it missed, of its topics, in order.
func TestSSEResumeReplaysMissedAndFiltersTopics(t *testing.T) {
	s, _, _ := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // before ts.Close: ends the stream's request

	first, _ := s.journal.Append("run", "run_started", "r1", nil)
	s.journal.Append("chat", "message", "bot:a", nil)
	s.journal.Append("run", "step_done", "r1", nil)
	s.journal.Append("run", "run_done", "r1", nil)

	resp := openStream(t, ctx, ts.URL+"/api/v1/events?topics=run", first.ID)
	got := readSSE(t, resp.Body, 2, 3*time.Second)
	if got[0].env.Type != "step_done" || got[1].env.Type != "run_done" {
		t.Fatalf("replayed %q, %q", got[0].env.Type, got[1].env.Type)
	}
}

// TestSSEForeignCursorResets: a cursor from another journal (a restart that
// lost the journal, an old numeric id) gets a reset, then live events.
func TestSSEForeignCursorResets(t *testing.T) {
	s, _, _ := newTestServer(t)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // before ts.Close: ends the stream's request
	s.journal.Append("run", "run_started", "r1", nil)

	resp := openStream(t, ctx, ts.URL+"/api/v1/events?topics=run", "5000")
	time.AfterFunc(50*time.Millisecond, func() { s.journal.Append("run", "run_done", "r1", nil) })
	got := readSSE(t, resp.Body, 2, 3*time.Second)
	if got[0].event != "reset" || got[0].id != s.journal.Epoch()+"-1" {
		t.Fatalf("first event = %+v, want a reset at the head", got[0])
	}
	if got[1].env.Type != "run_done" {
		t.Fatalf("after reset: %+v", got[1])
	}
}

func TestEventsSincePages(t *testing.T) {
	s, _, _ := newTestServer(t)
	start := s.journal.Cursor()
	for i := 0; i < 5; i++ {
		s.journal.Append("chat", "message", "", map[string]int{"i": i})
	}
	get := func(q string) api.EventsPage {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/events/since?"+q, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rr.Code, rr.Body)
		}
		var p api.EventsPage
		json.Unmarshal(rr.Body.Bytes(), &p)
		return p
	}
	p := get("topics=chat&limit=3&cursor=" + start)
	if len(p.Events) != 3 || p.Reset {
		t.Fatalf("page 1 = %+v", p)
	}
	p = get("topics=chat&cursor=" + p.Cursor)
	if len(p.Events) != 2 || p.Events[1].Seq != 5 {
		t.Fatalf("page 2 = %+v", p)
	}
	if p = get("cursor=bogus-1"); !p.Reset || p.Cursor != s.journal.Cursor() {
		t.Fatalf("bogus cursor = %+v", p)
	}
}

// TestFailedRunRaisesAttentionAndSuccessResolves covers the run → attention
// mapping and the snapshot + dismiss endpoints.
func TestFailedRunRaisesAttentionAndSuccessResolves(t *testing.T) {
	s, _, _ := newTestServer(t)
	get := func() api.AttentionResponse {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/attention", nil))
		var a api.AttentionResponse
		json.Unmarshal(rr.Body.Bytes(), &a)
		return a
	}
	RecordRunEvent(s.journal, s.attention, flow.RunEvent{Type: "run_done", Status: "failed", Run: "h1", RunID: "fix:o/r#2", Kind: "fix", Repo: "o/r", Number: 2, Detail: "boom"})
	a := get()
	if len(a.Items) != 1 || a.Items[0].Kind != "run_failed" || a.Items[0].Link != "#/runs/h1" || a.Items[0].Title != "fix on o/r#2 failed" {
		t.Fatalf("attention = %+v", a)
	}
	// The snapshot's cursor is after the opening: following from it sees only
	// what comes next.
	RecordRunEvent(s.journal, s.attention, flow.RunEvent{Type: "run_done", Status: "ok", Run: "h2", RunID: "fix:o/r#2"})
	evs, _, _, _ := s.journal.Read(a.Cursor, []string{"attention"}, 0)
	if len(evs) != 1 || evs[0].Type != "resolved" {
		t.Fatalf("after cursor: %+v", evs)
	}
	if a = get(); len(a.Items) != 0 {
		t.Fatalf("still open: %+v", a.Items)
	}

	RecordRunEvent(s.journal, s.attention, flow.RunEvent{Type: "run_done", Status: "failed", Run: "h3"})
	dismiss := func(key string) int {
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/attention/"+key+"/dismiss", nil))
		return rr.Code
	}
	if code := dismiss("run_failed:h3"); code != http.StatusOK {
		t.Fatalf("dismiss = %d", code)
	}
	if code := dismiss("run_failed:h3"); code != http.StatusNotFound {
		t.Fatalf("second dismiss = %d", code)
	}
}
