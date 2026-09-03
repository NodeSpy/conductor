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

func TestCheckAgentRefs(t *testing.T) {
	c := &Config{Agents: map[string]AgentProfile{"opus": {Provider: "claude"}, "sonnet": {Provider: "claude"}}}
	step := func(id, agent string) Action { return Action{Type: "agent", ID: id, Agent: agent} }

	// Defined profiles at the top level and in nested steps pass; command
	// actions/steps never need a profile.
	ok := []ActionRef{
		{Where: "a", Action: Action{Type: "agent", Agent: "opus"}},
		{Where: "b", Action: Action{Type: "command", Command: []string{"true"}}},
		{Where: "c", Action: Action{Steps: []Action{step("assess", "sonnet"), {Type: "command"}, step("handoff", "opus")}}},
	}
	if err := c.CheckAgentRefs(ok); err != nil {
		t.Fatalf("valid refs rejected: %v", err)
	}

	// An unknown profile in a workflow step is the MISSING_PROVIDER-at-dispatch
	// bug: it must fail up front and say where.
	bad := []ActionRef{{Where: "github[x] rules[0].actions.review_requested",
		Action: Action{Steps: []Action{step("assess", "sonnet"), step("handoff", "sonnet-interactive")}}}}
	err := c.CheckAgentRefs(bad)
	if err == nil {
		t.Fatal("unknown step profile should fail")
	}
	for _, want := range []string{"review_requested step handoff", `"sonnet-interactive"`, "opus, sonnet"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}

	// A top-level agent action with no profile at all is rejected too.
	if err := c.CheckAgentRefs([]ActionRef{{Where: "cron[x] schedule \"nightly\"", Action: Action{Type: "agent"}}}); err == nil ||
		!strings.Contains(err.Error(), "needs `agent:") {
		t.Fatalf("agent action without profile should fail, got %v", err)
	}

	// Unnamed steps report the engine's positional default id.
	err = c.CheckAgentRefs([]ActionRef{{Where: "w", Action: Action{Steps: []Action{step("", "nope")}}}})
	if err == nil || !strings.Contains(err.Error(), "w step step1") {
		t.Fatalf("unnamed step should report step1, got %v", err)
	}
}

func TestActionSetRefsNamesVariants(t *testing.T) {
	set := ActionSet{{Type: "agent", Agent: "a"}, {Name: "v", Type: "agent", Agent: "b"}}
	refs := set.Refs("issue_matched")
	if len(refs) != 2 || refs[0].Where != "issue_matched" || refs[1].Where != "issue_matched[v]" {
		t.Fatalf("unexpected refs: %+v", refs)
	}
}

// ctrlBaseCfg is a minimal valid config (one integration) to exercise the
// controllers/agent validation without tripping the no-integrations check.
func ctrlBaseCfg() *Config {
	return &Config{Integrations: []IntegrationRef{{Type: "github", Name: "gh"}}}
}

func TestControllersNoBlockIsValidAndPaseoDefault(t *testing.T) {
	c := ctrlBaseCfg()
	if err := c.Validate(); err != nil {
		t.Fatalf("no controllers block must validate, got %v", err)
	}
	if got := c.DefaultControllerName(); got != "" {
		t.Fatalf("no default flagged → empty name (falls back to built-in paseo), got %q", got)
	}
}

func TestControllersValidBlock(t *testing.T) {
	c := ctrlBaseCfg()
	c.Controllers = map[string]ControllerConfig{
		"pae":   {Type: "paseo", Default: true},
		"gem":   {Agent: "gemini"},
		"ocode": {Agent: "opencode", Transport: "native", SessionModel: "resumable"},
	}
	c.Agents = map[string]AgentProfile{"reviewer": {Controller: "gem"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid controllers block should pass, got %v", err)
	}
	if got := c.DefaultControllerName(); got != "pae" {
		t.Fatalf("default:true should be found, got %q", got)
	}
}

func TestControllersTwoDefaultsRejected(t *testing.T) {
	c := ctrlBaseCfg()
	c.Controllers = map[string]ControllerConfig{
		"a": {Type: "paseo", Default: true},
		"b": {Agent: "gemini", Default: true},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("two default:true controllers must be rejected")
	}
}

func TestControllerTypeXorAgent(t *testing.T) {
	both := ctrlBaseCfg()
	both.Controllers = map[string]ControllerConfig{"x": {Type: "paseo", Agent: "gemini"}}
	if err := both.Validate(); err == nil {
		t.Fatal("setting both type and agent must be rejected")
	}
	neither := ctrlBaseCfg()
	neither.Controllers = map[string]ControllerConfig{"x": {Transport: "acp"}}
	if err := neither.Validate(); err == nil {
		t.Fatal("setting neither type nor agent must be rejected")
	}
}

func TestControllerBadTransportAndModel(t *testing.T) {
	badT := ctrlBaseCfg()
	badT.Controllers = map[string]ControllerConfig{"x": {Agent: "gemini", Transport: "carrier-pigeon"}}
	if err := badT.Validate(); err == nil {
		t.Fatal("unknown transport must be rejected")
	}
	badM := ctrlBaseCfg()
	badM.Controllers = map[string]ControllerConfig{"x": {Agent: "gemini", SessionModel: "eternal"}}
	if err := badM.Validate(); err == nil {
		t.Fatal("unknown session_model must be rejected")
	}
}

func TestAgentUnknownControllerRejected(t *testing.T) {
	c := ctrlBaseCfg()
	c.Controllers = map[string]ControllerConfig{"pae": {Type: "paseo"}}
	c.Agents = map[string]AgentProfile{"fixer": {Controller: "does-not-exist"}}
	if err := c.Validate(); err == nil {
		t.Fatal("an agent referencing an undefined controller must be rejected")
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

func TestHandoffsNoBlockIsValid(t *testing.T) {
	c := ctrlBaseCfg()
	if err := c.Validate(); err != nil {
		t.Fatalf("no handoffs block must validate, got %v", err)
	}
	if got := c.DefaultHandoffName(); got != "" {
		t.Fatalf("no default flagged → empty name, got %q", got)
	}
}

func TestHandoffsValidBlock(t *testing.T) {
	c := ctrlBaseCfg()
	c.Handoffs = map[string]HandoffConfig{
		"phone": {Slack: &HandoffChat{To: "dm"}, Default: true},
		"page":  {Web: &HandoffWeb{BaseURL: "https://conductor.example.com"}},
		"pager": {Discord: &HandoffChat{To: "thread", Channel: "C1"}},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid handoffs block should pass, got %v", err)
	}
	if got := c.DefaultHandoffName(); got != "phone" {
		t.Fatalf("default:true should be found, got %q", got)
	}
}

func TestHandoffsExactlyOneChannelRequired(t *testing.T) {
	none := ctrlBaseCfg()
	none.Handoffs = map[string]HandoffConfig{"x": {}}
	if err := none.Validate(); err == nil {
		t.Fatal("an entry with no channel sub-block must be rejected")
	}
	both := ctrlBaseCfg()
	both.Handoffs = map[string]HandoffConfig{"x": {
		Web:   &HandoffWeb{BaseURL: "https://a.test"},
		Slack: &HandoffChat{To: "dm"},
	}}
	if err := both.Validate(); err == nil {
		t.Fatal("an entry with two channel sub-blocks must be rejected")
	}
}

func TestHandoffsTwoDefaultsRejected(t *testing.T) {
	c := ctrlBaseCfg()
	c.Handoffs = map[string]HandoffConfig{
		"a": {Web: &HandoffWeb{BaseURL: "https://a.test"}, Default: true},
		"b": {Slack: &HandoffChat{To: "dm"}, Default: true},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("two default:true handoffs must be rejected")
	}
}

func TestCheckAgentRefsUnknownHandoffRejected(t *testing.T) {
	c := ctrlBaseCfg()
	c.Handoffs = map[string]HandoffConfig{"page": {Web: &HandoffWeb{BaseURL: "https://a.test"}}}
	refs := []ActionRef{{Where: "w", Action: Action{Steps: []Action{
		{Type: "command", ID: "assess", Command: []string{"true"}},
		{Type: "command", ID: "review", Background: true, Handoff: "does-not-exist", Command: []string{"true"}},
	}}}}
	err := c.CheckAgentRefs(refs)
	if err == nil {
		t.Fatal("a step naming an undefined handoff must be rejected")
	}
	for _, want := range []string{"w step review", `"does-not-exist"`, "page"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestCheckAgentRefsKnownHandoffPasses(t *testing.T) {
	c := ctrlBaseCfg()
	c.Handoffs = map[string]HandoffConfig{"page": {Web: &HandoffWeb{BaseURL: "https://a.test"}}}
	refs := []ActionRef{{Where: "w", Action: Action{Steps: []Action{
		{Type: "command", ID: "review", Background: true, Handoff: "page", Command: []string{"true"}},
	}}}}
	if err := c.CheckAgentRefs(refs); err != nil {
		t.Fatalf("a step naming a defined handoff should pass, got %v", err)
	}
}

// TestHandoffCompatShimSynthesizesDefault verifies the legacy singular
// `handoff: { web: … }` block is folded into `handoffs: { default: { web: …,
// default: true } }` by applyDefaults when `handoffs:` is empty, so an old
// config keeps resolving to that channel unchanged.
func TestHandoffCompatShimSynthesizesDefault(t *testing.T) {
	c := &Config{}
	c.Handoff.Web = HandoffWeb{BaseURL: "https://old.example.com", Listen: ":9099"}
	c.applyDefaults()

	if len(c.Handoffs) != 1 {
		t.Fatalf("expected exactly one synthesized handoff entry, got %d: %+v", len(c.Handoffs), c.Handoffs)
	}
	hc, ok := c.Handoffs["default"]
	if !ok {
		t.Fatalf(`expected a "default" entry, got %+v`, c.Handoffs)
	}
	if !hc.Default {
		t.Fatal("the synthesized entry must be flagged default:true")
	}
	if hc.Web == nil || hc.Web.BaseURL != "https://old.example.com" || hc.Web.Listen != ":9099" {
		t.Fatalf("synthesized web config should carry over the legacy fields, got %+v", hc.Web)
	}
	if got := c.DefaultHandoffName(); got != "default" {
		t.Fatalf("DefaultHandoffName should find the synthesized entry, got %q", got)
	}
}

// TestHandoffCompatShimNoopWhenHandoffsSet confirms the new `handoffs:` map
// always wins — the legacy block is ignored once it's populated.
func TestHandoffCompatShimNoopWhenHandoffsSet(t *testing.T) {
	c := &Config{}
	c.Handoff.Web = HandoffWeb{BaseURL: "https://old.example.com"}
	c.Handoffs = map[string]HandoffConfig{
		"new": {Web: &HandoffWeb{BaseURL: "https://new.example.com"}, Default: true},
	}
	c.applyDefaults()

	if len(c.Handoffs) != 1 {
		t.Fatalf("handoffs map should be untouched, got %+v", c.Handoffs)
	}
	if _, ok := c.Handoffs["default"]; ok {
		t.Fatal("the shim must not run when handoffs: is already set")
	}
	if c.Handoffs["new"].Web.BaseURL != "https://new.example.com" {
		t.Fatalf("existing handoffs entry should be untouched, got %+v", c.Handoffs["new"])
	}
}

// TestHandoffCompatShimNoopWhenLegacyEmpty confirms an unset legacy block
// synthesizes nothing (an empty Handoffs map, not a spurious default entry).
func TestHandoffCompatShimNoopWhenLegacyEmpty(t *testing.T) {
	c := &Config{}
	c.applyDefaults()
	if len(c.Handoffs) != 0 {
		t.Fatalf("no legacy handoff and no handoffs: should synthesize nothing, got %+v", c.Handoffs)
	}
}
