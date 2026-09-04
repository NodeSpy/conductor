// Package sqlstore is the SQL half of the stores: framework: a registry of
// named SQL databases (postgres/mysql/sqlite — pure-Go drivers, no cgo)
// served by the `sql.query` / `sql.exec` verbs and the ctx.sql binding in
// in-process code steps. Conductor runs statements against the user's
// existing schema; it does not manage migrations.
//
// Statements are PARAMETERIZED ONLY: the sql text is fixed config, and
// event data binds through args to the driver's placeholders ($1 for
// postgres, ? for mysql/sqlite). Nothing here interpolates a value into the
// statement text, so a value containing quotes or `; DROP TABLE` is stored
// and returned as that literal string.
package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Store is one open SQL database — a *sql.DB plus the driver family, kept
// for introspection and error messages.
type Store struct {
	db     *sql.DB
	driver string // postgres | mysql | sqlite
}

// New wraps an open database handle. driver names the store type
// (postgres/mysql/sqlite) for errors and introspection.
func New(db *sql.DB, driver string) *Store {
	return &Store{db: db, driver: driver}
}

// Driver returns the store's driver family (postgres/mysql/sqlite).
func (s *Store) Driver() string { return s.driver }

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the connection (used by wiring tests and health checks).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Query runs a SELECT-shaped statement with args bound to the driver's
// placeholders and returns every row as a column→value map. Values are
// JSON-shaped: []byte columns decode to string, timestamps to RFC 3339.
func (s *Store) Query(ctx context.Context, query string, args []any) ([]map[string]any, error) {
	bound, err := bindArgs(args)
	if err != nil {
		return nil, fmt.Errorf("sql: query: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, query, bound...)
	if err != nil {
		return nil, fmt.Errorf("sql: query: %w", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, fmt.Errorf("sql: query: columns: %w", err)
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("sql: query: scan: %w", err)
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = normalize(vals[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sql: query: %w", err)
	}
	return out, nil
}

// Exec runs a mutating statement with args bound to the driver's
// placeholders. lastInsertID is nil when the driver doesn't report one
// (postgres — use RETURNING in a query instead).
func (s *Store) Exec(ctx context.Context, query string, args []any) (rowsAffected int64, lastInsertID *int64, err error) {
	bound, err := bindArgs(args)
	if err != nil {
		return 0, nil, fmt.Errorf("sql: exec: %w", err)
	}
	res, err := s.db.ExecContext(ctx, query, bound...)
	if err != nil {
		return 0, nil, fmt.Errorf("sql: exec: %w", err)
	}
	// Both counters are best-effort driver features: pgx supports neither
	// LastInsertId nor (for some statements) RowsAffected surprises — treat
	// an unsupported counter as absent, not a failed statement.
	rowsAffected, _ = res.RowsAffected()
	if id, idErr := res.LastInsertId(); idErr == nil {
		lastInsertID = &id
	}
	return rowsAffected, lastInsertID, nil
}

// bindArgs converts JSON-shaped option values into driver-bindable ones.
// Scalars pass through; a composite (object/list) binds as its JSON text —
// useful for JSON columns, and never interpreted as SQL.
func bindArgs(args []any) ([]any, error) {
	out := make([]any, len(args))
	for i, a := range args {
		switch a.(type) {
		case nil, string, bool, int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64, float32, float64,
			[]byte, time.Time:
			out[i] = a
		default:
			raw, err := json.Marshal(a)
			if err != nil {
				return nil, fmt.Errorf("args[%d]: not bindable (%T): %w", i, a, err)
			}
			out[i] = string(raw)
		}
	}
	return out, nil
}

// normalize maps a scanned driver value onto the JSON-shaped world the rest
// of conductor speaks: []byte → string, time.Time → RFC 3339; int64,
// float64, bool, string, and nil pass through.
func normalize(v any) any {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case time.Time:
		return x.Format(time.RFC3339Nano)
	}
	return v
}

// ---------------------------------------------------------------------------
// The SQL store registry — the sqlstore mirror of the kv registry. A
// `stores:` entry of a SQL type registers here; the kv types register in
// internal/kv. One name lives in exactly one registry (buildStores routes by
// type), which is what makes "kv.* on a SQL store" a resolvable error.
// ---------------------------------------------------------------------------

var (
	regMu sync.Mutex
	named = map[string]*Store{}
)

// Register adds a named SQL store (a `stores:` entry). Duplicates error.
func Register(name string, s *Store) error {
	regMu.Lock()
	defer regMu.Unlock()
	if name == "" {
		return fmt.Errorf("sql: stores: empty store name")
	}
	if _, dup := named[name]; dup {
		return fmt.Errorf("sql: store %q registered twice", name)
	}
	named[name] = s
	return nil
}

// ResetStores closes and clears every registered SQL store (config reload,
// tests).
func ResetStores() {
	regMu.Lock()
	defer regMu.Unlock()
	for _, s := range named {
		_ = s.Close()
	}
	named = map[string]*Store{}
}

// Names returns the registered SQL store names, sorted.
func Names() []string {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]string, 0, len(named))
	for n := range named {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Use resolves a store selector to its SQL store. Every sql operation names
// a store explicitly; there is no default.
func Use(name string) (*Store, error) {
	if name == "" {
		return nil, fmt.Errorf("sql: store: is required (defined SQL stores: %s)", nameList())
	}
	regMu.Lock()
	s, ok := named[name]
	regMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("sql: no SQL store named %q (defined SQL stores: %s)", name, nameList())
	}
	return s, nil
}

func nameList() string {
	names := Names()
	if len(names) == 0 {
		return "none — add a postgres/mysql/sqlite entry to stores:"
	}
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
