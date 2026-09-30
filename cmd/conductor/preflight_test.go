package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixerConfig is a minimal connectors-model config with one fixer-shaped
// step: a cli runtime, checkout-pr, expect_push — exactly the step preflight's
// checkGit treats as needing real git to land a change.
const fixerConfig = `
connectors:
  timer: { use: cron, schedules: { tick: { every: 1h } } }
runtimes:
  fixer: { use: cli, tool: claude-code }
triggers:
  - on: timer.tick
    steps:
      - id: fix
        type: agent
        runtime: fixer
        checkout: checkout-pr
        expect_push: true
        prompt: "fix it"
`

// TestValidateReportsMissingGitForAFixerConfig forces preflight's git lookup
// to fail (without touching the real PATH) and asserts `conductor validate`
// turns that into a failing exit for a config that has a fixer step — the
// wiring in cmdValidate/reportPreflight, not preflight's own logic (which
// internal/preflight already covers with a fake LookPath).
func TestValidateReportsMissingGitForAFixerConfig(t *testing.T) {
	prevLookPath := preflightLookPath
	preflightLookPath = func(name string) (string, error) {
		if name == "git" {
			return "", errors.New("forced for test: no git")
		}
		// Everything else resolves, so git is the ONLY missing binary
		// whatever this machine has installed.
		return "/usr/bin/" + name, nil
	}
	t.Cleanup(func() { preflightLookPath = prevLookPath })

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(fixerConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	err := cmdValidate([]string{"--config", path})
	if err == nil {
		t.Fatal("validate succeeded despite a missing-git error finding for a fixer config")
	}
	if !strings.Contains(err.Error(), "git") {
		t.Fatalf("validate error does not mention git: %v", err)
	}
}

// TestValidatePassesWhenGitIsPresent is the control: the SAME config with git
// resolvable must not fail preflight (the earlier test isn't vacuously
// failing for an unrelated reason). The lookup is stubbed so the result does
// not depend on this machine: git and the runtime's tool binary (claude) both
// resolve — a CI runner without claude installed would otherwise fail the
// cli-tool check, which is a different finding.
func TestValidatePassesWhenGitIsPresent(t *testing.T) {
	prevLookPath := preflightLookPath
	preflightLookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
	t.Cleanup(func() { preflightLookPath = prevLookPath })
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(fixerConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdValidate([]string{"--config", path}); err != nil {
		t.Fatalf("validate failed with a real git on PATH: %v", err)
	}
}
