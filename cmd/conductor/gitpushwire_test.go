package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
	"github.com/NodeSpy/conductor/internal/flow"
)

// The signer: sandbox.push's key, else git's own ssh signing key; with
// signing on and no key, pushes are refused; sign: false pushes unsigned.
func TestPushSigner(t *testing.T) {
	gc := filepath.Join(t.TempDir(), "gitconfig")
	t.Setenv("GIT_CONFIG_GLOBAL", gc)
	os.WriteFile(gc, nil, 0o600)
	if _, err := pushSigner(&config.Config{}); err == nil || !strings.Contains(err.Error(), "no signing key") {
		t.Fatalf("signing on, no key: %v", err)
	}
	off := false
	if s, err := pushSigner(&config.Config{Sandbox: &config.SandboxConfig{Push: &config.SandboxPush{Sign: &off}}}); err != nil || s != nil {
		t.Fatalf("sign: false: %+v %v", s, err)
	}
	for _, kv := range [][2]string{{"user.name", "Op"}, {"user.email", "op@x"}, {"user.signingkey", "/k/id.pub"}} {
		exec.Command("git", "config", "--file", gc, kv[0], kv[1]).Run()
	}
	if _, err := pushSigner(&config.Config{}); err == nil {
		t.Fatal("a signing key is only git's when gpg.format is ssh")
	}
	exec.Command("git", "config", "--file", gc, "gpg.format", "ssh").Run()
	s, err := pushSigner(&config.Config{})
	if err != nil || s.Key != "/k/id.pub" || s.Name != "Op" || s.Email != "op@x" {
		t.Fatalf("git's own: %+v %v", s, err)
	}
	home, _ := os.UserHomeDir()
	s, err = pushSigner(&config.Config{Sandbox: &config.SandboxConfig{Push: &config.SandboxPush{SigningKey: "~/.ssh/c", Name: "N", Email: "e@x"}}})
	if err != nil || s.Key != filepath.Join(home, ".ssh/c") || s.Name != "N" || s.Email != "e@x" {
		t.Fatalf("sandbox.push wins: %+v %v", s, err)
	}
}

// prWorkspace is a PR worktree the way gitwt provisions one — a linked
// worktree of a base clone whose origin is a bare remote, on branch pr-1 —
// registered for dispatchID as a sandboxed dispatch running step.
func prWorkspace(t *testing.T, dispatchID string, step config.Step) (ws, remote string, git func(dir string, args ...string) string) {
	t.Helper()
	root := t.TempDir()
	git = func(dir string, args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	remote, base := filepath.Join(root, "remote.git"), filepath.Join(root, "base")
	ws = filepath.Join(root, "ws")
	git(root, "init", "-q", "--bare", remote)
	git(root, "clone", "-q", remote, base)
	git(base, "commit", "-q", "--allow-empty", "-m", "init")
	git(base, "push", "-q", "origin", "HEAD:main", "HEAD:refs/heads/pr-1")
	git(base, "fetch", "-q", "origin")
	git(base, "worktree", "add", "-q", "-B", "pr-1", ws, "origin/pr-1")
	if _, err := dispatch.RegisterSandboxWorkspace(ws, "agent:fixer@a/b", "claude-code", &dispatch.Request{DispatchID: dispatchID, Step: step}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agentsandbox.Default.Forget(ws) })
	return ws, remote, git
}

func testPusher(rows *[]map[string]any) *workPusher {
	off := false
	return &workPusher{
		cfg:   &config.Config{Sandbox: &config.SandboxConfig{Push: &config.SandboxPush{Sign: &off}}},
		audit: func(r map[string]any) { *rows = append(*rows, r) },
		logf:  func(string, ...any) {},
	}
}

// The end-of-step push's `pushed` is whether the step's work reached its
// branch: true when it pushes, true when a git.push during the step already
// did (the end-of-step push itself is then up to date), and false for the
// next step end with nothing new — the mark is consumed.
func TestEndOfStepPushReportsMidStepPush(t *testing.T) {
	var rows []map[string]any
	p := testPusher(&rows)
	ctx := context.Background()
	ws, remote, git := prWorkspace(t, "run1:address", config.Step{})

	git(ws, "commit", "-q", "--allow-empty", "-m", "fix one")
	mid, err := p.push(ctx, "run1:address") // the agent's own `conductor call git.push`
	if err != nil || mid["pushed"] != true {
		t.Fatalf("git.push: %v %v", mid, err)
	}
	end, err := p.pushAtStepEnd(ctx, "run1:address")
	if err != nil || end["up_to_date"] != true || end["pushed"] != true {
		t.Fatalf("step end after a mid-step push must report pushed (up to date): %v %v", end, err)
	}
	again, err := p.pushAtStepEnd(ctx, "run1:address")
	if err != nil || again["pushed"] != false {
		t.Fatalf("a later step end with nothing new must report pushed=false: %v %v", again, err)
	}

	git(ws, "commit", "-q", "--allow-empty", "-m", "fix two")
	end, err = p.pushAtStepEnd(ctx, "run1:address")
	if err != nil || end["pushed"] != true || end["commits"] != 1 {
		t.Fatalf("step end push: %v %v", end, err)
	}
	if head := git(ws, "rev-parse", "HEAD"); git(remote, "rev-parse", "pr-1") != head {
		t.Fatalf("remote pr-1 is not the workspace HEAD %s", head)
	}
	if p.takeLanded("run1:address") {
		t.Fatal("the step end must consume its landed mark")
	}

	// A mid-step push landed, then the remote moved under the workspace: the
	// step end is refused (never forced) and claims nothing pushed — flow
	// then reports the commit left behind.
	git(ws, "commit", "-q", "--allow-empty", "-m", "fix three")
	if _, err := p.push(ctx, "run1:address"); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	git(filepath.Dir(other), "clone", "-q", "-b", "pr-1", remote, other)
	git(other, "commit", "-q", "--allow-empty", "-m", "a human's push")
	git(other, "push", "-q", "origin", "pr-1")
	git(ws, "commit", "-q", "--allow-empty", "-m", "fix four")
	out, err := p.pushAtStepEnd(ctx, "run1:address")
	var pe *flow.PushError
	if err == nil || !errors.As(err, &pe) || out != nil {
		t.Fatalf("a refused step end must fail with nothing claimed: out=%v err=%v", out, err)
	}
	if p.takeLanded("run1:address") {
		t.Fatal("a refused step end still consumes the mark")
	}
}

// What the end-of-step push answers when it cannot push: an output_schema
// step (a review, unless expect_push) has no branch — ErrNothingToPush, but
// NOT ErrUnsandboxed, so flow fails it if it left commits; an unregistered
// dispatch is unsandboxed.
func TestEndOfStepPushNothingToPushKinds(t *testing.T) {
	var rows []map[string]any
	p := testPusher(&rows)
	ctx := context.Background()
	ws2, _, git2 := prWorkspace(t, "run2:address", config.Step{OutputSchema: map[string]any{"type": "object"}})
	start := git2(ws2, "rev-parse", "HEAD")
	git2(ws2, "commit", "-q", "--allow-empty", "-m", "the agent's fix")
	_, err := p.pushAtStepEnd(ctx, "run2:address")
	if !errors.Is(err, flow.ErrNothingToPush) || errors.Is(err, flow.ErrUnsandboxed) {
		t.Fatalf("output_schema step: want ErrNothingToPush (not ErrUnsandboxed), got %v", err)
	}
	var pe *flow.PushError
	if !errors.As(err, &pe) || pe.Since != start {
		t.Fatalf("the step end names where the workspace started (%s): %#v", start, err)
	}
	_, err = p.pushAtStepEnd(ctx, "no-such-dispatch")
	if !errors.Is(err, flow.ErrUnsandboxed) || !errors.Is(err, flow.ErrNothingToPush) {
		t.Fatalf("unregistered dispatch: want ErrUnsandboxed, got %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("nothing pushed, nothing audited here (flow audits unlanded work): %v", rows)
	}

	// expect_push makes the same output_schema step a fixer: it pushes.
	ws, _, git := prWorkspace(t, "run3:address", config.Step{OutputSchema: map[string]any{"type": "object"}, ExpectPush: true})
	git(ws, "commit", "-q", "--allow-empty", "-m", "fix")
	out, err := p.pushAtStepEnd(ctx, "run3:address")
	if err != nil || out["pushed"] != true {
		t.Fatalf("output_schema + expect_push must push: %v %v", out, err)
	}
}
