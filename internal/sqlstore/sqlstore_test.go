package sqlstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openMem(t *testing.T) *Store {
	t.Helper()
	s, err := OpenSQLite(":memory:")
	if err != nil {
		t.Fatalf("OpenSQLite(:memory:): %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustExec(t *testing.T, s *Store, q string, args ...any) (int64, *int64) {
	t.Helper()
	n, id, err := s.Exec(context.Background(), q, args)
	if err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
	return n, id
}

func mustQuery(t *testing.T, s *Store, q string, args ...any) []map[string]any {
	t.Helper()
	rows, err := s.Query(context.Background(), q, args)
	if err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return rows
}

func TestExecAndQueryRoundTrip(t *testing.T) {
	s := openMem(t)
	if s.Driver() != "sqlite" {
		t.Fatalf("driver = %q, want sqlite", s.Driver())
	}
	mustExec(t, s, `CREATE TABLE notes (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT, score REAL, done BOOLEAN, extra TEXT)`)

	n, id := mustExec(t, s, `INSERT INTO notes (body, score, done, extra) VALUES (?, ?, ?, ?)`,
		"hello", 4.5, true, nil)
	if n != 1 {
		t.Fatalf("rows_affected = %d, want 1", n)
	}
	if id == nil || *id != 1 {
		t.Fatalf("last_insert_id = %v, want 1", id)
	}
	n, id = mustExec(t, s, `INSERT INTO notes (body, score, done, extra) VALUES (?, ?, ?, ?)`,
		"world", int64(2), false, "x")
	if n != 1 || id == nil || *id != 2 {
		t.Fatalf("second insert: rows=%d id=%v", n, id)
	}

	rows := mustQuery(t, s, `SELECT id, body, score, done, extra FROM notes ORDER BY id`)
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	r := rows[0]
	// Type fidelity through the sqlite driver: INTEGER → int64, REAL →
	// float64, TEXT → string, bool binds as sqlite INTEGER 0/1, NULL → nil.
	if got := r["id"]; got != int64(1) {
		t.Errorf("id = %#v, want int64(1)", got)
	}
	if got := r["body"]; got != "hello" {
		t.Errorf("body = %#v, want \"hello\"", got)
	}
	if got := r["score"]; got != 4.5 {
		t.Errorf("score = %#v, want 4.5", got)
	}
	if got := r["done"]; got != int64(1) {
		t.Errorf("done = %#v, want int64(1) (sqlite stores bool as INTEGER)", got)
	}
	if got, present := r["extra"]; !present || got != nil {
		t.Errorf("extra = %#v (present=%v), want nil (NULL)", got, present)
	}

	// Args bind positionally.
	rows = mustQuery(t, s, `SELECT body FROM notes WHERE score > ? AND done = ?`, 3.0, true)
	if len(rows) != 1 || rows[0]["body"] != "hello" {
		t.Fatalf("filtered rows = %v, want one row body=hello", rows)
	}

	// UPDATE reports rows_affected.
	n, _ = mustExec(t, s, `UPDATE notes SET done = ? WHERE done = ?`, true, false)
	if n != 1 {
		t.Fatalf("update rows_affected = %d, want 1", n)
	}
}

// TestInjectionSafety is the hard security property: an arg containing SQL
// metacharacters is bound as a literal value, never spliced into the
// statement. The table survives and the value round-trips verbatim.
func TestInjectionSafety(t *testing.T) {
	s := openMem(t)
	mustExec(t, s, `CREATE TABLE comments (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT)`)

	hostile := `'; DROP TABLE comments; -- "quoted" and 'quoted' and \backslash`
	n, _ := mustExec(t, s, `INSERT INTO comments (body) VALUES (?)`, hostile)
	if n != 1 {
		t.Fatalf("insert rows_affected = %d, want 1", n)
	}

	// The table survived the hostile value…
	rows := mustQuery(t, s, `SELECT body FROM comments`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	// …and the value stored is the literal string, byte for byte.
	if rows[0]["body"] != hostile {
		t.Fatalf("body = %q, want the hostile string verbatim", rows[0]["body"])
	}

	// The same value binds as a literal in a WHERE, too.
	rows = mustQuery(t, s, `SELECT COUNT(*) AS n FROM comments WHERE body = ?`, hostile)
	if len(rows) != 1 || rows[0]["n"] != int64(1) {
		t.Fatalf("lookup by hostile value = %v, want n=1", rows)
	}
}

func TestNullHandling(t *testing.T) {
	s := openMem(t)
	mustExec(t, s, `CREATE TABLE t (a TEXT, b INTEGER)`)
	mustExec(t, s, `INSERT INTO t (a, b) VALUES (?, ?)`, nil, nil)
	rows := mustQuery(t, s, `SELECT a, b FROM t`)
	if len(rows) != 1 {
		t.Fatalf("got %d rows", len(rows))
	}
	if rows[0]["a"] != nil || rows[0]["b"] != nil {
		t.Fatalf("NULLs = %#v, want nils", rows[0])
	}
	// NULL binds in a comparison arg position without error.
	rows = mustQuery(t, s, `SELECT COUNT(*) AS n FROM t WHERE a IS ?`, nil)
	if rows[0]["n"] != int64(1) {
		t.Fatalf("IS NULL count = %v, want 1", rows[0]["n"])
	}
}

func TestCompositeArgsBindAsJSON(t *testing.T) {
	s := openMem(t)
	mustExec(t, s, `CREATE TABLE t (doc TEXT)`)
	mustExec(t, s, `INSERT INTO t (doc) VALUES (?)`, map[string]any{"a": 1, "b": []any{"x"}})
	rows := mustQuery(t, s, `SELECT doc FROM t`)
	if got := rows[0]["doc"]; got != `{"a":1,"b":["x"]}` {
		t.Fatalf("doc = %#v, want JSON text", got)
	}
}

func TestQueryEmptyResult(t *testing.T) {
	s := openMem(t)
	mustExec(t, s, `CREATE TABLE t (a TEXT)`)
	rows := mustQuery(t, s, `SELECT a FROM t`)
	if rows == nil || len(rows) != 0 {
		t.Fatalf("rows = %#v, want empty non-nil slice", rows)
	}
}

func TestQueryErrors(t *testing.T) {
	s := openMem(t)
	if _, err := s.Query(context.Background(), `SELECT * FROM nope`, nil); err == nil {
		t.Fatal("query on a missing table should error")
	}
	if _, _, err := s.Exec(context.Background(), `INSERT INTO nope VALUES (1)`, nil); err == nil {
		t.Fatal("exec on a missing table should error")
	}
}

func TestSQLiteFileStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "db.sqlite")
	s, err := OpenSQLite(path)
	if err != nil {
		t.Fatalf("OpenSQLite(%s): %v", path, err)
	}
	defer s.Close()
	mustExec(t, s, `CREATE TABLE t (a TEXT)`)
	mustExec(t, s, `INSERT INTO t (a) VALUES (?)`, "persisted")
	rows := mustQuery(t, s, `SELECT a FROM t`)
	if len(rows) != 1 || rows[0]["a"] != "persisted" {
		t.Fatalf("rows = %v", rows)
	}
}

func TestOpenSQLiteErrors(t *testing.T) {
	if _, err := OpenSQLite(""); err == nil {
		t.Fatal("empty path should error")
	}
	if _, err := OpenSQLite("/dev/null/nope/db.sqlite"); err == nil {
		t.Fatal("unopenable path should error")
	}
}

// Driver-level wiring: the postgres and mysql openers validate their
// URL/DSN at load and construct a lazy handle without dialing — no live
// server needed. Live round-trips are covered by the build-tagged
// integration test (integration_test.go, -tags sqlintegration).
func TestOpenPostgresWiring(t *testing.T) {
	s, err := OpenPostgres("postgres://conductor@db.example:5432/analytics", "sekret")
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	if s.Driver() != "postgres" {
		t.Fatalf("driver = %q", s.Driver())
	}
	if _, err := OpenPostgres("://not a url", ""); err == nil ||
		!strings.Contains(err.Error(), "bad postgres url") {
		t.Fatalf("bad url error = %v", err)
	}
}

func TestOpenMySQLWiring(t *testing.T) {
	s, err := OpenMySQL("conductor:@tcp(db.example:3306)/billing", "sekret")
	if err != nil {
		t.Fatalf("OpenMySQL: %v", err)
	}
	defer s.Close()
	if s.Driver() != "mysql" {
		t.Fatalf("driver = %q", s.Driver())
	}
	if _, err := OpenMySQL("this is not a dsn", ""); err == nil ||
		!strings.Contains(err.Error(), "bad mysql dsn") {
		t.Fatalf("bad dsn error = %v", err)
	}
}

func TestPing(t *testing.T) {
	s := openMem(t)
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestNormalize(t *testing.T) {
	ts := time.Date(2026, 9, 4, 12, 30, 0, 0, time.UTC)
	if got := normalize(ts); got != "2026-09-04T12:30:00Z" {
		t.Fatalf("time = %#v", got)
	}
	if got := normalize([]byte("raw")); got != "raw" {
		t.Fatalf("bytes = %#v", got)
	}
	if got := normalize(int64(7)); got != int64(7) {
		t.Fatalf("passthrough = %#v", got)
	}
}

func TestBindArgsUnbindable(t *testing.T) {
	if _, err := bindArgs([]any{make(chan int)}); err == nil ||
		!strings.Contains(err.Error(), "not bindable") {
		t.Fatalf("chan arg error = %v", err)
	}
	// time.Time binds as itself (drivers accept it natively).
	out, err := bindArgs([]any{time.Unix(0, 0)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out[0].(time.Time); !ok {
		t.Fatalf("time arg = %#v", out[0])
	}
}

func TestRegistry(t *testing.T) {
	ResetStores()
	t.Cleanup(ResetStores)

	if _, err := Use(""); err == nil || !strings.Contains(err.Error(), "store: is required") {
		t.Fatalf("empty selector error = %v", err)
	}
	if _, err := Use("nope"); err == nil || !strings.Contains(err.Error(), `no SQL store named "nope"`) {
		t.Fatalf("unknown store error = %v", err)
	}

	s := openMem(t)
	if err := Register("", s); err == nil {
		t.Fatal("empty name should error")
	}
	if err := Register("analytics", s); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := Register("analytics", s); err == nil {
		t.Fatal("duplicate register should error")
	}
	got, err := Use("analytics")
	if err != nil || got != s {
		t.Fatalf("Use = %v, %v", got, err)
	}
	if names := Names(); len(names) != 1 || names[0] != "analytics" {
		t.Fatalf("Names = %v", names)
	}
	if _, err := Use("other"); err == nil || !strings.Contains(err.Error(), "analytics") {
		t.Fatalf("unknown-store error should list defined stores, got %v", err)
	}
}
