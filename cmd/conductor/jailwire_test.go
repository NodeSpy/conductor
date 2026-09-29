package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/controller"
	"github.com/NodeSpy/conductor/internal/dispatch"
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
	oldM, oldG, oldD := controller.JailManager, controller.GlobalIsolation, dispatch.GlobalIsolation
	defer func() {
		controller.JailManager, controller.GlobalIsolation, dispatch.GlobalIsolation = oldM, oldG, oldD
	}()

	egress := sandbox.NewProxyManager(nil)
	defer egress.Close()
	m := wireJail(jailWiring{cfg: &config.Config{}, stateDir: t.TempDir(), cfgDir: t.TempDir(),
		audit: func(map[string]any) {}, egress: egress, logf: func(string, ...any) {}})

	open := &jail.Dispatch{LaunchSpec: jail.LaunchSpec{Repo: "acme/app", Number: 42, IsPR: true, HeadBranch: "fix/42", UserToken: "t"}}
	merged := &jail.Dispatch{LaunchSpec: jail.LaunchSpec{Repo: "acme/app", Number: 43, IsPR: true, HeadBranch: "fix/43", UserToken: "t"}}

	if r := m.CheckPush(open, "fix/42", false, false); r != "" {
		t.Fatalf("a push to an open PR's own branch is allowed: %q", r)
	}
	if r := m.CheckWrite(open, hostcmd.Write{Kind: "comment", Repo: "acme/app", Number: 42}); r != "" {
		t.Fatalf("a comment on the open PR is allowed: %q", r)
	}
	// The live state says #43 merged: refused, and before any branch rule.
	if r := m.CheckPush(merged, "some-other-branch", false, false); !strings.Contains(r, "acme/app#43 is merged — writes refused") {
		t.Fatalf("a push for a merged PR must be refused as merged: %q", r)
	}
	if r := m.CheckWrite(merged, hostcmd.Write{Kind: "comment", Repo: "acme/app", Number: 43}); !strings.Contains(r, "is merged") {
		t.Fatalf("a comment on a merged PR must be refused: %q", r)
	}
	// …and the live read recorded it, so the registry refuses even with the
	// API unreachable.
	defer targets.Default.Reopen("acme/app", 43)
	srv.Close()
	if r := m.CheckPush(merged, "fix/43", false, false); !strings.Contains(r, "is merged") {
		t.Fatalf("the close registry refuses without the API: %q", r)
	}
	// A registry-marked close (the webhook path) refuses too.
	targets.Default.MarkClosed("acme/app", 42, false)
	defer targets.Default.Reopen("acme/app", 42)
	if r := m.CheckPush(open, "fix/42", false, false); !strings.Contains(r, "acme/app#42 is closed") {
		t.Fatalf("a webhook-closed PR refuses its own branch: %q", r)
	}
}
