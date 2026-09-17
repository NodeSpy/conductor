package dispatch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestReapOrphanWorkspaces proves the workspace-anchored sweep (Fix C) archives
// exactly the conductor-owned, agent-less, aged workspaces — and nothing else:
// not a fresh one (may be mid-create), not one an agent is in (the agent-anchored
// walk owns it), and not a worktree the operator made by hand (name is a prompt
// title, not a conductor/ branch).
func TestReapOrphanWorkspaces(t *testing.T) {
	aged := func() string {
		d := t.TempDir()
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(d, old, old); err != nil {
			t.Fatal(err)
		}
		return d
	}
	orphanBranch := aged() // conductor branch worktree, no agent, aged → reap
	orphanRun := aged()    // ephemeral run workspace, no agent, aged → reap
	freshBranch := t.TempDir()
	occupied := aged() // conductor branch worktree, but an agent is in it → spare
	userMade := aged() // worktree, aged, no agent — but NOT conductor-owned → spare

	fb := &fakeReaperBackend{
		agents: []AgentInfo{{ID: "a-live", Status: "idle", Cwd: occupied}},
		workspaces: []WorkspaceInfo{
			{WorkspaceID: "wks_orphan", Name: "conductor/merge_conflict-7", Isolation: "worktree", Cwd: orphanBranch},
			{WorkspaceID: "wks_run", Name: runWorkspacePrefix + "cron-1-abc", Isolation: "local", Cwd: orphanRun},
			{WorkspaceID: "wks_fresh", Name: "conductor/failing_checks-9", Isolation: "worktree", Cwd: freshBranch},
			{WorkspaceID: "wks_occupied", Name: "conductor/new_comment-3", Isolation: "worktree", Cwd: occupied},
			{WorkspaceID: "wks_user", Name: "add a docker plugin with verbs", Isolation: "worktree", Cwd: userMade},
			{WorkspaceID: "wks_gone", Name: "conductor/changes_requested-1", Isolation: "worktree", Cwd: "/nonexistent/path"},
		},
	}
	r := &Reaper{OrphanMinAge: time.Minute}
	r.SetBackend(fb)
	r.reapOrphanWorkspaces(context.Background())

	got := strings.Join(fb.archives, " ")
	for _, want := range []string{"wks_orphan", "wks_run"} {
		if !strings.Contains(got, want) {
			t.Errorf("orphan sweep must archive %s, got %v", want, fb.archives)
		}
	}
	for _, never := range []string{"wks_fresh", "wks_occupied", "wks_user", "wks_gone"} {
		if strings.Contains(got, never) {
			t.Errorf("orphan sweep must NOT archive %s, got %v", never, fb.archives)
		}
	}
}

// A remote reaper leaves the orphan sweep to the agent-anchored walk: the mtime
// probe is a local filesystem stat, and a remote runtime's worktree lives on its
// own box.
func TestReapOrphanWorkspacesSkippedWhenRemote(t *testing.T) {
	aged := t.TempDir()
	old := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(aged, old, old)
	fb := &fakeReaperBackend{
		workspaces: []WorkspaceInfo{
			{WorkspaceID: "wks_orphan", Name: "conductor/merge_conflict-7", Isolation: "worktree", Cwd: aged},
		},
	}
	r := &Reaper{OrphanMinAge: time.Minute, Remote: remoteTarget()}
	r.SetBackend(fb)
	r.reapOrphanWorkspaces(context.Background())
	if len(fb.archives) != 0 {
		t.Fatalf("remote reaper must not run the local orphan sweep, got %v", fb.archives)
	}
}
