package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// newTestServer builds a dashboard Server over a throwaway store in t.TempDir,
// with a fresh event hub. Hermetic: nothing touches HOME or a live daemon.
func newTestServer(t *testing.T) (*Server, *store.Store, *flow.EventHub) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(store.Options{
		StatePath: filepath.Join(dir, "state.json"),
		AuditPath: filepath.Join(dir, "audit.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub := flow.NewEventHub()
	cfg := &config.Config{Dashboard: config.DashboardConfig{Enabled: true}}
	s := New(Deps{
		Cfg:       cfg,
		Store:     st,
		Events:    hub,
		Version:   "test-1.2.3",
		ConfigSHA: "abc123def456",
		Paused:    func() bool { return true },
	})
	return s, st, hub
}

func TestStatusEndpoint(t *testing.T) {
	s, _, _ := newTestServer(t)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rr.Code)
	}
	var got api.StatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != "test-1.2.3" {
		t.Errorf("version = %q, want test-1.2.3", got.Version)
	}
	if got.ConfigSHA != "abc123def456" {
		t.Errorf("config_sha = %q, want abc123def456", got.ConfigSHA)
	}
	if !got.Paused {
		t.Errorf("paused = false, want true")
	}
	if got.PID == 0 {
		t.Errorf("pid = 0, want the process id")
	}
	if got.StartedAt == "" {
		t.Errorf("started_at empty")
	}
}

func TestRunsEndpointNewestFirstAndFilters(t *testing.T) {
	s, st, _ := newTestServer(t)
	base := time.Now()
	// Three records at increasing times; r-new is the newest.
	for i, rec := range []store.RunHistory{
		{ID: "r-old", Kind: "review", Variant: "pr-review", Status: "ok", Repo: "o/r", Number: 1,
			Started: base.Add(-3 * time.Hour), Finished: base.Add(-3*time.Hour + 2*time.Minute)},
		{ID: "r-mid", Kind: "fix", Status: "failed", Repo: "o/r", Number: 2, FailedStep: "fix",
			Started: base.Add(-2 * time.Hour), Finished: base.Add(-2*time.Hour + time.Minute), Error: "boom"},
		{ID: "r-new", Kind: "review", Variant: "pr-review", Status: "running",
			Started: base.Add(-30 * time.Minute)},
	} {
		if err := st.PutHistory(rec); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	// No filters: newest first.
	resp := getRuns(t, s, "/api/v1/runs")
	if len(resp.Runs) != 3 {
		t.Fatalf("got %d runs, want 3", len(resp.Runs))
	}
	if resp.Runs[0].ID != "r-new" || resp.Runs[2].ID != "r-old" {
		t.Errorf("order = %s..%s, want r-new..r-old", resp.Runs[0].ID, resp.Runs[2].ID)
	}
	// Projection: variant maps to workflow, kind falls back.
	if resp.Runs[0].Workflow != "pr-review" {
		t.Errorf("workflow = %q, want pr-review", resp.Runs[0].Workflow)
	}
	if resp.Runs[1].Workflow != "fix" {
		t.Errorf("workflow fallback = %q, want fix (kind)", resp.Runs[1].Workflow)
	}
	// Running run gets a live duration (> 0).
	if resp.Runs[0].DurationMS <= 0 {
		t.Errorf("running duration = %d, want > 0", resp.Runs[0].DurationMS)
	}

	// since=1h drops r-old and r-mid.
	resp = getRuns(t, s, "/api/v1/runs?since=1h")
	if len(resp.Runs) != 1 || resp.Runs[0].ID != "r-new" {
		t.Errorf("since=1h returned %d runs (want just r-new)", len(resp.Runs))
	}

	// limit=2 pages, then the cursor returns the remainder.
	resp = getRuns(t, s, "/api/v1/runs?limit=2")
	if len(resp.Runs) != 2 || resp.Next == "" {
		t.Fatalf("page 1: %d runs, next=%q (want 2 runs + a cursor)", len(resp.Runs), resp.Next)
	}
	page2 := getRuns(t, s, "/api/v1/runs?limit=2&cursor="+resp.Next)
	if len(page2.Runs) != 1 || page2.Runs[0].ID != "r-old" {
		t.Errorf("page 2 = %d runs (want 1: r-old)", len(page2.Runs))
	}
}

func getRuns(t *testing.T, s *Server, url string) api.RunsResponse {
	t.Helper()
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, url, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", url, rr.Code)
	}
	var resp api.RunsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return resp
}
