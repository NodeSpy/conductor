package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const sample = `
integrations:
  - type: github
    name: acme
    app: { app_id: 123, private_key_path: ~/key.pem, webhook_secret: ${TEST_WH_SECRET} }
    webhook: { smee_url: https://smee.io/abc }
control: { pause_label: "conductor:off" }
notify: { push: true, on: [dispatch, escalate] }
agents:
  fixer: { provider: claude, workspace: worktree, wait_timeout: 30m, archive_when_done: true }
dispatch:
  identity: { read_token: app, write_token: gh_auth }
store:
  state_ttl: 720h
  audit_max_size: 50MB
`

func TestLoadAndExpand(t *testing.T) {
	os.Setenv("TEST_WH_SECRET", "shhh")
	defer os.Unsetenv("TEST_WH_SECRET")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(sample), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(cfg.Integrations) != 1 || cfg.Integrations[0].Name != "acme" {
		t.Fatalf("bad integrations: %+v", cfg.Integrations)
	}
	if !cfg.Integrations[0].IsEnabled() {
		t.Fatal("integration should default to enabled")
	}

	// Env expansion reached the raw node.
	var gh struct {
		App struct {
			WebhookSecret string `yaml:"webhook_secret"`
		} `yaml:"app"`
	}
	if err := cfg.Integrations[0].Decode(&gh); err != nil {
		t.Fatal(err)
	}
	if gh.App.WebhookSecret != "shhh" {
		t.Fatalf("env not expanded: %q", gh.App.WebhookSecret)
	}

	if cfg.Store.StateTTL.D() != 720*time.Hour {
		t.Fatalf("state_ttl parse: %v", cfg.Store.StateTTL.D())
	}
	if cfg.Store.AuditMaxSize.Bytes() != 50*1024*1024 {
		t.Fatalf("audit_max_size parse: %d", cfg.Store.AuditMaxSize.Bytes())
	}
	// Defaults applied.
	if cfg.PaseoBin != "paseo" {
		t.Fatalf("paseo_bin default not applied: %q", cfg.PaseoBin)
	}
	if !cfg.Control.IsEnabled() {
		t.Fatal("control should default enabled")
	}
	if !cfg.Notify.Wants("escalate") || cfg.Notify.Wants("complete") {
		t.Fatal("notify.on parsing wrong")
	}
}

func TestUpdateDefaults(t *testing.T) {
	c := &Config{}
	c.Update.Auto = true
	c.applyDefaults()
	if c.Update.Interval.D() != 8*time.Hour {
		t.Fatalf("auto-update interval default = %v, want 8h", c.Update.Interval.D())
	}
	if !c.Update.ShouldApply() {
		t.Fatal("apply should default to true")
	}
	// Explicit apply:false is honored.
	no := false
	c.Update.Apply = &no
	if c.Update.ShouldApply() {
		t.Fatal("apply:false should be honored")
	}
	// No default interval when auto is off.
	c2 := &Config{}
	c2.applyDefaults()
	if c2.Update.Interval != 0 {
		t.Fatal("interval should stay 0 when auto is off")
	}
}

func TestValidateRejectsNoIntegrations(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	if err := c.Validate(); err == nil {
		t.Fatal("empty config should fail validation")
	}
}
