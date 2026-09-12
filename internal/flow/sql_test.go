package flow

import (
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
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
  svc: { use: fake }
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
// sql.exec writes (rows_affected / last_insert_id flow into scope), a run: js
// step writes through ctx.sql("db"), sql.query reads both rows back, and the
// templated outputs land in a downstream verb: verbs and code hit ONE
// defined store.
func TestSQLVerbSteps(t *testing.T) {
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	sqlstore.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); sqlstore.ResetStores(); kv.SetDataDir("") })
	cfg := loadConfig(t, `
connectors:
  svc: { use: fake }
stores:
  db: { type: sqlite, path: ":memory:", code_access: write } # the js step execs through ctx.sql
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
  - id: js
    run: js
    code: |
      const db = ctx.sql("db");
      db.exec("INSERT INTO events (body) VALUES (?)", [ctx.msg + "-js"]);
      return { total: db.query("SELECT COUNT(*) AS n FROM events")[0].n };
  - id: read
    uses: sql.query
    options:
      store: db
      sql: "SELECT id, body FROM events ORDER BY id"
  - id: post
    uses: svc.post
    options: { text: "n={{.ins.rows_affected}} id={{.ins.last_insert_id}} total={{.js.total}} count={{.read.count}} body={{ (index .read.rows 1).body }}" }
`)
	rig := newTestRunner(t, cfg, reg)
	runTrigger(rig, newTrigger("ping", map[string]any{"msg": "inv-77"}), spec)
	if failed, errStr := rig.workflowFailed(); failed {
		t.Fatalf("workflow failed: %s", errStr)
	}
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].Opts["text"] != "n=1 id=1 total=2 count=2 body=inv-77-js" {
		t.Fatalf("calls: %+v", calls)
	}
}

// REGRESSION (audit finding #10): a templated sql: statement is an injection
// foot-gun — event-controlled text would splice into the SQL before the
// driver sees placeholders. `conductor validate` (and the plan validator)
// reject it; args: templating stays fine.
func TestSQLTemplateInjectionRejected(t *testing.T) {
	kv.SetDataDir(t.TempDir())
	kv.ResetStores()
	sqlstore.ResetStores()
	t.Cleanup(func() { kv.ResetStores(); sqlstore.ResetStores(); kv.SetDataDir("") })
	base := `
connectors:
  svc: { use: fake }
stores:
  db: { type: sqlite, path: ":memory:" }
`
	valid := func(y string) error {
		kv.ResetStores()
		sqlstore.ResetStores()
		cfg := loadConfig(t, base+y)
		reg := buildRegistry(t, cfg)
		return Validate(cfg, reg)
	}
	err := valid(`
triggers:
  - on: svc.ping
    steps:
      - uses: sql.exec
        options: { store: db, sql: "DELETE FROM t WHERE name = '{{.title}}'" }
`)
	if err == nil || !strings.Contains(err.Error(), "must not contain templates") {
		t.Fatalf("templated sql must fail validate: %v", err)
	}
	// Hooks are covered by the same check.
	err = valid(`
triggers:
  - on: svc.ping
    steps: [ { uses: svc.post, options: { text: t } } ]
    hooks: [ { at: done, uses: sql.exec, options: { store: db, sql: "DROP {{.x}}" } } ]
`)
	if err == nil || !strings.Contains(err.Error(), "must not contain templates") {
		t.Fatalf("templated sql in a hook must fail validate: %v", err)
	}
	// Parameterized statements with templated ARGS are the supported shape.
	if err := valid(`
triggers:
  - on: svc.ping
    steps:
      - uses: sql.exec
        options: { store: db, sql: "DELETE FROM t WHERE name = $1", args: ["{{.title}}"] }
`); err != nil {
		t.Fatalf("parameterized sql with templated args must pass: %v", err)
	}
	// The plan validator applies the same rule to agent-emitted steps.
	kv.ResetStores()
	sqlstore.ResetStores()
	cfg := loadConfig(t, base+`
policy:
  agent_authored:
    allow: [ sql.* ]
`)
	reg := buildRegistry(t, cfg)
	perr := ValidatePlanSteps(cfg, reg, []config.Step{{
		Uses: "sql.exec", Options: map[string]any{"store": "db", "sql": "DELETE FROM t WHERE n = '{{.title}}'"},
	}})
	if perr == nil || !strings.Contains(perr.Error(), "must not contain templates") {
		t.Fatalf("plan sql template must reject: %v", perr)
	}
}
