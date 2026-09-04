package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T, ttl time.Duration, maxPRs int) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(Options{
		StatePath: filepath.Join(dir, "state.json"),
		AuditPath: filepath.Join(dir, "audit.jsonl"),
		TTL:       ttl, MaxPRs: maxPRs, AuditMaxSize: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestDedupAndAttempts(t *testing.T) {
	s := newTestStore(t, time.Hour, 100)
	key, kind, head := "acme/w#1", "failing_checks", "sha1"

	if got := s.LastSignature(key, kind); got != "" {
		t.Fatalf("fresh store should have no signature, got %q", got)
	}
	if err := s.Record(key, kind, "fail@sha1", head); err != nil {
		t.Fatal(err)
	}
	if got := s.LastSignature(key, kind); got != "fail@sha1" {
		t.Fatalf("want recorded signature, got %q", got)
	}
	if n := s.Attempts(key, kind, head); n != 1 {
		t.Fatalf("want 1 attempt, got %d", n)
	}
	s.Record(key, kind, "fail@sha1", head)
	if n := s.Attempts(key, kind, head); n != 2 {
		t.Fatalf("want 2 attempts, got %d", n)
	}
	// A different head has its own attempt counter.
	if n := s.Attempts(key, kind, "sha2"); n != 0 {
		t.Fatalf("new head should reset attempts, got %d", n)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StatePath: filepath.Join(dir, "state.json"),
		AuditPath: filepath.Join(dir, "audit.jsonl"), TTL: time.Hour, MaxPRs: 100, AuditMaxSize: 1024}
	s1, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	s1.Record("acme/w#1", "merge_conflict", "conflict:main/sha", "sha")
	s1.Close()

	s2, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if got := s2.LastSignature("acme/w#1", "merge_conflict"); got != "conflict:main/sha" {
		t.Fatalf("state did not persist, got %q", got)
	}
}

func TestCommentHighWaterMark(t *testing.T) {
	s := newTestStore(t, time.Hour, 100)
	if got := s.LastCommentID("acme/w#1", CommentKindIssue); got != 0 {
		t.Fatalf("unset mark should be 0, got %d", got)
	}
	if err := s.AdvanceCommentID("acme/w#1", CommentKindIssue, 100); err != nil {
		t.Fatal(err)
	}
	if got := s.LastCommentID("acme/w#1", CommentKindIssue); got != 100 {
		t.Fatalf("mark should be 100, got %d", got)
	}
	// Never lowers: an older id is a no-op.
	if err := s.AdvanceCommentID("acme/w#1", CommentKindIssue, 50); err != nil {
		t.Fatal(err)
	}
	if got := s.LastCommentID("acme/w#1", CommentKindIssue); got != 100 {
		t.Fatalf("mark should not lower, got %d", got)
	}
	// Kinds are independent: the issue mark says nothing about review comments,
	// whose ids come from a separate (lower) GitHub sequence.
	if got := s.LastCommentID("acme/w#1", CommentKindReview); got != 0 {
		t.Fatalf("review mark should be independent of issue mark, got %d", got)
	}
	if err := s.AdvanceCommentID("acme/w#1", CommentKindReview, 7); err != nil {
		t.Fatal(err)
	}
	if got := s.LastCommentID("acme/w#1", CommentKindReview); got != 7 {
		t.Fatalf("review mark should be 7, got %d", got)
	}
	if got := s.LastCommentID("acme/w#1", CommentKindIssue); got != 100 {
		t.Fatalf("issue mark should be untouched at 100, got %d", got)
	}
}

// A state file written before marks were kept per kind has a single
// last_comment_id. It's read as the issue mark and must not gate review comments.
func TestCommentHighWaterMarkMigratesLegacySingleMark(t *testing.T) {
	dir := t.TempDir()
	opts := Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.jsonl"), TTL: time.Hour, MaxPRs: 100}
	legacy := `{"acme/w#1":{"acted":{"new_comment":"comment:5515854542"},"last_comment_id":5515854542,"updated_at":"2026-09-02T14:21:26Z"}}`
	if err := os.WriteFile(opts.StatePath, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.LastCommentID("acme/w#1", CommentKindIssue); got != 5515854542 {
		t.Fatalf("legacy mark should become the issue mark, got %d", got)
	}
	if got := s.LastCommentID("acme/w#1", CommentKindReview); got != 0 {
		t.Fatalf("legacy mark must not gate review comments, got %d", got)
	}
	// Persisting drops the legacy field so the migration is one-way.
	if err := s.AdvanceCommentID("acme/w#1", CommentKindReview, 3918412084); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(opts.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"last_comment_id"`) {
		t.Fatalf("legacy last_comment_id should be dropped on save, got %s", b)
	}
	if !strings.Contains(string(b), `"last_comment_ids"`) {
		t.Fatalf("per-kind marks should be persisted, got %s", b)
	}
}

func TestDeleteAndGC(t *testing.T) {
	s := newTestStore(t, time.Hour, 100)
	s.Record("acme/w#1", "k", "sig", "h")
	if err := s.Delete("acme/w#1"); err != nil {
		t.Fatal(err)
	}
	if got := s.LastSignature("acme/w#1", "k"); got != "" {
		t.Fatal("record not deleted")
	}

	// TTL eviction: force an old timestamp.
	s.Record("acme/w#2", "k", "sig", "h")
	s.mu.Lock()
	s.recs["acme/w#2"].UpdatedAt = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()
	removed, err := s.GC()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("want 1 evicted, got %d", removed)
	}
}

func TestLRUCap(t *testing.T) {
	s := newTestStore(t, time.Hour, 2)
	base := time.Now()
	for i, k := range []string{"a#1", "a#2", "a#3"} {
		s.Record(k, "k", "s", "h")
		s.mu.Lock()
		s.recs[k].UpdatedAt = base.Add(time.Duration(i) * time.Minute)
		s.mu.Unlock()
	}
	if _, err := s.GC(); err != nil {
		t.Fatal(err)
	}
	if s.LastSignature("a#1", "k") != "" {
		t.Fatal("oldest record should have been LRU-evicted")
	}
	if s.LastSignature("a#3", "k") == "" {
		t.Fatal("newest record should survive")
	}
}

func TestRetryReadyBackoff(t *testing.T) {
	s := newTestStore(t, 0, 100)
	clock := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return clock }
	key, kind, head := "acme/w#1", "merge_conflict", "h"
	base, factor, max := 10*time.Minute, 3, 24*time.Hour
	soft := 2

	// Below soft → always ready.
	if ready, _ := s.RetryReady(key, kind, head, soft, base, factor, max); !ready {
		t.Fatal("below soft threshold should be ready")
	}
	// Two attempts → at soft. Cooldown step 0 = base (10m).
	_ = s.RecordAttempt(key, kind, head)
	_ = s.RecordAttempt(key, kind, head)
	if ready, wait := s.RetryReady(key, kind, head, soft, base, factor, max); ready || wait > base {
		t.Fatalf("at soft, just attempted → not ready, wait ~%s (<=%s)", wait, base)
	}
	clock = clock.Add(base) // 10m elapsed → ready
	if ready, _ := s.RetryReady(key, kind, head, soft, base, factor, max); !ready {
		t.Fatal("after the 10m cooldown it should be ready")
	}
	// Third attempt → step 1 cooldown = 30m (base*3).
	_ = s.RecordAttempt(key, kind, head)
	if _, wait := s.RetryReady(key, kind, head, soft, base, factor, max); wait <= base {
		t.Fatalf("step-1 cooldown should be ~30m (grew from 10m), got %s", wait)
	}
	clock = clock.Add(30 * time.Minute)
	if ready, _ := s.RetryReady(key, kind, head, soft, base, factor, max); !ready {
		t.Fatal("after 30m it should be ready again")
	}
}

func TestRetryReadySelfHealsMissingTimestamp(t *testing.T) {
	// A record with attempts past soft but NO attempt_at (old state.json) → ready,
	// so a previously hard-capped PR retries after upgrade instead of staying stuck.
	s := newTestStore(t, 0, 100)
	r := s.rec("acme/w#2")
	r.Attempts["merge_conflict@h"] = 5 // no AttemptAt entry
	if ready, _ := s.RetryReady("acme/w#2", "merge_conflict", "h", 2, 10*time.Minute, 3, 24*time.Hour); !ready {
		t.Fatal("missing attempt_at should be treated as ready (self-heal)")
	}
}

// TestRunsPersistAcrossReopen: PutRun/PendingRuns/DeleteRun round-trip through
// runs.json, so a restart resumes exactly the in-flight runs.
func TestRunsPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s, err := Open(Options{StatePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(WorkflowRun{ID: "flow:a", Kind: "new_comment", Repo: "a/w",
		StepIndex: 2, Outputs: map[string]map[string]any{"x": {"id": 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(WorkflowRun{ID: "flow:b", Kind: "release"}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRun("flow:b"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(Options{StatePath: path})
	if err != nil {
		t.Fatal(err)
	}
	runs := s2.PendingRuns()
	if len(runs) != 1 || runs[0].ID != "flow:a" || runs[0].StepIndex != 2 {
		t.Fatalf("reloaded runs: %+v", runs)
	}
	if runs[0].Outputs["x"]["id"] != float64(1) {
		t.Fatalf("outputs survive: %+v", runs[0].Outputs)
	}
	if runs[0].UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt stamped")
	}
}

// TestTouchExtendsTTLAndSetNow: Touch refreshes a record so GC keeps it.
func TestTouchExtendsTTLAndSetNow(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{StatePath: filepath.Join(dir, "s.json"), TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_700_000_000, 0)
	s.SetNow(func() time.Time { return clock })
	_ = s.RecordAttempt("a/w#1", "k", "h")

	// Move past the TTL, but Touch first — the record survives GC.
	clock = clock.Add(2 * time.Hour)
	s.Touch("a/w#1")
	if n, err := s.GC(); err != nil || n != 0 {
		t.Fatalf("touched record must survive GC: %d %v", n, err)
	}
	// Without another touch, the next TTL window evicts it.
	clock = clock.Add(3 * time.Hour)
	if n, err := s.GC(); err != nil || n != 1 {
		t.Fatalf("stale record should GC: %d %v", n, err)
	}
}

// TestOpenToleratesCorruptState: a corrupt state file starts fresh instead of
// failing the boot; a corrupt runs file likewise.
func TestOpenToleratesCorruptState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	os.WriteFile(path, []byte("{corrupt"), 0o644)
	os.WriteFile(filepath.Join(dir, "runs.json"), []byte("also corrupt"), 0o644)
	s, err := Open(Options{StatePath: path})
	if err != nil {
		t.Fatalf("corrupt files must not fail Open: %v", err)
	}
	if len(s.PendingRuns()) != 0 {
		t.Fatal("fresh runs map expected")
	}
}
