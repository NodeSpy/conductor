package code

import (
	"encoding/json"
	"fmt"
)

// The ctx DATA PLANE for out-of-process engines.
//
// The in-process engines (js/go-embed/risor/lua) reach ctx.store/ctx.sql/
// ctx.memory by holding a Go binding that calls kvInvoke/sqlInvoke/memInvoke
// directly (kvbind.go, sqlbind.go, membind.go). A subprocess cannot hold a
// binding, so it gets the same three dispatchers over a socket instead — and
// the thing on the other end of that socket is CtxHandler.
//
// The invariant this file exists to keep: ENFORCEMENT IS HOST-SIDE. The
// subprocess never receives a store handle, a connection string, or a
// capability — only the ability to ASK, one op at a time, with conductor
// deciding each time. Every request lands in CtxHandler.Invoke, which calls
// the exact same kvInvoke/sqlInvoke/memInvoke an in-process engine calls,
// carrying the exact same Spec.DataGuard. So a `use: cli` step's reach into
// kv/sql/memory is, op for op, the reach a `run: js` step has:
//
//	kvInvoke   → DataGuard → kv.Use → kv.CheckCapability        (kvbind.go)
//	sqlInvoke  → DataGuard → sqlstore.Use → CheckCodeAccess     (sqlbind.go)
//	memInvoke  → memory.CheckOp → DataGuard                     (membind.go)
//
// and the DataGuard itself is whatever the flow layer installed for this
// execution — flow.(*Runner).planDataGuard (internal/flow/plan.go), which is
// the no_secret_egress write barrier plus the agent-authored resource
// allowlist (resourcePolicy.storeOK / memoryScopeOK, internal/flow/
// resources.go).
//
// CtxHandler is deliberately transport-free: it takes a decoded request and
// returns a response. ctxsock.go puts a per-run authenticated unix socket in
// front of it for the `cli` engine; the out-of-process PLUGIN engine's
// host.kv/host.sql/host.memory methods will put the plugin JSON-RPC wire in
// front of the same handler without re-deciding anything.

// Ctx kinds — the three data-plane faces, named as they are on the wire.
const (
	CtxKindKV     = "kv"
	CtxKindSQL    = "sql"
	CtxKindMemory = "memory"
)

// CtxRequest is one data-plane call. Token authenticates it (see ctxsock.go;
// CtxHandler itself does not look at Token — authentication belongs to the
// transport, authorization to the handler).
//
// Kind/Op/Resource/Args mirror the DataGuard signature one-for-one, and Args
// follows the POSITIONAL convention of kvInvoke (kvbind.go): ns first, then
// key, then the value/count/flag the op takes. `kv get ns key` is
// Args:["ns","key"]; `kv set ns key v` is Args:["ns","key",v]; `kv slice ns
// key 1 3` is Args:["ns","key",1,3]. sqlInvoke takes (sql, args?) so `sql
// query` is Args:["SELECT …", [bind…]]. memInvoke's ops take their own
// positional args ((text, tags?, scope?) for remember, an options object for
// recall, an id for forget) and ignore Resource — memInvoke resolves the
// scope an op touches itself, via memScopeOf.
type CtxRequest struct {
	Token string `json:"token,omitempty"`
	Kind  string `json:"kind"`
	Op    string `json:"op"`
	// Resource is the DEFINED store the op names (kv/sql). Empty for
	// memory, which has no store dimension.
	Resource string `json:"resource,omitempty"`
	Args     []any  `json:"args,omitempty"`
}

// CtxResponse is the answer to one CtxRequest. Exactly one of Value (OK) or
// Error (not OK) is meaningful.
//
// Refused separates a POLICY denial from a failure: it is set when the
// execution's DataGuard rejected the call — the no_secret_egress write
// barrier or the agent-authored store/scope allowlist — so a client can tell
// "conductor will not let this step do that" from "the store is down" or
// "you passed two args to an op that wants three". Every other denial (an
// undefined store, a backend that can't serve the op, sql code_access, a
// reserved memory bucket) arrives as a plain Error: those are properties of
// the store's own configuration rather than of this execution's policy, and
// they read for themselves.
type CtxResponse struct {
	OK      bool   `json:"ok"`
	Value   any    `json:"value,omitempty"`
	Error   string `json:"error,omitempty"`
	Refused bool   `json:"refused,omitempty"`
}

// CtxHandler executes one ctx data-plane op on behalf of an out-of-process
// engine. Guard is the step's Spec.DataGuard — nil for a config-authored
// step, exactly as it is nil for an in-process engine's bindings, in which
// case only the store-level gates apply.
type CtxHandler struct {
	Guard DataGuard
}

// Invoke authorizes and runs one request. It never panics on a malformed
// request and never returns an error to its caller: a refusal IS a response,
// because the transport has to hand something back either way.
func (h CtxHandler) Invoke(req CtxRequest) CtxResponse {
	if req.Op == "" {
		return ctxErr(fmt.Errorf("ctx: no op — want {kind, op, resource?, args?}"))
	}
	// Wrapping the guard is how a refusal is TYPED without string-matching an
	// error message: the three invokers return the guard's error verbatim
	// (kvbind.go:kvInvoke, sqlbind.go:sqlInvoke, membind.go:memInvoke all do
	// `return nil, err` on a guard denial), so a flag set inside the wrapper
	// tells us the error we got back is that one. The guard is still called
	// from exactly one place — inside the invoker — so this adds a label, not
	// a second enforcement point.
	refused := false
	guard := h.Guard
	if guard != nil {
		inner := h.Guard
		guard = func(kind, op, resource string, args []any) error {
			err := inner(kind, op, resource, args)
			if err != nil {
				refused = true
			}
			return err
		}
	}

	var (
		v   any
		err error
	)
	switch req.Kind {
	case CtxKindKV:
		v, err = kvInvoke(guard, req.Resource, req.Op, req.Args)
	case CtxKindSQL:
		v, err = sqlInvoke(guard, req.Resource, req.Op, req.Args)
	case CtxKindMemory:
		v, err = memInvoke(guard, req.Op, req.Args)
	default:
		return ctxErr(fmt.Errorf("ctx: no kind %q — want %s, %s or %s",
			req.Kind, CtxKindKV, CtxKindSQL, CtxKindMemory))
	}
	if err != nil {
		res := ctxErr(err)
		res.Refused = refused
		return res
	}
	// The value has to survive the wire. A binding handed an in-process
	// engine any Go value it liked; a socket can only carry JSON, so an
	// unencodable result is reported here rather than corrupting the stream
	// with a half-written response.
	if _, merr := json.Marshal(v); merr != nil {
		return ctxErr(fmt.Errorf("ctx: %s.%s returned a value that will not encode: %w", req.Kind, req.Op, merr))
	}
	return CtxResponse{OK: true, Value: v}
}

// ctxErr is the not-OK response for err.
func ctxErr(err error) CtxResponse {
	return CtxResponse{OK: false, Error: err.Error()}
}
