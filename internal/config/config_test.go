package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const sample = `
connectors:
  hooks:
    use: webhook
    options:
      sources: { push: { path: /push } }
      secret: ${TEST_WH_SECRET}
triggers:
  - on: hooks.push
    steps:
      - command: ["true"]
x-steps:
  fixer: &fixer { type: agent, name: fixer, workspace: worktree, wait_timeout: 30m, archive_when_done: true }
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

	ref, ok := cfg.ConnectorsMap["hooks"]
	if !ok {
		t.Fatalf("bad connectors: %+v", cfg.ConnectorsMap)
	}
	if !ref.IsEnabled() {
		t.Fatal("connector should default to enabled")
	}

	// Env expansion reached the raw node.
	if secret, _ := ref.Options["secret"].(string); secret != "shhh" {
		t.Fatalf("env not expanded: %q", secret)
	}

	if cfg.Store.StateTTL.D() != 720*time.Hour {
		t.Fatalf("state_ttl parse: %v", cfg.Store.StateTTL.D())
	}
	if cfg.Store.AuditMaxSize.Bytes() != 50*1024*1024 {
		t.Fatalf("audit_max_size parse: %d", cfg.Store.AuditMaxSize.Bytes())
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
	c.Update.Apply = ApplyModeFor("false")
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

// TestStateDir proves StateDir always resolves to ~/.local/state/conductor.
func TestStateDir(t *testing.T) {
	t.Run("resolves to the conductor state dir", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		want := filepath.Join(tmp, ".local/state/conductor")
		if got := StateDir(); got != want {
			t.Fatalf("StateDir() = %q, want %q", got, want)
		}
	})

	t.Run("applyDefaults routes StateFile/AuditLog through StateDir", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)
		dir := filepath.Join(tmp, ".local/state/conductor")
		c := &Config{}
		c.applyDefaults()
		if want := filepath.Join(dir, "state.json"); c.Store.StateFile != want {
			t.Fatalf("StateFile = %q, want %q", c.Store.StateFile, want)
		}
		if want := filepath.Join(dir, "audit.jsonl"); c.Store.AuditLog != want {
			t.Fatalf("AuditLog = %q, want %q", c.Store.AuditLog, want)
		}
	})
}

func TestValidateRejectsNoConnectors(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	if err := c.Validate(); err == nil {
		t.Fatal("empty config should fail validation")
	}
}

func TestLoadUndefinedEnvVarFails(t *testing.T) {
	os.Unsetenv("TEST_WH_SECRET")
	os.Unsetenv("TEST_SOURCE_PATH")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := strings.Replace(sample, "path: /push", "path: ${TEST_SOURCE_PATH}", 1)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for undefined ${VAR} references")
	}
	// Names every missing variable (once) and points at the sibling conductor.env.
	for _, want := range []string{"TEST_WH_SECRET", "TEST_SOURCE_PATH", filepath.Join(dir, "conductor.env")} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error should mention %q, got: %v", want, err)
		}
	}
	if strings.Count(err.Error(), "TEST_WH_SECRET") != 1 {
		t.Fatalf("variable should be listed once, got: %v", err)
	}

	// Set-but-empty is intentional (KEY= in conductor.env) and must not error.
	t.Setenv("TEST_WH_SECRET", "")
	t.Setenv("TEST_SOURCE_PATH", "")
	if _, err := Load(path); err != nil {
		t.Fatalf("set-but-empty variables should load: %v", err)
	}
}

func TestImportsUndefinedEnvVarFails(t *testing.T) {
	os.Unsetenv("TEST_IMPORTED_SECRET")
	dir := t.TempDir()
	imported := filepath.Join(dir, "hooks.yaml")
	if err := os.WriteFile(imported, []byte(`
connectors:
  hooks:
    use: webhook
    options:
      sources: { push: { path: /push, sign: { secret: ${TEST_IMPORTED_SECRET} } } }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(main, []byte("imports: [hooks.yaml]\ntriggers:\n  - on: hooks.push\n    steps: [{command: [\"true\"]}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(main)
	if err == nil || !strings.Contains(err.Error(), "TEST_IMPORTED_SECRET") || !strings.Contains(err.Error(), imported) {
		t.Fatalf("expected error naming the variable and the imported file, got: %v", err)
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

	// Split across files: main + a conf.d dir, one connector/trigger per file.
	write("conf.d/hooks.yaml", `
connectors:
  hooks:
    use: webhook
    options:
      sources: { push: { path: /push, sign: { secret: ${TEST_WH_SECRET} } } }
triggers:
  - on: hooks.push
    steps: [{command: ["true"]}]
`)
	write("conf.d/rss.yaml", `
connectors:
  feeds:
    use: rss
workflows:
  plan:
    steps: [{ id: p, command: ["true"] }]
`)
	main := write("config.yaml", `
imports:
  - conf.d/*.yaml
connectors:
  chores:
    use: cron
triggers:
  - on: chores.tick
    steps: [{command: ["true"]}]
workflows:
  fix:
    steps: [{ id: f, command: ["true"] }]
dry_run: true    # importer scalar must win over any imported default
`)

	cfg, err := Load(main)
	if err != nil {
		t.Fatal(err)
	}
	// Maps merge: connectors from both the imports and the main file.
	for _, want := range []string{"hooks", "feeds", "chores"} {
		if _, ok := cfg.ConnectorsMap[want]; !ok {
			t.Fatalf("missing connector %q after merge: %+v", want, cfg.ConnectorsMap)
		}
	}
	// Lists concatenate: imported trigger + the main file's = 2.
	if len(cfg.Triggers) != 2 {
		t.Fatalf("want 2 triggers (1 imported + 1 inline), got %d: %+v", len(cfg.Triggers), cfg.Triggers)
	}
	// Maps merge: workflows from both the import and the main file.
	if _, ok := cfg.Workflows["fix"]; !ok {
		t.Fatal("main-file workflow 'fix' missing")
	}
	if _, ok := cfg.Workflows["plan"]; !ok {
		t.Fatal("imported workflow 'plan' missing")
	}
	// Importer scalar wins.
	if !cfg.DryRun {
		t.Fatal("importer scalar (dry_run: true) should win")
	}
	// Env expansion reached an imported connector's raw node.
	if secret, _ := cfg.ConnectorsMap["hooks"].Options["sources"].(map[string]any)["push"].(map[string]any)["sign"].(map[string]any)["secret"].(string); secret != "shhh" {
		t.Fatalf("env not expanded in imported file: %q", secret)
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
	w("base.yaml", "connectors:\n  base: { use: cron }\ntriggers:\n  - { on: base.tick, steps: [{command: [\"true\"]}] }\n")
	w("a.yaml", "imports: [base.yaml]\n")
	w("config.yaml", "imports: [a.yaml, base.yaml]\n")

	cfg, err := Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Triggers) != 1 {
		t.Fatalf("diamond import should include base's trigger once, got %d", len(cfg.Triggers))
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

func TestActionSetRefsNamesVariants(t *testing.T) {
	set := ActionSet{{Type: "agent", Agent: "a"}, {Name: "v", Type: "agent", Agent: "b"}}
	refs := set.Refs("issue_matched")
	if len(refs) != 2 || refs[0].Where != "issue_matched" || refs[1].Where != "issue_matched[v]" {
		t.Fatalf("unexpected refs: %+v", refs)
	}
}

func TestEffectiveTransportDefaults(t *testing.T) {
	if got := (ControllerConfig{Agent: "gemini"}).EffectiveTransport(); got != "acp" {
		t.Fatalf("an agent runtime defaults to acp, got %q", got)
	}
	if got := (ControllerConfig{Type: "paseo"}).EffectiveTransport(); got != "native" {
		t.Fatalf("a built-in type defaults to native, got %q", got)
	}
	if got := (ControllerConfig{Agent: "aider", Transport: "cli"}).EffectiveTransport(); got != "cli" {
		t.Fatalf("an explicit transport must win, got %q", got)
	}
}
