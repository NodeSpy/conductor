package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// An artifact workspace is registered with the sandbox like any workspace,
// but never gets a push target — even when the .git it carried names a
// remote and sits on a branch, as a trusted checkout's would.
func TestAnArtifactWorkspaceGetsNoPushTarget(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(root, "widget")
	os.MkdirAll(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "i")
	gitIn(t, repo, "remote", "add", "origin", "git@forge.example:acme/widget.git")
	ws := filepath.Join(root, "ws")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "fix-1", ws)
	t.Cleanup(func() { agentsandbox.Default.Forget(ws) })
	trusted := core.Trigger{Target: core.Target{Repo: "acme/widget"}, TargetTrusted: true}

	e, err := RegisterSandboxWorkspace(ws, "agent:x@acme/widget", "claude-code", &Request{DispatchID: "a1", Trigger: trusted, WorkspaceArchive: "/blobs/sha256/x"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Push != nil {
		t.Fatalf("an artifact workspace got a push target: %+v", e.Push)
	}
	if got, ok := agentsandbox.Default.ForDispatch("a1"); !ok || got.Workspace != ws {
		t.Fatalf("not registered with the sandbox: %+v %v", got, ok)
	}
	// The same checkout, provisioned as a checkout, would be pushed.
	agentsandbox.Default.Forget(ws)
	if e, err = RegisterSandboxWorkspace(ws, "agent:x@acme/widget", "claude-code", &Request{DispatchID: "c1", Trigger: trusted}); err != nil || e.Push == nil {
		t.Fatalf("control: a checkout's push target: %+v %v", e.Push, err)
	}
}

// The artifact is unpacked on this machine, so the paseo path (and a remote
// launch, which falls back to it) refuses rather than ignoring it and
// handing the agent a checkout instead.
func TestPaseoRefusesAnArtifactWorkspace(t *testing.T) {
	d := &Dispatcher{CheckoutDir: func(context.Context, string) (string, error) {
		t.Fatal("resolved a checkout for an artifact workspace")
		return "", nil
	}}
	req := Request{Trigger: core.Trigger{Target: core.Target{Repo: "o/r", PR: 1}, TargetTrusted: true},
		Action: config.Action{Type: "agent", Checkout: "none"}, WorkspaceArchive: "/blobs/sha256/x"}
	if _, err := d.Dispatch(context.Background(), req); err == nil || !IsUnrecoverable(err) {
		t.Fatalf("paseo Dispatch = %v, want an Unrecoverable refusal", err)
	}
	if _, _, err := d.ProvisionWorktree(context.Background(), req); err == nil || !IsUnrecoverable(err) {
		t.Fatalf("paseo ProvisionWorktree = %v, want an Unrecoverable refusal", err)
	}
}

// workspace.inputs are copied on this machine too: the paseo path refuses a
// dispatch that carries them rather than launching without them.
func TestPaseoRefusesWorkspaceInputs(t *testing.T) {
	d := &Dispatcher{CheckoutDir: func(context.Context, string) (string, error) {
		t.Fatal("resolved a checkout for a dispatch with inputs")
		return "", nil
	}}
	req := Request{Trigger: core.Trigger{Target: core.Target{Repo: "o/r", PR: 1}, TargetTrusted: true},
		Action: config.Action{Type: "agent", Checkout: "checkout-pr"},
		Inputs: []Input{{Name: "build", Path: "/state/step-inputs/step-1/build", Kind: "dir"}}, InputsDir: "/state/step-inputs/step-1"}
	for name, call := range map[string]func() error{
		"Dispatch":          func() error { _, err := d.Dispatch(context.Background(), req); return err },
		"ProvisionWorktree": func() error { _, _, err := d.ProvisionWorktree(context.Background(), req); return err },
	} {
		if err := call(); err == nil || !IsUnrecoverable(err) || !strings.Contains(err.Error(), "workspace.inputs") {
			t.Errorf("paseo %s = %v, want an Unrecoverable workspace.inputs refusal", name, err)
		}
	}
}
