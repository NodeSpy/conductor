package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/hostcmd"
	"github.com/NodeSpy/conductor/internal/jail"
	"github.com/NodeSpy/conductor/internal/sandbox"
	"github.com/NodeSpy/conductor/internal/targets"
)

// The broker's write binding refuses every write for a closed target — from
// the close registry, and (the backstop for a close conductor never heard
// about) from the PR's live state, which is checked BEFORE the branch binding
// so the refusal says what actually happened.
func TestJailBindingRefusesWritesForAClosedTarget(t *testing.T) {
	state := map[string]string{"/repos/acme/app/pulls/42": `{"state":"open","merged":false}`,
		"/repos/acme/app/pulls/43": `{"state":"closed","merged":true}`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if body, ok := state[r.URL.Path]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv("PC_GITHUB_API_BASE", srv.URL)
	oldM, oldG := controller.JailManager, controller.GlobalIsolation
	defer func() { controller.JailManager, controller.GlobalIsolation = oldM, oldG }()

	egress := sandbox.NewProxyManager(nil)
	defer egress.Close()
	m := wireJail(jailWiring{cfg: &config.Config{}, stateDir: t.TempDir(), cfgDir: t.TempDir(),
		audit: func(map[string]any) {}, egress: egress, logf: func(string, ...any) {}})

	open := &jail.Dispatch{LaunchSpec: jail.LaunchSpec{Repo: "acme/app", Number: 42, IsPR: true, HeadBranch: "fix/42", UserToken: "t"}}
	merged := &jail.Dispatch{LaunchSpec: jail.LaunchSpec{Repo: "acme/app", Number: 43, IsPR: true, HeadBranch: "fix/43", UserToken: "t"}}

	// The wired TargetClosed signal, read by the gh and git profiles.
	ctx := func(d *jail.Dispatch) hostcmd.Context {
		return hostcmd.Context{Repo: d.Repo, Number: d.Number, IsPR: d.IsPR, HeadBranch: d.HeadBranch,
			TargetClosed: func() string { return m.TargetClosed(d) }}
	}
	git := hostcmd.Resolve("git", nil, false)
	gh := func(d *jail.Dispatch, args ...string) hostcmd.Decision {
		return hostcmd.Decide(hostcmd.Request{Tool: "gh", Args: args}, hostcmd.Resolve("gh", nil, false), ctx(d))
	}
	if r := hostcmd.GitPush(git, ctx(open), "fix/42", false, false); r != "" {
		t.Fatalf("a push to an open PR's own branch is allowed: %q", r)
	}
	if d := gh(open, "pr", "comment", "42", "-b", "x"); !d.Allow {
		t.Fatalf("a comment on the open PR is allowed: %q", d.Reason)
	}
	// The live state says #43 merged: refused as merged, before any branch rule.
	if r := hostcmd.GitPush(git, ctx(merged), "some-other-branch", false, false); !strings.Contains(r, "acme/app#43 is merged — writes refused") {
		t.Fatalf("a push for a merged PR must be refused as merged: %q", r)
	}
	if d := gh(merged, "pr", "comment", "43", "-b", "x"); d.Allow || !strings.Contains(d.Reason, "is merged") {
		t.Fatalf("a comment on a merged PR must be refused: %+v", d)
	}
	// …and the live read recorded it, so the registry refuses even with the
	// API unreachable.
	defer targets.Default.Reopen("acme/app", 43)
	srv.Close()
	if r := hostcmd.GitPush(git, ctx(merged), "fix/43", false, false); !strings.Contains(r, "is merged") {
		t.Fatalf("the close registry refuses without the API: %q", r)
	}
	// A registry-marked close (the webhook path) refuses too.
	targets.Default.MarkClosed("acme/app", 42, false)
	defer targets.Default.Reopen("acme/app", 42)
	if r := hostcmd.GitPush(git, ctx(open), "fix/42", false, false); !strings.Contains(r, "acme/app#42 is closed") {
		t.Fatalf("a webhook-closed PR refuses its own branch: %q", r)
	}
}
