package controller

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"testing"

	"github.com/NodeSpy/conductor/internal/acp"
	"github.com/NodeSpy/conductor/internal/config"
)

// fakeACPAgent speaks the agent side of ACP over a Conn — the same pipe double
// pattern internal/acp uses for its own tests, rebuilt here on the exported API so
// the controller can be driven without a real agent subprocess.
type fakeACPAgent struct {
	conn       *acp.Conn
	initResult acp.InitializeResult
	sessionID  string
	onPrompt   func(ctx context.Context, a *fakeACPAgent, p acp.PromptParams) acp.PromptResult

	mu        sync.Mutex
	gotCwd    string
	gotPrompt string
	outcome   *acp.RequestPermissionOutcome
}

func (a *fakeACPAgent) HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, *acp.RPCError) {
	switch method {
	case acp.MethodInitialize:
		return a.initResult, nil
	case acp.MethodNewSession:
		var p acp.NewSessionParams
		_ = json.Unmarshal(params, &p)
		a.mu.Lock()
		a.gotCwd = p.Cwd
		a.mu.Unlock()
		return acp.NewSessionResult{SessionID: a.sessionID}, nil
	case acp.MethodPrompt:
		var p acp.PromptParams
		_ = json.Unmarshal(params, &p)
		a.mu.Lock()
		if len(p.Prompt) > 0 {
			a.gotPrompt = p.Prompt[0].Text
		}
		a.mu.Unlock()
		if a.onPrompt != nil {
			return a.onPrompt(ctx, a, p), nil
		}
		return acp.PromptResult{StopReason: acp.StopReasonEndTurn}, nil
	default:
		return nil, acp.NewRPCError(acp.CodeMethodNotFound, method)
	}
}

func (a *fakeACPAgent) HandleNotification(context.Context, string, json.RawMessage) {}

// requestPermission calls the client back mid-turn and records the decision.
func (a *fakeACPAgent) requestPermission(ctx context.Context, p acp.RequestPermissionParams) acp.RequestPermissionOutcome {
	var res acp.RequestPermissionResult
	if err := a.conn.Call(ctx, acp.MethodRequestPermission, p, &res); err != nil {
		return acp.RequestPermissionOutcome{}
	}
	a.mu.Lock()
	a.outcome = &res.Outcome
	a.mu.Unlock()
	return res.Outcome
}

// dialFake wires the controller's ACP client to the fake agent over two pipes.
func dialFake(agent *fakeACPAgent) acpDialer {
	return func(_ context.Context, cwd string, _ []string, del acp.ClientDelegate) (*acp.Client, func() error, error) {
		clientR, agentW := io.Pipe() // agent → client
		agentR, clientW := io.Pipe() // client → agent
		agent.conn = acp.NewConn(agentR, agentW, agent)
		client := acp.NewClient(clientR, clientW, del)
		cleanup := func() error {
			client.Close()
			agent.conn.Close()
			agentW.Close()
			clientW.Close()
			clientR.Close()
			agentR.Close()
			return nil
		}
		return client, cleanup, nil
	}
}

func TestACPInitializeNegotiatesResumable(t *testing.T) {
	agent := &fakeACPAgent{
		initResult: acp.InitializeResult{
			ProtocolVersion:   acp.ProtocolVersion,
			AgentCapabilities: acp.AgentCapabilities{LoadSession: true},
		},
		sessionID: "sess",
	}
	c := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	c.dial = dialFake(agent)

	caps, err := c.Initialize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.SessionModel != ModelResumable {
		t.Fatalf("loadSession must negotiate a resumable model, got %q", caps.SessionModel)
	}
	if caps.Transport != TransportACP || !caps.CheckoutPR {
		t.Fatalf("bad caps: %+v", caps)
	}
	if c.Model() != ModelResumable {
		t.Fatalf("controller model should cache the negotiation, got %q", c.Model())
	}
}

func TestACPNewSessionRunsFirstTurnInWorktree(t *testing.T) {
	agent := &fakeACPAgent{
		initResult: acp.InitializeResult{ProtocolVersion: acp.ProtocolVersion},
		sessionID:  "sess-acp",
		onPrompt: func(ctx context.Context, a *fakeACPAgent, p acp.PromptParams) acp.PromptResult {
			// Stream two message chunks that should be captured as the turn output.
			_ = a.conn.Notify(ctx, acp.MethodSessionUpdate, acp.SessionNotification{
				SessionID: p.SessionID,
				Update:    acp.SessionUpdate{Kind: acp.UpdateAgentMessageChunk, MessageChunk: &acp.MessageChunk{Content: acp.TextBlock("hello ")}},
			})
			_ = a.conn.Notify(ctx, acp.MethodSessionUpdate, acp.SessionNotification{
				SessionID: p.SessionID,
				Update:    acp.SessionUpdate{Kind: acp.UpdateAgentMessageChunk, MessageChunk: &acp.MessageChunk{Content: acp.TextBlock("world")}},
			})
			return acp.PromptResult{StopReason: acp.StopReasonEndTurn}
		},
	}
	c := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	c.dial = dialFake(agent)

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "please fix"), Cwd: "/wt/o-r-7"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sess.ID() != "sess-acp" {
		t.Fatalf("session id = %q, want sess-acp", sess.ID())
	}
	waitSession(t, sess)

	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.gotCwd != "/wt/o-r-7" {
		t.Fatalf("agent session cwd = %q, want the conductor worktree", agent.gotCwd)
	}
	if agent.gotPrompt != "please fix" {
		t.Fatalf("agent prompt = %q, want 'please fix'", agent.gotPrompt)
	}
	if out := sess.(*acpSession).del.output(); out != "hello world" {
		t.Fatalf("captured turn output = %q, want 'hello world'", out)
	}
}

func TestACPPermissionAutoApproved(t *testing.T) {
	agent := &fakeACPAgent{
		initResult: acp.InitializeResult{ProtocolVersion: acp.ProtocolVersion},
		sessionID:  "sess-perm",
		onPrompt: func(ctx context.Context, a *fakeACPAgent, p acp.PromptParams) acp.PromptResult {
			out := a.requestPermission(ctx, acp.RequestPermissionParams{
				SessionID: p.SessionID,
				ToolCall:  acp.ToolCallUpdate{ToolCallID: "call_write", Title: "Write file"},
				Options: []acp.PermissionOption{
					{OptionID: "allow-once", Name: "Allow once", Kind: acp.PermissionAllowOnce},
					{OptionID: "reject", Name: "Reject", Kind: acp.PermissionRejectOnce},
				},
			})
			_ = out
			return acp.PromptResult{StopReason: acp.StopReasonEndTurn}
		},
	}
	c := newACPController("gem", config.ControllerConfig{Agent: "gemini"}, nil)
	c.dial = dialFake(agent)

	sess, err := c.NewSession(context.Background(), Spec{Request: makeReq("merge_conflict", "x"), Cwd: "/wt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSession(t, sess)

	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.outcome == nil {
		t.Fatal("agent never received a permission decision")
	}
	// The no-handler policy auto-approves, selecting the allow-once option.
	if agent.outcome.Outcome != acp.OutcomeSelected || agent.outcome.OptionID != "allow-once" {
		t.Fatalf("permission outcome = %+v, want selected allow-once", agent.outcome)
	}
}

func TestACPCommandResolution(t *testing.T) {
	// Explicit command wins.
	if got := acpCommand(config.ControllerConfig{Agent: "gemini", Command: []string{"my", "acp"}}); joinArgs(got) != "my acp" {
		t.Fatalf("explicit command should win, got %v", got)
	}
	// Known agent → best-effort default.
	if got := acpCommand(config.ControllerConfig{Agent: "gemini"}); argIndex(got, "gemini") != 0 {
		t.Fatalf("gemini default command = %v", got)
	}
	// Unknown agent → bare name.
	if got := acpCommand(config.ControllerConfig{Agent: "weirdo"}); joinArgs(got) != "weirdo" {
		t.Fatalf("unknown agent should fall back to bare name, got %v", got)
	}
}
