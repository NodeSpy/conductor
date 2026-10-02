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

// TestDetachThroughFakePaseo drives a detach launch through the real CLI
// backend against a fake `paseo` binary that logs every invocation's argv
// and CONDUCTOR_* environment, with an on-disk ownership ledger. It pins the
// whole contract at the process boundary: a fresh `workspace create
// --mode branch-off --new-branch` (no `workspace ls` adoption probe), then
// `run … --mode plan --image … --workspace <id> -d --json` with the prompt
// exactly as given, no --env at all, no CONDUCTOR_* in the environment,
// owned.json byte-for-byte unchanged, and Archive refusing the agent id.
func TestDetachThroughFakePaseo(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "paseo")
	script := `#!/usr/bin/env bash
{ printf 'ARGV'; for a in "$@"; do printf ' [%s]' "${a//$'\n'/\\n}"; done; printf '\n'; env | grep '^CONDUCTOR_' | sed 's/^/ENV /'; } >> "` + log + `"
case "$1" in
  workspace)
    case "$2" in
      create) echo '{"workspaceId":"ws-fake-1","cwd":"` + dir + `/wt"}' ;;
      *) echo '[]' ;;
    esac ;;
  run) echo '{"agentId":"ag-fake-1"}' ;;
  inspect) echo '{"Cwd":"` + dir + `/wt"}' ;;
  *) echo '{}' ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	ownedPath := filepath.Join(dir, "owned.json")
	seed := NewOwnedSet(ownedPath)
	seed.AddAgent("ag-existing")
	seed.AddWorkspace("ws-existing")
	before, err := os.ReadFile(ownedPath)
	if err != nil {
		t.Fatal(err)
	}
	owned := NewOwnedSet(ownedPath)

	d := &Dispatcher{PaseoBin: bin, Owned: owned, repoDirs: map[string]string{}}
	d.CheckoutDir = func(context.Context, string) (string, error) { return dir, nil }
	prompt := "Investigate the thread.\n----- BEGIN SLACK THREAD -----\nhi {{.gh_token}}\n----- END -----"
	req := Request{
		Trigger: core.Trigger{Source: "slack", Kind: "message_shortcut", TargetTrusted: true,
			Target: core.Target{Repo: "slack:C1", Number: 3}, Title: "the login page 500s"},
		// DecisionLaunch-free agent step: the prompt arrives pre-rendered from
		// flow; dispatch renders it once with the request data.
		Action:      config.Action{Type: "agent", Prompt: prompt, Checkout: "none"},
		Step:        config.Step{Detach: true, Repo: "acme/widgets", Mode: "plan", Images: []string{dir + "/shot.png"}},
		Credentials: Credentials{Env: map[string]string{"GH_TOKEN": "APPTOK", "PC_GH_WRITE_TOKEN": "USERTOK"}, Templates: map[string]string{"app_token": "APPTOK", "gh_token": "USERTOK"}},
		Author:      Author{Name: "Op", Email: "op@example.com"},
		Provider:    "anthropic", Model: "claude-x", DispatchID: "disp-1",
	}
	// The fake logs any CONDUCTOR_* it sees; start from an environment with
	// none, so only what dispatch itself adds could appear.
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); strings.HasPrefix(k, "CONDUCTOR_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}

	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if ref.AgentID != "ag-fake-1" || ref.WorkspaceID != "ws-fake-1" || ref.Workdir != dir+"/wt" || !strings.HasPrefix(ref.Branch, "handover/the-login-page-500s-") {
		t.Fatalf("ref: %+v", ref)
	}

	raw, _ := os.ReadFile(log)
	calls := string(raw)
	lines := strings.Split(strings.TrimSpace(calls), "\n")
	var create, run string
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "ARGV [workspace] [create]"):
			create = l
		case strings.HasPrefix(l, "ARGV [run]"):
			run = l
		case strings.HasPrefix(l, "ARGV [workspace] [ls]"):
			t.Errorf("detach must not probe for an existing workspace to adopt: %s", l)
		}
	}
	for _, want := range []string{"[--isolation] [worktree]", "[--path] [" + dir + "]", "[--mode] [branch-off]", "[--new-branch] [handover/the-login-page-500s-"} {
		if !strings.Contains(create, want) {
			t.Errorf("workspace create missing %s: %s", want, create)
		}
	}
	for _, want := range []string{"[--mode] [plan]", "[--image] [" + dir + "/shot.png]", "[--workspace] [ws-fake-1]", "[-d]", "[--json]",
		"[--provider] [anthropic]", "[--model] [claude-x]", "[--title] [the login page 500s]"} {
		if !strings.Contains(run, want) {
			t.Errorf("run missing %s: %s", want, run)
		}
	}
	if !strings.Contains(run, `[run] [Investigate the thread.\n----- BEGIN SLACK THREAD -----\nhi `) || !strings.Contains(run, `\n----- END -----] [--title]`) {
		t.Errorf("the prompt must reach paseo with nothing appended: %s", run)
	}
	for _, bad := range []string{"--env", "GH_TOKEN", "USERTOK", "APPTOK", "GIT_AUTHOR", "CONDUCTOR_", "--background",
		"step.done", "DoneGuidance", "conductor done"} {
		if strings.Contains(run, bad) {
			t.Errorf("detach run argv carries %q: %s", bad, run)
		}
	}
	if strings.Contains(calls, "\nENV ") || strings.HasPrefix(calls, "ENV ") {
		t.Errorf("a CONDUCTOR_* variable reached paseo's environment:\n%s", calls)
	}

	after, _ := os.ReadFile(ownedPath)
	if string(after) != string(before) {
		t.Fatalf("owned.json changed:\nbefore %s\nafter  %s", before, after)
	}
	if err := d.Archive(context.Background(), "ag-fake-1"); err == nil {
		t.Fatal("Archive must refuse a detached agent")
	}
	if NewOwnedSet(ownedPath).HasAgent("ag-fake-1") {
		t.Fatal("the reloaded ledger must not know the detached agent")
	}
}
