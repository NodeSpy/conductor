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
	// the profile pins a runtime nothing defines. (`agent:` no longer names
	// anything resolvable — design §6 — so an unknown profile is not the
	// failure it used to be; an unknown RUNTIME still is.)
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
              agent: fixer
              prompt: "fix"
agents:
  fixer: { runtime: ghost-runtime }
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
              extends: fixer
              prompt: "fix"
steps:
  fixer: { type: agent, name: fixer }
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
    use: cron
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
		cfg, _, err := holdDegradedUntilLoadable(args, warning, os.ErrInvalid)
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
    use: cron
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

// REGRESSION (H1): a config already on the CONNECTORS schema (no legacy markers,
// so no migration runs → migrateWarning=="") that the strict loader rejects — a
// stray/unknown top-level key — used to slip past the degraded-hold guard, which
// only fired when migrateWarning!="". cmdRun then returned the load error and the
// service manager restarted into the same wall forever. Boot must now hold
// degraded on ANY post-migrate load failure, synthesizing an escalate message
// from the load error, and resume once the config becomes loadable.
func TestBootHoldsDegradedOnConnectorsSchemaUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// Connectors schema (no `integrations:`/legacy markers) with a stray
	// top-level key the strict loader rejects. This is the migrateWarning==""
	// case the old guard skipped.
	broken := `
connectors:
  timer:
    use: cron
    schedules: { tick: { every: 1h } }
bogus_unknown_key: true
triggers:
  - on: timer.tick
    steps: [{ id: t, type: command, command: ["true"] }]
`
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", path}

	// Precondition: no migration runs (already connectors schema) → empty
	// warning, exactly the branch the pre-fix guard skipped.
	if w := autoMigrateOnBoot(args); w != "" {
		t.Fatalf("connectors-schema config must not migrate (got warning %q)", w)
	}
	if _, _, err := loadConfig(args); err == nil {
		t.Fatal("the unknown top-level key must fail the strict load")
	}

	old := degradedRetryInterval
	degradedRetryInterval = 30 * time.Millisecond
	t.Cleanup(func() { degradedRetryInterval = old })

	type res struct {
		cfg     bool
		warning string
		err     error
	}
	done := make(chan res, 1)
	go func() {
		// migrateWarning=="" — the hold must still engage and synthesize a
		// non-empty escalate message from the load error.
		cfg, warning, err := holdDegradedUntilLoadable(args, "", os.ErrInvalid)
		done <- res{cfg: cfg != nil, warning: warning, err: err}
	}()

	// The hold must NOT return (must not fatally exit) while the config stays
	// broken — this is the crash-loop that H1 prevents.
	select {
	case r := <-done:
		t.Fatalf("degraded hold exited on a still-broken connectors-schema config: %+v", r)
	case <-time.After(200 * time.Millisecond):
	}

	// Operator drops the stray key — the hold resumes a normal boot.
	fixed := `
connectors:
  timer:
    use: cron
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
		if r.err != nil || !r.cfg {
			t.Fatalf("hold must resume with the fixed config: %+v", r)
		}
		if r.warning == "" {
			t.Fatal("hold must synthesize a non-empty escalate warning when migrateWarning is empty")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("degraded hold did not pick up the fixed connectors-schema config")
	}
}

// TestResolveBootConfigHoldsGateOnUnknownKey drives the actual boot seam
// resolveBootConfig (the gate cmdRun uses), not holdDegradedUntilLoadable in
// isolation, so a regression that re-narrows the hold to migrateWarning!="" is
// caught: a connectors-schema config with a stray key loads-fails with an empty
// warning, and the seam must HOLD (never return → never exit cmdRun) until the
// key is removed.
func TestResolveBootConfigHoldsGateOnUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	broken := `
connectors:
  timer:
    use: cron
    schedules: { tick: { every: 1h } }
bogus_unknown_key: true
triggers:
  - on: timer.tick
    steps: [{ id: t, type: command, command: ["true"] }]
`
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--config", path}
	// Precondition: no migration (already connectors schema) → migrateWarning=="".
	if w := autoMigrateOnBoot(args); w != "" {
		t.Fatalf("connectors-schema config must not migrate (warning %q)", w)
	}

	old := degradedRetryInterval
	degradedRetryInterval = 30 * time.Millisecond
	t.Cleanup(func() { degradedRetryInterval = old })

	done := make(chan error, 1)
	go func() { _, _, err := resolveBootConfig(args); done <- err }()

	// The seam must NOT return on a still-broken config — returning is exactly
	// the cmdRun exit → service-manager crash-loop H1 prevents.
	select {
	case err := <-done:
		t.Fatalf("resolveBootConfig returned on a still-broken connectors-schema config (would exit cmdRun → crash-loop): err=%v", err)
	case <-time.After(200 * time.Millisecond):
	}

	// Operator removes the stray key — the seam resumes a normal boot.
	fixed := `
connectors:
  timer:
    use: cron
    schedules: { tick: { every: 1h } }
triggers:
  - on: timer.tick
    steps: [{ id: t, type: command, command: ["true"] }]
`
	if err := os.WriteFile(path, []byte(fixed), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("seam must resume with the fixed config: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("seam did not pick up the fixed connectors-schema config")
	}
}
