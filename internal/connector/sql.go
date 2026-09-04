package connector

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
	"github.com/NodeSpy/conductor/internal/sqlstore"
)

// sqlDecl declares the built-in SQL verbs over the stores: section's SQL
// entries (postgres/mysql/sqlite). Like kv, the connector is registered
// unconditionally — no connection block of its own (connections live on the
// stores), and the name is reserved. Statements are parameterized only:
// event data binds through args to the driver's placeholders ($1 for
// postgres, ? for mysql/sqlite), never into the sql text.
var sqlDecl = &TypeDecl{
	Type: "sql",
	Desc: "Built-in SQL verbs over the stores: section's postgres/mysql/sqlite entries; always available, no configuration.",
	Verbs: []VerbDecl{
		{
			Name: "query", Desc: "run a row-returning statement with bound args",
			Options: Schema{
				"store": {Type: TString, Required: true, Desc: "which SQL stores: entry to use"},
				"sql":   {Type: TString, Required: true, Desc: "the statement, with driver placeholders ($1 / ?)"},
				"args":  {Type: TList, Desc: "values bound to the placeholders, in order"},
			},
			Outputs: Schema{
				"rows":  {Type: TList, Desc: "one {column: value} object per row"},
				"count": {Type: TInt},
			},
		},
		{
			Name: "exec", Desc: "run a mutating statement with bound args",
			Options: Schema{
				"store": {Type: TString, Required: true, Desc: "which SQL stores: entry to use"},
				"sql":   {Type: TString, Required: true, Desc: "the statement, with driver placeholders ($1 / ?)"},
				"args":  {Type: TList, Desc: "values bound to the placeholders, in order"},
			},
			Outputs: Schema{
				"rows_affected":  {Type: TInt},
				"last_insert_id": {Type: TInt, Desc: "absent when the driver doesn't report one (postgres — use RETURNING with sql.query)"},
			},
		},
	},
}

func init() { RegisterType(sqlDecl, newSQLImpl) }

type sqlImpl struct {
	cfg *config.Config
}

func newSQLImpl(name string, ref config.ConnectorRef, deps Deps) (Impl, error) {
	return sqlImpl{cfg: deps.Config}, nil
}

func (sqlImpl) Validate() error          { return nil }
func (sqlImpl) DeclaredEvents() []string { return nil }
func (sqlImpl) Source(triggers []CompiledTrigger) (core.Integration, error) {
	if len(triggers) == 0 {
		return nil, nil
	}
	return nil, fmt.Errorf("the sql verbs have no source events")
}

func (s sqlImpl) Invoke(ctx context.Context, verb string, opts map[string]any) (map[string]any, error) {
	name, _ := opts["store"].(string)
	st, err := sqlstore.Use(name)
	if err != nil {
		// A defined store of the wrong family gets the precise mismatch
		// (the load-time check catches config paths; this covers dynamic
		// callers).
		if ferr := checkStoreFamily(s.cfg, name, "sql"); ferr != nil {
			return nil, ferr
		}
		return nil, err
	}
	query, _ := opts["sql"].(string)
	if query == "" {
		return nil, fmt.Errorf("sql.%s: sql: is required", verb)
	}
	var args []any
	if raw, ok := opts["args"]; ok && raw != nil {
		lst, isList := raw.([]any)
		if !isList {
			return nil, fmt.Errorf("sql.%s: args must be a list, got %T", verb, raw)
		}
		args = lst
	}
	switch verb {
	case "query":
		rows, err := st.Query(ctx, query, args)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(rows))
		for i, r := range rows {
			out[i] = r
		}
		return map[string]any{"rows": out, "count": len(rows)}, nil
	case "exec":
		n, id, err := st.Exec(ctx, query, args)
		if err != nil {
			return nil, err
		}
		out := map[string]any{"rows_affected": n}
		if id != nil {
			out["last_insert_id"] = *id
		}
		return out, nil
	}
	return nil, fmt.Errorf("sql: no verb %q", verb)
}

// checkStoreFamily returns the precise family-mismatch error when name is a
// defined store of the other family, nil otherwise (the caller falls back
// to its registry's own not-found error).
func checkStoreFamily(cfg *config.Config, name, want string) error {
	if cfg == nil || name == "" {
		return nil
	}
	ref, defined := cfg.Stores[name]
	if !defined {
		return nil
	}
	if got := StoreFamily(ref.Type); got != want && got != "" {
		return StoreFamilyError("", name, ref.Type, want)
	}
	return nil
}

// StoreFamilyError words the kv-verb-on-SQL-store / sql-verb-on-KV-store
// mismatch, naming the store, its type, and what the verb family needs.
// where prefixes the config location when known ("" at runtime).
func StoreFamilyError(where, name, typ, want string) error {
	prefix := ""
	if where != "" {
		prefix = where + ": "
	}
	if want == "sql" {
		return fmt.Errorf("%sstore %q is type %s (a KV store) — sql.* verbs need a SQL store (%s)",
			prefix, name, typ, joinTypes(StoreTypes("sql")))
	}
	return fmt.Errorf("%sstore %q is type %s (a SQL store) — kv.* verbs need a KV store (%s)",
		prefix, name, typ, joinTypes(StoreTypes("kv")))
}

// ---------------------------------------------------------------------------
// The SQL store builders — the sqlstore side of buildStores. A stores:
// entry whose type registers here lands in the sqlstore registry (served by
// sql.*); the kv types land in the kv registry (served by kv.*). One name
// lives in exactly one registry.
// ---------------------------------------------------------------------------

var sqlStoreBuilders = map[string]func(name string, ref config.StoreRef, deps Deps) (*sqlstore.Store, error){
	"postgres": buildPostgresStore,
	"mysql":    buildMySQLStore,
	"sqlite":   buildSQLiteStore,
}

// StoreFamily reports which verb family serves a stores: type: "kv"
// (boltdb/redis/http and build-tagged extensions), "sql"
// (postgres/mysql/sqlite), or "" for an unknown type.
func StoreFamily(typ string) string {
	if _, ok := storeBuilders[typ]; ok {
		return "kv"
	}
	if _, ok := sqlStoreBuilders[typ]; ok {
		return "sql"
	}
	return ""
}

// StoreTypes returns one family's registered store types, sorted ("kv",
// "sql", or "" for all).
func StoreTypes(family string) []string {
	var out []string
	if family == "kv" || family == "" {
		for t := range storeBuilders {
			out = append(out, t)
		}
	}
	if family == "sql" || family == "" {
		for t := range sqlStoreBuilders {
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

func buildPostgresStore(name string, ref config.StoreRef, deps Deps) (*sqlstore.Store, error) {
	var conn struct {
		URL      string `yaml:"url"`
		Password string `yaml:"password"`
	}
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("store %q: decode: %w", name, err)
	}
	if conn.URL == "" {
		return nil, fmt.Errorf("store %q (postgres): url: is required (postgres://user@host/db)", name)
	}
	pw, err := resolveStorePassword(name, conn.Password, deps)
	if err != nil {
		return nil, err
	}
	st, err := sqlstore.OpenPostgres(conn.URL, pw)
	if err != nil {
		return nil, fmt.Errorf("store %q: %w", name, err)
	}
	return st, nil
}

func buildMySQLStore(name string, ref config.StoreRef, deps Deps) (*sqlstore.Store, error) {
	var conn struct {
		DSN      string `yaml:"dsn"`
		Password string `yaml:"password"`
	}
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("store %q: decode: %w", name, err)
	}
	if conn.DSN == "" {
		return nil, fmt.Errorf("store %q (mysql): dsn: is required (user:pass@tcp(host:3306)/db)", name)
	}
	pw, err := resolveStorePassword(name, conn.Password, deps)
	if err != nil {
		return nil, err
	}
	st, err := sqlstore.OpenMySQL(conn.DSN, pw)
	if err != nil {
		return nil, fmt.Errorf("store %q: %w", name, err)
	}
	return st, nil
}

func buildSQLiteStore(name string, ref config.StoreRef, deps Deps) (*sqlstore.Store, error) {
	var conn struct {
		Path string `yaml:"path"`
	}
	if err := ref.Decode(&conn); err != nil {
		return nil, fmt.Errorf("store %q: decode: %w", name, err)
	}
	path := conn.Path
	if path == "" {
		// Like boltdb, a path-less sqlite store is a file conductor manages
		// in its data dir, named after the store.
		if deps.Config == nil || deps.Config.Store.StateFile == "" {
			return nil, fmt.Errorf("store %q (sqlite): data dir not configured — set path:", name)
		}
		path = filepath.Join(filepath.Dir(deps.Config.Store.StateFile), name+".sqlite")
	}
	st, err := sqlstore.OpenSQLite(path)
	if err != nil {
		return nil, fmt.Errorf("store %q: %w", name, err)
	}
	return st, nil
}

// resolveStorePassword resolves a store's password: through the usual
// secret schemes ("" stays "").
func resolveStorePassword(name, raw string, deps Deps) (string, error) {
	if raw == "" {
		return "", nil
	}
	pw, err := deps.Secrets.Resolve(context.Background(), raw)
	if err != nil {
		return "", fmt.Errorf("store %q: resolve password: %w", name, err)
	}
	return pw, nil
}

func joinTypes(types []string) string {
	out := ""
	for i, t := range types {
		if i > 0 {
			out += "/"
		}
		out += t
	}
	return out
}
