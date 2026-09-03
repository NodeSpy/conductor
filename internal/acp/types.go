package acp

import "encoding/json"

// ProtocolVersion is the ACP MAJOR protocol version this library speaks.
const ProtocolVersion = 1

// ACP method names. Agent-role methods are called by the client; client-role
// methods are called by the agent.
const (
	MethodInitialize        = "initialize"                 // agent
	MethodAuthenticate      = "authenticate"               // agent
	MethodNewSession        = "session/new"                // agent
	MethodLoadSession       = "session/load"               // agent
	MethodPrompt            = "session/prompt"             // agent
	MethodSessionCancel     = "session/cancel"             // agent (notification)
	MethodSessionUpdate     = "session/update"             // client (notification)
	MethodRequestPermission = "session/request_permission" // client
)

// Implementation identifies a client or agent by name/title/version.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// ---- initialize ----

// InitializeParams is the initialize request payload.
type InitializeParams struct {
	ProtocolVersion    int                `json:"protocolVersion"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *Implementation    `json:"clientInfo,omitempty"`
}

// ClientCapabilities advertises which client-role methods the agent may use.
type ClientCapabilities struct {
	FS       FileSystemCapability `json:"fs"`
	Terminal bool                 `json:"terminal,omitempty"`
}

// FileSystemCapability gates the fs/* client methods.
type FileSystemCapability struct {
	ReadTextFile  bool `json:"readTextFile,omitempty"`
	WriteTextFile bool `json:"writeTextFile,omitempty"`
}

// InitializeResult is the agent's initialize response.
type InitializeResult struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
	AuthMethods       []AuthMethod      `json:"authMethods,omitempty"`
}

// AgentCapabilities describes optional agent behavior negotiated at initialize.
type AgentCapabilities struct {
	LoadSession        bool               `json:"loadSession,omitempty"`
	PromptCapabilities PromptCapabilities `json:"promptCapabilities,omitempty"`
	McpCapabilities    McpCapabilities    `json:"mcpCapabilities,omitempty"`
}

// PromptCapabilities lists which non-text content blocks a prompt may carry.
type PromptCapabilities struct {
	Image           bool `json:"image,omitempty"`
	Audio           bool `json:"audio,omitempty"`
	EmbeddedContext bool `json:"embeddedContext,omitempty"`
}

// McpCapabilities lists which MCP server transports the agent can connect to.
type McpCapabilities struct {
	HTTP bool `json:"http,omitempty"`
	SSE  bool `json:"sse,omitempty"`
}

// AuthMethod is one authentication option the agent offers.
type AuthMethod struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ---- session/new ----

// NewSessionParams is the session/new request payload.
type NewSessionParams struct {
	Cwd        string      `json:"cwd"`
	McpServers []McpServer `json:"mcpServers"`
}

// McpServer is a stdio MCP server the agent should connect for the session.
type McpServer struct {
	Name    string        `json:"name"`
	Command string        `json:"command"`
	Args    []string      `json:"args,omitempty"`
	Env     []EnvVariable `json:"env,omitempty"`
}

// EnvVariable is a name/value pair passed to an MCP server process.
type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// NewSessionResult is the session/new response.
type NewSessionResult struct {
	SessionID string `json:"sessionId"`
}

// ---- content blocks ----

// ContentBlock is a single piece of prompt or message content. Text is fully
// modeled; other block types carry their common fields and are otherwise passed
// through verbatim.
type ContentBlock struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	Data     string            `json:"data,omitempty"`     // image/audio base64
	MimeType string            `json:"mimeType,omitempty"` // image/audio/resource_link
	URI      string            `json:"uri,omitempty"`      // resource_link
	Name     string            `json:"name,omitempty"`     // resource_link
	Resource *ResourceContents `json:"resource,omitempty"` // embedded resource
}

// TextBlock is a shorthand for a text ContentBlock.
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: "text", Text: text}
}

// ResourceContents is inline resource content embedded in a content block.
type ResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

// ---- session/prompt ----

// PromptParams is the session/prompt request payload.
type PromptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

// StopReason is why a prompt turn ended.
type StopReason string

const (
	StopReasonEndTurn         StopReason = "end_turn"
	StopReasonMaxTokens       StopReason = "max_tokens"
	StopReasonMaxTurnRequests StopReason = "max_turn_requests"
	StopReasonRefusal         StopReason = "refusal"
	StopReasonCancelled       StopReason = "cancelled"
)

// PromptResult is the session/prompt response, returned when the turn ends.
type PromptResult struct {
	StopReason StopReason `json:"stopReason"`
}

// CancelParams is the session/cancel notification payload.
type CancelParams struct {
	SessionID string `json:"sessionId"`
}

// ---- session/update ----

// SessionNotification is the session/update notification payload.
type SessionNotification struct {
	SessionID string        `json:"sessionId"`
	Update    SessionUpdate `json:"update"`
}

// session/update discriminator values (the "sessionUpdate" field).
const (
	UpdateAgentMessageChunk = "agent_message_chunk"
	UpdateAgentThoughtChunk = "agent_thought_chunk"
	UpdateUserMessageChunk  = "user_message_chunk"
	UpdateToolCall          = "tool_call"
	UpdateToolCallUpdate    = "tool_call_update"
	UpdatePlan              = "plan"
)

// SessionUpdate is the discriminated "update" object of a session/update
// notification. Kind holds the discriminator; the matching typed field is set
// for the kinds this library models (message chunks, tool calls, plans). Raw
// always holds the original object so unmodeled kinds are not lost.
type SessionUpdate struct {
	Kind string

	MessageChunk   *MessageChunk   // *_message_chunk
	ToolCall       *ToolCall       // tool_call
	ToolCallUpdate *ToolCallUpdate // tool_call_update
	Plan           *Plan           // plan

	Raw json.RawMessage
}

// MessageChunk is a streamed chunk of an agent/user/thought message. Chunks that
// share a MessageID belong to the same message.
type MessageChunk struct {
	MessageID string       `json:"messageId,omitempty"`
	Content   ContentBlock `json:"content"`
}

// ToolCall reports a tool invocation the agent has started.
type ToolCall struct {
	ToolCallID string            `json:"toolCallId"`
	Title      string            `json:"title,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Status     string            `json:"status,omitempty"`
	Content    []ToolCallContent `json:"content,omitempty"`
	RawInput   json.RawMessage   `json:"rawInput,omitempty"`
}

// ToolCallUpdate reports a change to an in-flight tool call.
type ToolCallUpdate struct {
	ToolCallID string            `json:"toolCallId"`
	Title      string            `json:"title,omitempty"`
	Kind       string            `json:"kind,omitempty"`
	Status     string            `json:"status,omitempty"`
	Content    []ToolCallContent `json:"content,omitempty"`
	RawOutput  json.RawMessage   `json:"rawOutput,omitempty"`
}

// ToolCallContent is one item of tool-call output: regular content, a file diff,
// or a terminal reference.
type ToolCallContent struct {
	Type    string        `json:"type"` // "content" | "diff" | "terminal"
	Content *ContentBlock `json:"content,omitempty"`
	Path    string        `json:"path,omitempty"`    // diff
	OldText string        `json:"oldText,omitempty"` // diff
	NewText string        `json:"newText,omitempty"` // diff
}

// Tool call status values.
const (
	ToolStatusPending    = "pending"
	ToolStatusInProgress = "in_progress"
	ToolStatusCompleted  = "completed"
	ToolStatusFailed     = "failed"
)

// Plan is the agent's current execution plan for the turn.
type Plan struct {
	Entries []PlanEntry `json:"entries"`
}

// PlanEntry is one item in a Plan.
type PlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority,omitempty"`
	Status   string `json:"status,omitempty"`
}

// UnmarshalJSON decodes the update object, routing on the "sessionUpdate"
// discriminator into the matching typed field and preserving the raw object.
func (u *SessionUpdate) UnmarshalJSON(b []byte) error {
	var disc struct {
		Kind string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(b, &disc); err != nil {
		return err
	}
	*u = SessionUpdate{Kind: disc.Kind, Raw: append(json.RawMessage(nil), b...)}
	switch disc.Kind {
	case UpdateAgentMessageChunk, UpdateAgentThoughtChunk, UpdateUserMessageChunk:
		u.MessageChunk = &MessageChunk{}
		return json.Unmarshal(b, u.MessageChunk)
	case UpdateToolCall:
		u.ToolCall = &ToolCall{}
		return json.Unmarshal(b, u.ToolCall)
	case UpdateToolCallUpdate:
		u.ToolCallUpdate = &ToolCallUpdate{}
		return json.Unmarshal(b, u.ToolCallUpdate)
	case UpdatePlan:
		u.Plan = &Plan{}
		return json.Unmarshal(b, u.Plan)
	}
	return nil
}

// MarshalJSON encodes the update object with its discriminator merged in at the
// top level. Unmodeled kinds fall back to the preserved Raw payload.
func (u SessionUpdate) MarshalJSON() ([]byte, error) {
	var payload any
	switch u.Kind {
	case UpdateAgentMessageChunk, UpdateAgentThoughtChunk, UpdateUserMessageChunk:
		payload = u.MessageChunk
	case UpdateToolCall:
		payload = u.ToolCall
	case UpdateToolCallUpdate:
		payload = u.ToolCallUpdate
	case UpdatePlan:
		payload = u.Plan
	default:
		if len(u.Raw) > 0 {
			return u.Raw, nil
		}
	}

	fields := map[string]json.RawMessage{}
	if payload != nil {
		pb, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(pb, &fields); err != nil {
			return nil, err
		}
	}
	kb, err := json.Marshal(u.Kind)
	if err != nil {
		return nil, err
	}
	fields["sessionUpdate"] = kb
	return json.Marshal(fields)
}

// ---- session/request_permission ----

// RequestPermissionParams is the session/request_permission request payload.
type RequestPermissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

// PermissionOption is one choice the agent offers for a permission request.
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

// Permission option kinds.
const (
	PermissionAllowOnce    = "allow_once"
	PermissionAllowAlways  = "allow_always"
	PermissionRejectOnce   = "reject_once"
	PermissionRejectAlways = "reject_always"
)

// Permission outcome discriminator values.
const (
	OutcomeSelected  = "selected"
	OutcomeCancelled = "cancelled"
)

// RequestPermissionResult is the session/request_permission response.
type RequestPermissionResult struct {
	Outcome RequestPermissionOutcome `json:"outcome"`
}

// RequestPermissionOutcome is the client's decision on a permission request.
// Outcome is "selected" (with OptionID) or "cancelled".
type RequestPermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// SelectedOutcome builds a "selected" permission outcome for optionID.
func SelectedOutcome(optionID string) RequestPermissionOutcome {
	return RequestPermissionOutcome{Outcome: OutcomeSelected, OptionID: optionID}
}

// CancelledOutcome builds a "cancelled" permission outcome.
func CancelledOutcome() RequestPermissionOutcome {
	return RequestPermissionOutcome{Outcome: OutcomeCancelled}
}
