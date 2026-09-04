package code

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/risor-io/risor/object"
	lua "github.com/yuin/gopher-lua"

	"github.com/NodeSpy/conductor/internal/sqlstore"
)

// sqlInvoke is the single ctx.sql dispatcher behind every in-process
// engine's binding (js/go-embed/risor/lua — host-interpreter steps run in a
// separate process and use the sql.* verbs instead). Every call names a
// DEFINED SQL store — there is no default. Ops take (sql, args?): query
// returns the row list ([{col: val, …}, …]), exec returns
// {rows_affected, last_insert_id?}. Statements are parameterized only —
// values bind through args to the driver's placeholders, never into the
// sql text.
func sqlInvoke(store, op string, args []any) (any, error) {
	st, err := sqlstore.Use(store)
	if err != nil {
		return nil, err
	}
	if len(args) < 1 {
		return nil, fmt.Errorf("sql.%s: want (sql, args?), got no sql", op)
	}
	query, ok := args[0].(string)
	if !ok || query == "" {
		return nil, fmt.Errorf("sql.%s: sql must be a non-empty string, got %T", op, args[0])
	}
	var bind []any
	if len(args) > 1 && args[1] != nil {
		lst, isList := args[1].([]any)
		if !isList {
			return nil, fmt.Errorf("sql.%s: args must be a list, got %T", op, args[1])
		}
		bind = lst
	}
	ctx := context.Background()
	switch op {
	case "query":
		rows, err := st.Query(ctx, query, bind)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(rows))
		for i, r := range rows {
			out[i] = r
		}
		return out, nil
	case "exec":
		n, id, err := st.Exec(ctx, query, bind)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"rows_affected": n}
		if id != nil {
			out["last_insert_id"] = *id
		}
		return out, nil
	}
	return nil, fmt.Errorf("sql: no operation %q", op)
}

// sqlOps is the ctx.sql method set every in-process engine exposes.
var sqlOps = []string{"query", "exec"}

// sqlInvokeJSON is the JSON bridge used by the js engine: one host function
// taking {"store": …, "op": …, "args": […]} and returning {"v": …} or
// {"err": …}.
func sqlInvokeJSON(payload string) string {
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
			return `{"err":"sql: unencodable result"}`
		}
		return string(b)
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return enc(nil, fmt.Errorf("sql: bad bridge payload: %w", err))
	}
	v, err := sqlInvoke(req.Store, req.Op, req.Args)
	return enc(v, err)
}

// SQLHandle is the go-embed face of one defined SQL store: `import
// "conductor/sql"`, then `db, err := sql.Use("analytics")` and call Query
// or Exec.
type SQLHandle struct{ name string }

// Query runs a row-returning statement; each row is a column→value map.
func (h SQLHandle) Query(query string, args []any) ([]any, error) {
	r, err := sqlInvoke(h.name, "query", []any{query, args})
	if err != nil {
		return nil, err
	}
	return r.([]any), nil
}

// Exec runs a mutating statement, returning {rows_affected,
// last_insert_id?}.
func (h SQLHandle) Exec(query string, args []any) (map[string]any, error) {
	r, err := sqlInvoke(h.name, "exec", []any{query, args})
	if err != nil {
		return nil, err
	}
	return r.(map[string]any), nil
}

// sqlGoEmbedExports is the `import "conductor/sql"` virtual package for
// run: go-embed: sql.Use("analytics") resolves a defined SQL store to a
// SQLHandle.
func sqlGoEmbedExports() map[string]map[string]reflect.Value {
	return map[string]map[string]reflect.Value{
		"conductor/sql/sql": {
			"Use": reflect.ValueOf(func(name string) (SQLHandle, error) {
				if _, err := sqlstore.Use(name); err != nil {
					return SQLHandle{}, err
				}
				return SQLHandle{name: name}, nil
			}),
			"SQLHandle": reflect.ValueOf((*SQLHandle)(nil)),
		},
	}
}

// sqlRisorFn is the top-level `sql("name")` builtin for run: risor — it
// resolves a defined SQL store and returns a map of its ops:
// db := sql("analytics"); db.query("SELECT …", [args]).
func sqlRisorFn() object.Object {
	return object.NewBuiltin("sql", func(_ context.Context, args ...object.Object) object.Object {
		if len(args) != 1 {
			return object.Errorf("sql() takes the store name")
		}
		name, ok := args[0].Interface().(string)
		if !ok {
			return object.Errorf("sql() takes a string name")
		}
		if _, err := sqlstore.Use(name); err != nil {
			return object.NewError(err)
		}
		contents := map[string]object.Object{}
		for _, op := range sqlOps {
			op := op
			contents[op] = object.NewBuiltin("sql."+op, func(_ context.Context, args ...object.Object) object.Object {
				goArgs := make([]any, len(args))
				for i, a := range args {
					goArgs[i] = a.Interface()
				}
				v, err := sqlInvoke(name, op, goArgs)
				if err != nil {
					return object.NewError(err)
				}
				if v == nil {
					return object.Nil
				}
				return object.FromGoType(v)
			})
		}
		return object.NewBuiltinsModule("sql:"+name, contents)
	})
}

// luaSQLFn is ctx.sql for run: lua: ctx.sql("analytics") resolves a defined
// SQL store and returns a table of its ops; errors raise.
func luaSQLFn(L *lua.LState) *lua.LFunction {
	return L.NewFunction(func(L *lua.LState) int {
		name := L.CheckString(1)
		if _, err := sqlstore.Use(name); err != nil {
			L.RaiseError("%s", err.Error())
			return 0
		}
		t := L.NewTable()
		for _, op := range sqlOps {
			op := op
			t.RawSetString(op, L.NewFunction(func(L *lua.LState) int {
				n := L.GetTop()
				args := make([]any, 0, n)
				for i := 1; i <= n; i++ {
					args = append(args, luaToGo(L.Get(i)))
				}
				v, err := sqlInvoke(name, op, args)
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
