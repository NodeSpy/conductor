package acp

import (
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"
)

// ---- in-process fake ACP agent (test double) ----------------------------------

// promptFunc scripts one session/prompt turn. It runs on its own goroutine (the
// Conn serves each request concurrently), so it may send session/update
// notifications and issue session/request_permission calls back to the client
// before returning the turn's stop reason.
type promptFunc func(ctx context.Context, a *fakeAgent, p PromptParams) (PromptResult, *RPCError)

type fakeAgentConfig struct {
	initResult InitializeResult
	sessionID  string
	prompt     promptFunc
}

// fakeAgent speaks the agent side of ACP over a Conn: it answers initialize,
// session/new, and session/prompt, and drives updates/permission requests during
// a prompt turn.
type fakeAgent struct {
	ready chan struct{}
	conn  *Conn

	initResult InitializeResult
	sessionID  string
	prompt     promptFunc

	mu             sync.Mutex
	gotInit        *InitializeParams
	gotNewSession  *NewSessionParams
	gotPrompt      *PromptParams
	permitOutcome  *RequestPermissionOutcome
	promptTurnDone bool
}

func startFakeAgent(in io.Reader, out io.Writer, cfg fakeAgentConfig) *fakeAgent {
	a := &fakeAgent{
		ready:      make(chan struct{}),
		initResult: cfg.initResult,
		sessionID:  cfg.sessionID,
		prompt:     cfg.prompt,
	}
	a.conn = NewConn(in, out, a)
	close(a.ready)
	return a
}

func (a *fakeAgent) HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	<-a.ready
	switch method {
	case MethodInitialize:
		var p InitializeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, NewRPCError(CodeInvalidParams, err.Error())
		}
		a.mu.Lock()
		a.gotInit = &p
		a.mu.Unlock()
		return a.initResult, nil
	case MethodNewSession:
		var p NewSessionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, NewRPCError(CodeInvalidParams, err.Error())
		}
		a.mu.Lock()
		a.gotNewSession = &p
		a.mu.Unlock()
		return NewSessionResult{SessionID: a.sessionID}, nil
	case MethodPrompt:
		var p PromptParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, NewRPCError(CodeInvalidParams, err.Error())
		}
		a.mu.Lock()
		a.gotPrompt = &p
		a.mu.Unlock()
		if a.prompt == nil {
			return PromptResult{StopReason: StopReasonEndTurn}, nil
		}
		return a.prompt(ctx, a, p)
	default:
		return nil, NewRPCError(CodeMethodNotFound, "fake agent: unhandled method "+method)
	}
}

func (a *fakeAgent) HandleNotification(context.Context, string, json.RawMessage) {}

func (a *fakeAgent) sendUpdate(ctx context.Context, sessionID string, u SessionUpdate) error {
	return a.conn.Notify(ctx, MethodSessionUpdate, SessionNotification{SessionID: sessionID, Update: u})
}

func (a *fakeAgent) requestPermission(ctx context.Context, p RequestPermissionParams) (RequestPermissionOutcome, error) {
	var res RequestPermissionResult
	if err := a.conn.Call(ctx, MethodRequestPermission, p, &res); err != nil {
		return RequestPermissionOutcome{}, err
	}
	a.mu.Lock()
	a.permitOutcome = &res.Outcome
	a.mu.Unlock()
	return res.Outcome, nil
}

// ---- test harness -------------------------------------------------------------

type harness struct {
	client *Client
	agent  *fakeAgent
}

func newHarness(t *testing.T, cfg fakeAgentConfig, delegate ClientDelegate) *harness {
	t.Helper()

	// Two pipes cross-wire the client and agent stdio: each writes to one and
	// reads from the other.
	clientR, agentW := io.Pipe() // agent -> client
	agentR, clientW := io.Pipe() // client -> agent

	agent := startFakeAgent(agentR, agentW, cfg)
	client := NewClient(clientR, clientW, delegate)

	t.Cleanup(func() {
		client.Close()
		agent.conn.Close()
		// Close the write ends so both read loops observe EOF and exit.
		agentW.Close()
		clientW.Close()
		clientR.Close()
		agentR.Close()
	})

	return &harness{client: client, agent: agent}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// recordingDelegate collects streamed updates in order and answers permission
// requests via permit.
type recordingDelegate struct {
	mu      sync.Mutex
	updates []SessionNotification
	permit  func(RequestPermissionParams) RequestPermissionOutcome
}

func (d *recordingDelegate) SessionUpdate(_ context.Context, n SessionNotification) error {
	d.mu.Lock()
	d.updates = append(d.updates, n)
	d.mu.Unlock()
	return nil
}

func (d *recordingDelegate) RequestPermission(_ context.Context, p RequestPermissionParams) (RequestPermissionOutcome, error) {
	if d.permit != nil {
		return d.permit(p), nil
	}
	return CancelledOutcome(), nil
}

func (d *recordingDelegate) kinds() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ks := make([]string, len(d.updates))
	for i, n := range d.updates {
		ks[i] = n.Update.Kind
	}
	return ks
}

// ---- tests --------------------------------------------------------------------

func TestInitializeHandshake(t *testing.T) {
	tests := []struct {
		name       string
		clientInfo Implementation
		agentCaps  AgentCapabilities
	}{
		{
			name:       "minimal",
			clientInfo: Implementation{Name: "conductor", Version: "0.6.0"},
		},
		{
			name:       "with capabilities",
			clientInfo: Implementation{Name: "conductor", Title: "Paseo Conductor", Version: "0.6.0"},
			agentCaps: AgentCapabilities{
				LoadSession:        true,
				PromptCapabilities: PromptCapabilities{Image: true, EmbeddedContext: true},
				McpCapabilities:    McpCapabilities{HTTP: true},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fakeAgentConfig{
				initResult: InitializeResult{
					ProtocolVersion:   ProtocolVersion,
					AgentCapabilities: tc.agentCaps,
					AgentInfo:         &Implementation{Name: "fake-agent", Version: "1.2.3"},
				},
			}
			h := newHarness(t, cfg, &recordingDelegate{})
			ctx := testContext(t)

			res, err := h.client.Initialize(ctx, DefaultInitializeParams(tc.clientInfo))
			if err != nil {
				t.Fatalf("Initialize: %v", err)
			}

			if res.ProtocolVersion != ProtocolVersion {
				t.Errorf("protocol version = %d, want %d", res.ProtocolVersion, ProtocolVersion)
			}
			if !reflect.DeepEqual(res.AgentCapabilities, tc.agentCaps) {
				t.Errorf("agent capabilities = %+v, want %+v", res.AgentCapabilities, tc.agentCaps)
			}
			if res.AgentInfo == nil || res.AgentInfo.Name != "fake-agent" {
				t.Errorf("agent info = %+v, want name fake-agent", res.AgentInfo)
			}

			// The agent must have received the negotiated version and client info.
			h.agent.mu.Lock()
			got := h.agent.gotInit
			h.agent.mu.Unlock()
			if got == nil {
				t.Fatal("agent never received initialize")
			}
			if got.ProtocolVersion != ProtocolVersion {
				t.Errorf("agent saw protocol version %d, want %d", got.ProtocolVersion, ProtocolVersion)
			}
			if got.ClientInfo == nil || got.ClientInfo.Name != tc.clientInfo.Name {
				t.Errorf("agent saw client info %+v, want name %q", got.ClientInfo, tc.clientInfo.Name)
			}
		})
	}
}

func TestNewSession(t *testing.T) {
	tests := []struct {
		name    string
		params  NewSessionParams
		wantMCP int
	}{
		{
			name:   "bare cwd",
			params: NewSessionParams{Cwd: "/work/tree"},
		},
		{
			name: "with mcp server",
			params: NewSessionParams{
				Cwd: "/work/tree",
				McpServers: []McpServer{{
					Name:    "fs",
					Command: "/usr/bin/mcp-fs",
					Args:    []string{"--stdio"},
					Env:     []EnvVariable{{Name: "ROOT", Value: "/work/tree"}},
				}},
			},
			wantMCP: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := fakeAgentConfig{sessionID: "sess_test_123"}
			h := newHarness(t, cfg, &recordingDelegate{})
			ctx := testContext(t)

			res, err := h.client.NewSession(ctx, tc.params)
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			if res.SessionID != "sess_test_123" {
				t.Errorf("session id = %q, want sess_test_123", res.SessionID)
			}

			h.agent.mu.Lock()
			got := h.agent.gotNewSession
			h.agent.mu.Unlock()
			if got == nil {
				t.Fatal("agent never received session/new")
			}
			if got.Cwd != tc.params.Cwd {
				t.Errorf("agent saw cwd %q, want %q", got.Cwd, tc.params.Cwd)
			}
			if len(got.McpServers) != tc.wantMCP {
				t.Fatalf("agent saw %d mcp servers, want %d", len(got.McpServers), tc.wantMCP)
			}
			if tc.wantMCP == 1 {
				if got.McpServers[0].Name != "fs" || got.McpServers[0].Command != "/usr/bin/mcp-fs" {
					t.Errorf("mcp server = %+v", got.McpServers[0])
				}
				if len(got.McpServers[0].Env) != 1 || got.McpServers[0].Env[0].Value != "/work/tree" {
					t.Errorf("mcp env = %+v", got.McpServers[0].Env)
				}
			}
		})
	}
}

func TestPromptTurnStreamsUpdates(t *testing.T) {
	const sessionID = "sess_stream"

	// The scripted turn emits a representative stream: two message chunks sharing
	// a message id, a thought chunk, a plan, then a tool call and its completion.
	script := func(ctx context.Context, a *fakeAgent, p PromptParams) (PromptResult, *RPCError) {
		sid := p.SessionID
		mustSend(a, ctx, sid, SessionUpdate{Kind: UpdateAgentMessageChunk, MessageChunk: &MessageChunk{
			MessageID: "m1", Content: TextBlock("Hello, "),
		}})
		mustSend(a, ctx, sid, SessionUpdate{Kind: UpdateAgentMessageChunk, MessageChunk: &MessageChunk{
			MessageID: "m1", Content: TextBlock("world"),
		}})
		mustSend(a, ctx, sid, SessionUpdate{Kind: UpdateAgentThoughtChunk, MessageChunk: &MessageChunk{
			Content: TextBlock("thinking about it"),
		}})
		mustSend(a, ctx, sid, SessionUpdate{Kind: UpdatePlan, Plan: &Plan{Entries: []PlanEntry{
			{Content: "step one", Priority: "high", Status: "pending"},
			{Content: "step two", Priority: "low", Status: "pending"},
		}}})
		mustSend(a, ctx, sid, SessionUpdate{Kind: UpdateToolCall, ToolCall: &ToolCall{
			ToolCallID: "call_1", Title: "Run tests", Kind: "execute", Status: ToolStatusPending,
		}})
		mustSend(a, ctx, sid, SessionUpdate{Kind: UpdateToolCallUpdate, ToolCallUpdate: &ToolCallUpdate{
			ToolCallID: "call_1", Status: ToolStatusCompleted,
			Content: []ToolCallContent{{Type: "content", Content: ptr(TextBlock("all green"))}},
		}})
		return PromptResult{StopReason: StopReasonEndTurn}, nil
	}

	del := &recordingDelegate{}
	h := newHarness(t, fakeAgentConfig{sessionID: sessionID, prompt: script}, del)
	ctx := testContext(t)

	res, err := h.client.Prompt(ctx, PromptParams{
		SessionID: sessionID,
		Prompt:    []ContentBlock{TextBlock("please run the tests")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if res.StopReason != StopReasonEndTurn {
		t.Errorf("stop reason = %q, want end_turn", res.StopReason)
	}

	// Because notifications are delivered in order on the read loop before the
	// prompt response, every update is recorded by the time Prompt returns.
	wantKinds := []string{
		UpdateAgentMessageChunk, UpdateAgentMessageChunk, UpdateAgentThoughtChunk,
		UpdatePlan, UpdateToolCall, UpdateToolCallUpdate,
	}
	if got := del.kinds(); !reflect.DeepEqual(got, wantKinds) {
		t.Fatalf("update kinds = %v, want %v", got, wantKinds)
	}

	del.mu.Lock()
	defer del.mu.Unlock()

	// Message chunks decode into typed payloads with content preserved.
	if mc := del.updates[0].Update.MessageChunk; mc == nil || mc.MessageID != "m1" || mc.Content.Text != "Hello, " {
		t.Errorf("chunk 0 = %+v", del.updates[0].Update.MessageChunk)
	}
	if mc := del.updates[1].Update.MessageChunk; mc == nil || mc.Content.Text != "world" {
		t.Errorf("chunk 1 = %+v", del.updates[1].Update.MessageChunk)
	}
	// Plan decodes with all entries.
	if pl := del.updates[3].Update.Plan; pl == nil || len(pl.Entries) != 2 || pl.Entries[0].Content != "step one" {
		t.Errorf("plan = %+v", del.updates[3].Update.Plan)
	}
	// Tool call and its update decode into their typed fields.
	if tcv := del.updates[4].Update.ToolCall; tcv == nil || tcv.ToolCallID != "call_1" || tcv.Status != ToolStatusPending {
		t.Errorf("tool_call = %+v", del.updates[4].Update.ToolCall)
	}
	upd := del.updates[5].Update.ToolCallUpdate
	if upd == nil || upd.Status != ToolStatusCompleted {
		t.Fatalf("tool_call_update = %+v", upd)
	}
	if len(upd.Content) != 1 || upd.Content[0].Content == nil || upd.Content[0].Content.Text != "all green" {
		t.Errorf("tool_call_update content = %+v", upd.Content)
	}
	// Every update carries its originating session id.
	for i, n := range del.updates {
		if n.SessionID != sessionID {
			t.Errorf("update %d session id = %q, want %q", i, n.SessionID, sessionID)
		}
	}
}

func TestPromptPermissionRoundTrip(t *testing.T) {
	const sessionID = "sess_perm"

	// The scripted turn announces a tool call, asks the client for permission,
	// then reports the tool as completed or failed based on the decision.
	script := func(ctx context.Context, a *fakeAgent, p PromptParams) (PromptResult, *RPCError) {
		sid := p.SessionID
		mustSend(a, ctx, sid, SessionUpdate{Kind: UpdateToolCall, ToolCall: &ToolCall{
			ToolCallID: "call_write", Title: "Write config.yaml", Kind: "edit", Status: ToolStatusPending,
		}})
		outcome, err := a.requestPermission(ctx, RequestPermissionParams{
			SessionID: sid,
			ToolCall:  ToolCallUpdate{ToolCallID: "call_write"},
			Options: []PermissionOption{
				{OptionID: "allow-once", Name: "Allow once", Kind: PermissionAllowOnce},
				{OptionID: "reject-once", Name: "Reject", Kind: PermissionRejectOnce},
			},
		})
		if err != nil {
			return PromptResult{}, NewRPCError(CodeInternalError, err.Error())
		}
		status := ToolStatusFailed
		if outcome.Outcome == OutcomeSelected && outcome.OptionID == "allow-once" {
			status = ToolStatusCompleted
		}
		mustSend(a, ctx, sid, SessionUpdate{Kind: UpdateToolCallUpdate, ToolCallUpdate: &ToolCallUpdate{
			ToolCallID: "call_write", Status: status,
		}})
		return PromptResult{StopReason: StopReasonEndTurn}, nil
	}

	tests := []struct {
		name         string
		decision     RequestPermissionOutcome
		wantOutcome  string
		wantOptionID string
		wantStatus   string
	}{
		{
			name:         "allow",
			decision:     SelectedOutcome("allow-once"),
			wantOutcome:  OutcomeSelected,
			wantOptionID: "allow-once",
			wantStatus:   ToolStatusCompleted,
		},
		{
			name:        "cancel",
			decision:    CancelledOutcome(),
			wantOutcome: OutcomeCancelled,
			wantStatus:  ToolStatusFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotReq RequestPermissionParams
			var once sync.Once
			del := &recordingDelegate{
				permit: func(p RequestPermissionParams) RequestPermissionOutcome {
					once.Do(func() { gotReq = p })
					return tc.decision
				},
			}
			h := newHarness(t, fakeAgentConfig{sessionID: sessionID, prompt: script}, del)
			ctx := testContext(t)

			res, err := h.client.Prompt(ctx, PromptParams{
				SessionID: sessionID,
				Prompt:    []ContentBlock{TextBlock("write the config")},
			})
			if err != nil {
				t.Fatalf("Prompt: %v", err)
			}
			if res.StopReason != StopReasonEndTurn {
				t.Errorf("stop reason = %q, want end_turn", res.StopReason)
			}

			// The client saw a well-formed permission request.
			if gotReq.SessionID != sessionID {
				t.Errorf("permission session id = %q, want %q", gotReq.SessionID, sessionID)
			}
			if gotReq.ToolCall.ToolCallID != "call_write" {
				t.Errorf("permission tool call id = %q, want call_write", gotReq.ToolCall.ToolCallID)
			}
			if len(gotReq.Options) != 2 || gotReq.Options[0].OptionID != "allow-once" {
				t.Errorf("permission options = %+v", gotReq.Options)
			}

			// The agent received exactly the decision the client returned.
			h.agent.mu.Lock()
			gotOutcome := h.agent.permitOutcome
			h.agent.mu.Unlock()
			if gotOutcome == nil {
				t.Fatal("agent never received a permission outcome")
			}
			if gotOutcome.Outcome != tc.wantOutcome || gotOutcome.OptionID != tc.wantOptionID {
				t.Errorf("agent outcome = %+v, want outcome=%q optionID=%q", gotOutcome, tc.wantOutcome, tc.wantOptionID)
			}

			// The final streamed status reflects the decision.
			del.mu.Lock()
			defer del.mu.Unlock()
			last := del.updates[len(del.updates)-1].Update.ToolCallUpdate
			if last == nil || last.Status != tc.wantStatus {
				t.Errorf("final tool status = %+v, want %q", last, tc.wantStatus)
			}
		})
	}
}

func TestPromptPropagatesAgentError(t *testing.T) {
	script := func(ctx context.Context, a *fakeAgent, p PromptParams) (PromptResult, *RPCError) {
		return PromptResult{}, NewRPCError(CodeInternalError, "model exploded")
	}
	h := newHarness(t, fakeAgentConfig{sessionID: "s", prompt: script}, &recordingDelegate{})
	ctx := testContext(t)

	_, err := h.client.Prompt(ctx, PromptParams{SessionID: "s", Prompt: []ContentBlock{TextBlock("hi")}})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rpcErr *RPCError
	if !asRPCError(err, &rpcErr) {
		t.Fatalf("error %v (%T) is not *RPCError", err, err)
	}
	if rpcErr.Code != CodeInternalError || rpcErr.Message != "model exploded" {
		t.Errorf("rpc error = %+v", rpcErr)
	}
}

func TestCallAfterCloseFails(t *testing.T) {
	h := newHarness(t, fakeAgentConfig{sessionID: "s"}, &recordingDelegate{})
	h.client.Close()

	// A short independent context so the call can't hang on the timeout harness.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := h.client.Initialize(ctx, DefaultInitializeParams(Implementation{Name: "c"})); err == nil {
		t.Fatal("expected error calling on a closed client")
	}
}

func TestSessionUpdateJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		update SessionUpdate
		check  func(t *testing.T, got SessionUpdate)
	}{
		{
			name:   "message chunk",
			update: SessionUpdate{Kind: UpdateAgentMessageChunk, MessageChunk: &MessageChunk{MessageID: "m", Content: TextBlock("hi")}},
			check: func(t *testing.T, got SessionUpdate) {
				if got.MessageChunk == nil || got.MessageChunk.Content.Text != "hi" {
					t.Errorf("message chunk = %+v", got.MessageChunk)
				}
			},
		},
		{
			name:   "plan",
			update: SessionUpdate{Kind: UpdatePlan, Plan: &Plan{Entries: []PlanEntry{{Content: "a"}}}},
			check: func(t *testing.T, got SessionUpdate) {
				if got.Plan == nil || len(got.Plan.Entries) != 1 || got.Plan.Entries[0].Content != "a" {
					t.Errorf("plan = %+v", got.Plan)
				}
			},
		},
		{
			name:   "tool call",
			update: SessionUpdate{Kind: UpdateToolCall, ToolCall: &ToolCall{ToolCallID: "t", Status: ToolStatusInProgress}},
			check: func(t *testing.T, got SessionUpdate) {
				if got.ToolCall == nil || got.ToolCall.ToolCallID != "t" {
					t.Errorf("tool call = %+v", got.ToolCall)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.update)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// The discriminator must be present at the top level.
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(b, &raw); err != nil {
				t.Fatalf("unmarshal to map: %v", err)
			}
			if _, ok := raw["sessionUpdate"]; !ok {
				t.Fatalf("marshaled update missing sessionUpdate discriminator: %s", b)
			}

			var got SessionUpdate
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Kind != tc.update.Kind {
				t.Errorf("kind = %q, want %q", got.Kind, tc.update.Kind)
			}
			tc.check(t, got)
		})
	}
}

// ---- helpers ------------------------------------------------------------------

func mustSend(a *fakeAgent, ctx context.Context, sessionID string, u SessionUpdate) {
	if err := a.sendUpdate(ctx, sessionID, u); err != nil {
		panic("fake agent send update: " + err.Error())
	}
}

func ptr[T any](v T) *T { return &v }

func asRPCError(err error, target **RPCError) bool {
	if e, ok := err.(*RPCError); ok {
		*target = e
		return true
	}
	return false
}
