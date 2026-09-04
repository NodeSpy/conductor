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
    type: command
    env: { CI: "1" }
  timer:
    type: cron
    enabled: false
    schedules: { tick: { every: 1h } }
  broken:
    type: slack
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
	os.WriteFile(good, []byte("connectors:\n  box: { type: command }\n"), 0o600)
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
    type: github
    token: dummy-replay-token
    me: { logins: [danielcbaldwin] }
    repos: ["EdnitionCode/RosterStream"]
    webhook: { listen: "127.0.0.1:0", secret: replay-test }
    sweep: { enabled: false }
  box:
    type: command
triggers:
  - on: gh.review_requested
    steps:
      - { id: shape, run: js, code: "return { ok: true }" }
      - { id: notecmd, uses: box.run, options: { command: "true" } }
`
	if err := os.WriteFile(cfgPath, []byte(cfgDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "fixture.json")
	fx := `{"event": "pull_request", "body": {
  "action": "review_requested",
  "installation": { "id": 0 },
  "repository": { "full_name": "EdnitionCode/RosterStream", "name": "RosterStream",
    "default_branch": "main", "owner": { "login": "EdnitionCode" } },
  "pull_request": { "number": 5300, "state": "open", "draft": false,
    "title": "auth: rework session refresh",
    "html_url": "https://github.com/EdnitionCode/RosterStream/pull/5300",
    "head": { "sha": "cafebabe1234", "ref": "feature/auth-refresh" },
    "base": { "ref": "main" }, "user": { "login": "someone-else" } },
  "requested_reviewer": { "login": "danielcbaldwin" }
}}`
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
		"review_requested EdnitionCode/RosterStream#5300 [workflow: 2 steps] (dry-run)",
		"would run code step (js)",
		"would invoke box.run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("replay output missing %q:\n%s", want, out)
		}
	}
}
