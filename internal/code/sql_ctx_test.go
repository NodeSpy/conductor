package code

import (
	"context"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/sqlstore"
)

// tempSQL registers one sqlite :memory: store named "db" for a test,
// pre-created with an events table, and returns it.
func tempSQL(t *testing.T) *sqlstore.Store {
	t.Helper()
	sqlstore.ResetStores()
	t.Cleanup(sqlstore.ResetStores)
	st, err := sqlstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Exec(context.Background(),
		`CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT)`, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCodeAccess("write"); err != nil { // these tests exercise exec from code
		t.Fatal(err)
	}
	if err := sqlstore.Register("db", st); err != nil {
		t.Fatal(err)
	}
	return st
}

// The ctx.sql SURFACE, through the one dispatcher every engine reaches it
// by (CtxHandler → sqlInvoke). Replaces the four per-engine faces that used
// to assert this same contract through js/go-embed/risor/lua bindings.

// TestCtxSQLOpSurface: exec's counters come back, query returns row objects,
// a hostile bound value stays a literal, and a bad statement is an error.
func TestCtxSQLOpSurface(t *testing.T) {
	st := tempSQL(t)
	h := CtxHandler{}
	sqlCall := func(op string, args ...any) any {
		t.Helper()
		res := h.Invoke(CtxRequest{Kind: CtxKindSQL, Op: op, Resource: "db", Args: args})
		if !res.OK {
			t.Fatalf("sql.%s: %s", op, res.Error)
		}
		return res.Value
	}

	ins, _ := sqlCall("exec", "INSERT INTO events (body) VALUES (?)", []any{"hello"}).(map[string]any)
	if ins["rows_affected"] != int64(1) || ins["last_insert_id"] != int64(1) {
		t.Fatalf("exec counters = %#v", ins)
	}

	// Values BIND; they never reach the statement text. The classic injection
	// payload has to come back out as the literal string it went in as, and
	// the table has to survive it.
	const hostile = "'; DROP TABLE events; --"
	sqlCall("exec", "INSERT INTO events (body) VALUES (?)", []any{hostile})
	rows, _ := sqlCall("query", "SELECT id, body FROM events ORDER BY id").([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	if first, _ := rows[0].(map[string]any); first["body"] != "hello" {
		t.Errorf("first row = %#v", rows[0])
	}
	back, _ := sqlCall("query", "SELECT body FROM events WHERE body = ?", []any{hostile}).([]any)
	if len(back) != 1 {
		t.Fatalf("the hostile value did not round-trip as a literal: %#v", back)
	}
	got, err := st.Query(context.Background(), `SELECT COUNT(*) AS n FROM events`, nil)
	if err != nil || got[0]["n"] != int64(2) {
		t.Fatalf("store after the run: %v %v", got, err)
	}

	// A statement error and an undefined store are errors, not refusals.
	res := h.Invoke(CtxRequest{Kind: CtxKindSQL, Op: "query", Resource: "db",
		Args: []any{"SELECT * FROM nope"}})
	if res.OK || res.Refused || !strings.Contains(res.Error, "nope") {
		t.Fatalf("bad statement: %#v", res)
	}
	res = h.Invoke(CtxRequest{Kind: CtxKindSQL, Op: "query", Resource: "ghost",
		Args: []any{"SELECT 1"}})
	if res.OK || !strings.Contains(res.Error, `no SQL store named "ghost"`) {
		t.Fatalf("undefined store must name itself: %#v", res)
	}
}

// TestSQLInvokeShapes: the dispatcher's own arg contract.
func TestSQLInvokeShapes(t *testing.T) {
	tempSQL(t)
	for _, c := range []struct {
		op   string
		args []any
		want string
	}{
		{"query", nil, "got no sql"},
		{"query", []any{7}, "must be a non-empty string"},
		{"query", []any{"SELECT 1", "x"}, "args must be a list"},
		{"nosuch", []any{"SELECT 1"}, `no operation "nosuch"`},
	} {
		if _, err := sqlInvoke(nil, "db", c.op, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: want %q, got %v", c.op, c.args, c.want, err)
		}
	}
}
