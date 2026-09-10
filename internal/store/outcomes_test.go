package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestEngagementsLifecycle(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{StatePath: filepath.Join(dir, "s.json"), AuditPath: filepath.Join(dir, "a.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	s.RecordEngagement("o/r", 5, Engagement{Key: "fixer", Workflow: "gh.pr/x", CostUSD: 0.5})
	s.RecordEngagement("o/r", 5, Engagement{Key: "reviewer"})
	s.RecordEngagement("o/r", 6, Engagement{Key: "fixer"})
	// Guards: no repo / no number / no agent → dropped.
	s.RecordEngagement("", 5, Engagement{Key: "x"})
	s.RecordEngagement("o/r", 0, Engagement{Key: "x"})
	s.RecordEngagement("o/r", 7, Engagement{})

	if got := s.PeekEngagements("o/r", 5); len(got) != 2 || got[0].Key != "fixer" {
		t.Fatalf("peek: %+v", got)
	}
	// Peek doesn't consume.
	if got := s.PeekEngagements("o/r", 5); len(got) != 2 {
		t.Fatalf("peek consumed: %+v", got)
	}
	// Persistence across reopen.
	s.Close()
	s2, err := Open(Options{StatePath: filepath.Join(dir, "s.json"), AuditPath: filepath.Join(dir, "a2.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got := s2.TakeEngagements("o/r", 5)
	if len(got) != 2 || got[0].CostUSD != 0.5 {
		t.Fatalf("take after reopen: %+v", got)
	}
	// Take consumes.
	if got := s2.TakeEngagements("o/r", 5); len(got) != 0 {
		t.Fatalf("take must consume: %+v", got)
	}
	// The other target is untouched.
	if got := s2.PeekEngagements("o/r", 6); len(got) != 1 {
		t.Fatalf("other target: %+v", got)
	}
}

func TestEngagementsCapAndAgePrune(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(Options{StatePath: filepath.Join(dir, "s.json"), AuditPath: filepath.Join(dir, "a.jsonl")})
	defer s.Close()

	old := time.Now().Add(-60 * 24 * time.Hour)
	s.RecordEngagement("o/r", 9, Engagement{Key: "ancient", At: old})
	for i := 0; i < engagementCap+5; i++ {
		s.RecordEngagement("o/r", 9, Engagement{Key: "fixer"})
	}
	got := s.PeekEngagements("o/r", 9)
	if len(got) != engagementCap {
		t.Fatalf("cap: %d", len(got))
	}
	for _, e := range got {
		if e.Key == "ancient" {
			t.Fatal("aged engagement must be pruned")
		}
	}
}

func TestMarkCIFailureDedupsPerHead(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{StatePath: filepath.Join(dir, "s.json"), AuditPath: filepath.Join(dir, "a.jsonl")})
	if err != nil {
		t.Fatal(err)
	}

	// First failing_checks for a head records; the matrix's fan-out is suppressed.
	if !s.MarkCIFailure("o/r", 5, "headA") {
		t.Fatal("first head must record")
	}
	for i := 0; i < 20; i++ {
		if s.MarkCIFailure("o/r", 5, "headA") {
			t.Fatalf("same head must dedup (iter %d)", i)
		}
	}
	// A fresh push (new head) records again.
	if !s.MarkCIFailure("o/r", 5, "headB") {
		t.Fatal("new head must record")
	}
	// A different target is independent.
	if !s.MarkCIFailure("o/r", 6, "headA") {
		t.Fatal("other target must record")
	}
	// Empty head is never deduped (fail-safe) and leaves the marker unchanged.
	if !s.MarkCIFailure("o/r", 5, "") {
		t.Fatal("empty head must not dedup")
	}
	if !s.MarkCIFailure("o/r", 5, "") {
		t.Fatal("repeated empty head must not dedup")
	}

	// The marker survives a restart mid-fan-out.
	s.Close()
	s2, _ := Open(Options{StatePath: filepath.Join(dir, "s.json"), AuditPath: filepath.Join(dir, "a2.jsonl")})
	defer s2.Close()
	if s2.MarkCIFailure("o/r", 5, "headB") {
		t.Fatal("marker must survive reopen")
	}
	// The terminal signal (TakeEngagements) clears the marker.
	s2.TakeEngagements("o/r", 5)
	if !s2.MarkCIFailure("o/r", 5, "headB") {
		t.Fatal("terminal outcome must clear the marker")
	}
}

func TestOutcomeStats(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(Options{StatePath: filepath.Join(dir, "s.json"), AuditPath: filepath.Join(dir, "a.jsonl")})
	s.BumpOutcome("fixer", "merged")
	s.BumpOutcome("fixer", "merged")
	s.BumpOutcome("fixer", "reverted")
	s.BumpOutcome("", "merged") // no agent → dropped
	s.BumpOutcome("fixer", "")  // no outcome → dropped
	s.Close()

	s2, _ := Open(Options{StatePath: filepath.Join(dir, "s.json"), AuditPath: filepath.Join(dir, "a2.jsonl")})
	defer s2.Close()
	st := s2.OutcomeStats("fixer")
	if st["merged"] != 2 || st["reverted"] != 1 {
		t.Fatalf("stats after reopen: %+v", st)
	}
	if len(s2.OutcomeStats("ghost")) != 0 {
		t.Fatal("unknown agent stats")
	}
}
