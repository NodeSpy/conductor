package connector

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/kv"
	"github.com/NodeSpy/conductor/internal/secrets"
	"github.com/NodeSpy/conductor/internal/sqlstore"
)

// sqlInstance builds a registry whose config defines one sqlite :memory:
// store (db) and one boltdb store (state), and returns the always-on sql
// instance.
func sqlInstance(t *testing.T) *Instance {
	t.Helper()
	kv.SetDataDir(t.TempDir())
	t.Cleanup(func() { kv.SetDataDir(""); kv.ResetStores(); sqlstore.ResetStores() })
	reg := buildAPIRegistry(t, `
connectors: {}
stores:
  db:    { type: sqlite, path: ":memory:" }
  state: { type: boltdb }
`, secrets.New())
	in, ok := reg.Get("sql")
	if !ok || in.DisabledReason != "" || !in.Enabled {
		t.Fatalf("sql instance: ok=%v %+v", ok, in)
	}
	return in
}

// TestSQLAlwaysRegistered: the instance exists with no config, publishes the
// two verbs, and has no source events.
func TestSQLAlwaysRegistered(t *testing.T) {
	in := sqlInstance(t)
	if got := in.Decl.VerbNames(); !reflect.DeepEqual(got, []string{"exec", "query"}) {
		t.Fatalf("verbs: %v", got)
	}
	if evs := in.Decl.EventNames(); len(evs) != 0 {
		t.Fatalf("sql has no events: %v", evs)
	}
	if _, err := in.Impl.Source([]CompiledTrigger{{}}); err == nil {
		t.Fatal("sql must reject triggers")
	}
	// Both verbs require a store: selector naming a defined SQL store.
	if _, err := in.Invoke(context.Background(), "query", map[string]any{"sql": "SELECT 1"}); err == nil || !strings.Contains(err.Error(), "store") {
		t.Fatalf("missing store: %v", err)
	}
	if _, err := in.Invoke(context.Background(), "query", map[string]any{"store": "nope", "sql": "SELECT 1"}); err == nil || !strings.Contains(err.Error(), `no SQL store named "nope"`) {
		t.Fatalf("unknown store: %v", err)
	}
}

// TestSQLVerbSurface: exec/query through Invoke against a sqlite :memory:
// store — rows_affected, last_insert_id, rows + count, args binding, and
// the error shapes.
func TestSQLVerbSurface(t *testing.T) {
	in := sqlInstance(t)
	ctx := context.Background()
	call := func(verb string, opts map[string]any) map[string]any {
		t.Helper()
		out, err := in.Invoke(ctx, verb, opts)
		if err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		return out
	}

	call("exec", map[string]any{"store": "db",
		"sql": `CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT, n INTEGER)`})

	out := call("exec", map[string]any{"store": "db",
		"sql":  `INSERT INTO events (kind, n) VALUES (?, ?)`,
		"args": []any{"incident", 3}})
	if out["rows_affected"] != int64(1) || out["last_insert_id"] != int64(1) {
		t.Fatalf("insert outputs: %v", out)
	}
	call("exec", map[string]any{"store": "db",
		"sql":  `INSERT INTO events (kind, n) VALUES (?, ?)`,
		"args": []any{"deploy", 7}})

	out = call("query", map[string]any{"store": "db",
		"sql":  `SELECT kind, n FROM events WHERE n > ? ORDER BY n`,
		"args": []any{2}})
	if out["count"] != 2 {
		t.Fatalf("count: %v", out)
	}
	rows := out["rows"].([]any)
	first := rows[0].(map[string]any)
	if first["kind"] != "incident" || first["n"] != int64(3) {
		t.Fatalf("row 0: %v", first)
	}

	// The injection property holds through the verb layer: a hostile arg is
	// a literal value; the table survives.
	hostile := `'; DROP TABLE events; --`
	out = call("exec", map[string]any{"store": "db",
		"sql":  `INSERT INTO events (kind, n) VALUES (?, ?)`,
		"args": []any{hostile, 0}})
	if out["rows_affected"] != int64(1) {
		t.Fatalf("hostile insert: %v", out)
	}
	out = call("query", map[string]any{"store": "db",
		"sql":  `SELECT kind FROM events WHERE kind = ?`,
		"args": []any{hostile}})
	if out["count"] != 1 || out["rows"].([]any)[0].(map[string]any)["kind"] != hostile {
		t.Fatalf("hostile round-trip: %v", out)
	}

	// Error shapes.
	for _, bad := range []struct {
		verb string
		opts map[string]any
		want string
	}{
		{"query", map[string]any{"store": "db"}, "sql: is required"},
		{"exec", map[string]any{"store": "db"}, "sql: is required"},
		{"query", map[string]any{"store": "db", "sql": "SELECT 1", "args": "x"}, "args must be a list"},
		{"query", map[string]any{"store": "db", "sql": "SELECT * FROM nope"}, "nope"},
		{"nosuch", map[string]any{"store": "db", "sql": "SELECT 1"}, `no verb "nosuch"`},
	} {
		if _, err := in.Invoke(ctx, bad.verb, bad.opts); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: want %q, got %v", bad.verb, bad.want, err)
		}
	}
}

// TestStoreFamilyMismatchRuntime: a kv verb aimed at a SQL store and a sql
// verb aimed at a KV store both fail naming the store, its type, and the
// family the verb needs.
func TestStoreFamilyMismatchRuntime(t *testing.T) {
	kv.SetDataDir(t.TempDir())
	t.Cleanup(func() { kv.SetDataDir(""); kv.ResetStores(); sqlstore.ResetStores() })
	reg := buildAPIRegistry(t, `
connectors: {}
stores:
  db:    { type: sqlite, path: ":memory:" }
  state: { type: boltdb }
`, secrets.New())
	kvIn, _ := reg.Get("kv")
	sqlIn, _ := reg.Get("sql")
	ctx := context.Background()

	_, err := kvIn.Invoke(ctx, "get", map[string]any{"store": "db", "key": "k"})
	if err == nil || !strings.Contains(err.Error(), `store "db" is type sqlite (a SQL store)`) ||
		!strings.Contains(err.Error(), "kv.* verbs need a KV store") {
		t.Fatalf("kv on sql store: %v", err)
	}
	_, err = sqlIn.Invoke(ctx, "query", map[string]any{"store": "state", "sql": "SELECT 1"})
	if err == nil || !strings.Contains(err.Error(), `store "state" is type boltdb (a KV store)`) ||
		!strings.Contains(err.Error(), "sql.* verbs need a SQL store") {
		t.Fatalf("sql on kv store: %v", err)
	}
}

// TestSQLConfiguredNameRejected: a user-defined connector named sql is a
// load error (the name is reserved for the built-in).
func TestSQLConfiguredNameRejected(t *testing.T) {
	var cfg config.Config
	if err := yaml.Unmarshal([]byte("connectors:\n  sql: { type: command }\n"), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved name: %v", err)
	}
}

// TestSQLStoresBuildValidation: a bad SQL stores: entry is a LOAD error
// from Build naming the store; a valid postgres/mysql entry builds without
// dialing the server (driver-level wiring).
func TestSQLStoresBuildValidation(t *testing.T) {
	kv.SetDataDir(t.TempDir())
	t.Cleanup(func() { kv.SetDataDir(""); kv.ResetStores(); sqlstore.ResetStores() })
	build := func(y string) error {
		var cfg config.Config
		if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
			t.Fatal(err)
		}
		_, err := Build(&cfg, Deps{Secrets: secrets.New(), Config: &cfg})
		return err
	}

	// Valid postgres and mysql entries register without a live server: the
	// URL/DSN is validated at load, the connection dialed lazily.
	if err := build(`
connectors: {}
stores:
  analytics: { type: postgres, url: "postgres://conductor@db.example/analytics", password: pg-pw }
  billing:   { type: mysql,    dsn: "conductor:@tcp(db.example:3306)/billing" }
`); err != nil {
		t.Fatalf("postgres+mysql wiring: %v", err)
	}
	if got := sqlstore.Names(); !reflect.DeepEqual(got, []string{"analytics", "billing"}) {
		t.Fatalf("registered: %v", got)
	}
	sqlstore.ResetStores()
	kv.ResetStores()

	for _, c := range []struct{ name, yaml, want string }{
		{"postgres no url", "connectors: {}\nstores:\n  x: { type: postgres }\n", "url: is required"},
		{"postgres bad url", "connectors: {}\nstores:\n  x: { type: postgres, url: \"://nope\" }\n", "bad postgres url"},
		{"mysql no dsn", "connectors: {}\nstores:\n  x: { type: mysql }\n", "dsn: is required"},
		{"mysql bad dsn", "connectors: {}\nstores:\n  x: { type: mysql, dsn: \"not a dsn\" }\n", "bad mysql dsn"},
		{"sqlite bad path", "connectors: {}\nstores:\n  x: { type: sqlite, path: /dev/null/nope/x.sqlite }\n", `"x"`},
	} {
		err := build(c.yaml)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
		sqlstore.ResetStores()
		kv.ResetStores()
	}

	// A path-less sqlite store lands in the data dir, named after the store.
	if err := build("connectors: {}\nstores:\n  scratch: { type: sqlite }\nstore: { state_file: " + t.TempDir() + "/runs.json }\n"); err != nil {
		t.Fatalf("path-less sqlite: %v", err)
	}
}

// TestStoreFamilyHelpers: the family lookup the load-time check runs on.
func TestStoreFamilyHelpers(t *testing.T) {
	for typ, want := range map[string]string{
		"boltdb": "kv", "redis": "kv", "http": "kv",
		"postgres": "sql", "mysql": "sql", "sqlite": "sql",
		"dynamo": "",
	} {
		if got := StoreFamily(typ); got != want {
			t.Errorf("StoreFamily(%s) = %q, want %q", typ, got, want)
		}
	}
	if got := StoreTypes("sql"); !reflect.DeepEqual(got, []string{"mysql", "postgres", "sqlite"}) {
		t.Fatalf("StoreTypes(sql) = %v", got)
	}
	if got := StoreTypes(""); len(got) != 6 {
		t.Fatalf("StoreTypes(all) = %v", got)
	}
}
