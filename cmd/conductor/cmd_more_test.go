package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

const legacyMini = `
integrations:
  - type: cron
    name: chores
    schedules:
      - name: tidy
        cron: "0 4 * * *"
        action: { type: command, command: [make, tidy] }
`

func TestCmdValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	os.WriteFile(path, []byte(legacyMini), 0o600)
	if err := cmdValidate([]string{"--config", path}); err != nil {
		t.Fatalf("valid legacy config: %v", err)
	}
	// A connectors config validates through the flow stack too.
	os.WriteFile(path, []byte(`
connectors:
  timer: { type: cron, schedules: { tick: { every: 1h } } }
  box: { type: command }
triggers:
  - on: timer.tick
    steps: [{ id: t, uses: box.run, options: { command: "true" } }]
`), 0o600)
	if err := cmdValidate([]string{"--config", path}); err != nil {
		t.Fatalf("valid connectors config: %v", err)
	}
	// A broken reference fails.
	os.WriteFile(path, []byte(`
connectors:
  timer: { type: cron, schedules: { tick: { every: 1h } } }
triggers:
  - on: timer.nope
    steps: [{ id: t, type: command, command: [x] }]
`), 0o600)
	if err := cmdValidate([]string{"--config", path}); err == nil {
		t.Fatal("bad event must fail validate")
	}
}

func TestCmdConfigMigrate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	os.WriteFile(path, []byte(legacyMini), 0o600)

	// Usage error.
	if err := cmdConfig([]string{"--config", path}); err == nil {
		t.Fatal("missing subcommand must error")
	}
	// Dry run: nothing written.
	if err := cmdConfig([]string{"--config", path, "migrate", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); !strings.Contains(string(b), "integrations:") {
		t.Fatal("dry run must not touch the file")
	}
	// Real migrate: file transformed, backup written.
	if err := cmdConfig([]string{"--config", path, "migrate"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "connectors:") || strings.Contains(string(b), "integrations:") {
		t.Fatalf("migrated config:\n%s", b)
	}
	if _, err := os.Stat(path + ".pre-connectors"); err != nil {
		t.Fatal("backup missing")
	}
	// Idempotent second run.
	if err := cmdConfig([]string{"--config", path, "migrate"}); err != nil {
		t.Fatal(err)
	}
	// autoMigrateOnBoot on an already-migrated config is a quiet no-op; on a
	// missing path it stays silent.
	if warn := autoMigrateOnBoot([]string{"--config", path}); warn != "" {
		t.Fatalf("no-op boot migrate warned: %q", warn)
	}
	if warn := autoMigrateOnBoot([]string{"--config", filepath.Join(dir, "nope.yaml")}); warn != "" {
		t.Fatalf("missing config warned: %q", warn)
	}
}

func TestSmallCmdHelpers(t *testing.T) {
	if toInt64Any(int64(1)) != 1 || toInt64Any(2) != 2 || toInt64Any(3.0) != 3 || toInt64Any("x") != 0 {
		t.Fatal("toInt64Any")
	}
	cfg := &config.Config{}
	cfg.Store.StateFile = "/data/state.json"
	if pidPath(cfg) != "/data/conductor.pid" || controlSockPath(cfg) != "/data/control.sock" {
		t.Fatal("sibling paths")
	}
	on := true
	if anySlackIntegration(&config.Config{}) {
		t.Fatal("no integrations")
	}
	slackCfg := &config.Config{Integrations: []config.IntegrationRef{{Type: "slack", Enabled: &on}}}
	if !anySlackIntegration(slackCfg) {
		t.Fatal("enabled slack detected")
	}
	preflightPATH("definitely-not-a-binary") // warns, never fails
}
