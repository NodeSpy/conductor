package dispatch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The wedged sweep reclaims a stuck CONDUCTOR-owned hand-off worktree (name
// prefix conductor/*), but MUST NOT touch a workspace conductor did not create —
// the user's own paseo worktrees, whatever their isolation or how long their
// agent has been idle. (Regression: an earlier version keyed on "any worktree"
// and archived a pile of the user's real worktrees.) Held and actively-working
// conductor agents are also spared.
func TestReapWedgedAgent(t *testing.T) {
	old := "2020-01-01T00:00:00Z"
	fresh := time.Now().UTC().Format(time.RFC3339)
	condDir := "/wt/conductor/pr5"     // conductor-created hand-off worktree
	userDir := "/home/me/paseo/mine"   // the USER's own worktree — off limits
	healthyDir := "/wt/conductor/busy" // conductor agent, actively working
	heldDir := "/wt/conductor/open"    // conductor hand-off still open (Held)

	fb := &fakeReaperBackend{
		agents: []AgentInfo{
			{ID: "a-wedged", Status: "running", Cwd: condDir},
			{ID: "a-user", Status: "closed", Cwd: userDir},
			{ID: "a-healthy", Status: "running", Cwd: healthyDir},
			{ID: "a-held", Status: "running", Cwd: heldDir},
		},
		workspaces: []WorkspaceInfo{
			{WorkspaceID: "wks_cond", Name: "conductor/fix-pr5", Cwd: condDir, Isolation: "worktree"},
			{WorkspaceID: "wks_user", Name: "mundane-koala", Cwd: userDir, Isolation: "worktree"},
			{WorkspaceID: "wks_healthy", Name: "conductor/busy", Cwd: healthyDir, Isolation: "worktree"},
			{WorkspaceID: "wks_held", Name: "conductor/open", Cwd: heldDir, Isolation: "worktree"},
		},
		details: map[string]AgentDetail{
			"a-wedged":  {CreatedAt: old, LastUsage: old},   // no model activity in ages
			"a-user":    {CreatedAt: old, LastUsage: old},   // ALSO stale — but not conductor's
			"a-healthy": {CreatedAt: old, LastUsage: fresh}, // used the model just now
			"a-held":    {CreatedAt: old, LastUsage: old},
		},
	}

	held := NewHoldSet(filepath.Join(t.TempDir(), "held.json"))
	held.Add("a-held")
	r := &Reaper{Held: held}
	r.SetBackend(fb)
	r.reap(context.Background())

	got := strings.Join(fb.archives, " ")
	if !strings.Contains(got, "workspace:wks_cond") {
		t.Errorf("a wedged conductor-owned hand-off must be reclaimed: %v", fb.archives)
	}
	for _, never := range []string{"wks_user", "a-user", "wks_healthy", "wks_held", "a-held"} {
		if strings.Contains(got, never) {
			t.Errorf("must not archive %s (user-owned / actively-working / held): %v", never, fb.archives)
		}
	}
}
