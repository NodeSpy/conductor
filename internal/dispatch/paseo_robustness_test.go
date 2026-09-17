package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// fakeDispatchBackend is an in-process Backend double for the paseo() dispatch
// path: it serves ListWorkspaces/CreateWorktree/RunAgent from configured fields
// and records the workspace archives it was asked for, so a test can assert the
// teardown-on-failure (Fix A) and adopt-instead-of-collide (Fix D) behavior
// without a paseo binary. Methods the branch-off dispatch never reaches return
// benign zeros rather than panicking.
type fakeDispatchBackend struct {
	workspaces   []WorkspaceInfo
	createResult CreateWorktreeResult
	createErr    error
	createCalls  int
	runResult    RunAgentResult
	runErr       error
	archives     []string // workspace ids, in call order
}

func (f *fakeDispatchBackend) RunAgent(context.Context, RunAgentOptions) (RunAgentResult, error) {
	return f.runResult, f.runErr
}
func (f *fakeDispatchBackend) ListAgents(context.Context, map[string]string) ([]AgentInfo, error) {
	return nil, nil
}
func (f *fakeDispatchBackend) Inspect(context.Context, string) (AgentDetail, error) {
	return AgentDetail{}, nil
}
func (f *fakeDispatchBackend) ArchiveAgent(context.Context, string) error { return nil }
func (f *fakeDispatchBackend) ArchiveWorkspace(_ context.Context, id string) error {
	f.archives = append(f.archives, id)
	return nil
}
func (f *fakeDispatchBackend) CreateWorktree(context.Context, CreateWorktreeOptions) (CreateWorktreeResult, error) {
	f.createCalls++
	return f.createResult, f.createErr
}
func (f *fakeDispatchBackend) CreateWorkspace(context.Context, CreateWorkspaceOptions) (CreateWorkspaceResult, error) {
	return CreateWorkspaceResult{}, nil
}
func (f *fakeDispatchBackend) ListWorkspaces(context.Context) ([]WorkspaceInfo, error) {
	return f.workspaces, nil
}
func (f *fakeDispatchBackend) Clone(context.Context, CloneOptions) error { return nil }
func (f *fakeDispatchBackend) Send(context.Context, SendOptions) (SendResult, error) {
	return SendResult{}, nil
}
func (f *fakeDispatchBackend) Wait(context.Context, string) error { return nil }
func (f *fakeDispatchBackend) AgentLog(context.Context, string, int) (string, error) {
	return "", nil
}

func branchOffReq() Request {
	return Request{
		Trigger: core.Trigger{Kind: "merge_conflict",
			Target: core.Target{Repo: "a/w", PR: 7, Number: 7, BaseRef: "main"}},
		Action: config.Action{Type: "agent", Prompt: "fix it", Checkout: "branch-off"},
		Step:   config.Step{Runtime: "paseo"},
	}
}

func dispatcherWith(fb Backend, checkout string) *Dispatcher {
	d := &Dispatcher{CheckoutDir: func(context.Context, string) (string, error) { return checkout, nil }}
	d.SetBackend(fb)
	return d
}

// Fix A: a dispatch whose own launch fails after it CREATED a worktree tears
// that workspace down — otherwise it orphans (agent-less → invisible to the
// reaper's agent walk, and its deterministic branch collides with the retry).
func TestDispatchTeardownArchivesCreatedWorkspaceOnFailure(t *testing.T) {
	fb := &fakeDispatchBackend{
		createResult: CreateWorktreeResult{WorkspaceID: "wks_created", Cwd: t.TempDir()},
		runErr:       errors.New("paseo run: MISSING_PROVIDER"),
	}
	d := dispatcherWith(fb, t.TempDir())

	if _, err := d.paseo(context.Background(), branchOffReq()); err == nil {
		t.Fatal("expected the failed launch to surface an error")
	}
	if len(fb.archives) != 1 || fb.archives[0] != "wks_created" {
		t.Fatalf("failed dispatch must archive the workspace it created, got %v", fb.archives)
	}
}

// Fix D + A: when a same-branch worktree already exists, the dispatch ADOPTS it
// (no CreateWorktree) and launches into it — and if THAT launch fails, the
// adopted workspace is left alone (it isn't ours to reclaim; it may hold another
// live agent). The reaper's orphan sweep is the backstop for a truly dead one.
func TestDispatchReusesExistingWorkspaceAndSpareItOnFailure(t *testing.T) {
	fb := &fakeDispatchBackend{
		workspaces: []WorkspaceInfo{
			{WorkspaceID: "wks_existing", Name: "conductor/merge_conflict-7",
				Isolation: "worktree", Cwd: t.TempDir()},
		},
		runErr: errors.New("paseo run: boom"),
	}
	d := dispatcherWith(fb, t.TempDir())

	if _, err := d.paseo(context.Background(), branchOffReq()); err == nil {
		t.Fatal("expected the failed launch to surface an error")
	}
	if fb.createCalls != 0 {
		t.Fatalf("an existing same-branch worktree must be adopted, not re-created (createCalls=%d)", fb.createCalls)
	}
	if len(fb.archives) != 0 {
		t.Fatalf("a reused workspace must never be archived on our failure, got %v", fb.archives)
	}
}

// Fix D (unit): createWorktree adopts an existing conductor worktree whose paseo
// workspace name matches the deterministic branch, returning it with
// created=false and issuing no CreateWorktree call.
func TestCreateWorktreeAdoptsExistingBranchWorkspace(t *testing.T) {
	req := Request{Trigger: core.Trigger{Kind: "issue_matched",
		Target: core.Target{Repo: "a/w", Number: 9, BaseRef: "main"}},
		Action: config.Action{Checkout: "branch-off"}}

	// Match present → adopt.
	fb := &fakeDispatchBackend{workspaces: []WorkspaceInfo{
		{WorkspaceID: "wks_adopt", Name: "conductor/issue_matched-9", Isolation: "worktree", Cwd: "/wt/9"},
	}}
	d := &Dispatcher{}
	d.SetBackend(fb)
	id, cwd, created, err := d.createWorktree(context.Background(), req, "/base")
	if err != nil || id != "wks_adopt" || cwd != "/wt/9" || created {
		t.Fatalf("adopt: id=%q cwd=%q created=%v err=%v", id, cwd, created, err)
	}
	if fb.createCalls != 0 {
		t.Fatalf("adopt must not call CreateWorktree (calls=%d)", fb.createCalls)
	}

	// No match (wrong branch, and a local workspace named the same) → create.
	fb2 := &fakeDispatchBackend{
		workspaces: []WorkspaceInfo{
			{WorkspaceID: "wks_other", Name: "conductor/issue_matched-42", Isolation: "worktree", Cwd: "/wt/42"},
			{WorkspaceID: "wks_local", Name: "conductor/issue_matched-9", Isolation: "local", Cwd: "/x"},
		},
		createResult: CreateWorktreeResult{WorkspaceID: "wks_fresh", Cwd: "/wt/new"},
	}
	d2 := &Dispatcher{}
	d2.SetBackend(fb2)
	id, cwd, created, err = d2.createWorktree(context.Background(), req, "/base")
	if err != nil || id != "wks_fresh" || cwd != "/wt/new" || !created {
		t.Fatalf("create: id=%q cwd=%q created=%v err=%v", id, cwd, created, err)
	}
	if fb2.createCalls != 1 {
		t.Fatalf("no worktree match must fall through to one CreateWorktree (calls=%d)", fb2.createCalls)
	}
}

// Fix B: paseo's MISSING_PROVIDER is rewritten into conductor's vocabulary — the
// operator sees the two knobs that fix it, not paseo's provider concept.
func TestPaseoErrDetailTranslatesMissingProvider(t *testing.T) {
	stdout := []byte(`{"error":{"code":"MISSING_PROVIDER","message":"no provider configured"}}`)
	got := paseoErrDetail(stdout, nil)
	if got != missingProviderHelp {
		t.Fatalf("MISSING_PROVIDER not translated:\n got: %q\nwant: %q", got, missingProviderHelp)
	}
	if strings.Contains(got, "MISSING_PROVIDER") {
		t.Fatalf("translated message must not leak the raw paseo code: %q", got)
	}
	// A different coded error is still surfaced verbatim (code: message).
	other := paseoErrDetail([]byte(`{"error":{"code":"WORKSPACE_CREATE_FAILED","message":"nope"}}`), nil)
	if other != "WORKSPACE_CREATE_FAILED: nope" {
		t.Fatalf("unrelated error should pass through, got %q", other)
	}
}
