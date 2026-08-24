package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
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
	if c.Update.Interval.D() != 10*time.Minute {
		t.Fatalf("auto-update interval default = %v, want 10m", c.Update.Interval.D())
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

func TestImportsMergeAndConcat(t *testing.T) {
	os.Setenv("TEST_WH_SECRET", "shhh")
	defer os.Unsetenv("TEST_WH_SECRET")
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Split across files: main + a conf.d dir with one integration per file.
	write("conf.d/github.yaml", `
integrations:
  - type: github
    name: gh
    app: { app_id: 1, private_key_path: ~/k.pem, webhook_secret: ${TEST_WH_SECRET} }
    webhook: { smee_url: https://smee.io/x }
`)
	write("conf.d/rss.yaml", `
integrations:
  - type: rss
    name: feeds
agents:
  planner: { provider: claude }
`)
	main := write("config.yaml", `
imports:
  - conf.d/*.yaml
integrations:
  - type: cron
    name: chores
agents:
  fixer: { provider: claude, workspace: worktree }
paseo_bin: /custom/paseo    # importer scalar must win over any imported default
`)

	cfg, err := Load(main)
	if err != nil {
		t.Fatal(err)
	}
	// Lists concatenate: imported github + rss, then the main file's cron = 3.
	if len(cfg.Integrations) != 3 {
		t.Fatalf("want 3 integrations (2 imported + 1 inline), got %d: %+v", len(cfg.Integrations), cfg.Integrations)
	}
	names := map[string]bool{}
	for _, ig := range cfg.Integrations {
		names[ig.Name] = true
	}
	if !names["gh"] || !names["feeds"] || !names["chores"] {
		t.Fatalf("missing an integration after merge: %v", names)
	}
	// Maps merge: agents from both the import and the main file.
	if _, ok := cfg.Agents["fixer"]; !ok {
		t.Fatal("main-file agent 'fixer' missing")
	}
	if _, ok := cfg.Agents["planner"]; !ok {
		t.Fatal("imported agent 'planner' missing")
	}
	// Importer scalar wins.
	if cfg.PaseoBin != "/custom/paseo" {
		t.Fatalf("importer scalar should win, got %q", cfg.PaseoBin)
	}
	// Env expansion reached an imported integration's raw node.
	var gh struct {
		App struct {
			WebhookSecret string `yaml:"webhook_secret"`
		} `yaml:"app"`
	}
	for _, ig := range cfg.Integrations {
		if ig.Name == "gh" {
			if err := ig.Decode(&gh); err != nil {
				t.Fatal(err)
			}
		}
	}
	if gh.App.WebhookSecret != "shhh" {
		t.Fatalf("env not expanded in imported file: %q", gh.App.WebhookSecret)
	}
}

func TestImportsMissingFileErrors(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("imports: [nope.yaml]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("an import matching no files should error")
	}
}

func TestImportsDiamondDedup(t *testing.T) {
	dir := t.TempDir()
	w := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// base is imported by both a.yaml and the main file → must contribute once.
	w("base.yaml", "integrations:\n  - { type: cron, name: base }\n")
	w("a.yaml", "imports: [base.yaml]\n")
	w("config.yaml", "imports: [a.yaml, base.yaml]\n")

	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Integrations) != 1 {
		t.Fatalf("diamond import should include base once, got %d", len(cfg.Integrations))
	}
}

func TestActionSetUnmarshal(t *testing.T) {
	var m struct {
		Actions map[string]ActionSet `yaml:"actions"`
	}
	y := []byte("actions:\n" +
		"  merge_conflict: { type: agent, agent: opus }\n" +
		"  issue_matched:\n" +
		"    - { name: a, agent: x }\n" +
		"    - { name: b, agent: y }\n")
	if err := yaml.Unmarshal(y, &m); err != nil {
		t.Fatal(err)
	}
	// A single mapping parses to a 1-element set (backward compatible).
	if s := m.Actions["merge_conflict"]; len(s) != 1 || s[0].Agent != "opus" {
		t.Fatalf("single object should be a 1-element set: %+v", s)
	}
	// A sequence parses to N named variants.
	if s := m.Actions["issue_matched"]; len(s) != 2 || s[0].Name != "a" || s[1].Name != "b" || s[1].Agent != "y" {
		t.Fatalf("list should parse to named variants: %+v", s)
	}
}
