// Command fakeacp is a hermetic ACP (Agent Client Protocol) agent for the e2e
// harness (test/e2e/). It reuses the project's own internal/acp library — the same
// JSON-RPC-2.0-over-stdio Conn — so it is a faithful agent-side peer with NO LLM
// and NO secrets. Conductor's ACP controller (internal/controller/acp.go) spawns
// it with cmd.Dir set to the conductor-provisioned PR worktree and the acts-as-the-
// user identity in its env, answers initialize / session/new / session/load, and
// on session/prompt performs the shared fixer edit+commit+push (package fixer) in
// the session cwd — so the acp:gemini / acp:codex-adapter / opencode-acp rows land
// a real commit on the forge, exactly like the paseo path.
//
// It advertises loadSession:true so conductor negotiates session_model:resumable
// (Group C), and it re-attaches on session/load (Group D2). Fault injection:
//
//	FAKE_ACP_CRASH=1  → exit mid-turn on session/prompt (Group J3: ACP agent
//	                    crashes mid-session → conductor detects → escalate). This is
//	                    injected per-route via the agent action's env, so only the
//	                    crash row crashes.
//
// NOT part of the shipped product; harness-only.
package main

import (
	"context"
	"encoding/json"
	"os"
	"sync"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/test/e2e/services/fixer"
)

type agent struct {
	conn *acp.Conn

	mu  sync.Mutex
	cwd string // session cwd from session/new (the conductor-provisioned worktree)
}

func (a *agent) HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	switch method {
	case acp.MethodInitialize:
		return acp.InitializeResult{
			ProtocolVersion: 1,
			AgentCapabilities: acp.AgentCapabilities{
				LoadSession: true, // → conductor negotiates session_model: resumable
			},
			AgentInfo: &acp.Implementation{Name: "fakeacp", Version: "0.0.0"},
		}, nil
	case acp.MethodNewSession:
		if os.Getenv("FAKE_ACP_CRASH") != "" {
			// Die as the session is opened: the agent process vanishing is what
			// conductor detects synchronously (session/new RPC fails → NewSession
			// errors → dispatch fails → escalate). Group J3.
			os.Exit(1)
		}
		var p acp.NewSessionParams
		_ = json.Unmarshal(params, &p)
		a.mu.Lock()
		a.cwd = p.Cwd
		a.mu.Unlock()
		return acp.NewSessionResult{SessionID: "fakeacp-session-1"}, nil
	case acp.MethodLoadSession:
		// Re-attach: conductor resumes by id (Group D2). cwd may be re-supplied.
		var p acp.NewSessionParams
		if json.Unmarshal(params, &p) == nil && p.Cwd != "" {
			a.mu.Lock()
			a.cwd = p.Cwd
			a.mu.Unlock()
		}
		return acp.NewSessionResult{SessionID: "fakeacp-session-1"}, nil
	case acp.MethodPrompt:
		var p acp.PromptParams
		_ = json.Unmarshal(params, &p)
		if os.Getenv("FAKE_ACP_CRASH") != "" {
			os.Exit(1) // simulate a mid-session crash (Group J3)
		}
		// Do the fixer's work in the session worktree, then stream one message
		// chunk and end the turn.
		a.mu.Lock()
		cwd := a.cwd
		a.mu.Unlock()
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		_ = fixer.Apply(cwd, "acp", promptText(p))
		_ = a.conn.Notify(ctx, acp.MethodSessionUpdate, acp.SessionNotification{
			SessionID: p.SessionID,
			Update: acp.SessionUpdate{
				Kind:         "agent_message_chunk",
				MessageChunk: &acp.MessageChunk{Content: acp.TextBlock("fakeacp: done")},
			},
		})
		return acp.PromptResult{StopReason: acp.StopReasonEndTurn}, nil
	default:
		return nil, acp.NewRPCError(acp.CodeMethodNotFound, "unknown method "+method)
	}
}

func (a *agent) HandleNotification(ctx context.Context, method string, params json.RawMessage) {
	// session/cancel and friends: nothing to do in the fake.
}

func promptText(p acp.PromptParams) string {
	for _, b := range p.Prompt {
		if b.Text != "" {
			return b.Text
		}
	}
	return "resolve the issue"
}

func main() {
	a := &agent{}
	a.conn = acp.NewConn(os.Stdin, os.Stdout, a)
	<-a.conn.Done()
}
