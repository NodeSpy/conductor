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

// A target the event's sender chose (a webhook body's repo) is never
// checked out from the operator's clones nor pushed to: as with its
// credentials, conductor does not act as the operator on a repo a stranger
// named. A step's own repo: is the operator's choice and stands.
func TestAnUntrustedTargetIsNeverCheckedOut(t *testing.T) {
	trig := core.Trigger{Kind: "delivery", Source: "webhook", Target: core.Target{Repo: "victim/private", Number: 1}}
	req := Request{Trigger: trig, Action: config.Action{Checkout: "branch-off"}}
	if _, err := CheckoutRepo(req); err == nil || !strings.Contains(err.Error(), "the event's sender chose this target") {
		t.Fatalf("untrusted: %v", err)
	}
	var resolved []string
	d := &Dispatcher{CheckoutDir: func(_ context.Context, repo string) (string, error) {
		resolved = append(resolved, repo)
		return t.TempDir(), nil
	}}
	if _, _, err := d.ProvisionWorktree(context.Background(), req); err == nil || len(resolved) != 0 {
		t.Fatalf("provisioned an untrusted target: %v %v", err, resolved)
	}
	req.Step.Repo = "op/own"
	if r, err := CheckoutRepo(req); err != nil || r != "victim/private" {
		// (withStepRepo has already pointed Target.Project at the step's
		// repo by the time a checkout resolves it.)
		t.Fatalf("a step's own repo: %q %v", r, err)
	}
	req.Step.Repo = ""
	req.Trigger.TargetTrusted = true
	if r, err := CheckoutRepo(req); err != nil || r != "victim/private" {
		t.Fatalf("trusted: %q %v", r, err)
	}
}

// A workspace registered for an untrusted dispatch gets no push target:
// conductor never pushes its agent's work anywhere.
func TestAnUntrustedDispatchGetsNoPushTarget(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(root, "widget")
	os.MkdirAll(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "i")
	gitIn(t, repo, "remote", "add", "origin", "git@forge.example:acme/widget.git")
	wt := filepath.Join(root, "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "fix-1", wt)
	repo = wt
	t.Cleanup(func() { agentsandbox.Default.Forget(repo) })
	e, err := RegisterSandboxWorkspace(repo, "agent:x@dispatch-u1", "claude-code", &Request{DispatchID: "u1",
		Trigger: core.Trigger{Target: core.Target{Repo: "victim/private"}}})
	if err != nil {
		t.Fatal(err)
	}
	if e.Push != nil {
		t.Fatalf("an untrusted dispatch got a push target: %+v", e.Push)
	}
	agentsandbox.Default.Forget(repo)
	e, err = RegisterSandboxWorkspace(repo, "agent:x@acme/widget", "claude-code", &Request{DispatchID: "t1",
		Trigger: core.Trigger{Target: core.Target{Repo: "acme/widget"}, TargetTrusted: true}})
	if err != nil || e.Push == nil {
		t.Fatalf("a trusted dispatch's push target: %+v %v", e.Push, err)
	}
}
