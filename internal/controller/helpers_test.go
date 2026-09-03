package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
	"github.com/NodeSpy/paseo-conductor/internal/dispatch"
)

// makeReq builds a representative agent dispatch request: a PR trigger with the
// acts-as-user tokens/author the controllers must propagate.
func makeReq(kind, prompt string) dispatch.Request {
	return dispatch.Request{
		Trigger: core.Trigger{
			Kind:   kind,
			Target: core.Target{Repo: "o/r", PR: 7, Number: 7},
		},
		Action:  config.Action{Type: "agent", Prompt: prompt},
		Profile: config.AgentProfile{Provider: "anthropic", Model: "claude"},
		Tokens:  dispatch.Tokens{User: "utok", App: "atok"},
		Author:  dispatch.Author{Name: "Me", Email: "me@example.com"},
	}
}

// fakeProv is an injectable worktree provisioner that records the requests it saw
// and returns a canned workspace id + cwd.
type fakeProv struct {
	id, cwd string
	err     error
	got     []dispatch.Request
}

func (p *fakeProv) ProvisionWorktree(_ context.Context, req dispatch.Request) (string, string, error) {
	p.got = append(p.got, req)
	return p.id, p.cwd, p.err
}

// waitSession blocks on a session's waiter (all controller sessions implement it),
// failing if the turn doesn't finish promptly.
func waitSession(t *testing.T, s Session) {
	t.Helper()
	w, ok := s.(waiter)
	if !ok {
		t.Fatalf("session %T does not implement waiter", s)
	}
	done := make(chan struct{})
	go func() { w.Wait(context.Background(), 3*time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("session Wait did not return")
	}
}

// envHas reports whether env contains key=want.
func envHas(env []string, key, want string) bool {
	for _, e := range env {
		if e == key+"="+want {
			return true
		}
	}
	return false
}

// argIndex returns the index of arg in args, or -1.
func argIndex(args []string, arg string) int {
	for i, a := range args {
		if a == arg {
			return i
		}
	}
	return -1
}

// joinArgs is a readable form of an argv for failure messages.
func joinArgs(args []string) string { return strings.Join(args, " ") }
