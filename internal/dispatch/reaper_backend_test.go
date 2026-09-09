package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeReaperBackend is an in-process Backend double: it answers the reaper's
// list/inspect queries from fixtures and records the archives it was asked for,
// with no paseo binary anywhere. It proves the reaper's cull decisions really
// do flow through the Backend seam — a backend that never shells (an rpcBackend
// driving a conductor-paseo plugin, say) reaps the same population.
type fakeReaperBackend struct {
	agents     []AgentInfo
	workspaces []WorkspaceInfo
	details    map[string]AgentDetail
	archives   []string // "agent:<id>" / "workspace:<id>", in call order
}

func (f *fakeReaperBackend) ListAgents(_ context.Context, labels map[string]string) ([]AgentInfo, error) {
	if labels["archive"] != "1" {
		return f.agents, nil // the unfiltered listing (presentIDs / activeAgentCwds)
	}
	// Only archive_when_done agents carry archive=1; a-held (the hand-off) does not.
	var out []AgentInfo
	for _, a := range f.agents {
		if a.ID != "a-held" {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeReaperBackend) Inspect(_ context.Context, id string) (AgentDetail, error) {
	det, ok := f.details[id]
	if !ok {
		return AgentDetail{}, errors.New("no such agent")
	}
	return det, nil
}

func (f *fakeReaperBackend) ArchiveAgent(_ context.Context, id string) error {
	f.archives = append(f.archives, "agent:"+id)
	return nil
}

func (f *fakeReaperBackend) ArchiveWorkspace(_ context.Context, id string) error {
	f.archives = append(f.archives, "workspace:"+id)
	return nil
}

func (f *fakeReaperBackend) ListWorkspaces(context.Context) ([]WorkspaceInfo, error) {
	return f.workspaces, nil
}

// The reaper never launches, creates, sends or waits — these exist only to
// satisfy Backend, and failing loudly keeps it that way.
func (f *fakeReaperBackend) RunAgent(context.Context, RunAgentOptions) (RunAgentResult, error) {
	return RunAgentResult{}, errors.New("reaper must not run agents")
}

func (f *fakeReaperBackend) CreateWorktree(context.Context, CreateWorktreeOptions) (CreateWorktreeResult, error) {
	return CreateWorktreeResult{}, errors.New("reaper must not create worktrees")
}

func (f *fakeReaperBackend) CreateWorkspace(context.Context, CreateWorkspaceOptions) (CreateWorkspaceResult, error) {
	return CreateWorkspaceResult{}, errors.New("reaper must not create workspaces")
}

func (f *fakeReaperBackend) Clone(context.Context, CloneOptions) error {
	return errors.New("reaper must not clone")
}

func (f *fakeReaperBackend) Send(context.Context, SendOptions) (SendResult, error) {
	return SendResult{}, errors.New("reaper must not send")
}

func (f *fakeReaperBackend) Wait(context.Context, string) error {
	return errors.New("reaper must not wait")
}

// TestReapThroughInjectedBackend runs the TestReapScenario population through a
// non-CLI Backend and asserts the identical outcome: the finished worktree
// agent loses its workspace, the plain finished agent is archived, the
// hand-off / question-asker / spinning-up / running agents survive, and the
// idle scratch workspace is culled.
func TestReapThroughInjectedBackend(t *testing.T) {
	old := "2020-01-01T00:00:00Z"
	fresh := time.Now().UTC().Format(time.RFC3339)
	wt, scratch := "/wt/pr5", "/scratch"

	fb := &fakeReaperBackend{
		agents: []AgentInfo{
			{ID: "a-held", Status: "idle"},
			{ID: "a-ask", Status: "idle"},
			{ID: "a-done", Status: "idle", Cwd: wt},
			{ID: "a-plain", Status: "completed", Cwd: "/elsewhere"},
			{ID: "a-young", Status: "idle"},
			{ID: "a-running", Status: "running"},
			{ID: ""},
		},
		workspaces: []WorkspaceInfo{
			{WorkspaceID: "wks_wt", Cwd: wt, Isolation: "worktree"},
			{WorkspaceID: "wks_scratch", Name: scratchWorkspaceTitle, Isolation: "local", Cwd: scratch},
		},
		details: map[string]AgentDetail{
			"a-ask":   {PendingPermissions: []json.RawMessage{[]byte(`{"q":1}`)}, CreatedAt: old, LastUsage: old},
			"a-done":  {CreatedAt: old, LastUsage: old},
			"a-plain": {CreatedAt: old, LastUsage: old},
			"a-young": {CreatedAt: fresh},
		},
	}

	held := NewHoldSet(filepath.Join(t.TempDir(), "held.json"))
	held.Add("a-held")
	r := &Reaper{Held: held}
	r.SetBackend(fb)
	r.reap(context.Background())

	got := strings.Join(fb.archives, " ")
	for _, want := range []string{"workspace:wks_wt", "agent:a-plain", "workspace:wks_scratch"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in archives: %v", want, fb.archives)
		}
	}
	for _, never := range []string{"agent:a-held", "agent:a-ask", "agent:a-young", "agent:a-running", "agent:a-done"} {
		if strings.Contains(got, never) {
			t.Errorf("must not archive %s: %v", never, fb.archives)
		}
	}
	if !r.held["a-ask"] {
		t.Error("a pending permission seen over the Backend should make the agent sticky-held")
	}
	if !held.Has("a-held") {
		t.Error("the hand-off is still listed, so its hold must not be pruned")
	}
}
