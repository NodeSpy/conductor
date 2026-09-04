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
	if err := sqlstore.Register("db", st); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestCtxSQLJS: ctx.sql from run: js — exec's counters come back, query
// returns row objects, a bound hostile arg stays a literal, and a statement
// error throws.
func TestCtxSQLJS(t *testing.T) {
	st := tempSQL(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "js", Code: `
const db = ctx.sql("db");
const ins = db.exec("INSERT INTO events (body) VALUES (?)", ["hello"]);
const hostile = "'; DROP TABLE events; --";
db.exec("INSERT INTO events (body) VALUES (?)", [hostile]);
const rows = db.query("SELECT id, body FROM events ORDER BY id");
const back = db.query("SELECT body FROM events WHERE body = ?", [hostile]);
return {
  affected: ins.rows_affected,
  id: ins.last_insert_id,
  count: rows.length,
  first: rows[0].body,
  hostile_back: back[0].body === hostile,
};`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["affected"] != float64(1) || out["id"] != float64(1) ||
		out["count"] != float64(2) || out["first"] != "hello" || out["hostile_back"] != true {
		t.Fatalf("js sql: %#v", out)
	}
	// The writes landed in the shared store — and the table survived the
	// hostile value.
	rows, err := st.Query(context.Background(), `SELECT COUNT(*) AS n FROM events`, nil)
	if err != nil || rows[0]["n"] != int64(2) {
		t.Fatalf("store after js: %v %v", rows, err)
	}
	// A statement error surfaces as a thrown JS error.
	_, err = e.Exec(context.Background(), Spec{Run: "js", Code: `ctx.sql("db").query("SELECT * FROM nope"); return 1`}, nil)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("js sql error: %v", err)
	}
	// An undefined store names the defined ones.
	_, err = e.Exec(context.Background(), Spec{Run: "js", Code: `ctx.sql("ghost").query("SELECT 1"); return 1`}, nil)
	if err == nil || !strings.Contains(err.Error(), `no SQL store named "ghost"`) {
		t.Fatalf("js unknown store: %v", err)
	}
}

// TestCtxSQLGoEmbed: the `import "conductor/sql"` virtual package in
// run: go-embed.
func TestCtxSQLGoEmbed(t *testing.T) {
	tempSQL(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "go-embed", Code: `
import "conductor/sql"

func run(ctx map[string]any) (any, error) {
	db, err := sql.Use("db")
	if err != nil {
		return nil, err
	}
	ins, err := db.Exec("INSERT INTO events (body) VALUES (?)", []any{"embed"})
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT body FROM events WHERE body = ?", []any{"embed"})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"affected": ins["rows_affected"],
		"count":    len(rows),
		"body":     rows[0].(map[string]any)["body"],
	}, nil
}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["affected"] != int64(1) || out["count"] != 1 || out["body"] != "embed" {
		t.Fatalf("go-embed sql: %#v", out)
	}
}

// TestCtxSQLRisor: the top-level sql() builtin in run: risor.
func TestCtxSQLRisor(t *testing.T) {
	tempSQL(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "risor", Code: `
db := sql("db")
ins := db.exec("INSERT INTO events (body) VALUES (?)", ["risor"])
rows := db.query("SELECT body FROM events")
{
  "affected": ins.rows_affected,
  "count": len(rows),
  "body": rows[0].body,
}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["affected"] != int64(1) || out["count"] != int64(1) || out["body"] != "risor" {
		t.Fatalf("risor sql: %#v", out)
	}
}

// TestCtxSQLLua: the ctx.sql table in run: lua, including an error raise.
func TestCtxSQLLua(t *testing.T) {
	tempSQL(t)
	e := &Executor{}
	out, err := e.Exec(context.Background(), Spec{Run: "lua", Code: `
local db = ctx.sql("db")
local ins = db.exec("INSERT INTO events (body) VALUES (?)", {"lua"})
local rows = db.query("SELECT body FROM events")
return {
  affected = ins.rows_affected,
  count = #rows,
  body = rows[1].body,
}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["affected"] != int64(1) || out["count"] != int64(1) || out["body"] != "lua" {
		t.Fatalf("lua sql: %#v", out)
	}
	_, err = e.Exec(context.Background(), Spec{Run: "lua", Code: `ctx.sql("db").query("SELECT * FROM nope")`}, nil)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("lua sql error: %v", err)
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
		if _, err := sqlInvoke("db", c.op, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %v: want %q, got %v", c.op, c.args, c.want, err)
		}
	}
}
