package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/connector"
)

// captureStdout runs fn with os.Stdout redirected and returns what it printed.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	ferr := fn()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b), ferr
}

// writeCLIConfig writes a connectors config with one healthy, one authored-off,
// and one cred-broken connector, plus one healthy and one broken vault.
func writeCLIConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	secDir := filepath.Join(dir, "filesec")
	if err := os.MkdirAll(secDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secDir, "tok"), []byte("shh-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := `
vaults:
  goodvault: { type: file, dir: ` + secDir + ` }
  badvault:  { type: file, dir: /nonexistent/pc-cli-vault }
connectors:
  box:
    use: command
    env: { CI: "1" }
  timer:
    use: cron
    enabled: false
    schedules: { tick: { every: 1h } }
  broken:
    use: slack
    app_token: env:PC_CLI_NOPE_APP
    bot_token: env:PC_CLI_NOPE_BOT
triggers:
  - on: timer.tick
    steps: [{ id: hi, uses: box.run, options: { command: "true" } }]
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCmdConnectorsLs(t *testing.T) {
	path := writeCLIConfig(t)
	out, err := captureStdout(t, func() error { return cmdConnectors([]string{"--config", path, "ls"}) })
	if err != nil {
		t.Fatalf("connectors ls: %v\n%s", err, out)
	}
	for _, want := range []string{
		"box", "command", "enabled",
		"timer", "disabled (enabled: false)",
		"broken", `disabled: resolve "`, "is not set",
		"verbs:  run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ls output missing %q:\n%s", want, out)
		}
	}

	// finding 6: `connectors ls` builds its own throwaway, short-lived
	// stack just to describe each connector for display — any pid it could
	// show would be that one-off process's, never the running daemon's. It
	// must print no "pid:" line at all rather than one that looks live but
	// isn't (docs/wiki/Plugins.md "Multi-instance isolation" points at the
	// daemon log's own "subprocess started (pid N)" lines instead).
	if strings.Contains(out, "pid:") {
		t.Errorf("ls output must never show a pid (it is this CLI invocation's own throwaway process, not the daemon's):\n%s", out)
	}

	// Wrong subcommand → usage error.
	if _, err := captureStdout(t, func() error { return cmdConnectors([]string{"--config", path, "nope"}) }); err == nil {
		t.Error("bad subcommand should error")
	}
}

func TestCmdSchema(t *testing.T) {
	path := writeCLIConfig(t)
	out, err := captureStdout(t, func() error { return cmdSchema([]string{"--config", path, "box"}) })
	if err != nil {
		t.Fatalf("schema box: %v", err)
	}
	for _, want := range []string{"connector box (use command, type command)", "verb run", "stdout", "exit_code"} {
		if !strings.Contains(out, want) {
			t.Errorf("schema output missing %q:\n%s", want, out)
		}
	}

	// A bare type name works without a configured connector of that type.
	out, err = captureStdout(t, func() error { return cmdSchema([]string{"--config", path, "github"}) })
	if err != nil || !strings.Contains(out, "verb comment") {
		t.Errorf("schema by type: err=%v out:\n%s", err, out)
	}

	// Unknown name → error listing the available types.
	_, err = captureStdout(t, func() error { return cmdSchema([]string{"--config", path, "nope"}) })
	if err == nil || !strings.Contains(err.Error(), "types:") {
		t.Errorf("unknown connector should error with the type list, got %v", err)
	}
}

// TestCmdSchemaRestShowsDeclaredPlaceholder: `conductor schema rest` with no
// configured instance must still show an event is possible — the
// `<declared>` Dynamic placeholder restored on rest's type-level Describe()
// (git show 4cade34:internal/connector/rest.go had it; Q6 dropped it when
// real events moved to DescribeInstance).
// TestCmdSchemaBareTypeShowsEveryResolvedVersion is finding 5: `conductor
// schema <type>` for a bare, unconfigured type name must show EVERY
// resolved-version group's declaration when more than one is registered
// (side by side), not silently whichever one happened to register first.
func TestCmdSchemaBareTypeShowsEveryResolvedVersion(t *testing.T) {
	path := writeCLIConfig(t)
	const typ = "zz-schema-multi-version"
	t.Cleanup(func() {
		connector.UnregisterExternalType(typ)
		connector.ResetInstanceGroups()
	})
	gk1, gk2 := "connectors/"+typ+"@v1.0.0", "connectors/"+typ+"@v2.0.0"
	if err := connector.RegisterExternalTypeGroup(&connector.TypeDecl{Type: typ, Desc: "the v1 build"}, nil, gk1, "connectors/"+typ); err != nil {
		t.Fatalf("register group 1: %v", err)
	}
	if err := connector.RegisterExternalTypeGroup(&connector.TypeDecl{Type: typ, Desc: "the v2 build"}, nil, gk2, "connectors/"+typ); err != nil {
		t.Fatalf("register group 2: %v", err)
	}

	out, err := captureStdout(t, func() error { return cmdSchema([]string{"--config", path, typ}) })
	if err != nil {
		t.Fatalf("schema %s: %v", typ, err)
	}
	for _, want := range []string{"version 1 of 2", "version 2 of 2", "the v1 build", "the v2 build", gk1, gk2} {
		if !strings.Contains(out, want) {
			t.Errorf("schema output missing %q:\n%s", want, out)
		}
	}
}

func TestCmdSchemaRestShowsDeclaredPlaceholder(t *testing.T) {
	path := writeCLIConfig(t)
	out, err := captureStdout(t, func() error { return cmdSchema([]string{"--config", path, "rest"}) })
	if err != nil {
		t.Fatalf("schema rest: %v", err)
	}
	for _, want := range []string{"event <declared in connection>", "a polled events: entry produced a new item"} {
		if !strings.Contains(out, want) {
			t.Errorf("schema rest output missing %q:\n%s", want, out)
		}
	}
}

// TestCmdSchemaReportsDisabledInstance: a connector whose per-instance build
// failed (here: slack's missing credentials) must not print a silent,
// empty-looking schema and exit 0 — it must say WHY, the same reason
// `connectors ls` shows, and exit non-zero so a script checking `schema`
// against a broken instance notices instead of reading a bare shell.
func TestCmdSchemaReportsDisabledInstance(t *testing.T) {
	path := writeCLIConfig(t)
	out, err := captureStdout(t, func() error { return cmdSchema([]string{"--config", path, "broken"}) })
	if err == nil {
		t.Fatalf("schema of a disabled instance must exit non-zero, output:\n%s", out)
	}
	for _, want := range []string{"disabled: resolve \"", "is not set"} {
		if !strings.Contains(out, want) {
			t.Errorf("schema output missing %q:\n%s", want, out)
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("returned error missing %q: %v", want, err)
		}
	}

	// A deliberately `enabled: false` connector is not a failure — the
	// schema still prints and the command still succeeds.
	out, err = captureStdout(t, func() error { return cmdSchema([]string{"--config", path, "timer"}) })
	if err != nil {
		t.Fatalf("schema of an authored-off connector must not error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "disabled (enabled: false)") {
		t.Errorf("schema output missing the authored-off state:\n%s", out)
	}
}

// TestCmdSchemaDisabledByChoiceWinsOverABuildFailure covers an instance that
// is BOTH `enabled: false` AND would fail to build on its own (here: a slack
// connector with unresolvable credentials) — the registry still attempts the
// build for a disabled instance, so DisabledReason can be set right alongside
// Enabled=false. Disabled-by-choice must win: exit 0 (an operator's own
// `enabled: false` is not an error), and the output must still say it's
// disabled — ideally mentioning the underlying failure too, since otherwise
// re-enabling it later would surprise the operator with a second problem
// `connectors ls`/`schema` never mentioned. `ls` and `schema` must agree.
func TestCmdSchemaDisabledByChoiceWinsOverABuildFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	doc := `
connectors:
  offbroken:
    use: slack
    enabled: false
    app_token: env:PC_CLI_OFFBROKEN_APP
    bot_token: env:PC_CLI_OFFBROKEN_BOT
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	// The connector declares TWO credentials (app_token, bot_token), both
	// unresolvable here; the registry doesn't guarantee which one's error
	// wins the (unordered) DisabledReason, so assert on EITHER rather than a
	// specific one — an earlier version of this test pinned "PC_CLI_
	// OFFBROKEN_APP" specifically and flaked whenever bot_token's failure
	// was the one that got reported instead.
	mentionsBuildFailure := func(out string) bool {
		return strings.Contains(out, "PC_CLI_OFFBROKEN_APP") || strings.Contains(out, "PC_CLI_OFFBROKEN_BOT")
	}

	out, err := captureStdout(t, func() error { return cmdSchema([]string{"--config", path, "offbroken"}) })
	if err != nil {
		t.Fatalf("disabled BY CHOICE (enabled: false) must exit 0 even though the instance also fails to build: %v\n%s", err, out)
	}
	if !strings.Contains(out, "disabled (enabled: false)") {
		t.Errorf("schema output missing %q:\n%s", "disabled (enabled: false)", out)
	}
	if !mentionsBuildFailure(out) {
		t.Errorf("schema output should also mention the build failure (neither OFFBROKEN_APP nor OFFBROKEN_BOT found):\n%s", out)
	}

	// `connectors ls` must show the same posture for the same instance.
	out, err = captureStdout(t, func() error { return cmdConnectors([]string{"--config", path, "ls"}) })
	if err != nil {
		t.Fatalf("connectors ls: %v\n%s", err, out)
	}
	if !strings.Contains(out, "disabled (enabled: false)") {
		t.Errorf("ls output missing %q:\n%s", "disabled (enabled: false)", out)
	}
	if !mentionsBuildFailure(out) {
		t.Errorf("ls output should also mention the build failure (neither OFFBROKEN_APP nor OFFBROKEN_BOT found):\n%s", out)
	}
}

func TestCmdSecretsCheck(t *testing.T) {
	path := writeCLIConfig(t)
	out, err := captureStdout(t, func() error { return cmdSecrets([]string{"--config", path, "check"}) })
	if err == nil || !strings.Contains(err.Error(), "failed to resolve") {
		t.Fatalf("check with a bad ref should error, got %v", err)
	}
	for _, want := range []string{
		"ok   vault goodvault (file) unlocked", "FAIL vault badvault (file)",
		"ok   connector box", "--   connector timer (disabled in config)",
		"FAIL connector broken",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check output missing %q:\n%s", want, out)
		}
	}
	// Values never print.
	if strings.Contains(out, "shh-value") {
		t.Error("secrets check leaked a vault value")
	}

	// All-green config → nil error and the closing line.
	goodDir := t.TempDir()
	good := filepath.Join(goodDir, "config.yaml")
	os.WriteFile(good, []byte("connectors:\n  box: { use: command }\n"), 0o600)
	out, err = captureStdout(t, func() error { return cmdSecrets([]string{"--config", good, "check"}) })
	if err != nil || !strings.Contains(out, "all secret references resolve") {
		t.Errorf("green path: err=%v out:\n%s", err, out)
	}
}

// captureOutput runs fn with both stdout and stderr redirected (flow dry-run
// stubs log through logf → stderr).
func captureOutput(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = w, w
	ferr := fn()
	w.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	b, _ := io.ReadAll(r)
	return string(b), ferr
}

// TestCmdReplayConnectorsModel: `conductor replay` on a connectors-only
// config runs the trigger through the flow stack with every verb stubbed —
// previously it walked only legacy integrations and reported nothing.
func TestCmdReplayConnectorsModel(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfgDoc := `
connectors:
  gh:
    use: github
    token: dummy-replay-token
    me: { logins: [danielcbaldwin] }
    repos: ["AcmeCorp/Widget"]
    webhook: { listen: "127.0.0.1:0", secret: replay-test }
    sweep: { enabled: false }
  box:
    use: command
triggers:
  - on: gh.review_requested
    steps:
      - { id: shape, run: sh, code: "echo '{\"ok\": true}'" }
      - { id: notecmd, uses: box.run, options: { command: "true" } }
`
	if err := os.WriteFile(cfgPath, []byte(cfgDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "fixture.json")
	fx := reviewRequestedDelivery
	if err := os.WriteFile(fixture, []byte(fx), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := captureOutput(t, func() error { return cmdReplay([]string{"--config", cfgPath, fixture}) })
	if err != nil {
		t.Fatalf("replay: %v\n%s", err, out)
	}
	if strings.Contains(out, "no triggers produced") {
		t.Fatalf("connectors-model replay found nothing:\n%s", out)
	}
	for _, want := range []string{
		"review_requested AcmeCorp/Widget#5300 [workflow: 2 steps] (dry-run)",
		"would run code step (sh)",
		"would invoke box.run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("replay output missing %q:\n%s", want, out)
		}
	}
}

// TestCmdConnectorsLsNeverShowsPidForARealExternalPlugin is finding 6's
// mutation-sensitive proof: TestCmdConnectorsLs above never actually spawns
// an external plugin process (box/timer/broken all resolve to builtins or
// fail before ever reaching a live client), so it could not have caught a
// regression that reintroduced the pid line. This drives a REAL external
// plugin connector (acme-echo, built fresh) through `connectors ls` and
// confirms no "pid:" line appears, even though a real subprocess genuinely
// is live for the duration of this CLI invocation's own throwaway stack.
func TestCmdConnectorsLsNeverShowsPidForARealExternalPlugin(t *testing.T) {
	bin := buildTestPlugin(t, "acme-echo")
	// buildFlowStack registers "acme-echo" into the process-wide connector
	// type registry (connector.RegisterExternalType) and flowStack.Close
	// only stops its plugin subprocesses, never unregisters the type —
	// every other test in this package driving a real external plugin
	// through buildFlowStack cleans this up itself (connectors_test.go,
	// reload_inplace_test.go) for exactly this reason: a second test (or,
	// as here, `go test -count=2` rerunning this SAME test) in the same
	// binary would otherwise collide with "already provided by another
	// plugin".
	t.Cleanup(func() { connector.UnregisterExternalType("acme-echo") })
	path := filepath.Join(t.TempDir(), "config.yaml")
	doc := fmt.Sprintf("connectors:\n  echo:\n    use: %s\n    token: s3cr3t\n", bin)
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := captureStdout(t, func() error { return cmdConnectors([]string{"--config", path, "ls"}) })
	if err != nil {
		t.Fatalf("connectors ls: %v\n%s", err, out)
	}
	if !strings.Contains(out, "echo") || !strings.Contains(out, "verbs:  echo") {
		t.Fatalf("connectors ls did not describe the real acme-echo instance:\n%s", out)
	}
	if strings.Contains(out, "pid:") {
		t.Fatalf("connectors ls must never show a pid, even for a genuinely live external plugin instance (it is this CLI invocation's own throwaway process, not the daemon's):\n%s", out)
	}
}
