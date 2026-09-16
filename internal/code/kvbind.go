package code

import (
	"fmt"

	"github.com/NodeSpy/conductor/internal/kv"
)

// kvInvoke is the single ctx.store dispatcher behind every out-of-process
// engine's data plane — the `cli` engine's socket and an engine PLUGIN's
// host.kv callbacks both land here through CtxHandler (ctxhost.go), so there
// is exactly one place kv authorization happens. (Host-interpreter steps run
// in a separate process with no data plane and use the kv.* verbs instead.)
// Every call names a
// DEFINED store — there is no default. Ops take positional JSON-shaped
// args, ns first; results are JSON-shaped. Absent reads come back nil (the
// found flag folds into null), so dynamic code writes `if (!v) …`.
// kvValueWrites are the ops that persist caller-supplied values — the ones
// the DataGuard (plan write barrier) vets. Mirrors flow's internalWriteVerbs.
var kvValueWrites = map[string]bool{"set": true, "setnx": true, "merge": true, "append": true}

func kvInvoke(guard DataGuard, store, op string, args []any) (any, error) {
	if guard != nil {
		if err := guard("kv", op, store, args); err != nil {
			return nil, err
		}
	}
	st, err := kv.Use(store)
	if err != nil {
		return nil, err
	}
	if err := kv.CheckCapability(store, st, op); err != nil {
		return nil, err
	}
	argStr := func(i int) string {
		if i < len(args) {
			return fmt.Sprint(args[i])
		}
		return ""
	}
	argInt := func(i int, def int64) int64 {
		if i >= len(args) || args[i] == nil {
			return def
		}
		switch n := args[i].(type) {
		case int:
			return int64(n)
		case int64:
			return n
		case float64:
			return int64(n)
		}
		return def
	}
	arg := func(i int) any {
		if i < len(args) {
			return args[i]
		}
		return nil
	}
	need := func(n int) error {
		if len(args) < n {
			return fmt.Errorf("kv.%s: want %d args, got %d", op, n, len(args))
		}
		return nil
	}
	nsOf, keyOf := argStr(0), argStr(1)
	nullable := func(v any, found bool) any {
		if !found {
			return nil
		}
		return v
	}
	switch op {
	case "get":
		if err := need(2); err != nil {
			return nil, err
		}
		v, found, err := st.Get(nsOf, keyOf)
		return nullable(v, found), err
	case "set":
		if err := need(3); err != nil {
			return nil, err
		}
		return nil, st.Set(nsOf, keyOf, arg(2), 0)
	case "setnx":
		if err := need(3); err != nil {
			return nil, err
		}
		v, created, err := st.SetNX(nsOf, keyOf, arg(2), 0)
		if err != nil {
			return nil, err
		}
		return map[string]any{"value": v, "created": created}, nil
	case "merge":
		if err := need(3); err != nil {
			return nil, err
		}
		patch, ok := arg(2).(map[string]any)
		if !ok {
			return nil, fmt.Errorf("kv.merge: value must be an object, got %T", arg(2))
		}
		return st.Merge(nsOf, keyOf, patch)
	case "delete":
		if err := need(2); err != nil {
			return nil, err
		}
		return nil, st.Delete(nsOf, keyOf)
	case "incr":
		if err := need(2); err != nil {
			return nil, err
		}
		return st.Incr(nsOf, keyOf, argInt(2, 1))
	case "append", "remove":
		if err := need(3); err != nil {
			return nil, err
		}
		items, ok := arg(2).([]any)
		if !ok {
			items = []any{arg(2)}
		}
		if op == "remove" {
			return st.Remove(nsOf, keyOf, items)
		}
		unique, _ := arg(3).(bool)
		return st.Append(nsOf, keyOf, items, unique)
	case "contains":
		if err := need(3); err != nil {
			return nil, err
		}
		return st.Contains(nsOf, keyOf, arg(2))
	case "list":
		keys, entries, err := st.List(nsOf, argStr(1))
		if err != nil {
			return nil, err
		}
		ks := make([]any, len(keys))
		for i, k := range keys {
			ks[i] = k
		}
		return map[string]any{"keys": ks, "entries": entries}, nil
	case "first":
		if err := need(2); err != nil {
			return nil, err
		}
		v, found, err := st.First(nsOf, keyOf)
		return nullable(v, found), err
	case "last":
		if err := need(2); err != nil {
			return nil, err
		}
		v, found, err := st.Last(nsOf, keyOf)
		return nullable(v, found), err
	case "index":
		if err := need(3); err != nil {
			return nil, err
		}
		v, found, err := st.Index(nsOf, keyOf, int(argInt(2, 0)))
		return nullable(v, found), err
	case "slice":
		if err := need(2); err != nil {
			return nil, err
		}
		endSet := len(args) > 3 && args[3] != nil
		return st.Slice(nsOf, keyOf, int(argInt(2, 0)), int(argInt(3, 0)), endSet)
	case "len":
		if err := need(2); err != nil {
			return nil, err
		}
		return st.Len(nsOf, keyOf)
	case "pop":
		if err := need(2); err != nil {
			return nil, err
		}
		from := argStr(2)
		if from != "" && from != "front" && from != "back" {
			return nil, fmt.Errorf("kv.pop: from must be front or back, got %q", from)
		}
		v, found, _, err := st.Pop(nsOf, keyOf, from == "front")
		return nullable(v, found), err
	}
	return nil, fmt.Errorf("kv: no operation %q", op)
}

// kvOps is the ctx.kv method set the data plane exposes — the ops
// CtxHandler will dispatch, named in the reference client's usage errors.
var kvOps = []string{
	"get", "set", "setnx", "merge", "delete", "incr",
	"append", "remove", "contains", "list",
	"first", "last", "index", "slice", "len", "pop",
}
