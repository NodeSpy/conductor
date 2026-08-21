package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
	"github.com/NodeSpy/paseo-conductor/internal/core"
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

// fakeTuner is a minimal integration implementing core.Integration + dispatchTuner.
type fakeTuner struct {
	read, write, author string
	retry               config.Retry
}

func (f *fakeTuner) Name() string                               { return "fake" }
func (f *fakeTuner) Validate() error                            { return nil }
func (f *fakeTuner) Start(context.Context, core.EmitFunc) error { return nil }
func (f *fakeTuner) RetryPolicy() config.Retry                  { return f.retry }
func (f *fakeTuner) IdentityTokens() (r, w, a string)           { return f.read, f.write, f.author }

func TestDispatchTuning(t *testing.T) {
	// write=literal PAT, read=literal → both resolvers return the literals.
	igs := []core.Integration{&fakeTuner{read: "READTOK", write: "WRITETOK", retry: config.Retry{Max: 5}}}
	retry, write, read := dispatchTuning(igs)
	if retry.Max != 5 {
		t.Fatalf("retry not sourced from integration: %+v", retry)
	}
	if got, _ := write(); got != "WRITETOK" {
		t.Fatalf("write should resolve to the literal token, got %q", got)
	}
	if read == nil {
		t.Fatal("read override expected for a literal read_token")
	}
	if got, _ := read(); got != "READTOK" {
		t.Fatalf("read should resolve to the literal token, got %q", got)
	}

	// Defaults: read=app → no override (nil); write=gh_auth → userToken (not our literal).
	_, write2, read2 := dispatchTuning([]core.Integration{&fakeTuner{read: "app", write: "gh_auth"}})
	if read2 != nil {
		t.Fatal("read_token=app must not set a read override (uses the per-trigger App token)")
	}
	if write2 == nil {
		t.Fatal("write resolver should never be nil")
	}
}
