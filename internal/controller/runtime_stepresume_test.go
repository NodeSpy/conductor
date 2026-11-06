package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/dispatch"
)

// Each runtime's own step resume (StepResumer), driven through its runner the
// way the flow dispatches an interrupted step after a restart.

// stepReq is a foreground agent step that records its agent; starts collects
// what the runtime reports.
func stepReq(starts *[]dispatch.AgentStart, resume *dispatch.AgentSession) dispatch.Request {
	var mu sync.Mutex
	return dispatch.Request{
		Wait:   true,
		Action: config.Action{Type: "agent", Prompt: "fix the build"},
		Resume: resume,
		OnAgentStart: func(s dispatch.AgentStart) {
			mu.Lock()
			defer mu.Unlock()
			*starts = append(*starts, s)
		},
	}
}

func dispatchStep(t *testing.T, c Controller, req dispatch.Request) dispatch.RunRef {
	t.Helper()
	r, err := c.Runner()
	if err != nil {
		t.Fatal(err)
	}
	ref, err := r.Dispatch(context.Background(), req)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	return ref
}

// An ACP agent that advertises loadSession has its step's session recorded as
// resumable at launch — no Initialize call needed — and after a restart the
// step's session is loaded back (session/load) and continued with the restart
// note, instead of a new session.
func TestACPStepResumeLoadsTheRecordedSession(t *testing.T) {
	agent := &fakeACPAgent{sessionID: "sess-1",
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}}}
	c := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	c.dial = dialFake(agent)

	var starts []dispatch.AgentStart
	dispatchStep(t, c, stepReq(&starts, nil))
	if len(starts) != 1 || starts[0].SessionID != "sess-1" || !starts[0].CanResume || starts[0].Runtime != "gem" {
		t.Fatalf("launch reported %+v", starts)
	}

	// The restart: a fresh controller, nothing in memory.
	agent2 := &fakeACPAgent{sessionID: "sess-new",
		initResult: acp.InitializeResult{AgentCapabilities: acp.AgentCapabilities{LoadSession: true}}}
	c2 := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	c2.dial = dialFake(agent2)
	starts = nil
	ref := dispatchStep(t, c2, stepReq(&starts, &dispatch.AgentSession{Runtime: "gem", SessionID: "sess-1", CanResume: true}))
	agent2.mu.Lock()
	loaded, prompt := agent2.loadedID, agent2.gotPrompt
	agent2.mu.Unlock()
	if loaded != "sess-1" || prompt != dispatch.ResumeNote {
		t.Fatalf("session/load %q, prompt %q", loaded, prompt)
	}
	if ref.AgentID != "sess-1" || len(starts) != 1 || !starts[0].Resumed || starts[0].SessionID != "sess-1" {
		t.Fatalf("ref %+v, reported %+v", ref, starts)
	}
}

// An ACP agent without loadSession records no resumable session, and an agent
// that lost it since gets a fresh session told about the restart.
func TestACPStepResumeFallsBackWithoutLoadSession(t *testing.T) {
	agent := &fakeACPAgent{sessionID: "sess-1"}
	c := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	c.dial = dialFake(agent)
	var starts []dispatch.AgentStart
	dispatchStep(t, c, stepReq(&starts, nil))
	if len(starts) != 1 || starts[0].CanResume {
		t.Fatalf("an agent without session/load reported a resumable session: %+v", starts)
	}

	// Recorded resumable (it had loadSession then); the agent now has not.
	starts = nil
	dispatchStep(t, c, stepReq(&starts, &dispatch.AgentSession{Runtime: "gem", SessionID: "sess-0", CanResume: true}))
	agent.mu.Lock()
	loaded, prompt := agent.loadedID, agent.gotPrompt
	agent.mu.Unlock()
	if loaded != "" || !strings.HasPrefix(prompt, dispatch.RestartNote) {
		t.Fatalf("session/load %q, prompt %q", loaded, prompt)
	}
	if len(starts) != 1 || starts[0].Resumed || !starts[0].KeptWorkspace || starts[0].Fallback == "" {
		t.Fatalf("reported %+v", starts)
	}
}

// opencode continues the step's session by id on a server rooted at the kept
// workspace; one that no longer has the session gets a fresh session.
func TestOpencodeStepResumeContinuesTheSession(t *testing.T) {
	var mu sync.Mutex
	var created, prompted []string
	mux := http.NewServeMux()
	mux.HandleFunc("/session", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		created = append(created, "ses_new")
		mu.Unlock()
		writeJSON(w, map[string]any{"id": "ses_new"})
	})
	mux.HandleFunc("/session/", func(w http.ResponseWriter, r *http.Request) {
		id, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/session/"), "/")
		if id != "ses_old" && id != "ses_new" {
			http.Error(w, "no such session", http.StatusNotFound)
			return
		}
		if rest == "message" {
			mu.Lock()
			prompted = append(prompted, id)
			mu.Unlock()
		}
		writeJSON(w, map[string]any{"id": id, "parts": []map[string]any{{"type": "text", "text": "ok"}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c := newOpencodeFor(t, srv.URL, nil)

	var starts []dispatch.AgentStart
	dispatchStep(t, c, stepReq(&starts, &dispatch.AgentSession{Runtime: "oc", SessionID: "ses_old", CanResume: true}))
	if len(created) != 0 || len(prompted) != 1 || prompted[0] != "ses_old" {
		t.Fatalf("created %v prompted %v", created, prompted)
	}
	if len(starts) != 1 || !starts[0].Resumed || starts[0].SessionID != "ses_old" || !starts[0].CanResume {
		t.Fatalf("reported %+v", starts)
	}

	starts, prompted = nil, nil
	dispatchStep(t, c, stepReq(&starts, &dispatch.AgentSession{Runtime: "oc", SessionID: "ses_gone", CanResume: true}))
	if len(created) != 1 || len(prompted) != 1 || prompted[0] != "ses_new" {
		t.Fatalf("a lost session must give a fresh one: created %v prompted %v", created, prompted)
	}
	if len(starts) != 1 || starts[0].Resumed || !strings.Contains(starts[0].Fallback, "ses_gone") {
		t.Fatalf("reported %+v", starts)
	}
}

// agent-deck's session lives on in agent-deck across a restart: the step's
// session gets the restart note as its next turn when agent-deck still has
// it, and a fresh session is launched when it does not.
func TestAgentDeckStepResumeSendsToTheSession(t *testing.T) {
	gone := false
	deck := &fakeDeck{byArgs: func(args []string) ([]byte, error) {
		switch {
		case args[0] == "launch":
			return []byte(`{"id":"deck-new"}`), nil
		case len(args) > 2 && args[0] == "session" && args[1] == "show":
			if gone && args[2] == "deck-old" {
				return nil, errors.New("session not found")
			}
			return []byte(`{"status":"idle"}`), nil
		}
		return nil, nil
	}}
	c := newDeckFor(nil, deck)

	var starts []dispatch.AgentStart
	dispatchStep(t, c, stepReq(&starts, &dispatch.AgentSession{Runtime: "deck", SessionID: "deck-old", CanResume: true}))
	var sent, launched bool
	for _, call := range deck.calls {
		if len(call.args) >= 4 && call.args[0] == "session" && call.args[1] == "send" && call.args[2] == "deck-old" && call.args[3] == dispatch.ResumeNote {
			sent = true
		}
		launched = launched || call.args[0] == "launch"
	}
	if !sent || launched {
		t.Fatalf("want the note sent to deck-old and no launch: %+v", deck.calls)
	}
	if len(starts) != 1 || !starts[0].Resumed || starts[0].SessionID != "deck-old" {
		t.Fatalf("reported %+v", starts)
	}

	gone, starts, deck.calls = true, nil, nil
	dispatchStep(t, c, stepReq(&starts, &dispatch.AgentSession{Runtime: "deck", SessionID: "deck-old", CanResume: true}))
	if len(starts) != 1 || starts[0].Resumed || starts[0].SessionID != "deck-new" {
		t.Fatalf("a gone session must give a fresh launch; reported %+v, calls %+v", starts, deck.calls)
	}
}
