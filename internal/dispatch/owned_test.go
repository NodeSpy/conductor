package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnedSetRecordsAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "owned.json")
	o := NewOwnedSet(path)
	o.AddWorkspace("wks_1")
	o.AddAgent("ag_1")
	o.BindDispatch("d_1", "ag_1")

	if !o.HasWorkspace("wks_1") || !o.HasAgent("ag_1") {
		t.Fatal("recorded ids must be owned")
	}
	if o.HasWorkspace("wks_other") || o.HasAgent("ag_other") {
		t.Fatal("unrecorded ids must never be owned")
	}
	if got := o.AgentForDispatch("d_1"); got != "ag_1" {
		t.Fatalf("dispatch binding: got %q", got)
	}

	// Reload from disk: the ledger survives a daemon restart.
	o2 := NewOwnedSet(path)
	if !o2.HasWorkspace("wks_1") || !o2.HasAgent("ag_1") || o2.AgentForDispatch("d_1") != "ag_1" {
		t.Fatal("ledger must survive a reload")
	}

	// forget drops the agent, its workspace, and its dispatch binding.
	o2.forget("wks_1", "ag_1")
	if o2.HasWorkspace("wks_1") || o2.HasAgent("ag_1") || o2.AgentForDispatch("d_1") != "" {
		t.Fatal("forget must drop workspace, agent, and dispatch binding")
	}
	o3 := NewOwnedSet(path)
	if o3.HasAgent("ag_1") {
		t.Fatal("forget must persist")
	}
}

func TestOwnedSetNilIsFailClosed(t *testing.T) {
	var o *OwnedSet
	o.AddWorkspace("w")
	o.AddAgent("a")
	o.BindDispatch("d", "a")
	o.forget("w", "a")
	if o.HasWorkspace("w") || o.HasAgent("a") || o.AgentForDispatch("d") != "" {
		t.Fatal("a nil ledger owns nothing")
	}
}

// THE INCIDENT REGRESSION. Conductor's reaper once archived the user's own
// paseo worktrees because eligibility was inferred from workspace shape
// instead of recorded ownership. The ownership ledger is now the archive
// chokepoint: an agent conductor did not launch is refused by Dispatcher.
// Archive no matter what its workspace looks like, and a dispatcher with no
// ledger at all refuses everything (fail closed).
func TestArchiveRefusesAgentsConductorDidNotLaunch(t *testing.T) {
	// The daemon-side view: a user agent in a user worktree — exactly the shape
	// the old sweep misjudged as reclaimable.
	bin, dir := fakePaseoDir(t)
	os.WriteFile(filepath.Join(dir, "ls.json"),
		[]byte(`[{"id":"users-agent","status":"idle","cwd":"/home/u/.paseo/worktrees/x/users-branch"}]`), 0o644)
	os.WriteFile(filepath.Join(dir, "workspaces.json"),
		[]byte(`[{"workspaceId":"wks_users","isolation":"worktree","cwd":"/home/u/.paseo/worktrees/x/users-branch"}]`), 0o644)

	// A wired ledger that does NOT contain the agent: refused.
	d := &Dispatcher{PaseoBin: bin, Owned: NewOwnedSet("")}
	err := d.Archive(context.Background(), "users-agent")
	if err == nil || !strings.Contains(err.Error(), "not launched by conductor") {
		t.Fatalf("unowned agent must be refused, got err=%v", err)
	}

	// No ledger wired at all: also refused (fail closed), never best-effort.
	d2 := &Dispatcher{PaseoBin: bin}
	if err := d2.Archive(context.Background(), "users-agent"); err == nil {
		t.Fatal("a dispatcher with no ledger must refuse every archive")
	}

	if calls := callsLog(t, dir); strings.Contains(calls, "archive") {
		t.Fatalf("no paseo archive may run for an unowned agent, got:\n%s", calls)
	}
}

// An owned agent in an UNOWNED workspace (the user's own worktree, a pinned
// workspace) archives the agent only — the workspace is untouchable even
// though the agent inside it was conductor's.
func TestArchiveOwnedAgentNeverTakesUnownedWorkspace(t *testing.T) {
	bin, dir := fakePaseoDir(t)
	os.WriteFile(filepath.Join(dir, "ls.json"),
		[]byte(`[{"id":"ag_c","status":"idle","cwd":"/home/u/.paseo/worktrees/x/users-branch"}]`), 0o644)
	os.WriteFile(filepath.Join(dir, "workspaces.json"),
		[]byte(`[{"workspaceId":"wks_users","isolation":"worktree","cwd":"/home/u/.paseo/worktrees/x/users-branch"}]`), 0o644)

	d := &Dispatcher{PaseoBin: bin, Owned: NewOwnedSet("")}
	d.Owned.AddAgent("ag_c") // conductor launched the agent…
	// …but wks_users is not in the ledger (adopted / user workspace).
	if err := d.Archive(context.Background(), "ag_c"); err != nil {
		t.Fatal(err)
	}
	calls := callsLog(t, dir)
	if strings.Contains(calls, "workspace archive") {
		t.Fatalf("an unowned workspace must never be archived, got:\n%s", calls)
	}
	if !strings.Contains(calls, "archive ag_c") {
		t.Fatalf("the owned agent itself should be archived, got:\n%s", calls)
	}
	if d.Owned.HasAgent("ag_c") {
		t.Fatal("archived agent should be forgotten from the ledger")
	}
}

func TestDispatchInFlight(t *testing.T) {
	d := &Dispatcher{}
	if d.DispatchInFlight("") || d.DispatchInFlight("d1") {
		t.Fatal("nothing is in flight initially")
	}
	d.inflight.Store("d1", true)
	if !d.DispatchInFlight("d1") {
		t.Fatal("stored dispatch should report in flight")
	}
	d.inflight.Delete("d1")
	if d.DispatchInFlight("d1") {
		t.Fatal("deleted dispatch should not report in flight")
	}
}
