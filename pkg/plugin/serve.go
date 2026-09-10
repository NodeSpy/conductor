package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// JSON-RPC 2.0 error codes (a subset of the standard set the daemon uses).
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Error is a JSON-RPC error a handler can return to send a structured error
// response instead of a result. Any other (plain) error a handler returns is
// wrapped as CodeInternalError.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Errorf builds a JSON-RPC *Error with the given code.
func Errorf(code int, msg string) *Error { return &Error{Code: code, Message: msg} }

// Handler is what a plugin implements. Describe returns the plugin's self-
// description; Invoke runs one verb call. A Handler that also implements
// nothing else is a verb-only connector — the common case.
type Handler interface {
	Describe() Decl
	Invoke(InvokeRequest) (InvokeResult, error)
}

// SourceHandler is implemented by a plugin that ALSO emits events (a source).
// On a start_source call, Serve acks immediately and runs StartSource in the
// background: call emit for each event (the payload is marshaled into a
// plugin.event notification the daemon feeds to its engine). StartSource should
// return when ctx is cancelled (Serve cancels it when the daemon closes stdin).
type SourceHandler interface {
	StartSource(ctx context.Context, req StartSourceRequest, emit func(payload any) error) error
}

// ConnectorFunc adapts two funcs into a Handler, for a verb-only connector
// plugin with no other state.
func ConnectorFunc(describe func() Decl, invoke func(InvokeRequest) (InvokeResult, error)) Handler {
	return funcHandler{describe: describe, invoke: invoke}
}

type funcHandler struct {
	describe func() Decl
	invoke   func(InvokeRequest) (InvokeResult, error)
}

func (h funcHandler) Describe() Decl                               { return h.describe() }
func (h funcHandler) Invoke(r InvokeRequest) (InvokeResult, error) { return h.invoke(r) }

// Serve runs the plugin protocol on stdin/stdout until stdin closes (the daemon
// shut the plugin down) — the normal way a plugin's main() ends. All logging
// must go to stderr; stdout is the RPC transport. Returns nil on clean EOF.
func Serve(h Handler) error { return serve(os.Stdin, os.Stdout, h) }

// wireMessage is the JSON-RPC 2.0 envelope, matching the daemon's transport
// (internal/acp): a request carries method+id, a response carries id+result or
// id+error. id is echoed back verbatim.
type wireMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *Error           `json:"error,omitempty"`
}

func serve(in io.Reader, out io.Writer, h Handler) error {
	dec := json.NewDecoder(io.LimitReader(in, maxRequestBytes))
	enc := json.NewEncoder(out)
	var writeMu sync.Mutex
	write := func(m wireMessage) error {
		m.JSONRPC = "2.0"
		writeMu.Lock()
		defer writeMu.Unlock()
		return enc.Encode(m) // Encode appends '\n' — newline-delimited framing
	}
	// ctx bounds any background source stream: cancelled when the loop exits (the
	// daemon closed stdin), so a StartSource goroutine unwinds.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// In-flight request handlers. Serve must not return while one is still
	// writing: its response would be lost and the caller would see a dead
	// transport instead of an answer. Long-lived StartSource streams are
	// NOT tracked here — they unwind on ctx.
	var inflight sync.WaitGroup
	defer inflight.Wait()
	// emit writes a plugin.event notification (no id) — the source stream path.
	emit := func(payload any) error {
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		return write(wireMessage{Method: MethodEvent, Params: raw})
	}

	for {
		var m wireMessage
		if err := dec.Decode(&m); err != nil {
			if errors.Is(err, io.EOF) {
				return nil // daemon closed stdin: clean shutdown
			}
			return err
		}
		// Only requests (method + id) are serviced; notifications and stray
		// responses are ignored (a plugin never calls back into the daemon).
		if m.Method == "" || m.ID == nil {
			continue
		}
		// Service each request on its own goroutine. Running dispatch
		// inline meant ONE slow verb — a paseo `wait` on a long agent, an
		// unreachable API — blocked the read loop, so every later call
		// (including a plain Describe) queued behind it and the daemon
		// saw the whole plugin as hung. StartSource already got a
		// goroutine for exactly this reason; the rest of the surface
		// needs the same treatment.
		//
		// write() is mutex-guarded, and each response carries its own
		// echoed id, so out-of-order completion is the protocol working
		// as designed rather than a hazard.
		inflight.Add(1)
		go func(m wireMessage) {
			defer inflight.Done()
			resp := wireMessage{ID: m.ID}
			result, rpcErr := dispatch(ctx, h, m.Method, m.Params, emit)
			if rpcErr != nil {
				resp.Error = rpcErr
			} else if raw, err := json.Marshal(result); err != nil {
				resp.Error = Errorf(CodeInternalError, err.Error())
			} else {
				resp.Result = raw
			}
			// A write failure means stdout is gone; the read loop will
			// see stdin close and return. Nothing to escalate here.
			_ = write(resp)
		}(m)
	}
}

// maxRequestBytes bounds one JSON-RPC request. The daemon is the only
// writer, but a plugin must not be a way to turn a malformed or hostile
// frame into unbounded memory on the box.
const maxRequestBytes = 32 << 20

func dispatch(ctx context.Context, h Handler, method string, params json.RawMessage, emit func(any) error) (result any, rpcErr *Error) {
	// A panic in third-party plugin code is a protocol ERROR, not a dead
	// process. Letting it escape killed the plugin mid-conversation and
	// took every in-flight call with it; the daemon then saw a transport
	// failure with nothing to attribute it to.
	defer func() {
		if r := recover(); r != nil {
			result = nil
			rpcErr = Errorf(CodeInternalError, fmt.Sprintf("plugin panicked handling %s: %v", method, r))
		}
	}()
	switch method {
	case MethodDescribe:
		d := h.Describe()
		if d.ProtocolVersion == 0 {
			d.ProtocolVersion = ProtocolVersion
		}
		return d, nil
	case MethodInvoke:
		var req InvokeRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, Errorf(CodeInvalidParams, err.Error())
			}
		}
		res, err := h.Invoke(req)
		if err != nil {
			var re *Error
			if errors.As(err, &re) {
				return nil, re
			}
			return nil, Errorf(CodeInternalError, err.Error())
		}
		return res, nil
	case MethodStartSource:
		sh, ok := h.(SourceHandler)
		if !ok {
			return nil, Errorf(CodeMethodNotFound, "this plugin is not a source")
		}
		var req StartSourceRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, Errorf(CodeInvalidParams, err.Error())
			}
		}
		// Ack immediately; stream events in the background until ctx cancels.
		go func() { _ = sh.StartSource(ctx, req, emit) }()
		return struct{}{}, nil
	default:
		return nil, Errorf(CodeMethodNotFound, "unknown method "+method)
	}
}
