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
