package store

import (
	"encoding/json"
	"os"
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

// Regression (#140 Q-list): a small list cap must not be the reason a run that
// exists on disk — and is readable by id via ReadHistory — is absent from the
// listing. The cap keeps the newest; an uncapped list surfaces every record.
func TestHistoryListCapHidesButReadStillFinds(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	const n = 35
	var oldest string
	for i := 0; i < n; i++ {
		id := "r" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		if i == 0 {
			oldest = id
		}
		if err := WriteHistory(dir, RunHistory{ID: id,
			Started: base.Add(time.Duration(i) * time.Second), Status: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest record is genuinely on disk and readable by id.
	if _, err := ReadHistory(dir, oldest); err != nil {
		t.Fatalf("oldest record must be readable by id: %v", err)
	}
	// A small cap keeps only the newest and — this is the hole — drops the
	// oldest, even though it exists and detail can read it.
	capped := ListHistoryDir(dir, 30)
	if len(capped) != 30 {
		t.Fatalf("capped list: got %d, want 30", len(capped))
	}
	for _, r := range capped {
		if r.ID == oldest {
			t.Fatal("cap should have excluded the oldest record")
		}
	}
	// Uncapped, every record is present — the list is not lying about what exists.
	all := ListHistoryDir(dir, 0)
	if len(all) != n {
		t.Fatalf("uncapped list: got %d, want %d", len(all), n)
	}
	found := false
	for _, r := range all {
		if r.ID == oldest {
			found = true
		}
	}
	if !found {
		t.Fatal("uncapped list must include the oldest record that ReadHistory finds")
	}
	// Newest-first ordering holds regardless of cap.
	if all[0].ID != capped[0].ID {
		t.Fatalf("newest-first mismatch: all[0]=%s capped[0]=%s", all[0].ID, capped[0].ID)
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

// Regression (#36 review M8): history records are HMAC-signed at write and
// the retry path reads through verification — a record edited on disk (a
// doctored pinned output, a flipped step status) or one with its signature
// stripped is refused. Display reads stay best-effort.
func TestHistoryTamperRefusedOnVerifiedRead(t *testing.T) {
	dir := t.TempDir()
	rec := RunHistory{ID: "r-signed", Kind: "ping", Status: "failed", Started: time.Now(),
		Steps: []StepRecord{{ID: "first", Index: 0, Status: "ok",
			Outputs: map[string]any{"token": "honest"}}}}
	if err := WriteHistory(dir, rec); err != nil {
		t.Fatal(err)
	}

	// Intact record verifies.
	got, err := ReadHistoryVerified(dir, "r-signed")
	if err != nil {
		t.Fatalf("intact record must verify: %v", err)
	}
	if got.Steps[0].Outputs["token"] != "honest" {
		t.Fatalf("round trip: %+v", got.Steps)
	}

	// Tamper with a pinned output on disk → verified read refuses.
	path := filepath.Join(dir, "r-signed.json")
	b, _ := os.ReadFile(path)
	tampered := strings.Replace(string(b), "honest", "evil", 1)
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHistoryVerified(dir, "r-signed"); err == nil ||
		!strings.Contains(err.Error(), "integrity") {
		t.Fatalf("tampered record must fail verification: %v", err)
	}
	// The display read still works — list/detail are not the trust boundary.
	if _, err := ReadHistory(dir, "r-signed"); err != nil {
		t.Fatalf("display read: %v", err)
	}

	// Stripping the signature entirely is refused too (an attacker can't
	// just delete the field).
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	delete(raw, "sig")
	unsigned, _ := json.Marshal(raw)
	if err := os.WriteFile(path, unsigned, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadHistoryVerified(dir, "r-signed"); err == nil ||
		!strings.Contains(err.Error(), "integrity") {
		t.Fatalf("unsigned record must fail verification: %v", err)
	}
}
