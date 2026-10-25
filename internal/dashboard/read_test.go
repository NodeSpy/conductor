package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dashboard/api"
	"github.com/NodeSpy/conductor/internal/flow"
	"github.com/NodeSpy/conductor/internal/store"
)

// newReadServer builds a dashboard server with a configured workflow, a run
// history, and retry/stop stubs, for the READ-screen endpoints.
func newReadServer(t *testing.T) (*Server, *store.Store, *[]string) {
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

	enabled := false
	cfg := &config.Config{
		Dashboard: config.DashboardConfig{Enabled: true},
		Triggers: []config.TriggerSpec{
			{Name: "pr-review", On: "github.pull_request", Steps: []config.Step{
				{ID: "review", Agent: "reviewer", Prompt: "review it"},
				{ID: "post", Uses: "github.comment"},
			}},
			{Name: "nightly", On: "manual", Steps: []config.Step{{ID: "sweep", Uses: "github.sweep"}}},
			{Name: "off", On: "manual", Enabled: &enabled, Steps: []config.Step{{ID: "noop", Run: "bash"}}},
		},
	}

	var calls []string
	s := New(Deps{
		Cfg: cfg, Store: st, Events: flow.NewEventHub(), Version: "test",
		Retry: func(_ context.Context, id, from string, force bool) (string, error) {
			calls = append(calls, "retry:"+id+":"+from)
			return "retrying from \"" + from + "\" — the new run is r-abcd", nil
		},
		Stop: func(_ context.Context, id string) error {
			calls = append(calls, "stop:"+id)
			return nil
		},
		RunActive: func(id string) bool { return id == "r-live" },
	})
	return s, st, &calls
}

func seedRuns(t *testing.T, st *store.Store) {
	t.Helper()
	base := time.Now()
	recs := []store.RunHistory{
		{ID: "r-ok", Kind: "review", Variant: "pr-review", On: "github.pull_request", Status: "ok",
			Repo: "o/app", Number: 7, Started: base.Add(-2 * time.Hour), Finished: base.Add(-2*time.Hour + time.Minute),
			Steps: []store.StepRecord{
				{ID: "review", Status: "ok", Started: base.Add(-2 * time.Hour), DurationMS: 40000,
					Outputs: map[string]any{"summary": "looks good", "diff": "--- a\n+++ b\n@@\n-x\n+y"}},
				{ID: "post", Status: "ok", Started: base.Add(-2*time.Hour + 40*time.Second), DurationMS: 2000},
			}},
		{ID: "r-fail", Kind: "review", Variant: "pr-review", On: "github.pull_request", Status: "failed",
			Repo: "o/other", Number: 3, FailedStep: "review", Error: "model refused",
			Started: base.Add(-1 * time.Hour), Finished: base.Add(-1*time.Hour + 30*time.Second),
			Steps: []store.StepRecord{{ID: "review", Status: "failed", Error: "model refused"}}},
		{ID: "r-live", Kind: "sweep", Variant: "nightly", On: "manual", Status: "running",
			Started: base.Add(-5 * time.Minute)},
	}
	for i, rec := range recs {
		if err := st.PutHistory(rec); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
}

func getJSON[T any](t *testing.T, s *Server, method, url string) (T, int) {
	t.Helper()
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(method, url, nil))
	var out T
	if rr.Body.Len() > 0 {
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
	}
	return out, rr.Code
}

func TestRunDetail(t *testing.T) {
	s, st, _ := newReadServer(t)
	seedRuns(t, st)

	got, code := getJSON[api.RunDetailResponse](t, s, http.MethodGet, "/api/v1/runs/r-ok")
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	if got.Run.ID != "r-ok" || got.Live {
		t.Errorf("run = %q live=%v, want r-ok live=false", got.Run.ID, got.Live)
	}
	if len(got.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(got.Steps))
	}
	if got.Steps[0].Kind != "agent" {
		t.Errorf("step 0 kind = %q, want agent", got.Steps[0].Kind)
	}
	if got.Steps[1].Kind != "verb" {
		t.Errorf("step 1 kind = %q, want verb", got.Steps[1].Kind)
	}
	if !strings.Contains(got.Steps[0].OutputsYAML, "summary") {
		t.Errorf("outputs yaml missing summary: %q", got.Steps[0].OutputsYAML)
	}
	if !strings.Contains(got.Steps[0].Diff, "+y") {
		t.Errorf("diff not surfaced: %q", got.Steps[0].Diff)
	}

	// A running run reports live, and an unreached step shows as idle.
	live, _ := getJSON[api.RunDetailResponse](t, s, http.MethodGet, "/api/v1/runs/r-live")
	if !live.Live {
		t.Errorf("r-live not reported live")
	}

	// Unknown id → 404.
	if _, code := getJSON[api.RunDetailResponse](t, s, http.MethodGet, "/api/v1/runs/nope"); code != http.StatusNotFound {
		t.Errorf("unknown run code = %d, want 404", code)
	}
}

func TestRetryAndStop(t *testing.T) {
	s, st, calls := newReadServer(t)
	seedRuns(t, st)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs/r-fail/retry", strings.NewReader(`{"from":"review"}`))
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("retry code = %d: %s", rr.Code, rr.Body)
	}
	var ack api.ActionResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &ack)
	if ack.RunID != "r-abcd" {
		t.Errorf("retry run id = %q, want r-abcd (parsed from the ack)", ack.RunID)
	}

	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/runs/r-live/stop", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("stop code = %d: %s", rr.Code, rr.Body)
	}
	if len(*calls) != 2 || (*calls)[0] != "retry:r-fail:review" || (*calls)[1] != "stop:r-live" {
		t.Errorf("calls = %v", *calls)
	}
}

func TestWorkflows(t *testing.T) {
	s, st, _ := newReadServer(t)
	seedRuns(t, st)

	list, code := getJSON[api.WorkflowsResponse](t, s, http.MethodGet, "/api/v1/workflows")
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	if len(list.Workflows) != 3 {
		t.Fatalf("workflows = %d, want 3", len(list.Workflows))
	}
	byName := map[string]api.WorkflowSummary{}
	for _, w := range list.Workflows {
		byName[w.Name] = w
	}
	if w := byName["pr-review"]; w.Steps != 2 || w.Runs != 2 || w.Failed != 1 {
		t.Errorf("pr-review = %+v, want 2 steps / 2 runs / 1 failed", w)
	}
	if !byName["off"].Disabled {
		t.Errorf("off workflow should be disabled")
	}
	if !byName["nightly"].Manual {
		t.Errorf("nightly (on: manual) should be manual")
	}

	detail, code := getJSON[api.WorkflowDetailResponse](t, s, http.MethodGet, "/api/v1/workflows/pr-review")
	if code != http.StatusOK {
		t.Fatalf("detail code = %d", code)
	}
	if len(detail.Nodes) != 2 || detail.Nodes[1].Needs[0] != "review" {
		t.Errorf("graph nodes = %+v, want post needs review", detail.Nodes)
	}
	if !strings.Contains(detail.YAML, "github.pull_request") {
		t.Errorf("yaml missing on: %q", detail.YAML)
	}
	if len(detail.Runs) != 2 {
		t.Errorf("detail runs = %d, want 2", len(detail.Runs))
	}

	// Dry-run test: enabled workflow resolves; disabled one does not.
	ok, _ := getJSON[api.TestResponse](t, s, http.MethodPost, "/api/v1/workflows/pr-review/test")
	if !ok.Ok {
		t.Errorf("pr-review test not ok: %q", ok.Message)
	}
	bad, _ := getJSON[api.TestResponse](t, s, http.MethodPost, "/api/v1/workflows/off/test")
	if bad.Ok {
		t.Errorf("disabled workflow test should not be ok")
	}
}

func TestConnectorAttrs(t *testing.T) {
	s, st, _ := newReadServer(t)
	seedRuns(t, st)

	attrs, code := getJSON[api.AttrsResponse](t, s, http.MethodGet, "/api/v1/connectors/attrs")
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	keys := map[string]bool{}
	for _, a := range attrs.Attrs {
		keys[a.Key] = true
	}
	if !keys["github.repo"] || !keys["github.number"] {
		t.Errorf("attrs = %+v, want github.repo + github.number", attrs.Attrs)
	}

	vals, _ := getJSON[api.AttrValuesResponse](t, s, http.MethodGet, "/api/v1/connectors/attrs/values?attr=github.repo")
	if len(vals.Values) != 2 {
		t.Errorf("repo values = %v, want 2 (o/app, o/other)", vals.Values)
	}

	// The runs list accepts a connector qualifier.
	filtered := getRuns(t, s, "/api/v1/runs?github.repo=o/app")
	if len(filtered.Runs) != 1 || filtered.Runs[0].ID != "r-ok" {
		t.Errorf("github.repo=o/app → %d runs, want just r-ok", len(filtered.Runs))
	}
}
