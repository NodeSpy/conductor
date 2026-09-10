package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/config"
)

// §25: MethodLoadSession was DEFINED and never called. ResumeSession
// spawned a fresh agent, ran initialize, and handed back a session id the
// agent had never been told about — so every "resumed" turn started from
// an empty context while conductor reported the session as continuous.
// The transcript looked fine; only the agent's memory of the conversation
// was missing, which is the worst shape a bug can take.
func TestACPResumeIssuesSessionLoad(t *testing.T) {
	agent := &fakeACPAgent{
		sessionID:  "sess-9",
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}},
	}
	c := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	c.dial = dialFake(agent)
	// The session was opened in a worktree; the resume must re-root there.
	c.rememberCwd("sess-9", "/wt/o-r-7")

	if _, err := c.ResumeSession(context.Background(), "sess-9", false, nil); err != nil {
		t.Fatalf("ResumeSession: %v", err)
	}
	agent.mu.Lock()
	loaded, cwd := agent.loadedID, agent.gotCwd
	agent.mu.Unlock()
	if loaded != "sess-9" {
		t.Fatalf("resume must issue session/load for the id, got %q", loaded)
	}
	if cwd != "/wt/o-r-7" {
		t.Fatalf("session/load must carry the session's worktree, got %q", cwd)
	}
}

// An agent that does not advertise loadSession cannot be resumed at all.
// Saying so beats handing back a session that silently forgets.
func TestACPResumeRefusedWithoutTheCapability(t *testing.T) {
	agent := &fakeACPAgent{sessionID: "sess-9", initResult: acp.InitializeResult{}}
	c := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	c.dial = dialFake(agent)

	_, err := c.ResumeSession(context.Background(), "sess-9", false, nil)
	if err == nil || !strings.Contains(err.Error(), "session/load") {
		t.Fatalf("an agent without loadSession must refuse the resume, got %v", err)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.loadedID != "" {
		t.Fatalf("no session/load should have been attempted, got %q", agent.loadedID)
	}
}
