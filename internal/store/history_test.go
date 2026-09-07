package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func histStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(Options{StatePath: filepath.Join(dir, "s.json"), AuditPath: filepath.Join(dir, "a.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHistoryRoundTrip(t *testing.T) {
	s := histStore(t)
	rec := RunHistory{
		ID: "r1", RunID: "review:o/r#5", Kind: "review", On: "gh.review_requested",
		Repo: "o/r", Number: 5, Started: time.Now(), Status: "failed",
		Error: "step \"fix\": boom", FailedStep: "fix",
		Steps: []StepRecord{
			{ID: "triage", Index: 0, Status: "ok", DurationMS: 120,
				Inputs:  map[string]any{"uses": "svc.post"},
				Outputs: map[string]any{"id": 1}},
			{ID: "fix", Index: 1, Status: "failed", Error: "boom"},
		},
	}
	if err := s.PutHistory(rec); err != nil {
		t.Fatal(err)
	}
	got, ok := s.GetHistory("r1")
	if !ok || got.FailedStep != "fix" || len(got.Steps) != 2 {
		t.Fatalf("get: %+v %v", got, ok)
	}
	step, ok := got.Step("triage")
	if !ok || step.Outputs["id"] != float64(1) || step.DurationMS != 120 {
		t.Fatalf("step: %+v", step)
	}
	if _, ok := got.Step("ghost"); ok {
		t.Fatal("ghost step")
	}
	list := s.ListHistory(0)
	if len(list) != 1 || list[0].ID != "r1" {
		t.Fatalf("list: %+v", list)
	}
}

func TestHistoryListNewestFirstAndLimit(t *testing.T) {
	s := histStore(t)
	base := time.Now()
	for i, id := range []string{"r-old", "r-mid", "r-new"} {
		_ = s.PutHistory(RunHistory{ID: id, Started: base.Add(time.Duration(i) * time.Minute), Status: "ok"})
	}
	list := s.ListHistory(2)
	if len(list) != 2 || list[0].ID != "r-new" || list[1].ID != "r-mid" {
		t.Fatalf("list order: %+v", list)
	}
}

func TestHistoryPrune(t *testing.T) {
	dir := t.TempDir()
	old := RunHistory{ID: "r-ancient", Started: time.Now().Add(-30 * 24 * time.Hour), Status: "ok"}
	fresh := RunHistory{ID: "r-fresh", Started: time.Now(), Status: "ok"}
	for _, r := range []RunHistory{old, fresh} {
		if err := WriteHistory(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	// Age bound.
	n, err := PruneHistoryDir(dir, 14*24*time.Hour, 0)
	if err != nil || n != 1 {
		t.Fatalf("age prune: %d %v", n, err)
	}
	if _, err := ReadHistory(dir, "r-ancient"); err == nil {
		t.Fatal("ancient record must be pruned")
	}
	if _, err := ReadHistory(dir, "r-fresh"); err != nil {
		t.Fatalf("fresh record must survive: %v", err)
	}
	// Count bound: keep only the newest.
	for i := 0; i < 3; i++ {
		_ = WriteHistory(dir, RunHistory{ID: "r-extra" + string(rune('a'+i)),
			Started: time.Now().Add(time.Duration(i) * time.Second), Status: "ok"})
	}
	if n, _ = PruneHistoryDir(dir, 14*24*time.Hour, 2); n != 2 {
		t.Fatalf("count prune: %d", n)
	}
	if got := ListHistoryDir(dir, 0); len(got) != 2 {
		t.Fatalf("after count prune: %d", len(got))
	}
}

func TestHistoryBadIDsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"", "../evil", "a/b", `a\b`} {
		if err := WriteHistory(dir, RunHistory{ID: id}); err == nil && id != "" {
			t.Fatalf("bad id accepted: %q", id)
		}
		if _, err := ReadHistory(dir, id); err == nil || !strings.Contains(err.Error(), "bad run id") {
			t.Fatalf("bad id read: %q → %v", id, err)
		}
	}
}
