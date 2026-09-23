package dispatch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A hand-off released by handoff.done that stays `running` for hours carries no
// archive=1 label (so the idle walk never lists it) and still occupies its
// workspace (so the orphan sweep skips it). The wedged sweep is the backstop:
// it reclaims a conductor-owned agent with no model activity for the grace —
// but ONLY that one. An agent actively using the model (fresh usage) and one
// still held (an open hand-off) are spared.
func TestReapWedgedAgent(t *testing.T) {
	old := "2020-01-01T00:00:00Z"
	fresh := time.Now().UTC().Format(time.RFC3339)
	wedgedDir, healthyDir, heldDir := "/wt/wedged", "/wt/healthy", "/wt/held"

	fb := &fakeReaperBackend{
		agents: []AgentInfo{
			{ID: "a-wedged", Status: "running", Cwd: wedgedDir},   // done, but stuck running for hours
			{ID: "a-healthy", Status: "running", Cwd: healthyDir}, // actively working
			{ID: "a-held", Status: "running", Cwd: heldDir},       // an open hand-off (Held)
		},
		workspaces: []WorkspaceInfo{
			{WorkspaceID: "wks_wedged", Cwd: wedgedDir, Isolation: "worktree"},
			{WorkspaceID: "wks_healthy", Cwd: healthyDir, Isolation: "worktree"},
			{WorkspaceID: "wks_held", Cwd: heldDir, Isolation: "worktree"},
		},
		details: map[string]AgentDetail{
			"a-wedged":  {CreatedAt: old, LastUsage: old},   // no model activity in ages
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
	if !strings.Contains(got, "workspace:wks_wedged") {
		t.Errorf("a wedged (stale-usage) running agent must be reclaimed: %v", fb.archives)
	}
	if strings.Contains(got, "wks_healthy") {
		t.Errorf("an actively-working agent (fresh usage) must be spared: %v", fb.archives)
	}
	if strings.Contains(got, "wks_held") || strings.Contains(got, "a-held") {
		t.Errorf("a held (open) hand-off must be spared even when its usage is stale: %v", fb.archives)
	}
}
