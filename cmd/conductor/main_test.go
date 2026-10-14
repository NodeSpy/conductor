package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "conductor.env")
	content := "" +
		"# a comment\n" +
		"\n" +
		"GH_WEBHOOK_SECRET=abc123\n" +
		"GH_SMEE_URL=https://smee.io/chan\n" +
		"QUOTED=\"hi there\"\n" +
		"PRESET=fromfile\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// An already-set var must NOT be overridden by the file.
	t.Setenv("PRESET", "fromenv")
	// Ensure the others are unset going in.
	for _, k := range []string{"GH_WEBHOOK_SECRET", "GH_SMEE_URL", "QUOTED"} {
		os.Unsetenv(k)
		t.Cleanup(func() { os.Unsetenv(k) })
	}

	loadEnvFile(path)

	if got := os.Getenv("GH_WEBHOOK_SECRET"); got != "abc123" {
		t.Errorf("GH_WEBHOOK_SECRET = %q, want abc123", got)
	}
	if got := os.Getenv("GH_SMEE_URL"); got != "https://smee.io/chan" {
		t.Errorf("GH_SMEE_URL = %q", got)
	}
	if got := os.Getenv("QUOTED"); got != "hi there" {
		t.Errorf("QUOTED = %q, want unquoted 'hi there'", got)
	}
	if got := os.Getenv("PRESET"); got != "fromenv" {
		t.Errorf("PRESET = %q — exported env must win over the file", got)
	}
}

func TestLoadEnvFileMissingIsNoop(t *testing.T) {
	// A missing file must be a silent no-op (not panic/error).
	loadEnvFile(filepath.Join(t.TempDir(), "does-not-exist.env"))
}
