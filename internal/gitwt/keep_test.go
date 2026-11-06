package gitwt

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A worktree an interrupted step's run still records is kept across a
// restart — uncommitted work included — and handed back to the resumed step.
func TestKeptWorktreeSurvivesRestartAndIsAdopted(t *testing.T) {
	origin := originRepo(t)
	p := newProv(t, origin)
	ctx := context.Background()
	id, cwd, err := p.ProvisionWorktree(ctx, prReq())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "wip.txt"), []byte("half done"), 0o600); err != nil {
		t.Fatal(err)
	}
	touch(t, cwd, time.Now().Add(-2*time.Hour))

	// "Restart": a new provisioner over the same state dir knows no live set.
	p2 := New(p.root)
	p2.RemoteURL = p.RemoteURL
	p2.MinAge = time.Hour
	p2.Keep = func(path string) bool { return path == id }
	p2.Reap(ctx)
	if _, err := os.Stat(filepath.Join(cwd, "wip.txt")); err != nil {
		t.Fatalf("the reaper took a kept worktree: %v", err)
	}

	got, err := p2.AdoptWorktree(ctx, prReq(), id)
	if err != nil || got != cwd {
		t.Fatalf("AdoptWorktree = %q, %v; want %q", got, err, cwd)
	}
	// Adopted, it is live: released like any other on Close.
	p2.Keep = nil
	p2.Reap(ctx)
	if _, err := os.Stat(cwd); err != nil {
		t.Fatalf("an adopted worktree is live: %v", err)
	}
	if err := p2.RemoveWorktree(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatalf("adopted worktree not released: %v", err)
	}

	// Once its run is gone it is an orphan again, and nothing is adopted
	// that is not there or not ours.
	if _, err := p2.AdoptWorktree(ctx, prReq(), id); err == nil {
		t.Fatal("adopted a worktree that is gone")
	}
	if _, err := p2.AdoptWorktree(ctx, prReq(), t.TempDir()); err == nil {
		t.Fatal("adopted a directory outside <state>/worktrees")
	}
}
