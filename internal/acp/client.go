// Package acp implements a client for the Agent Client Protocol (ACP,
// agentclientprotocol.com): JSON-RPC 2.0 over stdio between a client (the editor
// or, here, conductor) and an agent subprocess.
//
// The client drives the agent through the ACP lifecycle — initialize handshake
// with capability negotiation, session/new, and session/prompt turns — while a
// ClientDelegate handles the traffic the agent sends back mid-turn: streamed
// session/update notifications and session/request_permission callbacks.
//
// This package is a self-contained transport library. It does not implement the
// optional fs/* and terminal/* client methods (a later milestone) and so
// advertises no such capabilities by default.
package acp

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
)

// ClientDelegate receives agent-initiated traffic during a session: streamed
// session/update notifications and session/request_permission callbacks. Both
// may be invoked concurrently for different sessions; implementations that share
// state must guard it.
type ClientDelegate interface {
	// SessionUpdate handles a streamed session/update notification. It is called
	// in order per connection; it should not block for long.
	SessionUpdate(ctx context.Context, n SessionNotification) error
	// RequestPermission is called when the agent asks the client to authorize a
	// tool call. It returns the selected option (SelectedOutcome) or
	// CancelledOutcome.
	RequestPermission(ctx context.Context, p RequestPermissionParams) (RequestPermissionOutcome, error)
}

// Client is an ACP client bound to a single agent connection.
type Client struct {
	conn     *Conn
	delegate ClientDelegate
}

// NewClient builds a Client that reads agent messages from in and writes to out
// (the agent subprocess's stdout and stdin). It starts the background read loop
// immediately. A nil delegate cancels every permission request and drops updates.
func NewClient(in io.Reader, out io.Writer, delegate ClientDelegate) *Client {
	if delegate == nil {
		delegate = DelegateFuncs{}
	}
	c := &Client{delegate: delegate}
	c.conn = NewConn(in, out, c)
	return c
}

// Spawn starts name+args as an agent subprocess and returns a Client wired to its
// stdio. The child's stderr is inherited from the parent. Close the Client and
// wait on the returned Cmd to reap the process.
func Spawn(ctx context.Context, delegate ClientDelegate, name string, args ...string) (*Client, *exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return NewClient(stdout, stdin, delegate), cmd, nil
}

// HandleRequest implements Handler for the client-role methods.
func (c *Client) HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	switch method {
	case MethodRequestPermission:
		var p RequestPermissionParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, NewRPCError(CodeInvalidParams, err.Error())
		}
		outcome, err := c.delegate.RequestPermission(ctx, p)
		if err != nil {
			return nil, NewRPCError(CodeInternalError, err.Error())
		}
		return RequestPermissionResult{Outcome: outcome}, nil
	default:
		return nil, NewRPCError(CodeMethodNotFound, "acp: unhandled client method "+method)
	}
}

// HandleNotification implements Handler for the client-role notifications.
func (c *Client) HandleNotification(ctx context.Context, method string, params json.RawMessage) {
	switch method {
	case MethodSessionUpdate:
		var n SessionNotification
		if err := json.Unmarshal(params, &n); err != nil {
			return
		}
		_ = c.delegate.SessionUpdate(ctx, n)
	}
}

// Initialize performs the initialize handshake and returns the agent's
// negotiated capabilities.
func (c *Client) Initialize(ctx context.Context, params InitializeParams) (*InitializeResult, error) {
	if params.ProtocolVersion == 0 {
		params.ProtocolVersion = ProtocolVersion
	}
	var res InitializeResult
	if err := c.conn.Call(ctx, MethodInitialize, params, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// NewSession creates a new session rooted at params.Cwd and returns its id.
func (c *Client) NewSession(ctx context.Context, params NewSessionParams) (*NewSessionResult, error) {
	if params.McpServers == nil {
		params.McpServers = []McpServer{}
	}
	var res NewSessionResult
	if err := c.conn.Call(ctx, MethodNewSession, params, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Prompt runs one prompt turn and blocks until the agent ends it, returning the
// stop reason. Streamed updates and permission requests reach the delegate while
// this call is in flight.
func (c *Client) Prompt(ctx context.Context, params PromptParams) (*PromptResult, error) {
	var res PromptResult
	if err := c.conn.Call(ctx, MethodPrompt, params, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Cancel sends a session/cancel notification for the given session. The agent
// still ends the in-flight turn with a "cancelled" stop reason.
func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	return c.conn.Notify(ctx, MethodSessionCancel, CancelParams{SessionID: sessionID})
}

// Conn exposes the underlying JSON-RPC connection for advanced use.
func (c *Client) Conn() *Conn { return c.conn }

// Close shuts the client's connection down.
func (c *Client) Close() error { return c.conn.Close() }

// Done is closed when the underlying connection shuts down.
func (c *Client) Done() <-chan struct{} { return c.conn.Done() }

// DefaultInitializeParams returns initialize params identifying the client via
// info, advertising no fs/terminal capabilities (unimplemented at this layer).
func DefaultInitializeParams(info Implementation) InitializeParams {
	return InitializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo:      &info,
	}
}

// DelegateFuncs is a function-based ClientDelegate. Nil hooks are safe: a nil
// SessionUpdate hook drops updates, and a nil RequestPermission hook cancels.
type DelegateFuncs struct {
	OnUpdate     func(ctx context.Context, n SessionNotification) error
	OnPermission func(ctx context.Context, p RequestPermissionParams) (RequestPermissionOutcome, error)
}

// SessionUpdate implements ClientDelegate.
func (d DelegateFuncs) SessionUpdate(ctx context.Context, n SessionNotification) error {
	if d.OnUpdate != nil {
		return d.OnUpdate(ctx, n)
	}
	return nil
}

// RequestPermission implements ClientDelegate.
func (d DelegateFuncs) RequestPermission(ctx context.Context, p RequestPermissionParams) (RequestPermissionOutcome, error) {
	if d.OnPermission != nil {
		return d.OnPermission(ctx, p)
	}
	return CancelledOutcome(), nil
}
