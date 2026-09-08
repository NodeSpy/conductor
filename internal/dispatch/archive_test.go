package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A finished agent living in an isolated worktree is reclaimed by archiving the
// WORKSPACE (worktree + agent together), never the agent alone — otherwise the
// worktree is orphaned forever (the archived agent leaves `paseo ls`, so the
// reaper can't map it back to its workspace).
func TestArchiveReclaimsWorktreeWorkspace(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	wt := "/home/u/.paseo/worktrees/abc/ids-per-datasource-storage"
	os.WriteFile(filepath.Join(dir, "ls.json"),
		[]byte(`[{"id":"a1","status":"idle","cwd":"`+wt+`"}]`), 0o644)
	os.WriteFile(filepath.Join(dir, "workspaces.json"),
		[]byte(`[{"workspaceId":"wks_wt","isolation":"worktree","cwd":"`+wt+`"}]`), 0o644)

	d := &Dispatcher{PaseoBin: bin}
	if err := d.Archive(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	if !strings.Contains(string(calls), "workspace archive wks_wt") {
		t.Fatalf("expected `workspace archive wks_wt`, got:\n%s", calls)
	}
}

// An agent in a shared/base checkout (checkout:none — the scratch workspace) is
// NOT in a worktree, so it archives just the agent; the shared workspace is left
// for cullScratch, never taken out from under other work.
func TestArchiveScratchAgentArchivesAgentOnly(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	scratch := "/home/u"
	os.WriteFile(filepath.Join(dir, "ls.json"),
		[]byte(`[{"id":"a1","status":"idle","cwd":"`+scratch+`"}]`), 0o644)
	os.WriteFile(filepath.Join(dir, "workspaces.json"),
		[]byte(`[{"workspaceId":"wks_scratch","isolation":"local","cwd":"`+scratch+`"}]`), 0o644)

	d := &Dispatcher{PaseoBin: bin}
	if err := d.Archive(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	if strings.Contains(string(calls), "workspace archive") {
		t.Fatalf("a scratch agent must NOT trigger a workspace archive, got:\n%s", calls)
	}
	if !strings.Contains(string(calls), "archive a1") {
		t.Fatalf("expected a bare `archive a1`, got:\n%s", calls)
	}
}

// A blank id is a no-op — no paseo calls at all.
func TestArchiveBlankIDNoop(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	d := &Dispatcher{PaseoBin: bin}
	if err := d.Archive(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "calls.log")); len(b) != 0 {
		t.Fatalf("blank id must make no calls, got:\n%s", b)
	}
}
