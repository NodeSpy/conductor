package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/core"
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

// GUARD (#60, I1): a pure connectors-model config (no legacy integrations:
// block) must still have its github connector's identity.write_token reach
// dispatchTuning — otherwise the acts-as-the-user write silently falls back
// to a bare `gh auth token`. buildIntegrations alone (the legacy path) sees
// nothing; resolveDispatchIdentity must fold the connectors-model stack's
// lowered integrations in before deriving the token resolvers.
func TestResolveDispatchIdentitySeesConnectorsModelWriteToken(t *testing.T) {
	cfg := loadConfigDoc(t, `
connectors:
  github:
    use: github
    identity:
      write_token: e2e-user-write-token
      read_token: app
    webhook:
      listen: 0.0.0.0:8787
      secret: test-webhook-secret
triggers:
  - on: github.merge_conflict
    steps: [{ id: fix, type: agent, prompt: "fix" }]
`)
	igs, err := buildIntegrations(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(igs) != 0 {
		t.Fatalf("a config with no legacy integrations: block must build zero legacy "+
			"integrations, got %d — this test's premise (the connectors-model stack is "+
			"the ONLY source of the write token) doesn't hold", len(igs))
	}
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	all, _, write, _ := resolveDispatchIdentity(igs, stack)
	if len(all) == 0 {
		t.Fatal("resolveDispatchIdentity must fold the connectors-model stack's lowered " +
			"integrations into the returned set")
	}
	got, _ := write()
	if got != "e2e-user-write-token" {
		t.Fatalf("write token = %q, want the connector's identity.write_token "+
			"(e2e-user-write-token) — dispatchTuning never saw the connectors-model "+
			"integration, so it fell back to a bare `gh auth token`", got)
	}
}
