package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// REGRESSION: a config the migration refuses AND the strict loader rejects
// used to exit cmdRun — the service manager restarted it into the same wall
// forever (a crash-loop on auto-update). Boot now holds degraded, retries,
// and resumes the moment the config becomes loadable.
func TestBootHoldsDegradedUntilConfigLoadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Mixed schema (migration refuses: "finish the migration by hand") plus
	// a retired top-level block (strict load refuses: unknown key).
	broken := `
integrations:
  - name: gh
    type: github
connectors:
  timer:
    type: cron
    schedules: { tick: { every: 1h } }
dispatch:
  identity: { read_token: app }
triggers:
  - on: timer.tick
    steps: [{ id: t, type: command, command: ["true"] }]
`
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", path}
	warning := autoMigrateOnBoot(args)
	if warning == "" {
		t.Fatal("mixed-schema config must produce a migration warning")
	}
	if _, _, err := loadConfig(args); err == nil {
		t.Fatal("the broken config must fail the strict load (that is the crash-loop scenario)")
	}

	old := degradedRetryInterval
	degradedRetryInterval = 30 * time.Millisecond
	t.Cleanup(func() { degradedRetryInterval = old })

	type res struct {
		err error
	}
	done := make(chan res, 1)
	go func() {
		cfg, err := holdDegradedUntilLoadable(args, warning, os.ErrInvalid)
		if err == nil && cfg == nil {
			err = os.ErrInvalid
		}
		done <- res{err}
	}()

	// The hold must NOT return while the config stays broken.
	select {
	case r := <-done:
		t.Fatalf("degraded hold exited on a still-broken config: %v", r.err)
	case <-time.After(200 * time.Millisecond):
	}

	// The operator (or a newer binary's migration) fixes the file — the hold
	// picks it up and returns a loaded config.
	fixed := `
connectors:
  timer:
    type: cron
    schedules: { tick: { every: 1h } }
triggers:
  - on: timer.tick
    steps: [{ id: t, type: command, command: ["true"] }]
`
	if err := os.WriteFile(path, []byte(fixed), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("hold must resume with the fixed config: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("degraded hold did not pick up the fixed config")
	}
}
