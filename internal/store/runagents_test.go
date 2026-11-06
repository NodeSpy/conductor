package store

import (
	"path/filepath"
	"testing"
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
