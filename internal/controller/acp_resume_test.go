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

// THE RESTART CASE. The controller's session→worktree map is in-process, so
// after a daemon restart it is empty — and a resume that consulted only that
// map re-rooted the agent at the daemon's own working directory instead of the
// worktree, where it read and wrote the wrong tree. The cwd has to travel with
// the PERSISTED session ref.
//
// This exercises exactly that: bind through one broker, then resolve through a
// SECOND broker over a FRESH controller instance, with nothing shared but the
// store.
func TestAResumeAfterRestartStillRootsAtTheWorktree(t *testing.T) {
	const (
		prKey = "acme/api#7"
		sid   = "sess-restart"
		wt    = "/wt/acme-api-7"
	)
	st := newFakeStore()

	// --- before the restart: a controller that opened the session in wt ---
	before := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	before.dial = dialFake(&fakeACPAgent{
		sessionID:  sid,
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}},
	})
	before.rememberCwd(sid, wt)

	b1 := NewBroker(&Registry{controllers: map[string]Controller{"gem": before}}, st, nil)
	b1.Bind(prKey, before, &stubSession{id: sid}, false)

	if got := st.recs[prKey].Cwd; got != wt {
		t.Fatalf("the bound session's cwd was not persisted: %q — after a restart there is "+
			"nothing else that knows where the agent's worktree is", got)
	}

	// --- the restart: a FRESH controller, empty cwd map, new broker ---
	agent := &fakeACPAgent{
		sessionID:  sid,
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}},
	}
	after := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	after.dial = dialFake(agent)
	if got := after.cwdFor(sid); got != "" {
		t.Fatalf("test setup: the fresh controller should know nothing, got %q", got)
	}

	// NewBroker restores refs from the store — that IS the restart.
	b2 := NewBroker(&Registry{controllers: map[string]Controller{"gem": after}}, st, nil)

	if _, err := b2.Session(context.Background(), prKey, nil); err != nil {
		t.Fatalf("Session after restart: %v", err)
	}
	agent.mu.Lock()
	loaded, cwd := agent.loadedID, agent.gotCwd
	agent.mu.Unlock()

	if loaded != sid {
		t.Fatalf("the restarted daemon did not load the persisted session, got %q", loaded)
	}
	if cwd != wt {
		t.Fatalf("post-restart resume rooted the agent at %q, not its worktree %q — the agent "+
			"would read and write the wrong tree", cwd, wt)
	}
}
