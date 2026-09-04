package code

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/risor-io/risor/object"
	lua "github.com/yuin/gopher-lua"

	"github.com/NodeSpy/conductor/internal/kv"
)

// kvInvoke is the single ctx.store dispatcher behind every in-process
// engine's binding (js/go-embed/risor/lua — host-interpreter steps run in a
// separate process and use the kv.* verbs instead). Every call names a
// DEFINED store — there is no default. Ops take positional JSON-shaped
// args, ns first; results are JSON-shaped. Absent reads come back nil (the
// found flag folds into null), so dynamic code writes `if (!v) …`.
func kvInvoke(store, op string, args []any) (any, error) {
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

// kvOps is the ctx.kv method set every in-process engine exposes.
var kvOps = []string{
	"get", "set", "setnx", "merge", "delete", "incr",
	"append", "remove", "contains", "list",
	"first", "last", "index", "slice", "len", "pop",
}

// kvInvokeJSON is the JSON bridge used by the js engine: one host function
// taking {"store": …, "op": …, "args": […]} and returning {"v": …} or
// {"err": …}.
func kvInvokeJSON(payload string) string {
	var req struct {
		Store string `json:"store"`
		Op    string `json:"op"`
		Args  []any  `json:"args"`
	}
	enc := func(v any, err error) string {
		var out struct {
			V   any    `json:"v"`
			Err string `json:"err,omitempty"`
		}
		out.V = v
		if err != nil {
			out.Err = err.Error()
		}
		b, merr := json.Marshal(out)
		if merr != nil {
			return `{"err":"kv: unencodable result"}`
		}
		return string(b)
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return enc(nil, fmt.Errorf("kv: bad bridge payload: %w", err))
	}
	v, err := kvInvoke(req.Store, req.Op, req.Args)
	return enc(v, err)
}

// KVHandle is the go-embed face of one defined store: `import
// "conductor/store"`, then `st, err := store.Use("cache")` and call the
// typed methods. Reads fold "absent" into nil; Slice's end is exclusive
// (use Len for "to the end").
type KVHandle struct{ name string }

func (h KVHandle) call(op string, args ...any) (any, error) { return kvInvoke(h.name, op, args) }

func (h KVHandle) Get(ns, key string) (any, error) { return h.call("get", ns, key) }
func (h KVHandle) Set(ns, key string, v any) error {
	_, err := h.call("set", ns, key, v)
	return err
}
func (h KVHandle) SetNX(ns, key string, v any) (any, bool, error) {
	r, err := h.call("setnx", ns, key, v)
	if err != nil {
		return nil, false, err
	}
	m := r.(map[string]any)
	return m["value"], m["created"].(bool), nil
}
func (h KVHandle) Merge(ns, key string, patch map[string]any) (map[string]any, error) {
	r, err := h.call("merge", ns, key, patch)
	if err != nil {
		return nil, err
	}
	return r.(map[string]any), nil
}
func (h KVHandle) Delete(ns, key string) error {
	_, err := h.call("delete", ns, key)
	return err
}
func (h KVHandle) Incr(ns, key string, by int64) (int64, error) {
	r, err := h.call("incr", ns, key, by)
	if err != nil {
		return 0, err
	}
	return r.(int64), nil
}
func (h KVHandle) Append(ns, key string, items any, unique bool) ([]any, error) {
	r, err := h.call("append", ns, key, items, unique)
	if err != nil {
		return nil, err
	}
	return r.([]any), nil
}
func (h KVHandle) Remove(ns, key string, items any) ([]any, error) {
	r, err := h.call("remove", ns, key, items)
	if err != nil {
		return nil, err
	}
	return r.([]any), nil
}
func (h KVHandle) Contains(ns, key string, item any) (bool, error) {
	r, err := h.call("contains", ns, key, item)
	if err != nil {
		return false, err
	}
	return r.(bool), nil
}
func (h KVHandle) List(ns, prefix string) (map[string]any, error) {
	r, err := h.call("list", ns, prefix)
	if err != nil {
		return nil, err
	}
	return r.(map[string]any), nil
}
func (h KVHandle) First(ns, key string) (any, error) { return h.call("first", ns, key) }
func (h KVHandle) Last(ns, key string) (any, error)  { return h.call("last", ns, key) }
func (h KVHandle) Index(ns, key string, i int) (any, error) {
	return h.call("index", ns, key, i)
}
func (h KVHandle) Slice(ns, key string, start, end int) ([]any, error) {
	r, err := h.call("slice", ns, key, start, end)
	if err != nil {
		return nil, err
	}
	return r.([]any), nil
}
func (h KVHandle) Len(ns, key string) (int, error) {
	r, err := h.call("len", ns, key)
	if err != nil {
		return 0, err
	}
	return r.(int), nil
}
func (h KVHandle) Pop(ns, key, from string) (any, error) { return h.call("pop", ns, key, from) }

// kvGoEmbedExports is the `import "conductor/store"` virtual package for
// run: go-embed: store.Use("cache") resolves a defined store to a KVHandle.
func kvGoEmbedExports() map[string]map[string]reflect.Value {
	return map[string]map[string]reflect.Value{
		"conductor/store/store": {
			"Use": reflect.ValueOf(func(name string) (KVHandle, error) {
				if _, err := kv.Use(name); err != nil {
					return KVHandle{}, err
				}
				return KVHandle{name: name}, nil
			}),
			"KVHandle": reflect.ValueOf((*KVHandle)(nil)),
		},
	}
}

// kvRisorStoreFn is the top-level `store("name")` builtin for run: risor —
// it resolves a defined store and returns a map of its ops:
// s := store("cache"); s.get("ns", "k").
func kvRisorStoreFn() object.Object {
	return object.NewBuiltin("store", func(_ context.Context, args ...object.Object) object.Object {
		if len(args) != 1 {
			return object.Errorf("store() takes the store name")
		}
		name, ok := args[0].Interface().(string)
		if !ok {
			return object.Errorf("store() takes a string name")
		}
		if _, err := kv.Use(name); err != nil {
			return object.NewError(err)
		}
		contents := map[string]object.Object{}
		for _, op := range kvOps {
			op := op
			contents[op] = object.NewBuiltin("store."+op, func(_ context.Context, args ...object.Object) object.Object {
				goArgs := make([]any, len(args))
				for i, a := range args {
					goArgs[i] = a.Interface()
				}
				v, err := kvInvoke(name, op, goArgs)
				if err != nil {
					return object.NewError(err)
				}
				if v == nil {
					return object.Nil
				}
				return object.FromGoType(v)
			})
		}
		return object.NewBuiltinsModule("store:"+name, contents)
	})
}

// luaStoreFn is ctx.store for run: lua: ctx.store("cache") resolves a
// defined store and returns a table of its ops; errors raise.
func luaStoreFn(L *lua.LState) *lua.LFunction {
	return L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		if _, err := kv.Use(name); err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		t := L.NewTable()
		for _, op := range kvOps {
			op := op
			t.RawSetString(op, L.NewFunction(func(L *lua.LState) int {
				n := L.GetTop()
				args := make([]any, 0, n)
				for i := 1; i <= n; i++ {
					args = append(args, luaToGo(L.Get(i)))
				}
				v, err := kvInvoke(name, op, args)
				if err != nil {
					L.RaiseError("%s", err.Error())
					return 0
				}
				L.Push(goToLua(L, v))
				return 1
			}))
		}
		L.Push(t)
		return 1
	})
}
