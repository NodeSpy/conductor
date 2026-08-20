package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

func newDispatcher() *Dispatcher {
	return &Dispatcher{
		PaseoBin:        "paseo",
		DefaultBackends: map[string]string{"agent": "paseo", "command": "local"},
		DryRun:          true,
	}
}

func joined(argv []string) string { return strings.Join(argv, " ") }

func TestPaseoAgentArgv(t *testing.T) {
	d := newDispatcher()
	req := Request{
		Trigger: core.Trigger{
			Source: "github", Instance: "acme", Kind: "merge_conflict",
			Target: core.Target{Repo: "acme/w", Owner: "acme", Name: "w", PR: 5, Number: 5, HeadSHA: "deadbeef", BaseRef: "main"},
		},
		Action:  config.Action{Type: "agent", Agent: "fixer", Prompt: "fix {{.repo}}#{{.pr}} on {{.base}}"},
		Profile: config.AgentProfile{Provider: "claude", Model: "claude-opus", Workspace: "worktree"},
		Tokens:  Tokens{App: "APPTOK", User: "USERTOK"},
		Author:  Author{Name: "Me", Email: "me@example.com"},
	}
	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !ref.Shadowed {
		t.Fatal("dry-run should be shadowed")
	}
	s := joined(ref.Argv)
	for _, want := range []string{
		"paseo run", "fix acme/w#5 on main",
		"--provider claude", "--model claude-opus",
		"--worktree-mode checkout-pr", "--pr-number 5", "--forge github",
		"--env GH_TOKEN=APPTOK", "--env " + envGHWriteToken + "=USERTOK",
		"--env GIT_AUTHOR_NAME=Me", "--env GIT_AUTHOR_EMAIL=me@example.com",
		"--label kind=merge_conflict", "--label pr=acme/w#5", "--label head=deadbeef",
		"--background", "--json",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("argv missing %q\n got: %s", want, s)
		}
	}
}

func TestPaseoBranchOffForIssue(t *testing.T) {
	d := newDispatcher()
	req := Request{
		Trigger: core.Trigger{Kind: "issue_assigned",
			Target: core.Target{Repo: "acme/w", Issue: 9, Number: 9, BaseRef: "main"}},
		Action:  config.Action{Type: "agent", Agent: "fixer", Checkout: "branch-off", Prompt: "start"},
		Profile: config.AgentProfile{Workspace: "worktree"},
	}
	ref, _ := d.Dispatch(context.Background(), req)
	s := joined(ref.Argv)
	if !strings.Contains(s, "--worktree-mode branch-off") || !strings.Contains(s, "--base main") {
		t.Fatalf("expected branch-off worktree, got: %s", s)
	}
}

func TestLocalCommandWorkDir(t *testing.T) {
	dir := t.TempDir()
	d := &Dispatcher{DefaultBackends: map[string]string{"command": "local"}} // real exec (not dry-run)
	req := Request{
		Trigger: core.Trigger{Kind: "cron", Target: core.Target{}},
		Action:  config.Action{Type: "command", Backend: "local", WorkDir: dir, Command: []string{"pwd"}},
	}
	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(ref.Output); got != dir {
		t.Fatalf("command ran in %q, want workdir %q", got, dir)
	}
}

func TestPaseoWorkDirFlag(t *testing.T) {
	d := newDispatcher() // dry-run
	req := Request{
		Trigger: core.Trigger{Kind: "issue_assigned", Target: core.Target{Repo: "a/w", Issue: 1, Number: 1}},
		Action:  config.Action{Type: "agent", Agent: "fixer", Checkout: "none", WorkDir: "/tmp/ws", Prompt: "go"},
	}
	ref, _ := d.Dispatch(context.Background(), req)
	if !strings.Contains(joined(ref.Argv), "--cwd /tmp/ws") {
		t.Fatalf("expected --cwd flag, got: %s", joined(ref.Argv))
	}
}

func TestPaseoCheckoutPRUsesResolvedCwd(t *testing.T) {
	d := newDispatcher()
	d.CheckoutDir = func(_ context.Context, repo string) (string, error) {
		if repo != "acme/w" {
			t.Fatalf("resolver got repo %q", repo)
		}
		return "/checkouts/acme-w", nil
	}
	req := Request{
		Trigger: core.Trigger{Kind: "merge_conflict",
			Target: core.Target{Repo: "acme/w", Owner: "acme", Name: "w", PR: 5, Number: 5}},
		Action:    config.Action{Type: "agent", Agent: "fixer", Prompt: "fix"},
		Profile:   config.AgentProfile{Workspace: "worktree"},
		Workspace: "wks_should_be_ignored", // must NOT combine with --new-workspace
	}
	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	s := joined(ref.Argv)
	if !strings.Contains(s, "--cwd /checkouts/acme-w") {
		t.Errorf("expected resolved --cwd, got: %s", s)
	}
	if strings.Contains(s, "--workspace ") {
		t.Errorf("--workspace must not be combined with a worktree checkout: %s", s)
	}
	if !strings.Contains(s, "--worktree-mode checkout-pr") {
		t.Errorf("expected checkout-pr worktree, got: %s", s)
	}
}

func TestPaseoNoneUsesExistingWorkspace(t *testing.T) {
	d := newDispatcher()
	d.CheckoutDir = func(context.Context, string) (string, error) {
		t.Fatal("resolver must not run for checkout=none")
		return "", nil
	}
	req := Request{
		Trigger:   core.Trigger{Kind: "review_requested", Target: core.Target{Repo: "acme/w", PR: 5, Number: 5}},
		Action:    config.Action{Type: "agent", Agent: "assess", Checkout: "none", Prompt: "assess"},
		Workspace: "wks_base",
	}
	ref, _ := d.Dispatch(context.Background(), req)
	s := joined(ref.Argv)
	if !strings.Contains(s, "--workspace wks_base") {
		t.Errorf("checkout=none should run in the pinned workspace, got: %s", s)
	}
	if strings.Contains(s, "--cwd ") || strings.Contains(s, "--new-workspace") {
		t.Errorf("checkout=none should not create a worktree, got: %s", s)
	}
}

func TestIsTransientPaseoErr(t *testing.T) {
	transient := []string{
		"WORKSPACE_CREATE_FAILED: could not lock config file .git/config: File exists",
		"Git command timed out after 30000ms: git branch --set-upstream-to ...",
		"fatal: Unable to create '.git/index.lock': File exists",
	}
	for _, d := range transient {
		if !isTransientPaseoErr(d) {
			t.Errorf("should be transient: %q", d)
		}
	}
	permanent := []string{
		"INVALID_OPTIONS: --new-workspace and --workspace cannot be combined",
		"authentication failed",
		"",
	}
	for _, d := range permanent {
		if isTransientPaseoErr(d) {
			t.Errorf("should NOT be transient: %q", d)
		}
	}
}

func TestPaseoErrDetail(t *testing.T) {
	// paseo --json prints its error object to stdout.
	got := paseoErrDetail([]byte(`{"error":{"code":"WORKSPACE_CREATE_FAILED","message":"boom"}}`), nil)
	if got != "WORKSPACE_CREATE_FAILED: boom" {
		t.Errorf("stdout json: got %q", got)
	}
	// Otherwise fall back to stderr text.
	if got := paseoErrDetail([]byte("not json"), []byte("stderr detail\n")); got != "stderr detail" {
		t.Errorf("stderr fallback: got %q", got)
	}
}

func TestParseWorktreeWorkspaces(t *testing.T) {
	data := []byte(`[
	  {"workspaceId":"wks_wt","project":"a/w","isolation":"worktree","cwd":"/wt/one"},
	  {"workspaceId":"wks_base","project":"a/w","isolation":"local","cwd":"/home/me/w"},
	  {"workspaceId":"wks_nocwd","isolation":"worktree","cwd":""}
	]`)
	m := parseWorktreeWorkspaces(data)
	if m["/wt/one"] != "wks_wt" {
		t.Errorf("worktree should map: %v", m)
	}
	if _, ok := m["/home/me/w"]; ok {
		t.Errorf("base (local) checkout must not be archivable: %v", m)
	}
	if len(m) != 1 {
		t.Errorf("only the valid worktree should be kept: %v", m)
	}
}

func TestLocalCommandArgv(t *testing.T) {
	d := newDispatcher()
	req := Request{
		Trigger: core.Trigger{Kind: "merge_ready", Target: core.Target{Repo: "acme/w", PR: 5, Number: 5}},
		Action: config.Action{
			Type:    "command",
			Backend: "local",
			Command: []string{"gh", "pr", "merge", "{{.repo}}#{{.pr}}", "--squash"},
		},
	}
	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got := joined(ref.Argv); got != "gh pr merge acme/w#5 --squash" {
		t.Fatalf("unexpected command argv: %s", got)
	}
}
