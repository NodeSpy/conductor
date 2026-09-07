package store

import (
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/controller"
)

func TestSessionPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	opts := Options{
		StatePath: filepath.Join(dir, "state.json"),
		AuditPath: filepath.Join(dir, "audit.jsonl"),
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	ref := controller.SessionRef{
		PRKey:      "o/r#3",
		Controller: "paseo",
		SessionID:  "ag_42",
		Model:      controller.ModelResumable,
	}
	if err := s.PutSession(ref); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	// Reopen: the ref must survive on disk (restart survival).
	s2, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got := s2.Sessions()
	if len(got) != 1 {
		t.Fatalf("want 1 persisted session, got %d", len(got))
	}
	if got[0].SessionID != "ag_42" || got[0].Controller != "paseo" || got[0].Model != controller.ModelResumable {
		t.Fatalf("persisted ref mismatch: %+v", got[0])
	}
	if got[0].UpdatedAt.IsZero() {
		t.Fatal("PutSession should stamp UpdatedAt")
	}

	if err := s2.DeleteSession("o/r#3"); err != nil {
		t.Fatal(err)
	}
	if len(s2.Sessions()) != 0 {
		t.Fatal("DeleteSession should remove the ref")
	}
}
