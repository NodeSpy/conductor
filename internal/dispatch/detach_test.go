package dispatch

import (
	"context"
	"errors"
	"fmt"
	"github.com/NodeSpy/conductor/internal/agentsandbox"
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
func (b *fakeDetachBackend) ListProjects(context.Context) ([]ProjectInfo, error) {
	panic("not used")
}
func (b *fakeDetachBackend) RemoveProject(context.Context, string) error { panic("not used") }
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
			// mode:/images: arrive ALREADY RENDERED (flow.renderLaunchFields).
			Mode: "plan", Images: []string{"/tmp/screenshot.png"},
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
	req.Credentials = Credentials{Env: map[string]string{"GH_TOKEN": "APPTOK", "PC_GH_WRITE_TOKEN": "USERTOK"}, Templates: map[string]string{"app_token": "APPTOK", "gh_token": "USERTOK"}}
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

// TestDetachDispatchStripsCredentialFromPrompt proves a detached launch strips
// conductor's credentials BEFORE the prompt is rendered, so a {{.<credential>}}
// template alias in the prompt resolves to nothing — the secret never reaches
// the agent through the prompt, the way TestDetachDispatchNoConductorEnv proves
// it never reaches it through env. This is the other half of detach's "no
// conductor credential reaches a detached launch in any form".
func TestDetachDispatchStripsCredentialFromPrompt(t *testing.T) {
	owned := NewOwnedSet("")
	req, backend := detachRequest(owned)
	const secret = "SECRET-APP-TOKEN-xyz"
	req.Action.Prompt = "deploy with token {{.app_token}}"
	req.Credentials = Credentials{Templates: map[string]string{"app_token": secret}}
	d := newDetachDispatcher(backend, owned)

	if _, err := d.Dispatch(context.Background(), req); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	prompt := backend.runCalls[0].Args[1]
	if strings.Contains(prompt, secret) {
		t.Fatalf("detach leaked a credential into the rendered prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "deploy with token") {
		t.Fatalf("the prompt body was lost entirely: %q", prompt)
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

// TestDetachAsksForFreshWorktree: a detach launch never adopts a workspace
// already on its branch — CreateWorktreeOptions.Fresh is set, and the cli
// backend's adopt-existing path is skipped for it.
func TestDetachAsksForFreshWorktree(t *testing.T) {
	owned := NewOwnedSet("")
	req, backend := detachRequest(owned)
	d := newDetachDispatcher(backend, owned)
	if _, err := d.Dispatch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !backend.createCalls[0].Fresh {
		t.Fatal("detach must request a fresh worktree (Fresh: true)")
	}
}

// TestDetachRefusesOwnedWorkspace: if the backend nonetheless hands back a
// workspace the ownership ledger records, the detached agent is not launched
// into it.
func TestDetachRefusesOwnedWorkspace(t *testing.T) {
	owned := NewOwnedSet("")
	owned.AddWorkspace("wks_conductor")
	req, backend := detachRequest(owned)
	backend.workspaceID = "wks_conductor"
	d := newDetachDispatcher(backend, owned)
	if _, err := d.Dispatch(context.Background(), req); err == nil {
		t.Fatal("expected a refusal for a conductor-owned workspace")
	}
	if len(backend.runCalls) != 0 {
		t.Fatal("no agent may be launched into a conductor-owned workspace")
	}
}

// TestDetachDerivedBranchIsUniqueAndValidated: with no branch:, the derived
// branch is a slug of the title plus a random suffix (two launches never
// collide); an explicit branch: that is not a sane ref is refused.
func TestDetachDerivedBranchIsUniqueAndValidated(t *testing.T) {
	req := Request{Step: config.Step{Agent: "Fix the Login Page!"}}
	a, err := detachBranch(req, detachTitle(req))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := detachBranch(req, detachTitle(req))
	if !strings.HasPrefix(a, "handover/fix-the-login-page-") || a == b {
		t.Fatalf("derived branches: %q %q", a, b)
	}
	for _, bad := range []string{"-x", "a..b", "a b", "a/", "x.lock", "--force", "a//b", "../x"} {
		req.Step.Branch = bad
		if _, err := detachBranch(req, "t"); err == nil {
			t.Errorf("branch %q should be refused", bad)
		}
	}
}

// TestValidateRepoRef covers the accepted and refused repo: shapes.
func TestValidateRepoRef(t *testing.T) {
	for _, ok := range []string{"acme/widgets", "a.b/c-d_e", "https://github.com/a/b.git", "git@github.com:a/b.git", "ssh://git@host/a/b"} {
		if err := ValidateRepoRef(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "nope", "a/b/c", "-a/b", "../a", "a/..", "file:///etc", "https://x y/a", "slack:C1"} {
		if err := ValidateRepoRef(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

// TestStepRepoOverridesCheckout: a NON-detach agent step's repo: changes
// only the checkout — resolveCheckoutDir sees the step's repo, the strategy
// defaults to branch-off, the trigger's PR/base are not carried into the
// other repo — and the agent stays owned as usual.
func TestStepRepoOverridesCheckout(t *testing.T) {
	d := newDispatcher()
	var gotRepo string
	d.CheckoutDir = func(_ context.Context, repo string) (string, error) {
		gotRepo = repo
		return "/checkouts/api", nil
	}
	var gotStrat string
	var gotPR int
	d.WorktreeCreator = func(_ context.Context, req Request, _ string) (string, string, error) {
		gotStrat, gotPR = effectiveStrategy(req), req.Trigger.Target.PR
		return "wks_api", "/wt/api", nil
	}
	req := Request{
		Trigger: core.Trigger{Kind: "review_requested", TargetTrusted: true,
			Target: core.Target{Repo: "acme/web", PR: 5, Number: 5, BaseRef: "main"}},
		Action: config.Action{Type: "agent", Prompt: "go"},
		Step:   config.Step{Repo: "acme/api"},
	}
	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if gotRepo != "acme/api" || gotStrat != "branch-off" || gotPR != 0 {
		t.Fatalf("checkout repo=%q strat=%q pr=%d", gotRepo, gotStrat, gotPR)
	}
	if !strings.Contains(strings.Join(ref.Argv, " "), "--workspace wks_api") {
		t.Fatalf("argv: %v", ref.Argv)
	}
	req.Step.Repo = "not a repo"
	if _, err := d.Dispatch(context.Background(), req); err == nil {
		t.Fatal("a malformed repo: must be refused after templating")
	}
}

// A detached agent is seeded from content conductor does not vouch for (a
// Slack thread carries other people's messages), so with the sandbox on it
// launches through the provider's sandboxed entry like a hand-off; the
// operator's isolation: {mode: none} launches the plain provider. Either
// verdict is in its title, on its labels and in the log — and it stays
// unregistered, unowned and token-free.
func TestDetachSandboxDefaultAndOptOut(t *testing.T) {
	oldS, oldL := PaseoSandboxed, PaseoSandboxLog
	t.Cleanup(func() { PaseoSandboxed, PaseoSandboxLog = oldS, oldL })
	var logs []string
	PaseoSandboxLog = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	launch := func(mut func(*Request)) string {
		t.Helper()
		owned := NewOwnedSet("")
		req, backend := detachRequest(owned)
		backend.workspaceID, backend.cwd, backend.agentID = "wks_s", "/worktrees/s", "ag_s"
		if mut != nil {
			mut(&req)
		}
		if _, err := newDetachDispatcher(backend, owned).Dispatch(context.Background(), req); err != nil {
			t.Fatal(err)
		}
		if owned.HasAgent("ag_s") || owned.HasWorkspace("wks_s") {
			t.Fatal("a detached launch is never owned")
		}
		if _, ok := agentsandbox.Default.Lookup("/worktrees/s"); ok {
			t.Fatal("a detached workspace is not registered: its sandbox gets the default policy")
		}
		return strings.Join(backend.runCalls[0].Args, "\x00") + "\x00"
	}
	PaseoSandboxed = true
	argv := launch(nil)
	for _, want := range []string{"--provider\x00anthropic-sandboxed\x00", " [sandboxed]\x00", "--label\x00sandbox=on\x00"} {
		if !strings.Contains(argv, want) {
			t.Errorf("default detach lacks %q: %q", want, argv)
		}
	}
	if strings.Contains(argv, "CONDUCTOR_") {
		t.Fatal("still no conductor env")
	}
	argv = launch(func(r *Request) { r.Provider = "" })
	if !strings.Contains(argv, "--provider\x00claude-sandboxed\x00") {
		t.Fatalf("no provider: the default one's sandboxed entry, never paseo's bare default: %q", argv)
	}
	argv = launch(func(r *Request) { r.Step.Isolation = &config.IsolationConfig{Mode: "none"} })
	for _, want := range []string{"--provider\x00anthropic\x00", " [UNSANDBOXED]\x00", "--label\x00sandbox=off\x00"} {
		if !strings.Contains(argv, want) {
			t.Errorf("opted-out detach lacks %q: %q", want, argv)
		}
	}
	if strings.Contains(argv, "-sandboxed") {
		t.Fatalf("opted out: no sandboxed entry: %q", argv)
	}
	if len(logs) != 3 || !strings.HasPrefix(logs[0], "paseo detached launch ") || !strings.Contains(logs[0], ": sandboxed (provider anthropic-sandboxed)") ||
		!strings.Contains(logs[2], ": UNSANDBOXED — isolation: {mode: none}") || !strings.Contains(logs[2], "plain provider anthropic") || strings.Contains(logs[0], "[sandboxed]") {
		t.Fatalf("logs: %q", logs)
	}
	// The sandbox off: exactly as before — no mark, no label, the request's
	// own provider, no log.
	PaseoSandboxed = false
	argv = launch(nil)
	if strings.Contains(argv, "sandboxed") || strings.Contains(argv, "sandbox=") || !strings.Contains(argv, "--provider\x00anthropic\x00") || len(logs) != 3 {
		t.Fatalf("sandbox off: %q", argv)
	}
	if argv = launch(func(r *Request) { r.Provider = "" }); strings.Contains(argv, "--provider") {
		t.Fatalf("sandbox off, no provider: paseo's default, as before: %q", argv)
	}
	// A shadow (preview) shows the same verdict and logs nothing.
	PaseoSandboxed = true
	owned := NewOwnedSet("")
	req, backend := detachRequest(owned)
	req.Shadow = true
	ref, err := newDetachDispatcher(backend, owned).Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if p := strings.Join(ref.Argv, " "); !strings.Contains(p, "--provider anthropic-sandboxed") || !strings.Contains(p, "[sandboxed]") || len(logs) != 3 {
		t.Fatalf("shadow: %s / %q", p, logs)
	}
}
