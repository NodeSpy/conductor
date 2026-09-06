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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is one open SQL database — a *sql.DB plus the driver family, kept
// for introspection and error messages.
type Store struct {
	db     *sql.DB
	driver string // postgres | mysql | sqlite
	// codeAccess gates what in-process code steps (ctx.sql) may do against
	// this store: "none" (no code access), "read" (query only — the
	// default), "write" (query + exec). The sql.query/sql.exec workflow
	// verbs are config-authored and not gated by this.
	codeAccess string
}

// New wraps an open database handle. driver names the store type
// (postgres/mysql/sqlite) for errors and introspection.
func New(db *sql.DB, driver string) *Store {
	return &Store{db: db, driver: driver}
}

// Driver returns the store's driver family (postgres/mysql/sqlite).
func (s *Store) Driver() string { return s.driver }

// SetCodeAccess sets the store's code-step capability ("" keeps the "read"
// default). An unknown mode is a config error.
func (s *Store) SetCodeAccess(mode string) error {
	switch mode {
	case "", "none", "read", "write":
		s.codeAccess = mode
		return nil
	}
	return fmt.Errorf("sql: code_access: unknown mode %q (none | read | write)", mode)
}

// CheckCodeAccess reports whether an in-process code step may run op
// ("query"/"exec") against this store. Code steps are query-only unless the
// store opts in with code_access: write — arbitrary exec from a code step is
// the capability the sandbox otherwise doesn't grant.
func (s *Store) CheckCodeAccess(storeName, op string) error {
	mode := s.codeAccess
	if mode == "" {
		mode = "read"
	}
	switch mode {
	case "none":
		return fmt.Errorf("sql: store %q does not allow code-step access (code_access: none)", storeName)
	case "read":
		if op != "query" {
			return fmt.Errorf("sql: store %q is query-only from code steps — set code_access: write on the store to allow exec", storeName)
		}
	}
	return nil
}

// sqliteDenied matches statements that reach outside the database file:
// ATTACH opens/creates an arbitrary host file through the SQL text (a host
// file-write from a "no filesystem" code sandbox), DETACH is its pair,
// PRAGMA can rewrite journal/database behavior, and VACUUM INTO writes a
// file. Denied at the store layer for every caller — parameterized
// statements against the user's schema never need them.
var sqliteDenied = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_"'])(attach|detach|pragma|vacuum)($|[^A-Za-z0-9_])`)

// mysqlDenied: INTO OUTFILE / INTO DUMPFILE write server-side files;
// LOAD_FILE() and LOAD DATA read them.
var mysqlDenied = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_"'])(into\s+(outfile|dumpfile)|load_file|load\s+data)($|[^A-Za-z0-9_])`)

// postgresDenied: COPY … TO/FROM PROGRAM executes a server-side command;
// COPY … TO/FROM '<path>' reads/writes server-side files. Plain COPY to
// STDIN/STDOUT is a protocol feature the drivers don't expose here anyway,
// so the whole server-side COPY family is refused.
var postgresDenied = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_"'])(copy)\s`)

// checkStatement refuses statements that reach the host filesystem or shell
// through the database server — on every driver, for every caller.
func (s *Store) checkStatement(query string) error {
	var m []string
	switch s.driver {
	case "sqlite":
		m = sqliteDenied.FindStringSubmatch(query)
	case "mysql":
		m = mysqlDenied.FindStringSubmatch(query)
	case "postgres":
		m = postgresDenied.FindStringSubmatch(query)
	}
	if m != nil {
		what := strings.ToUpper(strings.Join(strings.Fields(m[2]), " "))
		if what == "" {
			what = "COPY"
		}
		return fmt.Errorf("sql: %s is not allowed on a %s store (it reaches the host filesystem/engine, not your schema)", what, s.driver)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies the connection (used by wiring tests and health checks).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Query runs a SELECT-shaped statement with args bound to the driver's
// placeholders and returns every row as a column→value map. Values are
// JSON-shaped: []byte columns decode to string, timestamps to RFC 3339.
func (s *Store) Query(ctx context.Context, query string, args []any) ([]map[string]any, error) {
	if err := s.checkStatement(query); err != nil {
		return nil, err
	}
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
	if err := s.checkStatement(query); err != nil {
		return 0, nil, err
	}
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
