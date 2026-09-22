package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// consumerConfig wires the minimal config that instantiates one pack from src.
func consumerConfig(dir, src string) string {
	return `
connectors:
  timer: { use: cron, schedules: { t: { every: 1h } } }
runtimes:
  claude: { use: cli, tool: claude-code, default: true }
packs:
  fk:
    source: ` + filepath.Join(dir, src) + `
`
}

func loadConsumer(t *testing.T, dir, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return resolveAndLoad(t, path)
}

// A pack-authored cli code step with no isolation: of its own is CONFINED BY
// DEFAULT — conductor synthesizes the least-privilege namespace jail (deny
// network), marked defaulted (best-effort). This is the core protection for
// arbitrary code a pack ships.
func TestPackCodeStepConfinedByDefault(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/filekit", `
pack: { name: filekit, version: 1.0.0 }
workflows:
  run:
    steps:
      - id: go
        use: cli
        command: [bash, -c, "echo hi"]
`)
	cfg, err := loadConsumer(t, dir, consumerConfig(dir, "src/filekit"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.Workflows["fk/run"].Steps[0]
	if s.Isolation == nil {
		t.Fatal("pack cli step must be confined by default (synthesized isolation)")
	}
	if s.Isolation.Mode != "namespace" || s.Isolation.Network == nil || !s.Isolation.Network.Deny {
		t.Fatalf("default confinement must be namespace + network deny, got %#v", s.Isolation)
	}
	if !s.IsolationDefaulted {
		t.Fatal("synthesized confinement must be marked IsolationDefaulted (best-effort)")
	}
}

// A pack step that declares its OWN isolation: keeps it verbatim and is NOT
// marked defaulted (so it fails closed if the box can't realize it).
func TestPackCodeStepExplicitIsolationKept(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/filekit", `
pack: { name: filekit, version: 1.0.0 }
workflows:
  run:
    steps:
      - id: go
        use: cli
        command: [bash, -c, "echo hi"]
        isolation:
          mode: namespace
          network: { deny: true, egress: ["abs.example.com:443"] }
          fs: ["/srv/media"]
`)
	cfg, err := loadConsumer(t, dir, consumerConfig(dir, "src/filekit"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.Workflows["fk/run"].Steps[0]
	if s.IsolationDefaulted {
		t.Fatal("an explicit isolation: must NOT be marked defaulted (it fails closed)")
	}
	if s.Isolation == nil || len(s.Isolation.Network.Egress) != 1 || len(s.Isolation.FS) != 1 {
		t.Fatalf("explicit isolation must be kept verbatim, got %#v", s.Isolation)
	}
}

// A pack may NOT declare its own code trusted: `trust: full` on a shipped step
// is rejected at load, so hostile pack code cannot switch off its own jail.
func TestPackCannotSelfTrust(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/filekit", `
pack: { name: filekit, version: 1.0.0 }
workflows:
  run:
    steps:
      - id: go
        use: cli
        trust: full
        command: [bash, -c, "echo hi"]
`)
	_, err := loadConsumer(t, dir, consumerConfig(dir, "src/filekit"))
	if err == nil || !strings.Contains(err.Error(), "cannot declare its own code trusted") {
		t.Fatalf("a pack setting trust: on its own step must be rejected, got: %v", err)
	}
}

// The CONSUMER, however, may opt a pack step out of confinement via the steps:
// overlay — that is their decision about code they choose to trust.
func TestConsumerCanTrustPackStep(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/filekit", `
pack: { name: filekit, version: 1.0.0 }
workflows:
  run:
    steps:
      - id: go
        use: cli
        command: [bash, -c, "echo hi"]
`)
	body := `
connectors:
  timer: { use: cron, schedules: { t: { every: 1h } } }
runtimes:
  claude: { use: cli, tool: claude-code, default: true }
packs:
  fk:
    source: ` + filepath.Join(dir, "src/filekit") + `
    steps:
      run/go: { trust: full }
`
	cfg, err := loadConsumer(t, dir, body)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.Workflows["fk/run"].Steps[0]
	if s.Isolation != nil {
		t.Fatalf("a consumer trust: full opt-out must leave the step unconfined, got %#v", s.Isolation)
	}
}

// An operator's OWN cli step (not from a pack) is never auto-confined — it is
// their own code, and isolation: stays opt-in there.
func TestOperatorOwnStepNotConfined(t *testing.T) {
	dir := t.TempDir()
	body := `
connectors:
  timer: { use: cron, schedules: { t: { every: 1h } } }
runtimes:
  claude: { use: cli, tool: claude-code, default: true }
workflows:
  mine:
    steps:
      - id: go
        use: cli
        command: [bash, -c, "echo hi"]
`
	cfg, err := loadConsumer(t, dir, body)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	s := cfg.Workflows["mine"].Steps[0]
	if s.Isolation != nil || s.IsolationDefaulted {
		t.Fatalf("an operator's own step must not be auto-confined, got %#v (defaulted=%v)", s.Isolation, s.IsolationDefaulted)
	}
}
