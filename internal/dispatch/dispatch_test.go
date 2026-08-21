package dispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
)

func newDispatcher() *Dispatcher {
	return &Dispatcher{PaseoBin: "paseo", DryRun: true}
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
	d := &Dispatcher{} // real exec (not dry-run); command routes to local by default
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

func TestCheckoutUsesTargetProject(t *testing.T) {
	// When Target.Project is set (an integration remapped the repo to an existing
	// paseo project), checkout resolution uses it instead of the forge Repo.
	d := newDispatcher()
	var got string
	d.CheckoutDir = func(_ context.Context, repo string) (string, error) {
		got = repo
		return "/checkouts/rosterstream", nil
	}
	req := Request{
		Trigger: core.Trigger{Kind: "merge_conflict",
			Target: core.Target{Repo: "EdnitionCode/RosterStream", Project: "ednition/rosterstream", PR: 5, Number: 5}},
		Action:  config.Action{Type: "agent", Agent: "fixer", Prompt: "fix"},
		Profile: config.AgentProfile{Workspace: "worktree"},
	}
	ref, err := d.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ednition/rosterstream" {
		t.Fatalf("resolver should receive mapped project, got %q", got)
	}
	if !strings.Contains(joined(ref.Argv), "--cwd /checkouts/rosterstream") {
		t.Fatalf("expected mapped checkout cwd, got: %s", joined(ref.Argv))
	}
}

func TestPaseoNoneUsesScratchWorkspace(t *testing.T) {
	// checkout:none with no pinned workspace reuses the shared scratch workspace
	// instead of letting paseo spawn (and leak) a throwaway one.
	d := newDispatcher()
	called := 0
	d.ScratchWorkspace = func(context.Context) (string, error) { called++; return "wks_scratch", nil }
	req := Request{
		Trigger: core.Trigger{Kind: "review_requested", Target: core.Target{Repo: "acme/w", PR: 6, Number: 6}},
		Action:  config.Action{Type: "agent", Agent: "assess", Checkout: "none", Prompt: "assess"},
	}
	ref, _ := d.Dispatch(context.Background(), req)
	s := joined(ref.Argv)
	if !strings.Contains(s, "--workspace wks_scratch") {
		t.Fatalf("checkout:none should reuse the scratch workspace, got: %s", s)
	}
	if strings.Contains(s, "--new-workspace") {
		t.Fatalf("checkout:none must not create a worktree: %s", s)
	}
	if called != 1 {
		t.Fatalf("scratch resolver should be consulted once, got %d", called)
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

func TestHoldMarkerPresent(t *testing.T) {
	dir := t.TempDir()
	if holdMarkerPresent(dir) {
		t.Fatal("no marker yet")
	}
	if err := os.WriteFile(filepath.Join(dir, HoldMarker), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !holdMarkerPresent(dir) {
		t.Fatal("marker should be detected")
	}
	if holdMarkerPresent("") {
		t.Fatal("empty cwd is not held")
	}
}

func TestReaperMarkAndSpareStaysHeld(t *testing.T) {
	r := &Reaper{}
	// Not holding, never held → not spared.
	if spared, _ := r.markAndSpare("a", false); spared {
		t.Fatal("agent that never interacted should not be spared")
	}
	// First time it holds → spared, firstHold=true.
	if spared, first := r.markAndSpare("a", true); !spared || !first {
		t.Fatalf("first hold should spare with firstHold=true, got spared=%v first=%v", spared, first)
	}
	// Once held, it stays spared even when the question is answered (holdingNow=false),
	// and firstHold is false on subsequent polls.
	if spared, first := r.markAndSpare("a", false); !spared || first {
		t.Fatalf("held agent must stay spared after answering, got spared=%v first=%v", spared, first)
	}
	// A different agent is independent.
	if spared, _ := r.markAndSpare("b", false); spared {
		t.Fatal("unrelated agent should not be spared")
	}
}

func TestIsGitRepoAndMainWorkTree(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if isGitRepo(ctx, dir) {
		t.Fatal("plain temp dir must not be a git repo")
	}
	if isGitRepo(ctx, filepath.Join(dir, "missing")) {
		t.Fatal("missing dir must not be a git repo")
	}
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %s", out)
	}
	if !isGitRepo(ctx, dir) {
		t.Fatal("git-init dir should be a repo")
	}
	// The main working tree of a repo root resolves to a valid git repo.
	if !isGitRepo(ctx, mainWorkTree(ctx, dir)) {
		t.Fatal("mainWorkTree should return a git repo")
	}
}

func TestNormCwdMatchesTildeAndAbsolute(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	// `paseo workspace ls` gives absolute; `paseo ls` gives `~/…`. They must match.
	abs := filepath.Join(home, ".paseo/worktrees/x/branch")
	data := []byte(`[{"workspaceId":"wks_wt","isolation":"worktree","cwd":` + strconv.Quote(abs) + `}]`)
	m := parseWorktreeWorkspaces(data)
	if m[normCwd("~/.paseo/worktrees/x/branch")] != "wks_wt" {
		t.Fatalf("tilde agent cwd must map to the absolute workspace: %v", m)
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

func TestBackendRouting(t *testing.T) {
	d := &Dispatcher{PaseoBin: "paseo", DryRun: true} // dry-run: no real exec
	// agent with no backend → paseo
	if ref, _ := d.Dispatch(context.Background(), Request{
		Trigger: core.Trigger{Kind: "changes_requested"},
		Action:  config.Action{Type: "agent", Agent: "fixer"},
	}); ref.Backend != "paseo" {
		t.Fatalf("agent should route to paseo, got %q", ref.Backend)
	}
	// command with no backend → local
	if ref, _ := d.Dispatch(context.Background(), Request{
		Trigger: core.Trigger{Kind: "pr_behind"},
		Action:  config.Action{Type: "command", Command: []string{"true"}},
	}); ref.Backend != "local" {
		t.Fatalf("command should route to local, got %q", ref.Backend)
	}
	// per-action backend override still works (command forced onto paseo)
	if ref, _ := d.Dispatch(context.Background(), Request{
		Trigger: core.Trigger{Kind: "review_requested"},
		Action:  config.Action{Type: "command", Backend: "paseo", Command: []string{"critique"}},
	}); ref.Backend != "paseo" {
		t.Fatalf("per-action backend override should win, got %q", ref.Backend)
	}
}
