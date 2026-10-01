package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// fakeDetachBackend is an in-process Backend double for the detach path: it
// records exactly the CreateWorktree + RunAgent calls paseoDetached makes and
// answers them, and panics on every method a detach launch must never reach
// (listing/queueing/pinning machinery that assumes conductor keeps a
// relationship with the agent afterward).
type fakeDetachBackend struct {
	createCalls []CreateWorktreeOptions
	runCalls    []RunAgentOptions
	createErr   error
	runErr      error
	workspaceID string
	cwd         string
	agentID     string
}

func (b *fakeDetachBackend) CreateWorktree(_ context.Context, opts CreateWorktreeOptions) (CreateWorktreeResult, error) {
	b.createCalls = append(b.createCalls, opts)
	if b.createErr != nil {
		return CreateWorktreeResult{}, b.createErr
	}
	ws, cwd := b.workspaceID, b.cwd
	if ws == "" {
		ws = "wks_detach_1"
	}
	if cwd == "" {
		cwd = "/worktrees/handover-1"
	}
	return CreateWorktreeResult{WorkspaceID: ws, Cwd: cwd}, nil
}

func (b *fakeDetachBackend) RunAgent(_ context.Context, opts RunAgentOptions) (RunAgentResult, error) {
	b.runCalls = append(b.runCalls, opts)
	if b.runErr != nil {
		return RunAgentResult{}, b.runErr
	}
	id := b.agentID
	if id == "" {
		id = "ag_detach_1"
	}
	return RunAgentResult{AgentID: id, Output: `{"agentId":"` + id + `"}`}, nil
}

func (b *fakeDetachBackend) ListAgents(context.Context, map[string]string) ([]AgentInfo, error) {
	panic("detach must never list agents — there is nothing to queue/adopt onto")
}
func (b *fakeDetachBackend) Inspect(context.Context, string) (AgentDetail, error) {
	panic("detach must never inspect — verifyWorktree is a non-detach path")
}
func (b *fakeDetachBackend) ArchiveAgent(context.Context, string) error {
	panic("detach must never archive directly — Archive() itself refuses an unowned id")
}
func (b *fakeDetachBackend) ArchiveWorkspace(context.Context, string) error {
	panic("detach must never archive directly — Archive() itself refuses an unowned id")
}
func (b *fakeDetachBackend) CreateWorkspace(context.Context, CreateWorkspaceOptions) (CreateWorkspaceResult, error) {
	panic("detach always creates a WORKTREE, never a plain scratch workspace")
}
func (b *fakeDetachBackend) ListWorkspaces(context.Context) ([]WorkspaceInfo, error) {
	panic("not used")
}
func (b *fakeDetachBackend) Clone(context.Context, CloneOptions) error { panic("not used") }
func (b *fakeDetachBackend) Send(context.Context, SendOptions) (SendResult, error) {
	panic("not used")
}
func (b *fakeDetachBackend) Wait(context.Context, string) error { panic("not used") }
func (b *fakeDetachBackend) AgentLog(context.Context, string, int) (string, error) {
	panic("not used")
}

func detachRequest(owned *OwnedSet) (Request, *fakeDetachBackend) {
	return Request{
		Wait: true,
		Trigger: core.Trigger{
			Source: "slack", Kind: "message_shortcut",
			TargetTrusted: true,
			Target:        core.Target{Repo: "slack:C123", Number: 42},
		},
		Action: config.Action{Type: "agent", Prompt: "help the user with their thing", Checkout: "none"},
		Step: config.Step{
			Detach: true, Repo: "acme/widgets", Branch: "handover/ticket-9",
			Mode: "{{.slack.form.mode}}", Images: []string{"/tmp/screenshot.png"},
		},
		Data:     map[string]any{"slack": map[string]any{"form": map[string]any{"mode": "plan"}}},
		Provider: "anthropic", Model: "claude-x",
		DispatchID: "dispatch-1",
	}, &fakeDetachBackend{}
}

func newDetachDispatcher(b Backend, owned *OwnedSet) *Dispatcher {
	d := &Dispatcher{PaseoBin: "paseo", Owned: owned}
	d.SetBackend(b)
	d.CheckoutDir = func(context.Context, string) (string, error) { return "/checkouts/widgets", nil }
	return d
}

// TestDetachDispatchArgvAndNoOwnership is the mutation-gated core assertion:
// a detach launch builds the right argv (new worktree, branch-off from the
// step's own repo:, --image, --mode templated, -d) and — the point of the
// whole feature — records NEITHER the agent NOR the workspace in the
// ownership ledger, so Archive refuses the id forever.
func TestDetachDispatchArgvAndNoOwnership(t *testing.T) {
	owned := NewOwnedSet("") // in-memory
	req, backend := detachRequest(owned)
	backend.workspaceID = "wks_h1"
	backend.cwd = "/worktrees/h1"
	backend.agentID = "ag_h1"
	d := newDetachDispatcher(backend, owned)

	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if ref.AgentID != "ag_h1" {
		t.Fatalf("agent id: got %q", ref.AgentID)
	}
	if ref.WorkspaceID != "wks_h1" || ref.Workdir != "/worktrees/h1" || ref.Branch != "handover/ticket-9" {
		t.Fatalf("ref: got workspace=%q workdir=%q branch=%q", ref.WorkspaceID, ref.Workdir, ref.Branch)
	}
	if !ref.Detached {
		t.Fatal("ref.Detached must be true")
	}

	if len(backend.createCalls) != 1 {
		t.Fatalf("expected exactly one CreateWorktree call, got %d", len(backend.createCalls))
	}
	co := backend.createCalls[0]
	if co.Strategy != "branch-off" || co.NewBranch != "handover/ticket-9" || co.Isolation != "worktree" {
		t.Fatalf("CreateWorktree opts: %+v", co)
	}

	if len(backend.runCalls) != 1 {
		t.Fatalf("expected exactly one RunAgent call, got %d", len(backend.runCalls))
	}
	argv := strings.Join(backend.runCalls[0].Args, " ")
	for _, want := range []string{"--image /tmp/screenshot.png", "--mode plan", "-d", "--workspace wks_h1", "--json"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q: %s", want, argv)
		}
	}
	if strings.Contains(argv, "--background") {
		t.Errorf("detach uses -d, not --background: %s", argv)
	}

	// THE MUTATION-GATED GUARD: nothing a detach launch created is in the
	// ownership ledger, so Archive structurally refuses it.
	if owned.HasAgent("ag_h1") {
		t.Fatal("detach agent must NOT be recorded in the ownership ledger")
	}
	if owned.HasWorkspace("wks_h1") {
		t.Fatal("detach workspace must NOT be recorded in the ownership ledger")
	}
	if err := d.Archive(context.Background(), "ag_h1"); err == nil {
		t.Fatal("Archive must refuse a detached agent id")
	}
}

// TestDetachDispatchNoCondutorEnv proves a detach launch carries no
// CONDUCTOR_*/skill env and no GH_TOKEN/author identity env either — it is
// not conductor's dispatch in any observable way beyond having launched it.
func TestDetachDispatchNoConductorEnv(t *testing.T) {
	owned := NewOwnedSet("")
	req, backend := detachRequest(owned)
	req.Tokens = Tokens{App: "APPTOK", User: "USERTOK"}
	req.Author = Author{Name: "Operator", Email: "op@example.com"}
	d := newDetachDispatcher(backend, owned)

	if _, err := d.Dispatch(context.Background(), req); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	argv := strings.Join(backend.runCalls[0].Args, " ")
	for _, bad := range []string{"CONDUCTOR_", "GH_TOKEN", "GITHUB_TOKEN", "GIT_AUTHOR_NAME", "--env"} {
		if strings.Contains(argv, bad) {
			t.Errorf("detach argv must carry no conductor/git identity env, found %q: %s", bad, argv)
		}
	}
}

// TestDetachDispatchNoGuidanceAppended proves the prompt reaches paseo
// EXACTLY as templated — none of WriteWrapperGuidance/DoneGuidance/
// HandoffGuidance (flow.go's execAgent skips that whole block for a detach
// step before building the request; this asserts the dispatch layer itself
// does not append anything either).
func TestDetachDispatchNoGuidanceAppended(t *testing.T) {
	owned := NewOwnedSet("")
	req, backend := detachRequest(owned)
	req.Action.Prompt = "exactly this, nothing more"
	req.Step.Mode = "" // isolate: no template needed
	d := newDetachDispatcher(backend, owned)

	if _, err := d.Dispatch(context.Background(), req); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := backend.runCalls[0].Args[1]; got != "exactly this, nothing more" {
		t.Fatalf("prompt was altered: %q", got)
	}
}

// TestDetachDispatchRejectsBadRepo exercises the templated repo: validation.
func TestDetachDispatchRejectsBadRepo(t *testing.T) {
	owned := NewOwnedSet("")
	req, backend := detachRequest(owned)
	req.Step.Repo = "not a repo"
	d := newDetachDispatcher(backend, owned)

	if _, err := d.Dispatch(context.Background(), req); err == nil {
		t.Fatal("expected a validation error for a malformed repo:")
	}
	if len(backend.createCalls) != 0 || len(backend.runCalls) != 0 {
		t.Fatal("a rejected repo: must never reach the backend")
	}
}

// TestDetachDispatchFallsBackToTriggerRepo: with no step `repo:`, detach uses
// the trigger's own target as the checkout repo.
func TestDetachDispatchFallsBackToTriggerRepo(t *testing.T) {
	owned := NewOwnedSet("")
	req, backend := detachRequest(owned)
	req.Step.Repo = ""
	req.Trigger.Target = core.Target{Repo: "acme/fromtrigger", BaseRef: "main"}
	d := newDetachDispatcher(backend, owned)
	d.CheckoutDir = func(_ context.Context, repo string) (string, error) {
		if repo != "acme/fromtrigger" {
			t.Fatalf("resolveCheckoutDir got repo %q", repo)
		}
		return "/checkouts/fromtrigger", nil
	}

	if _, err := d.Dispatch(context.Background(), req); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if backend.createCalls[0].Path != "/checkouts/fromtrigger" {
		t.Fatalf("CreateWorktree path: %q", backend.createCalls[0].Path)
	}
	if backend.createCalls[0].BaseRef != "main" {
		t.Fatalf("BaseRef should come from the trigger when repo: is unset: %q", backend.createCalls[0].BaseRef)
	}
}

// TestDetachDispatchCreateWorktreeFailureIsUnrecoverable: a worktree-creation
// failure is an operator-facing escalation, not an ordinary step failure a
// retry loop would re-attempt forever against a repo that will never clone.
func TestDetachDispatchCreateWorktreeFailureIsUnrecoverable(t *testing.T) {
	owned := NewOwnedSet("")
	req, backend := detachRequest(owned)
	backend.createErr = errors.New("boom")
	d := newDetachDispatcher(backend, owned)

	_, err := d.Dispatch(context.Background(), req)
	if err == nil {
		t.Fatal("expected an error")
	}
	var uerr *UnrecoverableError
	if !errors.As(err, &uerr) {
		t.Fatalf("expected an Unrecoverable error, got %T: %v", err, err)
	}
}
