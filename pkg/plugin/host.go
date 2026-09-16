package plugin

import (
	"context"
	"errors"
	"fmt"
)

// The HOST CLIENT: an engine plugin's typed face onto conductor's ctx data
// plane, so an engine author writes
//
//	v, err := host.KV().Get(ctx, "cache", "run", "attempts")
//
// instead of hand-rolling a JSON-RPC request with a positional args array and
// remembering which op wants ns before key.
//
// Nothing here is privileged, and nothing here decides anything. Every method
// is one host.* round trip carrying the run's RunID; conductor authorizes each
// op against the step's DataGuard on its own side (internal/code's CtxHandler
// — the SAME handler, the same guard, the same store gates a `use: js` step
// goes through) and answers with a value or a refusal. The engine holds no
// store, no connection string, and no capability beyond the ability to ask
// while its run is in flight.
//
// The three faces mirror the in-process bindings exactly:
//
//	host.KV()      ctx.store   get/set/setnx/merge/delete/incr/append/remove/
//	                           contains/list/first/last/index/slice/len/pop
//	host.SQL()     ctx.sql     query/exec
//	host.Memory()  ctx.memory  remember/recall/forget/list
//
// A store NAME is an argument rather than part of the handle because that is
// how the wire carries it (HostRequest.Resource) and how every other face
// spells it (`ctx kv <store> get …`, `ctx.store("cache").get(…)`): an engine
// that addresses two stores makes two calls, not two clients.

// Host is one run's callback channel into conductor. It is handed to
// EngineHandler.Run and stops answering when that Run returns — a refused
// run_id, not a panic, is what a stashed Host gets.
type Host struct {
	calls *callTable
	runID string
}

// RunID is the run's capability token, for an engine that wants to speak the
// wire directly. Treat it as a secret: it is what authorizes this run's data
// plane, so it does not belong in a log line or an output.
func (h *Host) RunID() string { return h.runID }

// Available reports whether this run was granted a data plane at all. It is
// false when conductor sent no run_id — the engine should then behave like a
// remote `use: cli` step: do the inputs→outputs work and leave durable state
// to the surrounding steps, rather than failing the step on a call it was
// never going to be allowed to make.
func (h *Host) Available() bool { return h != nil && h.calls != nil && h.runID != "" }

// Refusal is the error a host.* call returns when CONDUCTOR SAID NO: the
// plan's write barrier, the agent-authored resource allowlist, or an
// unauthenticated run_id. It is distinct from every other error on purpose —
// a refusal is a decision about what this step may do, so retrying it, or
// reporting it as "the store is down", is wrong in both directions.
//
//	if v, err := host.KV().Get(ctx, "cache", "ns", "k"); plugin.IsRefused(err) {
//		// conductor will not let this step read that store
//	}
type Refusal struct {
	Kind string // kv | sql | memory
	Op   string
	Msg  string
}

func (e *Refusal) Error() string {
	return fmt.Sprintf("refused by conductor: %s.%s: %s", e.Kind, e.Op, e.Msg)
}

// IsRefused reports whether err is a policy refusal (a *Refusal anywhere in
// its chain) rather than a failure.
func IsRefused(err error) bool {
	var r *Refusal
	return errors.As(err, &r)
}

// Call is the untyped escape hatch: one data-plane op, exactly as the wire
// carries it. The typed clients below are all thin wrappers over it, and an
// engine reaching an op they do not cover (a store backend grows one) can use
// this without waiting for an SDK release.
func (h *Host) Call(ctx context.Context, kind, op, resource string, args ...any) (any, error) {
	if !h.Available() {
		return nil, fmt.Errorf("%s.%s: this run was granted no ctx data plane (no run_id)", kind, op)
	}
	method := HostMethodFor(kind)
	if method == "" {
		return nil, fmt.Errorf("no data-plane kind %q — want %s, %s or %s", kind, HostKindKV, HostKindSQL, HostKindMemory)
	}
	req := HostRequest{RunID: h.runID, Kind: kind, Op: op, Resource: resource, Args: args}
	var res HostResult
	if err := h.calls.call(ctx, method, req, &res); err != nil {
		return nil, fmt.Errorf("%s.%s: %w", kind, op, err)
	}
	if !res.OK {
		if res.Refused {
			return nil, &Refusal{Kind: kind, Op: op, Msg: res.Error}
		}
		return nil, fmt.Errorf("%s.%s: %s", kind, op, res.Error)
	}
	return res.Value, nil
}

// KV is ctx.store: the key/value face, addressed by store name.
func (h *Host) KV() HostKV { return HostKV{h} }

// SQL is ctx.sql: the relational face, addressed by store name.
func (h *Host) SQL() HostSQL { return HostSQL{h} }

// Memory is ctx.memory: the agent-memory face, which has no store dimension
// (conductor resolves the scope each op touches itself).
func (h *Host) Memory() HostMemory { return HostMemory{h} }

// HostKV is the ctx.store op set. Every method names the store first, which
// is the `resource` the daemon authorizes.
type HostKV struct{ h *Host }

func (k HostKV) call(ctx context.Context, op, store string, args ...any) (any, error) {
	return k.h.Call(ctx, HostKindKV, op, store, args...)
}

// Get returns the value at ns/key, or nil when it is absent.
func (k HostKV) Get(ctx context.Context, store, ns, key string) (any, error) {
	return k.call(ctx, "get", store, ns, key)
}

// Set writes value at ns/key.
func (k HostKV) Set(ctx context.Context, store, ns, key string, value any) error {
	_, err := k.call(ctx, "set", store, ns, key, value)
	return err
}

// SetNX writes value only if ns/key is absent, returning {value, created}.
func (k HostKV) SetNX(ctx context.Context, store, ns, key string, value any) (any, error) {
	return k.call(ctx, "setnx", store, ns, key, value)
}

// Merge patches the object at ns/key.
func (k HostKV) Merge(ctx context.Context, store, ns, key string, patch map[string]any) (any, error) {
	return k.call(ctx, "merge", store, ns, key, patch)
}

// Delete removes ns/key.
func (k HostKV) Delete(ctx context.Context, store, ns, key string) error {
	_, err := k.call(ctx, "delete", store, ns, key)
	return err
}

// Incr adds by to the counter at ns/key and returns the new value.
func (k HostKV) Incr(ctx context.Context, store, ns, key string, by int64) (any, error) {
	return k.call(ctx, "incr", store, ns, key, by)
}

// Append adds items to the list at ns/key; unique skips values already there.
func (k HostKV) Append(ctx context.Context, store, ns, key string, items []any, unique bool) (any, error) {
	return k.call(ctx, "append", store, ns, key, items, unique)
}

// Remove drops items from the list at ns/key.
func (k HostKV) Remove(ctx context.Context, store, ns, key string, items []any) (any, error) {
	return k.call(ctx, "remove", store, ns, key, items)
}

// Contains reports whether the list at ns/key holds value.
func (k HostKV) Contains(ctx context.Context, store, ns, key string, value any) (any, error) {
	return k.call(ctx, "contains", store, ns, key, value)
}

// List returns the namespace's keys (and entries), optionally prefix-filtered.
func (k HostKV) List(ctx context.Context, store, ns, prefix string) (any, error) {
	return k.call(ctx, "list", store, ns, prefix)
}

// First / Last return an end of the list at ns/key.
func (k HostKV) First(ctx context.Context, store, ns, key string) (any, error) {
	return k.call(ctx, "first", store, ns, key)
}

// Last returns the final element of the list at ns/key.
func (k HostKV) Last(ctx context.Context, store, ns, key string) (any, error) {
	return k.call(ctx, "last", store, ns, key)
}

// Index returns the element at position i of the list at ns/key.
func (k HostKV) Index(ctx context.Context, store, ns, key string, i int) (any, error) {
	return k.call(ctx, "index", store, ns, key, i)
}

// Slice returns [from,to) of the list at ns/key.
func (k HostKV) Slice(ctx context.Context, store, ns, key string, from, to int) (any, error) {
	return k.call(ctx, "slice", store, ns, key, from, to)
}

// Len returns the length of the list at ns/key.
func (k HostKV) Len(ctx context.Context, store, ns, key string) (any, error) {
	return k.call(ctx, "len", store, ns, key)
}

// Pop removes and returns the last element of the list at ns/key.
func (k HostKV) Pop(ctx context.Context, store, ns, key string) (any, error) {
	return k.call(ctx, "pop", store, ns, key)
}

// HostSQL is the ctx.sql op set. args are the bind values, carried as ONE
// positional argument (a list) exactly as the in-process binding does.
type HostSQL struct{ h *Host }

// Query runs a read and returns the rows.
func (s HostSQL) Query(ctx context.Context, store, sql string, args ...any) (any, error) {
	return s.h.Call(ctx, HostKindSQL, "query", store, sql, args)
}

// Exec runs a write and returns its result.
func (s HostSQL) Exec(ctx context.Context, store, sql string, args ...any) (any, error) {
	return s.h.Call(ctx, HostKindSQL, "exec", store, sql, args)
}

// HostMemory is the ctx.memory op set.
type HostMemory struct{ h *Host }

// Remember stores one memory entry. scope may be empty (conductor resolves
// the execution's own scope), and is authorized host-side either way.
func (m HostMemory) Remember(ctx context.Context, text string, tags []string, scope string) (any, error) {
	anyTags := make([]any, len(tags))
	for i, t := range tags {
		anyTags[i] = t
	}
	return m.h.Call(ctx, HostKindMemory, "remember", "", text, anyTags, scope)
}

// Recall queries memory. opts takes {tags, scope, substring, limit}.
func (m HostMemory) Recall(ctx context.Context, opts map[string]any) (any, error) {
	return m.h.Call(ctx, HostKindMemory, "recall", "", opts)
}

// Forget drops one entry by id.
func (m HostMemory) Forget(ctx context.Context, id string) (any, error) {
	return m.h.Call(ctx, HostKindMemory, "forget", "", id)
}

// List returns this execution's own scope's entries.
func (m HostMemory) List(ctx context.Context) (any, error) {
	return m.h.Call(ctx, HostKindMemory, "list", "")
}
