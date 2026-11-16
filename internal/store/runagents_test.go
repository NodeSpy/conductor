package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRunAgentsPersistWithTheRun(t *testing.T) {
	s := newTestStore(t, 0, 0)
	if err := s.PutRunAgent("gone", "k", RunAgent{SessionID: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.RunAgent("gone", "k"); ok {
		t.Fatal("an agent of a run that is not stored is not recorded")
	}

	_ = s.PutRun(WorkflowRun{ID: "r1", StepIndex: 1})
	a := RunAgent{Runtime: "rt", SessionID: "sess", Workspace: "/wt/a", Cwd: "/wt/a", CanResume: true, Resumes: 1}
	if err := s.PutRunAgent("r1", "wf/fix", a); err != nil {
		t.Fatal(err)
	}
	// A checkpoint rewrites the run from the runner's own copy, which never
	// carries agents: the recorded ones stay.
	_ = s.PutRun(WorkflowRun{ID: "r1", StepIndex: 2})
	got, ok := s.RunAgent("r1", "wf/fix")
	if !ok || got.SessionID != "sess" || got.Resumes != 1 {
		t.Fatalf("RunAgent = %+v, %v", got, ok)
	}
	if !s.KeepsWorkspace("/wt/a") || s.KeepsWorkspace("/wt/b") || s.KeepsWorkspace("") {
		t.Fatal("KeepsWorkspace")
	}

	// It survives a reopen (a restart).
	s2, err := Open(Options{StatePath: filepath.Join(filepath.Dir(s.runsPath), "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })
	if got, ok := s2.RunAgent("r1", "wf/fix"); !ok || got.Workspace != "/wt/a" || !got.CanResume {
		t.Fatalf("after reopen: %+v, %v", got, ok)
	}

	if err := s.DeleteRunAgent("r1", "wf/fix"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.RunAgent("r1", "wf/fix"); ok || s.KeepsWorkspace("/wt/a") {
		t.Fatal("a finished step's agent is dropped")
	}

	// A run that ends takes its agents with it: their workspaces are no
	// longer kept.
	_ = s.PutRunAgent("r1", "other", RunAgent{Workspace: "/wt/c"})
	_ = s.DeleteRun("r1")
	if s.KeepsWorkspace("/wt/c") {
		t.Fatal("a gone run keeps no workspace")
	}
}

// A close of a run's target is marked on the stored run, once, and survives
// both the running flow's checkpoints (written from its own copy of the
// record, which never saw the mark) and a restart. The run's start does too.
func TestRunTargetClosedMarkOutlivesCheckpoints(t *testing.T) {
	s := newTestStore(t, 0, 0)
	began := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	closed := began.Add(time.Minute)
	_ = s.PutRun(WorkflowRun{ID: "r1", Repo: "o/r", Number: 7, StartedAt: began})
	_ = s.PutRun(WorkflowRun{ID: "r2", Repo: "o/r", Number: 8, StartedAt: began})
	onSeven := func(r WorkflowRun) bool { return r.Number == 7 }

	if n, err := s.MarkRunsTargetClosed(onSeven, closed); err != nil || n != 1 {
		t.Fatalf("MarkRunsTargetClosed = %d, %v, want 1", n, err)
	}
	// Marked once: a second close does not move it.
	if n, _ := s.MarkRunsTargetClosed(onSeven, closed.Add(time.Hour)); n != 0 {
		t.Fatalf("marked %d runs already marked", n)
	}
	// A checkpoint carrying neither field keeps both.
	_ = s.PutRun(WorkflowRun{ID: "r1", Repo: "o/r", Number: 7, StepIndex: 2})

	check := func(s *Store, when string) {
		t.Helper()
		for _, r := range s.PendingRuns() {
			switch r.ID {
			case "r1":
				if !r.TargetClosedAt.Equal(closed) || !r.StartedAt.Equal(began) || r.StepIndex != 2 {
					t.Fatalf("%s: r1 = closed %v, started %v, step %d", when, r.TargetClosedAt, r.StartedAt, r.StepIndex)
				}
			case "r2":
				if !r.TargetClosedAt.IsZero() {
					t.Fatalf("%s: a run on another target was marked closed", when)
				}
			}
		}
	}
	check(s, "after a checkpoint")

	s2, err := Open(Options{StatePath: filepath.Join(filepath.Dir(s.runsPath), "state.json")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s2.Close() })
	check(s2, "after reopen")
}
