package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"
	_ "modernc.org/sqlite"           // database/sql driver "sqlite"
)

// The three drivers are pure Go — pgx, go-sql-driver/mysql, and
// modernc.org/sqlite — keeping the zero-cgo static-binary invariant.
// Placeholder syntax is the driver's own: $1, $2, … for postgres; ? for
// mysql and sqlite.

// OpenPostgres opens a postgres store from a pgx-style URL or DSN. The URL
// is validated now (a bad url is a load error); the server is dialed lazily
// on first use, so an unreachable database doesn't block boot. password,
// when non-empty, overrides any password in the URL (it's the
// secret-resolved value).
func OpenPostgres(url, password string) (*Store, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("bad postgres url: %w", err)
	}
	if password != "" {
		cfg.Password = password
	}
	db, err := sql.Open("pgx", stdlib.RegisterConnConfig(cfg))
	if err != nil {
		return nil, err
	}
	return New(db, "postgres"), nil
}

// OpenMySQL opens a mysql store from a go-sql-driver DSN
// (user:pass@tcp(host:port)/db). The DSN is validated now; the server is
// dialed lazily. password, when non-empty, overrides the DSN's password.
func OpenMySQL(dsn, password string) (*Store, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("bad mysql dsn: %w", err)
	}
	if password != "" {
		cfg.Passwd = password
	}
	// Timestamps should come back as time.Time (normalized to RFC 3339),
	// not raw []byte.
	cfg.ParseTime = true
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return nil, err
	}
	return New(db, "mysql"), nil
}

// OpenSQLite opens (creating if absent) a sqlite store at path — a file
// path, ":memory:", or a full file: URI. Like boltdb, the file is local, so
// it's opened and pinged eagerly: an unopenable path is a load error. File
// databases get a busy timeout so pooled connections don't fail on write
// contention; :memory: databases pin to one connection (each sqlite
// connection otherwise gets its own private memory database).
func OpenSQLite(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite: path: is required")
	}
	dsn := path
	memory := path == ":memory:" || strings.Contains(path, "mode=memory")
	switch {
	case memory, strings.HasPrefix(path, "file:"):
		// Pass URIs and :memory: through untouched.
	default:
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("sqlite: create data dir: %w", err)
		}
		dsn = "file:" + path + "?_pragma=busy_timeout(5000)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if memory {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		db.SetConnMaxIdleTime(0)
		db.SetConnMaxLifetime(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	return New(db, "sqlite"), nil
}
