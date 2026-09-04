package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NodeSpy/paseo-conductor/internal/config"
)

func loadConfigDoc(t *testing.T, doc string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestDisabledConnectorSourceSuppressed: an author-set `enabled: false`
// suppresses the connector's source integration — its triggers go inert
// instead of firing (and instead of failing the boot).
func TestDisabledConnectorSourceSuppressed(t *testing.T) {
	cfg := loadConfigDoc(t, `
connectors:
  timer:
    type: cron
    enabled: false
    schedules: { tick: { every: 1h } }
  box:
    type: command
triggers:
  - on: timer.tick
    steps: [{ id: hi, uses: box.run, options: { command: "true" } }]
`)
	stack, err := buildFlowStack(cfg, nil, nil, true)
	if err != nil {
		t.Fatalf("a disabled connector must not fail the boot: %v", err)
	}
	if len(stack.Integrations) != 0 {
		t.Fatalf("disabled connector must open no sources, got %d", len(stack.Integrations))
	}
	in, _ := stack.Registry.Get("timer")
	if in.Enabled {
		t.Fatal("enabled: false lost in lowering")
	}
}
