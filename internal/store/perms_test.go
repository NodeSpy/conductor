package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/controller"
)

// REGRESSION (audit finding #4): every state file conductor writes is 0600 —
// runs/sessions/affinity/plans hold workflow data (and, before the taint
// scrub, could hold worse); none of it is other-users' business.
func TestStateFilesAre0600(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{StatePath: filepath.Join(dir, "state.json"), AuditPath: filepath.Join(dir, "audit.log")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Touch every persistence path.
	if err := s.Record("k", "kind", "sig", "head"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutRun(WorkflowRun{ID: "r1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSession(controller.SessionRef{PRKey: "p", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutAffinity(controller.AffinityRef{Runtime: "a", Key: "k", SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPlan(PlanRecord{RunID: "r1", StepID: "s1"}); err != nil {
		t.Fatal(err)
	}
	s.Audit(map[string]any{"event": "x"})

	for _, name := range []string{"state.json", "runs.json", "sessions.json", "affinity.json", "plans.json", "audit.log"} {
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s: mode %o, want 0600", name, perm)
		}
	}
}
