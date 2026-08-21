package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

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
