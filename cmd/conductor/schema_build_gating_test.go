package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
	"github.com/NodeSpy/conductor/internal/connector"
	"github.com/NodeSpy/conductor/internal/plugin"
)

// Finding 3 (MEDIUM): `conductor schema <name>` used to build the WHOLE
// plugin stack unconditionally — `stack, _ := buildFlowStack(...)` — before
// even checking whether name was a bare, already-registered type. That
// spawned every configured plugin connector in the file just to answer a
// bare type query a builtin's init-time registration already had the
// answer for, and it discarded whatever the build failed with. These tests
// drive cmdSchema end to end (no manual connector.Register* standing in for
// what a build would otherwise do) to prove: a bare builtin type query
// never builds at all, an external plugin type query still works exactly
// as before, and a build that genuinely happens and fails is reported
// rather than swallowed.

// captureStdoutStderr runs fn with both os.Stdout and os.Stderr redirected
// to pipes, returning everything written to each.
func captureStdoutStderr(t *testing.T, fn func() error) (stdout, stderr string, ferr error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	re, we, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wo, we
	ferr = fn()
	wo.Close()
	we.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	bo, _ := io.ReadAll(ro)
	be, _ := io.ReadAll(re)
	return string(bo), string(be), ferr
}

// TestCmdSchemaBareBuiltinTypeNeverBuildsOrSpawnsPlugins is finding 3's core
// case: `schema webhook` (a bare, unconfigured type query) must answer from
// webhook's init-time registration alone, even when the SAME config also
// references an installed external plugin connector — if a build ever
// happened, that plugin would actually spawn (proven by
// TestCmdSchemaBareExternalTypeWithOneGroup's own captured "subprocess
// started" log line for the identical fixture). No network is involved:
// the plugin is pre-installed into local install state exactly as
// `conductor init` would have left it.
func TestCmdSchemaBareBuiltinTypeNeverBuildsOrSpawnsPlugins(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin, sum := buildAcmeEchoPing(t, false)
	t.Cleanup(func() { connector.UnregisterExternalType("acme-echo") })

	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{Key: "connectors/acme-echo", Kind: config.PluginKindConnector, Name: "acme-echo",
		Resolved: "acme-echo/v1.0.0", Sha256: sum, Path: bin})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	doc := "connectors:\n  hook:\n    use: webhook\n  echoer:\n    use: acme/plugins/acme-echo\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := captureStdoutStderr(t, func() error {
		return cmdSchema([]string{"--config", path, "webhook"})
	})
	if err != nil {
		t.Fatalf("schema webhook: %v\nstdout:\n%sstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "sources") || !strings.Contains(stdout, "connection:") {
		t.Fatalf("schema webhook must still print the type's real declaration, got:\n%s", stdout)
	}
	combined := stdout + stderr
	if strings.Contains(combined, "acme-echo") || strings.Contains(combined, "subprocess started") {
		t.Fatalf("schema webhook (a bare builtin type query) must never build the plugin stack or touch an unrelated configured plugin, got:\nstdout:\n%sstderr:\n%s", stdout, stderr)
	}
}

// TestCmdSchemaBareExternalTypeStillBuilds is the complementary half: a
// bare query for a type that is NOT yet known (an external plugin with no
// configured instance, here reusing a type name no builtin or test fixture
// registers) must still build — exactly the pre-existing
// TestCmdSchemaBareExternalTypeWithOneGroup behavior, kept passing by this
// change rather than duplicated here.
func TestCmdSchemaBareExternalTypeStillBuilds(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	bin, sum := buildAcmeEchoPing(t, false)
	t.Cleanup(func() { connector.UnregisterExternalType("acme-echo") })

	state := plugin.LoadInstallState(plugin.InstallDir())
	state.Put(plugin.Installed{Key: "connectors/acme-echo", Kind: config.PluginKindConnector, Name: "acme-echo",
		Resolved: "acme-echo/v1.0.0", Sha256: sum, Path: bin})
	if err := state.Save(); err != nil {
		t.Fatal(err)
	}
	args := writeAcmeEchoConfig(t, "")

	stdout, stderr, err := captureStdoutStderr(t, func() error {
		return cmdSchema(append(append([]string(nil), args...), "acme-echo"))
	})
	if err != nil {
		t.Fatalf("schema acme-echo: %v\nstdout:\n%sstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, "reference echo connector (example plugin)") {
		t.Fatalf("schema acme-echo must still print the plugin's own declaration, got:\n%s", stdout)
	}
}

// TestCmdSchemaReportsBuildFailureAsWarningNotSwallowed is finding 3's
// other half: when answering DOES require a build (here forced to fail
// inside buildFlowStack itself — blob.Open's MkdirAll — by pointing the
// state file through a path component that is a plain FILE, not a
// directory; deterministic, no network or plugin involved, and never
// caught by config load/validate, only by the build) the error must be
// reported, never silently discarded the way `stack, _ :=
// buildFlowStack(...)` used to. The command still degrades to its
// best-effort answer (webhook's type-level declaration) rather than
// failing outright — consistent with every other "a build problem
// disables gracefully" posture in this codebase — but the failure itself
// must be visible.
func TestCmdSchemaReportsBuildFailureAsWarningNotSwallowed(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(blocker, "state.json")

	path := filepath.Join(dir, "config.yaml")
	doc := "connectors:\n  hook:\n    use: webhook\nstore:\n  state_file: " + stateFile + "\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := captureStdoutStderr(t, func() error {
		return cmdSchema([]string{"--config", path, "hook"})
	})
	if err != nil {
		t.Fatalf("schema hook: %v\nstdout:\n%sstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "warning:") || !strings.Contains(stderr, "blob:") {
		t.Fatalf("the build failure must be reported as a warning, not swallowed, got stderr:\n%s", stderr)
	}
	if !strings.Contains(stdout, "connector hook (use webhook, type webhook)") {
		t.Fatalf("schema must still answer with its best-effort (type-level) declaration, got:\n%s", stdout)
	}
}
