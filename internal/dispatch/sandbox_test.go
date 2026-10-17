package dispatch

import (
	"testing"

	"github.com/NodeSpy/conductor/internal/agentsandbox"
	"github.com/NodeSpy/conductor/internal/config"
)

// A workspace its owner registered (a member, under its own identity) keeps
// that identity when a dispatch launches into it; a dispatch's own
// registration is reused on its resume.
func TestRegisterSandboxWorkspaceRespectsTheOwner(t *testing.T) {
	ws := t.TempDir()
	unreg := agentsandbox.Default.Register(agentsandbox.Entry{Workspace: ws, Identity: "member:alice"})
	defer unreg()
	e, err := RegisterSandboxWorkspace(ws, "agent:fixer@a/b", "claude-code", &Request{DispatchID: "d1"})
	if err != nil || e.Identity != "member:alice" {
		t.Fatalf("owner identity: %+v %v", e, err)
	}
	ws2 := t.TempDir()
	e1, _ := RegisterSandboxWorkspace(ws2, "agent:fixer@a/b", "claude-code", &Request{DispatchID: "d2"})
	defer agentsandbox.Default.Forget(ws2)
	e2, _ := RegisterSandboxWorkspace(ws2, "agent:fixer@a/b", "claude-code", nil)
	if e1.Dispatch != "d2" || e2.Dispatch != "d2" {
		t.Fatalf("resume reuses the dispatch's registration: %+v %+v", e1, e2)
	}
	e3, _ := RegisterSandboxWorkspace(ws2, "agent:other@a/b", "codex", &Request{DispatchID: "d3"})
	if e3.Dispatch != "d3" || e3.Identity != "agent:other@a/b" {
		t.Fatalf("another dispatch re-registers a dispatch's workspace: %+v", e3)
	}
}

// A step's isolation: {mode: container} (resolved onto it from its
// workflow, trigger or paseo runtime) rides the workspace's registration:
// `conductor sandbox exec` then runs the agent in its identity's container.
func TestRegisterSandboxWorkspaceCarriesTheContainer(t *testing.T) {
	ws := t.TempDir()
	defer agentsandbox.Default.Forget(ws)
	yes := true
	req := &Request{DispatchID: "dc", Step: config.Step{Isolation: &config.IsolationConfig{Mode: "container", Image: "img:1", Persistent: &yes}}}
	e, err := RegisterSandboxWorkspace(ws, "agent:fixer@a/b", "claude-code", req)
	if err != nil || e.Container == nil || e.Container.Image != "img:1" || !e.Container.Persistent {
		t.Fatalf("container registration: %+v %v", e, err)
	}
	ws2 := t.TempDir()
	defer agentsandbox.Default.Forget(ws2)
	e, _ = RegisterSandboxWorkspace(ws2, "agent:fixer@a/b", "claude-code", &Request{DispatchID: "dn"})
	if e.Container != nil {
		t.Errorf("no isolation: the jail, got %+v", e.Container)
	}
}
