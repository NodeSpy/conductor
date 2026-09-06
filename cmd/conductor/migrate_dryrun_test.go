package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// REGRESSION: `config migrate --dry-run` used to only re-parse the transform
// — it false-passed configs the REAL migration (and the next boot) would
// refuse. The dry run now runs the same full validation pipeline.
func TestMigrateDryRunValidatesLikeRealPath(t *testing.T) {
	write := func(t *testing.T, doc string) string {
		t.Helper()
		dir := t.TempDir()
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A legacy config whose transform PARSES but fails semantic validation:
	// the action names an agent no profile defines.
	t.Setenv("GH_WEBHOOK_SECRET", "dummy")
	keyPath := writeTempRSAKey(t)
	bad := write(t, `
integrations:
  - name: gh
    type: github
    app: { app_id: 123, private_key_path: `+keyPath+`, webhook_secret: ${GH_WEBHOOK_SECRET} }
    webhook: { smee_url: https://smee.io/x }
    rules:
      - match: { repos: ["acme/*"] }
        actions:
          merge_conflict:
            - type: agent
              agent: ghost-profile
              prompt: "fix"
`)
	err := cmdConfigMigrate([]string{"--config", bad, "--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "FAILS validation") {
		t.Fatalf("dry-run must fail validation like the real path, got: %v", err)
	}
	if _, statErr := os.Stat(bad + ".migrate-dryrun"); statErr == nil {
		t.Fatal("dry-run scratch file left behind")
	}
	if _, statErr := os.Stat(bad + ".pre-connectors"); statErr == nil {
		t.Fatal("dry-run wrote a backup — it must write nothing")
	}

	// The same config with a defined agent passes.
	good := write(t, `
integrations:
  - name: gh
    type: github
    app: { app_id: 123, private_key_path: `+keyPath+`, webhook_secret: ${GH_WEBHOOK_SECRET} }
    webhook: { smee_url: https://smee.io/x }
    rules:
      - match: { repos: ["acme/*"] }
        actions:
          merge_conflict:
            - type: agent
              agent: fixer
              prompt: "fix"
agents:
  fixer: { provider: claude }
`)
	if err := cmdConfigMigrate([]string{"--config", good, "--dry-run"}); err != nil {
		t.Fatalf("valid dry-run must pass: %v", err)
	}
	// And nothing was written: the on-disk file is still the legacy one.
	raw, _ := os.ReadFile(good)
	if !strings.Contains(string(raw), "integrations:") {
		t.Fatal("dry-run modified the config")
	}
}
