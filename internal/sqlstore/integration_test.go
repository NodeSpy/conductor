//go:build sqlintegration

package sqlstore

// Live postgres/mysql round-trips, kept behind a build tag so the default
// `go test ./...` never needs a server. Run with a throwaway database:
//
//	CONDUCTOR_TEST_PG_URL=postgres://user:pw@localhost:5432/scratch \
//	CONDUCTOR_TEST_MYSQL_DSN=user:pw@tcp(localhost:3306)/scratch \
//	go test -tags sqlintegration ./internal/sqlstore
//
// Each test skips when its env var is unset.

import (
	"context"
	"os"
	"testing"
)

func TestPostgresLive(t *testing.T) {
	url := os.Getenv("CONDUCTOR_TEST_PG_URL")
	if url == "" {
		t.Skip("CONDUCTOR_TEST_PG_URL not set")
	}
	s, err := OpenPostgres(url, "")
	if err != nil {
		t.Fatalf("OpenPostgres: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, _, err := s.Exec(ctx, `CREATE TEMPORARY TABLE conductor_it (id SERIAL PRIMARY KEY, body TEXT)`, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	hostile := `'; DROP TABLE conductor_it; --`
	n, _, err := s.Exec(ctx, `INSERT INTO conductor_it (body) VALUES ($1)`, []any{hostile})
	if err != nil || n != 1 {
		t.Fatalf("insert: n=%d err=%v", n, err)
	}
	rows, err := s.Query(ctx, `SELECT id, body FROM conductor_it WHERE body = $1`, []any{hostile})
	if err != nil || len(rows) != 1 || rows[0]["body"] != hostile {
		t.Fatalf("select: rows=%v err=%v", rows, err)
	}
}

func TestMySQLLive(t *testing.T) {
	dsn := os.Getenv("CONDUCTOR_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("CONDUCTOR_TEST_MYSQL_DSN not set")
	}
	s, err := OpenMySQL(dsn, "")
	if err != nil {
		t.Fatalf("OpenMySQL: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	if err := s.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if _, _, err := s.Exec(ctx, `CREATE TEMPORARY TABLE conductor_it (id INT AUTO_INCREMENT PRIMARY KEY, body TEXT)`, nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	hostile := `'; DROP TABLE conductor_it; --`
	n, id, err := s.Exec(ctx, `INSERT INTO conductor_it (body) VALUES (?)`, []any{hostile})
	if err != nil || n != 1 || id == nil {
		t.Fatalf("insert: n=%d id=%v err=%v", n, id, err)
	}
	rows, err := s.Query(ctx, `SELECT id, body FROM conductor_it WHERE body = ?`, []any{hostile})
	if err != nil || len(rows) != 1 || rows[0]["body"] != hostile {
		t.Fatalf("select: rows=%v err=%v", rows, err)
	}
}
