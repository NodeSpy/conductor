package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// RPCError is a JSON-RPC 2.0 error object. It doubles as a Go error so it can be
// returned directly from Call.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("acp rpc error %d: %s (%s)", e.Code, e.Message, e.Data)
	}
	return fmt.Sprintf("acp rpc error %d: %s", e.Code, e.Message)
}

// NewRPCError builds an *RPCError with the given code and message.
func NewRPCError(code int, msg string) *RPCError {
	return &RPCError{Code: code, Message: msg}
}

// ErrClosed is returned by calls made after the connection has shut down.
var ErrClosed = errors.New("acp: connection closed")

// Handler processes peer-initiated traffic: requests that expect a response and
// one-way notifications. A Client implements this against the ACP client-role
// methods; a test agent double implements the agent-role methods.
type Handler interface {
	// HandleRequest handles an incoming request. Returning a non-nil *RPCError
	// sends an error response; otherwise the returned value is marshaled as the
	// result. It runs on its own goroutine, so it may itself Call the peer.
	HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError)
	// HandleNotification handles an incoming one-way notification. Notifications
	// are delivered in order on the read loop, so this must not block on a Call
	// back to the same peer.
	HandleNotification(ctx context.Context, method string, params json.RawMessage)
}

// wireMessage is the on-the-wire JSON-RPC 2.0 envelope. A single struct covers
// requests, notifications, responses, and errors; the combination of populated
// fields discriminates the kind.
type wireMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *RPCError        `json:"error,omitempty"`
}

// Conn is a bidirectional JSON-RPC 2.0 peer speaking newline-delimited JSON over
// a reader/writer pair. For a subprocess agent that pair is the child's stdout
// (read) and stdin (write). Conn is safe for concurrent use.
type Conn struct {
	enc     *json.Encoder
	dec     *json.Decoder
	writeMu sync.Mutex

	handler Handler

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan wireMessage

	ctx     context.Context
	cancel  context.CancelFunc
	closed  chan struct{}
	once    sync.Once
	readErr error
}

// NewConn creates a Conn over in/out and starts its read loop in the background.
// The read loop runs until in returns an error (e.g. EOF) or Close is called and
// the underlying reader unblocks.
func NewConn(in io.Reader, out io.Writer, handler Handler) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		enc:     json.NewEncoder(out),
		dec:     json.NewDecoder(in),
		handler: handler,
		pending: make(map[int64]chan wireMessage),
		ctx:     ctx,
		cancel:  cancel,
		closed:  make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *Conn) readLoop() {
	for {
		var m wireMessage
		if err := c.dec.Decode(&m); err != nil {
			c.shutdown(err)
			return
		}
		c.dispatch(m)
	}
}

func (c *Conn) dispatch(m wireMessage) {
	switch {
	case m.Method != "" && m.ID != nil:
		// Request: serve on its own goroutine so the handler may Call back into
		// this peer (e.g. an agent's session/prompt handler issuing
		// session/request_permission) without deadlocking the read loop.
		go c.serveRequest(m)
	case m.Method != "":
		// Notification: delivered in order so streamed session/update chunks
		// stay sequenced.
		c.handler.HandleNotification(c.ctx, m.Method, m.Params)
	case m.ID != nil:
		// Response to one of our outstanding calls.
		c.deliver(m)
	}
}

func (c *Conn) serveRequest(m wireMessage) {
	result, rpcErr := c.handler.HandleRequest(c.ctx, m.Method, m.Params)
	resp := wireMessage{ID: m.ID}
	if rpcErr != nil {
		resp.Error = rpcErr
	} else {
		raw, err := json.Marshal(result)
		if err != nil {
			resp.Error = NewRPCError(CodeInternalError, err.Error())
		} else {
			resp.Result = raw
		}
	}
	_ = c.write(resp)
}

func (c *Conn) deliver(m wireMessage) {
	var id int64
	if err := json.Unmarshal(*m.ID, &id); err != nil {
		return
	}
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

// Call sends a request and blocks until the response arrives, ctx is cancelled,
// or the connection closes. result, if non-nil, receives the unmarshaled result.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}

	c.mu.Lock()
	id := c.nextID
	c.nextID++
	ch := make(chan wireMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	idRaw := json.RawMessage(strconv.FormatInt(id, 10))
	if err := c.write(wireMessage{ID: &idRaw, Method: method, Params: raw}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	case <-c.closed:
		return c.err()
	case resp := <-ch:
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

// Notify sends a one-way notification; it does not wait for a response.
func (c *Conn) Notify(_ context.Context, method string, params any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}
	return c.write(wireMessage{Method: method, Params: raw})
}

func (c *Conn) write(m wireMessage) error {
	m.JSONRPC = "2.0"
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.closed:
		return ErrClosed
	default:
	}
	// json.Encoder.Encode appends a newline, giving newline-delimited framing.
	return c.enc.Encode(m)
}

func (c *Conn) shutdown(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.readErr = err
		c.pending = make(map[int64]chan wireMessage)
		c.mu.Unlock()
		close(c.closed)
		c.cancel()
	})
}

func (c *Conn) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil && !errors.Is(c.readErr, io.EOF) {
		return c.readErr
	}
	return ErrClosed
}

// Close shuts the connection down and unblocks pending calls. The background read
// loop ends once the underlying reader unblocks (close it, or terminate the
// subprocess, to guarantee that).
func (c *Conn) Close() error {
	c.shutdown(ErrClosed)
	return nil
}

// Done is closed when the connection shuts down.
func (c *Conn) Done() <-chan struct{} { return c.closed }

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return nil, nil
	}
	return json.Marshal(params)
}
