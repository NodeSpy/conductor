// Command fakeacp is a hermetic ACP (Agent Client Protocol) agent for the e2e
// harness (test/e2e/). It reuses the project's own internal/acp library — the
// same JSON-RPC-2.0-over-stdio Conn the test double in internal/acp is built on —
// so it is a faithful agent-side peer with NO LLM and NO secrets. It answers
// initialize, session/new, session/load and session/prompt, streams a
// session/update, and ends the turn.
//
// It is a SCAFFOLD for the ACP transport (issue #9, M3): the ACP client library
// (T3.1) has landed, but the transport isn't wired into a runnable controller yet
// (registered as ErrNotRunnable in M1), so the e2e runner does not assert against
// it. Set FAKE_ACP_CRASH=1 to have it exit mid-turn — the fixture group J3 (ACP
// agent crashes mid-session → escalate) will use this once the transport lands.
//
// NOT part of the shipped product; harness-only.
package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/NodeSpy/paseo-conductor/internal/acp"
)

type agent struct {
	conn *acp.Conn
}

func (a *agent) HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	switch method {
	case acp.MethodInitialize:
		return acp.InitializeResult{
			ProtocolVersion: 1,
			AgentCapabilities: acp.AgentCapabilities{
				LoadSession: true,
			},
			AgentInfo: &acp.Implementation{Name: "fakeacp", Version: "0.0.0"},
		}, nil
	case acp.MethodNewSession:
		return acp.NewSessionResult{SessionID: "fakeacp-session-1"}, nil
	case acp.MethodLoadSession:
		return acp.NewSessionResult{SessionID: "fakeacp-session-1"}, nil
	case acp.MethodPrompt:
		var p acp.PromptParams
		_ = json.Unmarshal(params, &p)
		if os.Getenv("FAKE_ACP_CRASH") != "" {
			os.Exit(1) // simulate a mid-session crash (group J3, once M3 wired)
		}
		// Stream one assistant message chunk, then end the turn.
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

func main() {
	a := &agent{}
	a.conn = acp.NewConn(os.Stdin, os.Stdout, a)
	<-a.conn.Done()
}
