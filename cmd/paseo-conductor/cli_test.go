package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
// and one cred-broken connector, plus a secrets: block with one good and one
// bad reference.
func writeCLIConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	doc := `
secrets:
  good: env:PC_CLI_TEST_SECRET
  bad: file:/nonexistent/pc-cli-secret
connectors:
  box:
    type: command
    env: { CI: "1" }
  timer:
    type: cron
    enabled: false
    schedules: { tick: { every: 1h } }
  broken:
    type: slack
    app_token: file:/nonexistent/pc-cli-app
    bot_token: file:/nonexistent/pc-cli-bot
triggers:
  - on: timer.tick
    steps: [{ id: hi, uses: box.run, options: { command: "true" } }]
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conductor.env"), []byte("PC_CLI_TEST_SECRET=shh\n"), 0o600); err != nil {
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
		"broken", "disabled: app_token",
		"verbs:  run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ls output missing %q:\n%s", want, out)
		}
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
	for _, want := range []string{"connector box (type command)", "verb run", "stdout", "exit_code"} {
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

func TestCmdSecretsCheck(t *testing.T) {
	path := writeCLIConfig(t)
	out, err := captureStdout(t, func() error { return cmdSecrets([]string{"--config", path, "check"}) })
	if err == nil || !strings.Contains(err.Error(), "failed to resolve") {
		t.Fatalf("check with a bad ref should error, got %v", err)
	}
	for _, want := range []string{
		"ok   secrets.good", "FAIL secrets.bad",
		"ok   connector box", "--   connector timer (disabled in config)",
		"FAIL connector broken",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("check output missing %q:\n%s", want, out)
		}
	}
	// Values never print.
	if strings.Contains(out, "shh") {
		t.Error("secrets check leaked a resolved value")
	}

	// All-green config → nil error and the closing line.
	goodDir := t.TempDir()
	good := filepath.Join(goodDir, "config.yaml")
	os.WriteFile(good, []byte("connectors:\n  box: { type: command }\n"), 0o600)
	out, err = captureStdout(t, func() error { return cmdSecrets([]string{"--config", good, "check"}) })
	if err != nil || !strings.Contains(out, "all secret references resolve") {
		t.Errorf("green path: err=%v out:\n%s", err, out)
	}
}
