package gitwt

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/dispatch"
	sdk "github.com/NodeSpy/conductor/pkg/plugin"
)

// manualReplayReq is a dispatch from a MANUAL run (its own target synthetic,
// "manual:<name>", no PR, no declared checkout) whose step names the PR to
// check out with checkout_pr — the review-eval replay. flow.applyCheckoutPR
// leaves exactly this on the trigger.
func manualReplayReq() dispatch.Request {
	return dispatch.Request{
		DispatchID: "disp-replay",
		Action:     dispatchAction("checkout-pr"),
		Trigger: core.Trigger{
			Source: "manual", Kind: "manual", Operator: true, TargetTrusted: true,
			Target:     core.Target{Repo: "manual:review/review-eval", Key: "manual:review/review-eval", Project: "acme/web"},
			CheckoutPR: &core.PRRef{Repo: "acme/web", Number: 7},
		},
	}
}

func dispatchAction(checkout string) config.Action {
	return config.Action{Type: "agent", Checkout: checkout}
}

// recordingProv clones from origin and records which repo each clone was for.
func recordingProv(t *testing.T, origin string) (*Provisioner, *[]string) {
	t.Helper()
	p := New(t.TempDir())
	var asked []string
	p.RemoteURL = func(repo string) string { asked = append(asked, repo); return origin }
	return p, &asked
}

func TestAManualReplayChecksOutTheNamedPR(t *testing.T) {
	origin := originRepo(t)
	p, asked := recordingProv(t, origin)
	_, cwd, err := p.ProvisionWorktree(context.Background(), manualReplayReq())
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	if len(*asked) == 0 || (*asked)[0] != "acme/web" {
		t.Fatalf("cloned %v, want acme/web — never the run's own synthetic target", *asked)
	}
	if _, err := os.Stat(filepath.Join(cwd, "FEATURE.md")); err != nil {
		t.Fatalf("PR head not checked out: %v", err)
	}
	want := strings.TrimSpace(out(t, origin, "git", "rev-parse", "refs/pull/7/head"))
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "HEAD")); got != want {
		t.Fatalf("HEAD = %s, want the PR head %s", got, want)
	}
}

// Without checkout_pr the same run clones its own synthetic target — the bug.
func TestWithoutCheckoutPRAManualRunHasNoPRToCheckOut(t *testing.T) {
	origin := originRepo(t)
	p, asked := recordingProv(t, origin)
	req := manualReplayReq()
	req.Trigger.CheckoutPR = nil
	req.Trigger.Target.Project = ""
	if _, _, err := p.ProvisionWorktree(context.Background(), req); err == nil {
		t.Fatal("a manual run with no checkout_pr provisioned a PR checkout")
	}
	if len(*asked) == 0 || (*asked)[0] != "manual:review/review-eval" {
		t.Fatalf("cloned %v", *asked)
	}
}

// An event's own declared checkout describes ITS target: a checkout_pr wins
// over it (fetch ref and push branch both).
func TestCheckoutPRWinsOverTheEventsDeclaredCheckout(t *testing.T) {
	origin := originRepo(t)
	p, _ := recordingProv(t, origin)
	req := prReq()
	req.Action = dispatchAction("checkout-pr")
	req.Trigger.Sem = &sdk.EventSemantics{Checkout: &sdk.CheckoutSemantics{FetchRef: "refs/heads/main", PushBranch: "head_ref"}}
	req.Trigger.Target.Project = "acme/web"
	req.Trigger.CheckoutPR = &core.PRRef{Repo: "acme/web", Number: 7}
	_, cwd, err := p.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "FEATURE.md")); err != nil {
		t.Fatalf("checked out the event's own ref, not the checkout_pr: %v", err)
	}
}

// A manual run's context carries its inputs flattened, so an input named
// head_ref must not pick the branch a checkout_pr lands on: the branch comes
// from the PR itself (its head on the remote), never from the run's context.
func TestACheckoutPRBranchIgnoresTheRunsContext(t *testing.T) {
	origin := originRepo(t)
	p, _ := recordingProv(t, origin)
	req := manualReplayReq()
	req.Trigger.Context = map[string]any{"head_ref": "main", "inputs": map[string]any{"head_ref": "main"}}
	_, cwd, err := p.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatalf("ProvisionWorktree: %v", err)
	}
	if got := strings.TrimSpace(out(t, cwd, "git", "rev-parse", "--abbrev-ref", "HEAD")); got != "feature" {
		t.Fatalf("worktree branch = %q, want the PR's own head branch %q", got, "feature")
	}
}
