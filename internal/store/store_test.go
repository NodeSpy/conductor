package store

import (
	"path/filepath"
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
