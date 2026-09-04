package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
)

func TestProvisionWorktreeCreatesForPR(t *testing.T) {
	d := newDispatcher()
	d.CheckoutDir = func(context.Context, string) (string, error) { return "/checkouts/o-r", nil }
	var gotBase, gotStrat string
	d.WorktreeCreator = func(_ context.Context, req Request, baseDir string) (string, string, error) {
		gotBase, gotStrat = baseDir, effectiveStrategy(req)
		return "wks_pr7", "/worktrees/o-r/pr7", nil
	}
	req := Request{
		Trigger: core.Trigger{Kind: "review_requested",
			Target: core.Target{Repo: "o/r", PR: 7, Number: 7}},
		Action:  config.Action{Type: "agent", Prompt: "review"},
		Profile: config.AgentProfile{Workspace: "worktree"},
	}
	id, cwd, err := d.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if id != "wks_pr7" || cwd != "/worktrees/o-r/pr7" {
		t.Fatalf("provision returned id=%q cwd=%q", id, cwd)
	}
	if gotBase != "/checkouts/o-r" || gotStrat != "checkout-pr" {
		t.Fatalf("createWorktree got base=%q strat=%q", gotBase, gotStrat)
	}
}

func TestProvisionWorktreeExplicitWorkDirWins(t *testing.T) {
	d := newDispatcher()
	req := Request{
		Trigger: core.Trigger{Kind: "cron", Target: core.Target{Repo: "o/r", PR: 7, Number: 7}},
		Action:  config.Action{Type: "agent", WorkDir: "~/somewhere", Prompt: "x"},
	}
	id, cwd, err := d.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatalf("an explicit workdir has no workspace id, got %q", id)
	}
	if strings.HasPrefix(cwd, "~") || cwd == "" {
		t.Fatalf("workdir should be tilde-expanded, got %q", cwd)
	}
}

func TestProvisionWorktreeNoRepoContext(t *testing.T) {
	d := newDispatcher()
	req := Request{
		Trigger: core.Trigger{Kind: "cron"}, // no repo/PR
		Action:  config.Action{Type: "agent", Prompt: "x"},
	}
	id, cwd, err := d.ProvisionWorktree(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if id != "" || cwd != "" {
		t.Fatalf("no repo context means no dedicated worktree, got id=%q cwd=%q", id, cwd)
	}
}

func TestAgentEnvActsAsUser(t *testing.T) {
	req := Request{
		Trigger: core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "o/r"}},
		Action:  config.Action{Type: "agent", Env: map[string]string{"EXTRA": "v-{{.repo}}"}},
		Tokens:  Tokens{User: "utok", App: "atok"},
		Author:  Author{Name: "Me", Email: "me@example.com"},
	}
	env, err := AgentEnv(req)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"GH_TOKEN":          "utok",
		"GITHUB_TOKEN":      "utok",
		"PC_GH_WRITE_TOKEN": "utok",
		"PC_GH_APP_TOKEN":   "atok",
		"GIT_AUTHOR_NAME":   "Me",
		"GIT_AUTHOR_EMAIL":  "me@example.com",
		"EXTRA":             "v-o/r", // action env is rendered against the trigger
	}
	got := map[string]string{}
	for _, e := range env {
		if i := strings.IndexByte(e, '='); i >= 0 {
			got[e[:i]] = e[i+1:]
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env %s = %q, want %q", k, got[k], v)
		}
	}
}

func TestRenderPrompt(t *testing.T) {
	req := Request{
		Trigger: core.Trigger{Kind: "merge_conflict", Target: core.Target{Repo: "o/r", PR: 7, Number: 7}},
		Action:  config.Action{Type: "agent", Prompt: "fix {{.repo}}#{{.pr}}"},
	}
	got, err := RenderPrompt(req)
	if err != nil {
		t.Fatal(err)
	}
	if got != "fix o/r#7" {
		t.Fatalf("rendered prompt = %q, want 'fix o/r#7'", got)
	}
}
