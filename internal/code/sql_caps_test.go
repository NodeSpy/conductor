package code

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/sqlstore"
)

// codeSQL runs one ctx.sql op the way a code step reaches it — through the
// data plane every engine shares (CtxHandler → sqlInvoke).
func codeSQL(op, store, query string, args []any) CtxResponse {
	return CtxHandler{}.Invoke(CtxRequest{Kind: CtxKindSQL, Op: op,
		Resource: store, Args: []any{query, args}})
}

// capsSQL registers one sqlite :memory: store named "db" with the given
// code_access mode ("" = the default).
func capsSQL(t *testing.T, mode string) *sqlstore.Store {
	t.Helper()
	sqlstore.ResetStores()
	t.Cleanup(sqlstore.ResetStores)
	st, err := sqlstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetCodeAccess(mode); err != nil {
		t.Fatal(err)
	}
	if err := sqlstore.Register("db", st); err != nil {
		t.Fatal(err)
	}
	return st
}

// Regression: ctx.sql used to allow arbitrary SQL from any code step —
// ATTACH from a "no filesystem" sandbox is a host file-write. The sqlite
// store layer now refuses ATTACH/DETACH/PRAGMA/VACUUM for every caller,
// even a store opted into code_access: write, and the target file is
// never created.
func TestCodeStepAttachRefused(t *testing.T) {
	capsSQL(t, "write")
	target := filepath.Join(t.TempDir(), "evil.db")
	res := codeSQL("exec", "db", "ATTACH DATABASE '"+target+"' AS evil", nil)
	if res.OK || !strings.Contains(res.Error, "ATTACH is not allowed") {
		t.Fatalf("ATTACH from a code step was not refused: %#v", res)
	}
	if _, statErr := os.Stat(target); statErr == nil {
		t.Fatalf("ATTACH created %s despite the refusal", target)
	}

	// PRAGMA and VACUUM INTO are refused the same way (store layer, so the
	// direct API is enough to prove the workflow-verb path too).
	st, _ := sqlstore.Use("db")
	if _, sqErr := st.Query(context.Background(), "PRAGMA journal_mode", nil); sqErr == nil ||
		!strings.Contains(sqErr.Error(), "PRAGMA is not allowed") {
		t.Fatalf("PRAGMA was not refused: %v", sqErr)
	}
	if _, _, exErr := st.Exec(context.Background(), "VACUUM INTO '"+target+"'", nil); exErr == nil ||
		!strings.Contains(exErr.Error(), "VACUUM is not allowed") {
		t.Fatalf("VACUUM INTO was not refused: %v", exErr)
	}
}

// Regression: exec (writes) from a code step now requires the store to opt
// in with code_access: write; the default is query-only, and none cuts code
// steps off entirely. The sql.* workflow verbs stay ungated.
func TestCodeStepExecRequiresWriteCapability(t *testing.T) {
	st := capsSQL(t, "") // default
	if _, _, err := st.Exec(context.Background(),
		`CREATE TABLE events (id INTEGER PRIMARY KEY, body TEXT)`, nil); err != nil {
		t.Fatal(err)
	}
	// exec from code: refused under the default.
	res := codeSQL("exec", "db", "INSERT INTO events (body) VALUES (?)", []any{"x"})
	if res.OK || !strings.Contains(res.Error, "query-only from code steps") {
		t.Fatalf("exec from code under default code_access was not refused: %#v", res)
	}

	// query from code: allowed under the default.
	res = codeSQL("query", "db", "SELECT COUNT(*) AS n FROM events", nil)
	rows, _ := res.Value.([]any)
	if !res.OK || len(rows) != 1 {
		t.Fatalf("query from code under default code_access: %#v", res)
	}
	if row, _ := rows[0].(map[string]any); row["n"] != int64(0) {
		t.Fatalf("query result = %#v", rows[0])
	}

	// code_access: none refuses even query.
	capsSQL(t, "none")
	res = codeSQL("query", "db", "SELECT 1", nil)
	if res.OK || !strings.Contains(res.Error, "code_access: none") {
		t.Fatalf("query under code_access none was not refused: %#v", res)
	}
}

// An unknown code_access mode is a config error at store build time.
func TestCodeAccessUnknownMode(t *testing.T) {
	st, err := sqlstore.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetCodeAccess("rw"); err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("bad code_access mode accepted: %v", err)
	}
}
