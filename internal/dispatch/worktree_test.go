package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

// A worktree dispatch pre-creates the isolated workspace with `paseo workspace
// create` (via the WorktreeCreator seam) and pins the agent into it with
// --workspace, instead of the `paseo run --new-workspace worktree` path that can
// silently drop the agent in $HOME with no PR checked out.
func TestPaseoPreCreatesWorktreeAndPins(t *testing.T) {
	d := newDispatcher() // DryRun: argv is built and returned, no real exec
	d.CheckoutDir = func(context.Context, string) (string, error) { return "/checkouts/acme-w", nil }
	var gotBase, gotStrat string
	d.WorktreeCreator = func(_ context.Context, req Request, baseDir string) (string, string, error) {
		gotBase, gotStrat = baseDir, effectiveStrategy(req)
		return "wks_pr5", "/home/x/.paseo/worktrees/g/pr5", nil
	}
	req := Request{
		Trigger: core.Trigger{Kind: "review_requested",
			Target: core.Target{Repo: "acme/w", Owner: "acme", Name: "w", PR: 5, Number: 5}},
		Action:  config.Action{Type: "agent", Agent: "a", Prompt: "review"},
		Profile: config.AgentProfile{Workspace: "worktree"},
	}
	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	s := strings.Join(ref.Argv, " ")
	if !strings.Contains(s, "--workspace wks_pr5") {
		t.Fatalf("agent should be pinned into the pre-created worktree; got: %s", s)
	}
	// The whole point: never the silent-fallback-prone --new-workspace path.
	for _, bad := range []string{"--new-workspace", "--worktree-mode", "--cwd"} {
		if strings.Contains(s, bad) {
			t.Fatalf("pre-created worktree dispatch must not emit %q; got: %s", bad, s)
		}
	}
	if gotBase != "/checkouts/acme-w" || gotStrat != "checkout-pr" {
		t.Fatalf("createWorktree got base=%q strat=%q, want /checkouts/acme-w + checkout-pr", gotBase, gotStrat)
	}
}

// A worktree-creation failure must surface as an error so the engine escalates +
// retries — not a silent scratch fallback (the bug this replaced).
func TestPaseoWorktreeCreateFailureIsLoud(t *testing.T) {
	d := newDispatcher()
	d.CheckoutDir = func(context.Context, string) (string, error) { return "/checkouts/acme-w", nil }
	d.WorktreeCreator = func(context.Context, Request, string) (string, string, error) {
		return "", "", fmt.Errorf("branch already checked out")
	}
	req := Request{
		Trigger: core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "acme/w", PR: 5, Number: 5}},
		Action:  config.Action{Type: "agent", Agent: "a", Prompt: "fix"},
		Profile: config.AgentProfile{Workspace: "worktree"},
	}
	if _, err := d.Dispatch(context.Background(), req); err == nil {
		t.Fatal("a worktree-creation failure must surface as an error, not a silent scratch fallback")
	}
}

func TestEffectiveStrategyInteractive(t *testing.T) {
	pr := core.Target{Repo: "a/w", PR: 7, Number: 7}
	repoOnly := core.Target{Repo: "a/w"}
	bare := core.Target{}
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{"interactive+PR+none → checkout-pr", Request{Interactive: true, Action: config.Action{Checkout: "none"}, Trigger: core.Trigger{Target: pr}}, "checkout-pr"},
		{"interactive+repo+none → branch-off", Request{Interactive: true, Action: config.Action{Checkout: "none"}, Trigger: core.Trigger{Target: repoOnly}}, "branch-off"},
		{"interactive+PR+unset → checkout-pr", Request{Interactive: true, Trigger: core.Trigger{Target: pr}}, "checkout-pr"},
		{"interactive+no-repo+none → none", Request{Interactive: true, Action: config.Action{Checkout: "none"}, Trigger: core.Trigger{Target: bare}}, "none"},
		{"non-interactive+none stays none", Request{Action: config.Action{Checkout: "none"}, Trigger: core.Trigger{Target: pr}}, "none"},
		{"interactive+explicit checkout-pr unchanged", Request{Interactive: true, Action: config.Action{Checkout: "checkout-pr"}, Trigger: core.Trigger{Target: pr}}, "checkout-pr"},
	}
	for _, c := range cases {
		if got := effectiveStrategy(c.req); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestInteractiveNeverPinsScratch(t *testing.T) {
	mk := func() *Dispatcher {
		d := newDispatcher() // DryRun: builds argv, no exec
		d.ScratchWorkspace = func(context.Context) (string, error) { return "scratch-1", nil }
		return d
	}
	// Non-interactive checkout:none with no repo → pinned to the shared scratch.
	nreq := Request{Action: config.Action{Type: "agent", Agent: "a", Checkout: "none", Prompt: "x"},
		Trigger: core.Trigger{Kind: "cron", Target: core.Target{}}}
	nref, _ := mk().Dispatch(context.Background(), nreq)
	if !strings.Contains(strings.Join(nref.Argv, " "), "--workspace scratch-1") {
		t.Fatalf("non-interactive checkout:none should pin the shared scratch; got: %s", strings.Join(nref.Argv, " "))
	}
	// Interactive checkout:none with no repo → its OWN workspace, never the scratch.
	ireq := Request{Interactive: true, Action: config.Action{Type: "agent", Agent: "a", Checkout: "none", Prompt: "x"},
		Trigger: core.Trigger{Kind: "handoff", Target: core.Target{}}}
	iref, _ := mk().Dispatch(context.Background(), ireq)
	if strings.Contains(strings.Join(iref.Argv, " "), "--workspace scratch-1") {
		t.Fatalf("interactive hand-off must NOT use the shared scratch; got: %s", strings.Join(iref.Argv, " "))
	}
	// Interactive + PR + checkout:none → upgraded to a PR worktree (PR-centric).
	preq := Request{Interactive: true, Action: config.Action{Type: "agent", Agent: "a", Checkout: "none", Prompt: "x"},
		Trigger: core.Trigger{Kind: "handoff", Target: core.Target{Repo: "acme/w", PR: 5, Number: 5}},
		Profile: config.AgentProfile{Workspace: "worktree"}}
	pref, _ := mk().Dispatch(context.Background(), preq)
	ps := strings.Join(pref.Argv, " ")
	if !strings.Contains(ps, "--worktree-mode checkout-pr") || strings.Contains(ps, "--workspace scratch-1") {
		t.Fatalf("interactive PR hand-off should get a PR worktree, not scratch; got: %s", ps)
	}
}

func TestRequestedWorktree(t *testing.T) {
	pr := Request{Trigger: core.Trigger{Target: core.Target{Repo: "a/b", PR: 7, Number: 7}}}
	if !requestedWorktree(pr) {
		t.Fatal("a PR trigger should request a worktree (checkout-pr)")
	}
	branch := Request{Trigger: core.Trigger{Target: core.Target{Repo: "a/b"}}} // repo, no PR → branch-off
	if !requestedWorktree(branch) {
		t.Fatal("a repo trigger should request a worktree (branch-off)")
	}
	none := Request{Action: config.Action{Checkout: "none"}, Trigger: core.Trigger{Target: core.Target{Repo: "a/b", PR: 7}}}
	if requestedWorktree(none) {
		t.Fatal("checkout:none must not request a worktree")
	}
}

// fakeInspect writes a stub `paseo` whose `inspect … --json` returns the given cwd
// and records any `archive <id>` call.
func fakeInspect(t *testing.T, cwd string) (bin, archiveLog string) {
	t.Helper()
	dir := t.TempDir()
	archiveLog = filepath.Join(dir, "archived.log")
	bin = filepath.Join(dir, "paseo")
	script := `#!/usr/bin/env bash
case "$1" in
  inspect) echo '{"Cwd":"` + cwd + `"}' ;;
  archive) echo "$2" >> "` + archiveLog + `" ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, archiveLog
}

func TestVerifyWorktreeDetectsHomeFallback(t *testing.T) {
	home, _ := os.UserHomeDir()
	bin, archiveLog := fakeInspect(t, home) // checkout fell back to $HOME → no worktree
	d := &Dispatcher{PaseoBin: bin}
	req := Request{Trigger: core.Trigger{Target: core.Target{Repo: "acme/w", PR: 5239, Number: 5239}}}
	ref := RunRef{AgentID: "broken"}

	err := d.verifyWorktree(context.Background(), req, &ref)
	if err == nil {
		t.Fatal("a home-fallback (no worktree) checkout must be reported as an error")
	}
	if ref.AgentID != "" {
		t.Fatal("the broken agent id should be cleared")
	}
	if b, _ := os.ReadFile(archiveLog); string(b) == "" {
		t.Fatal("the broken agent should have been archived")
	}
}

func TestVerifyWorktreeAcceptsWorktreeCwd(t *testing.T) {
	home, _ := os.UserHomeDir()
	bin, archiveLog := fakeInspect(t, filepath.Join(home, ".paseo/worktrees/abc/tall-dolphin"))
	d := &Dispatcher{PaseoBin: bin}
	req := Request{Trigger: core.Trigger{Target: core.Target{Repo: "acme/w", PR: 5239, Number: 5239}}}
	ref := RunRef{AgentID: "good"}

	if err := d.verifyWorktree(context.Background(), req, &ref); err != nil {
		t.Fatalf("an agent in a real worktree must pass: %v", err)
	}
	if ref.AgentID != "good" {
		t.Fatal("a good agent id must be preserved")
	}
	if b, _ := os.ReadFile(archiveLog); string(b) != "" {
		t.Fatal("a good agent must NOT be archived")
	}
}

func TestVerifyWorktreeSkipsForegroundAndNonWorktree(t *testing.T) {
	home, _ := os.UserHomeDir()
	bin, _ := fakeInspect(t, home)
	d := &Dispatcher{PaseoBin: bin}

	// Foreground (Wait) is never checked — even sitting in $HOME.
	fg := Request{Wait: true, Trigger: core.Trigger{Target: core.Target{Repo: "acme/w", PR: 1, Number: 1}}}
	ref := RunRef{AgentID: "x"}
	if err := d.verifyWorktree(context.Background(), fg, &ref); err != nil {
		t.Fatalf("foreground dispatch must be skipped: %v", err)
	}
	// checkout:none is never checked.
	none := Request{Action: config.Action{Checkout: "none"}, Trigger: core.Trigger{Target: core.Target{Repo: "acme/w", PR: 1, Number: 1}}}
	ref2 := RunRef{AgentID: "y"}
	if err := d.verifyWorktree(context.Background(), none, &ref2); err != nil {
		t.Fatalf("checkout:none dispatch must be skipped: %v", err)
	}
}
