package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
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

// THE NO-PILE-UP INVARIANT. An un-pinned checkout:none run gets a workspace of
// its own, so that workspace MUST come back when the run finishes — otherwise
// every triage dispatch leaves an empty workspace behind forever. It is
// reclaimed by the same Archive path a worktree agent uses.
func TestArchiveReclaimsEphemeralRunWorkspace(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	runDir := "/home/u/.conductor/runs/cron-7-a1b2c3"
	os.WriteFile(filepath.Join(dir, "ls.json"),
		[]byte(`[{"id":"a1","status":"idle","cwd":"`+runDir+`"}]`), 0o644)
	os.WriteFile(filepath.Join(dir, "workspaces.json"),
		[]byte(`[{"workspaceId":"wks_run","name":"conductor-run-cron-7-a1b2c3","isolation":"local","cwd":"`+runDir+`"}]`), 0o644)

	d := &Dispatcher{PaseoBin: bin}
	if err := d.Archive(context.Background(), "a1"); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	if !strings.Contains(string(calls), "workspace archive wks_run") {
		t.Fatalf("an ephemeral run workspace must be reclaimed on finish, got:\n%s", calls)
	}
}

// A PINNED workspace is the opposite case: it exists precisely to outlive the
// run and be reused by the next one, so only the agent is archived. Same for a
// base checkout — never taken out from under other work.
func TestArchivePinnedWorkspaceSurvives(t *testing.T) {
	for _, tc := range []struct{ name, cwd, ws string }{
		{"pinned workspace", "/home/u/triage",
			`{"workspaceId":"wks_pin","name":"triage","isolation":"local","cwd":"/home/u/triage"}`},
		{"base checkout", "/home/u",
			`{"workspaceId":"wks_base","isolation":"local","cwd":"/home/u"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, dir := fakePaseoDir(t)
			os.WriteFile(filepath.Join(dir, "ls.json"),
				[]byte(`[{"id":"a1","status":"idle","cwd":"`+tc.cwd+`"}]`), 0o644)
			os.WriteFile(filepath.Join(dir, "workspaces.json"), []byte(`[`+tc.ws+`]`), 0o644)

			d := &Dispatcher{PaseoBin: bin}
			if err := d.Archive(context.Background(), "a1"); err != nil {
				t.Fatal(err)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
			if strings.Contains(string(calls), "workspace archive") {
				t.Fatalf("%s must NOT be archived, got:\n%s", tc.name, calls)
			}
			if !strings.Contains(string(calls), "archive a1") {
				t.Fatalf("expected a bare `archive a1`, got:\n%s", calls)
			}
		})
	}
}

// The invariant end to end, over the real dispatch path rather than a
// hand-written workspace fixture: dispatch a checkout:none step with no pin,
// then archive the agent it launched, and assert the workspace the DISPATCH
// created is the one that comes back. This is the regression test for the
// pile-up — if runWorkspace and the reclaim map ever stop agreeing on what a
// run's workspace looks like, the archive silently degrades to agent-only and
// only this test notices.
func TestCheckoutNoneDispatchThenArchiveLeavesNothingBehind(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	d := &Dispatcher{PaseoBin: bin, repoDirs: map[string]string{}}
	req := Request{
		Trigger: core.Trigger{Kind: "cron", Target: core.Target{Repo: "acme/w", Number: 7}},
		Action:  config.Action{Type: "agent", Agent: "triage", Checkout: "none", Prompt: "triage"},
	}
	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	// The run's own workspace (the fake's default create response) was pinned.
	if !strings.Contains(joined(ref.Argv), "--workspace wks_new") {
		t.Fatalf("checkout:none should run in the workspace it just created: %s", joined(ref.Argv))
	}
	// Where the dispatch actually put it — the agent reports this cwd, and it is
	// what joins it back to its workspace.
	runs, err := os.ReadDir(filepath.Join(home, ".conductor", runWorkspaceDirs))
	if err != nil || len(runs) != 1 {
		t.Fatalf("dispatch should have made one run dir: %v %v", runs, err)
	}
	runDir := filepath.Join(home, ".conductor", runWorkspaceDirs, runs[0].Name())

	// Now the daemon's view once the agent is live in that workspace.
	os.WriteFile(filepath.Join(dir, "ls.json"),
		[]byte(`[{"id":"`+ref.AgentID+`","status":"idle","cwd":"`+runDir+`"}]`), 0o644)
	os.WriteFile(filepath.Join(dir, "workspaces.json"),
		[]byte(`[{"workspaceId":"wks_new","name":"`+runWorkspacePrefix+runs[0].Name()+`","isolation":"local","cwd":"`+runDir+`"}]`), 0o644)
	os.Truncate(filepath.Join(dir, "calls.log"), 0)

	if err := d.Archive(context.Background(), ref.AgentID); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls.log"))
	if !strings.Contains(string(calls), "workspace archive wks_new") {
		t.Fatalf("the dispatch's own workspace must be reclaimed, got:\n%s", calls)
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
