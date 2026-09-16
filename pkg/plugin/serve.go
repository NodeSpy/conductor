package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
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

// EngineHandler is implemented by a STEP ENGINE plugin (Decl.Kind = KindStep):
// Run executes one code step. The *Host it is handed is the callback channel
// back into conductor for that run and ONLY that run — it stops answering the
// moment Run returns, so an engine must not stash it.
//
// Run may be called concurrently (conductor runs steps in parallel), and each
// call gets its own *Host.
type EngineHandler interface {
	Run(ctx context.Context, req RunRequest, host *Host) (RunResult, error)
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

// EngineFunc adapts describe+run into a Handler for a step-engine plugin —
// the engine-author mirror of ConnectorFunc:
//
//	plugin.Serve(plugin.EngineFunc(
//		func() plugin.Decl {
//			return plugin.Decl{Kind: plugin.KindStep, ABI: plugin.EngineABI, Type: "acme-engine"}
//		},
//		func(ctx context.Context, req plugin.RunRequest, host *plugin.Host) (plugin.RunResult, error) {
//			v, err := host.KV().Get(ctx, "cache", "run", "attempts")
//			…
//			return plugin.RunResult{Outputs: map[string]any{"attempts": v}}, nil
//		},
//	))
//
// The returned Handler answers plugin.invoke with a method-not-found error:
// an engine has no verbs, and a config that wired it under connectors: is a
// mistake worth saying out loud rather than a nil map.
func EngineFunc(describe func() Decl, run func(context.Context, RunRequest, *Host) (RunResult, error)) Handler {
	return engineHandler{describe: describe, run: run}
}

type engineHandler struct {
	describe func() Decl
	run      func(context.Context, RunRequest, *Host) (RunResult, error)
}

func (h engineHandler) Describe() Decl { return h.describe() }
func (h engineHandler) Invoke(InvokeRequest) (InvokeResult, error) {
	return InvokeResult{}, Errorf(CodeMethodNotFound, "this plugin is a step engine, not a connector — it has no verbs")
}
func (h engineHandler) Run(ctx context.Context, req RunRequest, host *Host) (RunResult, error) {
	return h.run(ctx, req, host)
}

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
	// Per-MESSAGE, not per-stream (see msgLimitReader): a step engine is
	// long-lived and a cumulative cap would kill it mid-life.
	lr := &msgLimitReader{r: in, limit: maxRequestBytes}
	dec := json.NewDecoder(lr)
	enc := json.NewEncoder(out)
	var writeMu sync.Mutex
	write := func(m wireMessage) error {
		m.JSONRPC = "2.0"
		writeMu.Lock()
		defer writeMu.Unlock()
		return enc.Encode(m) // Encode appends '\n' — newline-delimited framing
	}
	// calls tracks the requests THIS side issued (host.* callbacks) awaiting
	// the daemon's response. It is empty — and everything touching it is
	// dead code — for a plugin that never calls back, which is every
	// connector and runtime plugin.
	calls := newCallTable(write)
	defer calls.shutdown()
	// ctx bounds any background source stream: cancelled when the loop exits (the
	// daemon closed stdin), so a StartSource goroutine unwinds.
	ctx, cancel := context.WithCancel(context.Background())
	// In-flight request handlers. Serve must not return while one is still
	// writing: its response would be lost and the caller would see a dead
	// transport instead of an answer. Long-lived StartSource streams are
	// NOT tracked here — they unwind on ctx.
	var inflight sync.WaitGroup
	// ORDER MATTERS. Defers run LIFO, so registering Wait first and cancel
	// second makes cancel fire FIRST on the way out: handlers are told to
	// unwind, and only then do we wait for them. The other order waits on
	// handlers that were never told to stop — a handler blocked on this ctx
	// would hang shutdown forever. Nothing blocks on it today; this keeps
	// that from becoming a deadlock the first time one does.
	defer inflight.Wait()
	defer cancel()
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
		lr.reset() // a message boundary: the size window starts again
		// Three shapes arrive on this stream, and the combination of fields
		// discriminates them exactly as JSON-RPC intends:
		//
		//	method + id   a daemon REQUEST     → serve it (below)
		//	method only   a notification       → ignored (the daemon sends none)
		//	id only       a RESPONSE to a host.* call WE issued → deliver it
		//
		// The third case is the new direction. It was previously part of the
		// "ignore" arm, so nothing that used to be serviced changes meaning:
		// a message with no method and an id is something only a plugin that
		// called out can receive, and a plugin that never calls out has no
		// pending call for it to match.
		if m.Method == "" {
			if m.ID != nil {
				calls.deliver(m)
			}
			continue
		}
		if m.ID == nil {
			continue // notification
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
			result, rpcErr := dispatch(ctx, h, m.Method, m.Params, emit, calls)
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

// msgLimitReader bounds ONE message rather than the whole stream.
//
// This used to be an io.LimitReader over stdin, which is a CUMULATIVE cap:
// after 32 MiB of total traffic the decoder saw EOF and Serve returned nil —
// a clean shutdown, reported as one. A connector plugin never noticed (a
// describe and some verb calls do not add up to 32 MiB), but a STEP ENGINE is
// long-lived and sees every step's inputs, so the same cap would quietly
// retire it mid-life and the daemon would attribute it to a crash.
//
// The window resets at each message boundary, so the bound is what it was
// always meant to be: no single frame can turn into unbounded memory. The
// decoder may have buffered part of the NEXT message when we reset, which
// only makes the next window slightly generous — never cumulative.
//
// Not safe for concurrent use; only the single read loop touches it.
type msgLimitReader struct {
	r     io.Reader
	n     int64
	limit int64
}

func (l *msgLimitReader) Read(p []byte) (int, error) {
	if l.n >= l.limit {
		return 0, errMessageTooLarge
	}
	if room := l.limit - l.n; int64(len(p)) > room {
		p = p[:room]
	}
	n, err := l.r.Read(p)
	l.n += int64(n)
	return n, err
}

func (l *msgLimitReader) reset() { l.n = 0 }

var errMessageTooLarge = errors.New("plugin: incoming message exceeds the size bound")

// callTable is the plugin→daemon half of the conversation: requests this
// process issued, keyed by the id it minted, waiting for a response the read
// loop will hand back.
//
// Ids are strings with an "h" prefix ("h1", "h2", …) and the daemon echoes
// them verbatim. That is not decoration: the daemon numbers ITS requests with
// integers from 0, and two independent id spaces sharing one stream are much
// easier to read in a trace — and much harder to confuse in a half-correct
// implementation — when they do not look alike.
type callTable struct {
	write func(wireMessage) error

	mu      sync.Mutex
	next    int64
	pending map[string]chan wireMessage
	closed  bool
}

func newCallTable(write func(wireMessage) error) *callTable {
	return &callTable{write: write, pending: map[string]chan wireMessage{}}
}

// call sends one request and waits for its response, ctx, or the loop ending.
//
// It CANNOT deadlock against the daemon's own traffic: the handler that calls
// this is already on its own goroutine (Serve dispatches every request that
// way), so the read loop stays free to decode the response and hand it over —
// and to keep servicing whatever else the daemon sends meanwhile.
func (t *callTable) call(ctx context.Context, method string, params, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errServeClosed
	}
	t.next++
	id := "h" + strconv.FormatInt(t.next, 10)
	ch := make(chan wireMessage, 1)
	t.pending[id] = ch
	t.mu.Unlock()

	drop := func() {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
	}
	idRaw := json.RawMessage(strconv.Quote(id))
	if err := t.write(wireMessage{ID: &idRaw, Method: method, Params: raw}); err != nil {
		drop()
		return err
	}
	select {
	case <-ctx.Done():
		drop()
		return ctx.Err()
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if result != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, result)
		}
		return nil
	}
}

// deliver routes one response to the call waiting on it. An id nobody is
// waiting for is dropped: a late response to a cancelled call is not an error
// the plugin has anything useful to do about.
func (t *callTable) deliver(m wireMessage) {
	var id string
	if err := json.Unmarshal(*m.ID, &id); err != nil {
		return
	}
	t.mu.Lock()
	ch := t.pending[id]
	delete(t.pending, id)
	t.mu.Unlock()
	if ch != nil {
		ch <- m
	}
}

// shutdown unblocks every waiting call when the read loop ends, so a handler
// parked on a host.* response returns instead of hanging Serve's drain.
func (t *callTable) shutdown() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for id, ch := range t.pending {
		ch <- wireMessage{Error: Errorf(CodeInternalError, errServeClosed.Error())}
		delete(t.pending, id)
	}
}

var errServeClosed = errors.New("plugin: the daemon closed the connection")

func dispatch(ctx context.Context, h Handler, method string, params json.RawMessage, emit func(any) error, calls *callTable) (result any, rpcErr *Error) {
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
	case MethodRun:
		eh, ok := h.(EngineHandler)
		if !ok {
			return nil, Errorf(CodeMethodNotFound, "this plugin is not a step engine")
		}
		var req RunRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, Errorf(CodeInvalidParams, err.Error())
			}
		}
		// The run's data-plane handle lives exactly as long as the call. The
		// engine gets it as an argument rather than off the Handler, so there
		// is no place to keep one past the run that authorized it.
		res, err := eh.Run(ctx, req, &Host{calls: calls, runID: req.RunID})
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
