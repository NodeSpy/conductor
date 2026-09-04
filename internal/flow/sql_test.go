package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/kv"
	"github.com/NodeSpy/conductor/internal/sqlstore"
)

// TestSQLVerbValidation: uses: sql.* steps validate at LOAD — store: must be
// a literal defined store of a SQL type, and the family check catches both
// directions of the kv/sql mismatch naming the store and its type.
func TestSQLVerbValidation(t *testing.T) {
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	sqlstore.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); sqlstore.ResetStores(); kv.SetDataDir("") })
	base := `
connectors:
  svc: { type: fake }
stores:
  main: { type: boltdb }
  db:   { type: sqlite, path: ":memory:" }
`
	valid := func(y string) error {
		kv.ResetStores()
		sqlstore.ResetStores()
		cfg := loadConfig(t, base+y)
		reg := buildRegistry(t, cfg)
		return Validate(cfg, reg)
	}
	if err := valid(`
triggers:
  - on: svc.ping
    steps:
      - { uses: sql.exec,  options: { store: db, sql: "INSERT INTO t (a) VALUES (?)", args: [ "{{.msg}}" ] } }
      - { uses: sql.query, options: { store: db, sql: "SELECT a FROM t" } }
    hooks: [ { at: done, uses: sql.exec, options: { store: db, sql: "INSERT INTO audit (a) VALUES (?)", args: [ done ] } } ]
`); err != nil {
		t.Fatalf("sql verbs + hook must validate: %v", err)
	}

	cases := []struct{ name, yaml, wantErr string }{
		{"kv verb on a SQL store", `
triggers:
  - on: svc.ping
    steps: [ { uses: kv.get, options: { store: db, key: k } } ]`,
			`store "db" is type sqlite (a SQL store) — kv.* verbs need a KV store`},
		{"sql verb on a KV store", `
triggers:
  - on: svc.ping
    steps: [ { uses: sql.query, options: { store: main, sql: "SELECT 1" } } ]`,
			`store "main" is type boltdb (a KV store) — sql.* verbs need a SQL store`},
		{"missing store", `
triggers:
  - on: svc.ping
    steps: [ { uses: sql.query, options: { sql: "SELECT 1" } } ]`, "sql verbs require store:"},
		{"unknown store", `
triggers:
  - on: svc.ping
    steps: [ { uses: sql.query, options: { store: ghost, sql: "SELECT 1" } } ]`, `unknown store "ghost"`},
		{"templated store", `
triggers:
  - on: svc.ping
    steps: [ { uses: sql.query, options: { store: "{{.msg}}", sql: "SELECT 1" } } ]`, "literal store name"},
		{"missing sql option", `
triggers:
  - on: svc.ping
    steps: [ { uses: sql.query, options: { store: db } } ]`, `"sql"`},
		{"args must be a list", `
triggers:
  - on: svc.ping
    steps: [ { uses: sql.query, options: { store: db, sql: "SELECT 1", args: nope } } ]`, "want list"},
		{"hook family mismatch", `
triggers:
  - on: svc.ping
    steps: [ { uses: svc.post, options: { text: t } } ]
    hooks: [ { at: done, uses: sql.exec, options: { store: main, sql: "SELECT 1" } } ]`,
			`store "main" is type boltdb (a KV store)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := valid(tc.yaml); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestSQLVerbSteps: an end-to-end step chain over one sqlite :memory: store —
// sql.exec writes (rows_affected / last_insert_id flow into scope), sql.query
// reads the rows back, and the templated outputs land in a downstream verb.
func TestSQLVerbSteps(t *testing.T) {
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	sqlstore.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); sqlstore.ResetStores(); kv.SetDataDir("") })
	cfg := loadConfig(t, `
connectors:
  svc: { type: fake }
stores:
  db: { type: sqlite, path: ":memory:" }
`)
	reg := buildRegistry(t, cfg) // buildStores registers "db"
	fake := newFakeState(t, "svc")

	spec := mustSpec(t, `
on: svc.ping
steps:
  - id: ddl
    uses: sql.exec
    options: { store: db, sql: "CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, body TEXT)" }
  - id: ins
    uses: sql.exec
    options:
      store: db
      sql: "INSERT INTO events (body) VALUES (?)"
      args: [ "{{.msg}}" ]
  - id: read
    uses: sql.query
    options:
      store: db
      sql: "SELECT id, body FROM events WHERE body = ?"
      args: [ "{{.msg}}" ]
  - id: post
    uses: svc.post
    options: { text: "n={{.ins.rows_affected}} id={{.ins.last_insert_id}} count={{.read.count}} body={{ (index .read.rows 0).body }}" }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "inv-77"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].Opts["text"] != "n=1 id=1 count=1 body=inv-77" {
		t.Fatalf("calls: %+v", calls)
	}
}
