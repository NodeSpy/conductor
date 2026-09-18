package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

// Reuse is the runtime's concern, not conductor's: the bundled cliBackend (a
// paseo runtime adapter) adopts an existing branch-off worktree — paseo names it
// after its branch — and returns it, issuing no `workspace create`. Conductor's
// createWorktree just gets a working worktree back, blind to create-vs-reuse. A
// no-match (and a same-named LOCAL workspace, which must not match the worktree
// filter) falls through to a real create.
func TestCreateWorktreeReusesExistingBranchWorktree(t *testing.T) {
	req := Request{Trigger: core.Trigger{Kind: "issue_matched",
		Target: core.Target{Repo: "a/w", Number: 9, BaseRef: "main"}},
		Action: config.Action{Checkout: "branch-off"}}

	// A worktree already on the branch → adopt it, no `workspace create`.
	bin, dir := fakePaseoDir(t)
	put(t, dir, "workspaces.json",
		`[{"workspaceId":"wks_adopt","name":"conductor/issue_matched-9","isolation":"worktree","cwd":"/wt/9"}]`)
	d := &Dispatcher{PaseoBin: bin}
	id, cwd, err := d.createWorktree(context.Background(), req, "/base")
	if err != nil || id != "wks_adopt" || cwd != "/wt/9" {
		t.Fatalf("adopt: id=%q cwd=%q err=%v", id, cwd, err)
	}
	if strings.Contains(callsLog(t, dir), "workspace create") {
		t.Fatalf("adopt must not create a worktree: %s", callsLog(t, dir))
	}

	// No worktree match (wrong branch; a LOCAL workspace on the same name must
	// not match) → fall through to a real create.
	bin2, dir2 := fakePaseoDir(t)
	put(t, dir2, "workspaces.json",
		`[{"workspaceId":"wks_other","name":"conductor/issue_matched-42","isolation":"worktree","cwd":"/wt/42"},`+
			`{"workspaceId":"wks_local","name":"conductor/issue_matched-9","isolation":"local","cwd":"/x"}]`)
	put(t, dir2, "wscreate.json", `{"workspaceId":"wks_fresh","cwd":"`+dir2+`/wt"}`)
	d2 := &Dispatcher{PaseoBin: bin2}
	id, _, err = d2.createWorktree(context.Background(), req, "/base")
	if err != nil || id != "wks_fresh" {
		t.Fatalf("create: id=%q err=%v", id, err)
	}
	if !strings.Contains(callsLog(t, dir2), "workspace create") {
		t.Fatalf("no match must fall through to a real create: %s", callsLog(t, dir2))
	}
}

// Fix B: paseo's MISSING_PROVIDER is rewritten into conductor's vocabulary — the
// operator sees the two knobs that fix it, not paseo's provider concept.
func TestPaseoErrDetailTranslatesMissingProvider(t *testing.T) {
	stdout := []byte(`{"error":{"code":"MISSING_PROVIDER","message":"no provider configured"}}`)
	got := paseoErrDetail(stdout, nil)
	if got != missingProviderHelp {
		t.Fatalf("MISSING_PROVIDER not translated:\n got: %q\nwant: %q", got, missingProviderHelp)
	}
	if strings.Contains(got, "MISSING_PROVIDER") {
		t.Fatalf("translated message must not leak the raw paseo code: %q", got)
	}
	// A different coded error is still surfaced verbatim (code: message).
	other := paseoErrDetail([]byte(`{"error":{"code":"WORKSPACE_CREATE_FAILED","message":"nope"}}`), nil)
	if other != "WORKSPACE_CREATE_FAILED: nope" {
		t.Fatalf("unrelated error should pass through, got %q", other)
	}
}
