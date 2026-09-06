package code

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/risor-io/risor/object"
	lua "github.com/yuin/gopher-lua"

	"github.com/NodeSpy/conductor/internal/memory"
)

// memInvoke is the single ctx.memory dispatcher behind every in-process
// engine's binding (js/go-embed/risor/lua — host-interpreter steps run in a
// separate process and use the memory.* verbs instead). It reads the
// process-wide configured memory; without a memory: section every op errors
// plainly. Code-path writes carry no run provenance, so relative scopes
// ("repo"/"agent") need their explicit forms here (repo:<owner/repo>,
// agent:<name>).
func memInvoke(guard DataGuard, op string, args []any) (any, error) {
	if guard != nil && op == "remember" {
		if err := guard("memory", op, "", args); err != nil {
			return nil, err
		}
	}
	m := memory.Active()
	if m == nil {
		return nil, fmt.Errorf("memory: not configured — add a top-level memory: section")
	}
	argStr := func(i int) string {
		if i < len(args) && args[i] != nil {
			return fmt.Sprint(args[i])
		}
		return ""
	}
	switch op {
	case "remember":
		if len(args) < 1 {
			return nil, fmt.Errorf("memory.remember: want (text, tags?, scope?)")
		}
		var tags []string
		if len(args) > 1 && args[1] != nil {
			switch x := args[1].(type) {
			case []any:
				for _, t := range x {
					tags = append(tags, fmt.Sprint(t))
				}
			case []string:
				tags = x
			case string:
				tags = []string{x}
			default:
				return nil, fmt.Errorf("memory.remember: tags must be a list, got %T", args[1])
			}
		}
		e, err := m.Remember(argStr(0), tags, argStr(2), memory.Source{})
		if err != nil {
			return nil, err
		}
		return e.Map(), nil
	case "recall":
		q := memory.Query{}
		if len(args) > 0 && args[0] != nil {
			opts, ok := args[0].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("memory.recall: want an options map {tags, scope, substring, limit}, got %T", args[0])
			}
			for _, t := range asAnyList(opts["tags"]) {
				q.Tags = append(q.Tags, fmt.Sprint(t))
			}
			if s, ok := opts["scope"].(string); ok && s != "" {
				resolved, err := memory.ResolveScope(s, memory.Source{})
				if err != nil {
					return nil, err
				}
				q.Scopes = []string{resolved}
			}
			if s, ok := opts["substring"].(string); ok {
				q.Substring = s
			}
			switch n := opts["limit"].(type) {
			case int:
				q.Limit = n
			case int64:
				q.Limit = int(n)
			case float64:
				q.Limit = int(n)
			}
		}
		entries, err := m.Recall(q)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(entries))
		for i, e := range entries {
			out[i] = e.Map()
		}
		return out, nil
	case "forget":
		if argStr(0) == "" {
			return nil, fmt.Errorf("memory.forget: want (id)")
		}
		return m.Forget(argStr(0))
	case "list":
		entries, err := m.List()
		if err != nil {
			return nil, err
		}
		out := make([]any, len(entries))
		for i, e := range entries {
			out[i] = e.Map()
		}
		return out, nil
	}
	return nil, fmt.Errorf("memory: no operation %q", op)
}

func asAnyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	case string:
		if x == "" {
			return nil
		}
		return []any{x}
	}
	return nil
}

// memOps is the ctx.memory method set every in-process engine exposes.
var memOps = []string{"remember", "recall", "forget", "list"}

// memInvokeJSON is the JSON bridge used by the js engine, mirroring
// kvInvokeJSON: {"op": …, "args": […]} → {"v": …} or {"err": …}.
func memInvokeJSON(guard DataGuard, payload string) string {
	var req struct {
		Op   string `json:"op"`
		Args []any  `json:"args"`
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
			return `{"err":"memory: unencodable result"}`
		}
		return string(b)
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return enc(nil, fmt.Errorf("memory: bad bridge payload: %w", err))
	}
	v, err := memInvoke(guard, req.Op, req.Args)
	return enc(v, err)
}

// jsMemShim builds ctx.memory over the __conductor_memory host bridge.
func jsMemShim() string {
	ops, _ := json.Marshal(memOps)
	return `ctx.memory = (() => {
  const call = (op, args) => {
    const r = JSON.parse(__conductor_memory(JSON.stringify({ op, args })));
    if (r.err) throw new Error(r.err);
    return r.v ?? null;
  };
  const o = {};
  for (const op of ` + string(ops) + `) o[op] = (...args) => call(op, args);
  return o;
})();
`
}

// MemHandle is the go-embed face of the configured memory: `import
// "conductor/memory"`, then `mem.Remember(…)` / `mem.Recall(…)`.
type MemHandle struct{ guard DataGuard }

// Remember stores one memory and returns it as a map.
func (h MemHandle) Remember(text string, tags []string, scope string) (map[string]any, error) {
	anyTags := make([]any, len(tags))
	for i, t := range tags {
		anyTags[i] = t
	}
	r, err := memInvoke(h.guard, "remember", []any{text, anyTags, scope})
	if err != nil {
		return nil, err
	}
	return r.(map[string]any), nil
}

// Recall filters memories ({tags, scope, substring, limit}), newest first.
func (h MemHandle) Recall(q map[string]any) ([]any, error) {
	r, err := memInvoke(h.guard, "recall", []any{q})
	if err != nil {
		return nil, err
	}
	return r.([]any), nil
}

// Forget removes one memory by id; reports whether it existed.
func (h MemHandle) Forget(id string) (bool, error) {
	r, err := memInvoke(h.guard, "forget", []any{id})
	if err != nil {
		return false, err
	}
	return r.(bool), nil
}

// List returns every memory, newest first.
func (h MemHandle) List() ([]any, error) {
	r, err := memInvoke(h.guard, "list", nil)
	if err != nil {
		return nil, err
	}
	return r.([]any), nil
}

// memGoEmbedExports is the `import "conductor/memory"` virtual package for
// run: go-embed.
func memGoEmbedExports(guard DataGuard) map[string]map[string]reflect.Value {
	h := MemHandle{guard: guard}
	return map[string]map[string]reflect.Value{
		"conductor/memory/memory": {
			"Remember": reflect.ValueOf(h.Remember),
			"Recall":   reflect.ValueOf(h.Recall),
			"Forget":   reflect.ValueOf(h.Forget),
			"List":     reflect.ValueOf(h.List),
		},
	}
}

// memRisorFn is the top-level `memory` module for run: risor:
// memory.remember("txt", ["tag"], "repo:o/r"), memory.recall({...}).
func memRisorFn(guard DataGuard) object.Object {
	contents := map[string]object.Object{}
	for _, op := range memOps {
		op := op
		contents[op] = object.NewBuiltin("memory."+op, func(_ context.Context, args ...object.Object) object.Object {
			goArgs := make([]any, len(args))
			for i, a := range args {
				goArgs[i] = a.Interface()
			}
			v, err := memInvoke(guard, op, goArgs)
			if err != nil {
				return object.NewError(err)
			}
			if v == nil {
				return object.Nil
			}
			return object.FromGoType(v)
		})
	}
	return object.NewBuiltinsModule("memory", contents)
}

// luaMemFn is ctx.memory for run: lua — a table of the ops; errors raise.
func luaMemFn(L *lua.LState, guard DataGuard) *lua.LTable {
	t := L.NewTable()
	for _, op := range memOps {
		op := op
		t.RawSetString(op, L.NewFunction(func(L *lua.LState) int {
			n := L.GetTop()
			args := make([]any, 0, n)
			for i := 1; i <= n; i++ {
				args = append(args, luaToGo(L.Get(i)))
			}
			v, err := memInvoke(guard, op, args)
			if err != nil {
				L.RaiseError("%s", err.Error())
				return 0
			}
			L.Push(goToLua(L, v))
			return 1
		}))
	}
	return t
}
