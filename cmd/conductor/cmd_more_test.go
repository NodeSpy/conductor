package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NodeSpy/conductor/internal/config"
)

// legacyMini is a config still on the top-level `integrations:` block removed
// with the legacy config schema (plugin-contract.md decision Q4) — `validate`
// must name it rather than silently accepting it.
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
	err := cmdValidate([]string{"--config", path})
	if err == nil || !strings.Contains(err.Error(), "`integrations:` was removed with the legacy config schema") {
		t.Fatalf("a config still using integrations: should name it as removed, got %v", err)
	}
	// A connectors config validates through the flow stack too.
	os.WriteFile(path, []byte(`
connectors:
  timer: { use: cron, schedules: { tick: { every: 1h } } }
  box: { use: command }
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
  timer: { use: cron, schedules: { tick: { every: 1h } } }
triggers:
  - on: timer.nope
    steps: [{ id: t, type: command, command: [x] }]
`), 0o600)
	if err := cmdValidate([]string{"--config", path}); err == nil {
		t.Fatal("bad event must fail validate")
	}
}

// `config migrate` went with the legacy schema (plugin-contract.md Q4); the
// command says so instead of failing as an unknown subcommand.
func TestCmdConfigMigrateRemoved(t *testing.T) {
	err := cmdConfig([]string{"migrate"})
	if err == nil || !strings.Contains(err.Error(), "removed with the legacy config schema") {
		t.Fatalf("config migrate must explain its removal, got %v", err)
	}
}

// TestMigrateHintForExe (finding 8, see 5b): every "run `conductor config
// migrate` with the release before the plugin contract" hint in this
// codebase is only actionable if the operator still HAS that release's
// binary — which an unattended auto-update used to make untrue (it replaces
// the executable in place with nothing kept behind). Once doUpdate's
// rollback copy (<exe>.prev) exists, the hint must name it directly instead
// of sending the operator to re-fetch a release they already have on disk.
func TestMigrateHintForExe(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "conductor")

	t.Run("no rollback copy on disk", func(t *testing.T) {
		got := migrateHintForExe(exe)
		if !strings.Contains(got, "run it with the release before the plugin contract") {
			t.Fatalf("want the generic fetch-it-yourself hint, got: %q", got)
		}
		if strings.Contains(got, "config migrate`") {
			t.Fatalf("must not name a binary that isn't there: %q", got)
		}
	})

	t.Run("a rollback copy from a prior auto-update", func(t *testing.T) {
		prev := exe + prevBinarySuffix
		if err := os.WriteFile(prev, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		got := migrateHintForExe(exe)
		if !strings.Contains(got, prev) || !strings.Contains(got, "config migrate`") {
			t.Fatalf("want the hint to name the actual rollback binary %q, got: %q", prev, got)
		}
	})
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
	preflightPATH("definitely-not-a-binary") // warns, never fails
}
