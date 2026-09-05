package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/NodeSpy/conductor/internal/controller"
)

// TestAffinityPersistence: bindings round-trip affinity.json across a store
// reopen (the restart path), and deletes stick.
func TestAffinityPersistence(t *testing.T) {
	dir := t.TempDir()
	open := func() *Store {
		s, err := Open(Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.log")})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	created := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	ref := controller.AffinityRef{
		Agent: "reviewer", Key: "o/r#7", Controller: "paseo", SessionID: "agent-9",
		Created: created, LastUsed: created.Add(time.Hour),
	}
	if err := s.PutAffinity(ref); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAffinity(controller.AffinityRef{Agent: "reviewer", Key: "o/r#8", Controller: "paseo", SessionID: "agent-10", Created: created, LastUsed: created}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Reopen (restart): both bindings restore with timestamps intact.
	s = open()
	got := s.Affinities()
	if len(got) != 2 {
		t.Fatalf("want 2 restored bindings, got %d", len(got))
	}
	byKey := map[string]controller.AffinityRef{}
	for _, r := range got {
		byKey[r.Key] = r
	}
	r := byKey["o/r#7"]
	if r.SessionID != "agent-9" || r.Controller != "paseo" || !r.Created.Equal(created) || !r.LastUsed.Equal(created.Add(time.Hour)) {
		t.Fatalf("restored ref: %+v", r)
	}

	// Delete persists too.
	if err := s.DeleteAffinity("reviewer", "o/r#7"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAffinity("reviewer", "nope"); err != nil {
		t.Fatal(err) // absent delete is a no-op
	}
	s.Close()
	s = open()
	if got := s.Affinities(); len(got) != 1 || got[0].Key != "o/r#8" {
		t.Fatalf("after delete: %+v", got)
	}
	s.Close()
}
