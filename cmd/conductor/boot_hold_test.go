package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// REGRESSION (H1): a config the strict loader rejects — a stray/unknown
// top-level key — used to slip past the degraded-hold guard, which only fired
// on a migration warning. cmdRun then returned the load error and the service
// manager restarted into the same wall forever. Boot must hold degraded on ANY
// load failure, synthesizing an escalate message from the load error, and
// resume once the config becomes loadable.
func TestBootHoldsDegradedOnConnectorsSchemaUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	// A stray top-level key the strict loader rejects.
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
		// No warning in — the hold must still engage and synthesize a
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
			t.Fatal("hold must synthesize a non-empty escalate warning when none is passed in")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("degraded hold did not pick up the fixed connectors-schema config")
	}
}

// TestResolveBootConfigHoldsGateOnUnknownKey drives the actual boot seam
// resolveBootConfig (the gate cmdRun uses), not holdDegradedUntilLoadable in
// isolation, so a regression that re-narrows the hold is
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
