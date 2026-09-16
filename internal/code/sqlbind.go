package code

import (
	"context"
	"fmt"

	"github.com/NodeSpy/conductor/internal/sqlstore"
)

// sqlInvoke is the single ctx.sql dispatcher behind every out-of-process
// engine's data plane — the `cli` engine's socket and an engine PLUGIN's
// host.sql callbacks both land here through CtxHandler (ctxhost.go).
// (Host-interpreter steps run in a separate process with no data plane and
// use the sql.* verbs instead.) Every call names a
// DEFINED SQL store — there is no default. Ops take (sql, args?): query
// returns the row list ([{col: val, …}, …]), exec returns
// {rows_affected, last_insert_id?}. Statements are parameterized only —
// values bind through args to the driver's placeholders, never into the
// sql text.
func sqlInvoke(guard DataGuard, store, op string, args []any) (any, error) {
	if guard != nil {
		if err := guard("sql", op, store, args); err != nil {
			return nil, err
		}
	}
	st, err := sqlstore.Use(store)
	if err != nil {
		return nil, err
	}
	// The per-store capability gate (mirrors kv.CheckCapability): code steps
	// are query-only unless the store opts in with code_access: write, and
	// code_access: none cuts them off entirely. Without this, any code step
	// could run arbitrary exec against every defined store.
	if err := st.CheckCodeAccess(store, op); err != nil {
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

// sqlOps is the ctx.sql method set the data plane exposes — the ops
// CtxHandler will dispatch, named in the reference client's usage errors.
var sqlOps = []string{"query", "exec"}
