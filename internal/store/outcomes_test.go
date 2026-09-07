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

	s.RecordEngagement("o/r", 5, Engagement{Agent: "fixer", Workflow: "gh.pr/x", CostUSD: 0.5})
	s.RecordEngagement("o/r", 5, Engagement{Agent: "reviewer"})
	s.RecordEngagement("o/r", 6, Engagement{Agent: "fixer"})
	// Guards: no repo / no number / no agent → dropped.
	s.RecordEngagement("", 5, Engagement{Agent: "x"})
	s.RecordEngagement("o/r", 0, Engagement{Agent: "x"})
	s.RecordEngagement("o/r", 7, Engagement{})

	if got := s.PeekEngagements("o/r", 5); len(got) != 2 || got[0].Agent != "fixer" {
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
	s.RecordEngagement("o/r", 9, Engagement{Agent: "ancient", At: old})
	for i := 0; i < engagementCap+5; i++ {
		s.RecordEngagement("o/r", 9, Engagement{Agent: "fixer"})
	}
	got := s.PeekEngagements("o/r", 9)
	if len(got) != engagementCap {
		t.Fatalf("cap: %d", len(got))
	}
	for _, e := range got {
		if e.Agent == "ancient" {
			t.Fatal("aged engagement must be pruned")
		}
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
	st := s2.AgentOutcomeStats("fixer")
	if st["merged"] != 2 || st["reverted"] != 1 {
		t.Fatalf("stats after reopen: %+v", st)
	}
	if len(s2.AgentOutcomeStats("ghost")) != 0 {
		t.Fatal("unknown agent stats")
	}
}
