package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestCheckNoEnvironmentAllSections proves EACH bind-only section is rejected by
// name — the security boundary is six independent branches, not just connectors.
func TestCheckNoEnvironmentAllSections(t *testing.T) {
	one := map[string]yaml.Node{"x": {Kind: yaml.ScalarNode, Value: "y"}}
	cases := []struct {
		name string
		m    PackManifest
	}{
		{"connectors", PackManifest{Connectors: one}},
		{"secrets", PackManifest{Secrets: one}},
		{"vaults", PackManifest{Vaults: one}},
		{"stores", PackManifest{StoresRaw: one}},
		{"runtimes", PackManifest{Runtimes: one}},
		{"hosts", PackManifest{Hosts: one}},
	}
	for _, c := range cases {
		c.m.Pack.Name = "p"
		err := c.m.checkNoEnvironment()
		if err == nil {
			t.Errorf("shipping %s should be rejected", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.name) {
			t.Errorf("rejection for %s should name it: %v", c.name, err)
		}
	}
	// A behavior-only manifest passes.
	if err := (&PackManifest{Pack: PackMeta{Name: "p"}}).checkNoEnvironment(); err != nil {
		t.Errorf("a behavior-only pack must pass: %v", err)
	}
}

// TestPackWarnings exercises the three warning sites, which otherwise have no
// observer: deprecation, armed-with-no-repos, and an undeclared setting.
func TestPackWarnings(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/dep", `
pack:
  name: dep-pack
  version: 0.1.0
  deprecated: "use dep-pack v2"
  requires: { conductor: ">=0.1" }
settings:
  known: { type: string, default: k }
triggers:
  - name: t1
    on: manual
    steps: [ { id: s, run: js, code: "return {}" } ]
`)
	body := `
connectors: { gh: { type: github } }
packs:
  dep:
    source: ./src/dep
    settings: { unknown_setting: v }
    triggers:
      t1: { enabled: true }        # armed but NO repos
`
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := resolveAndLoad(t, path)
	if err != nil {
		t.Fatalf("resolveAndLoad: %v", err)
	}
	ws := strings.Join(cfg.PackWarnings(), "\n")
	for _, want := range []string{"deprecated", "armed with no repos", "not declared"} {
		if !strings.Contains(ws, want) {
			t.Errorf("expected a warning containing %q, got:\n%s", want, ws)
		}
	}
}

// TestLintPackManifestNegatives feeds one malformed manifest per problem type
// and asserts the specific lint message — currently only the clean pack is linted.
func TestLintPackManifestNegatives(t *testing.T) {
	cases := []struct {
		name string
		man  PackManifest
		want string
	}{
		{"missing version", PackManifest{Pack: PackMeta{Name: "p", Requires: PackRequires{Conductor: ">=0.1"}}}, "pack.version is required"},
		{"missing conductor", PackManifest{Pack: PackMeta{Name: "p", Version: "1"}}, "requires.conductor is required"},
		{"dangling export wf", PackManifest{
			Pack:    PackMeta{Name: "p", Version: "1", Requires: PackRequires{Conductor: ">=0.1"}},
			Exports: PackExports{Workflows: []string{"ghost"}},
		}, "exports.workflows"},
		{"role without agent", PackManifest{
			Pack: PackMeta{Name: "p", Version: "1", Requires: PackRequires{Conductor: ">=0.1", Roles: map[string]RoleReq{"r": {}}}},
		}, "requires.roles"},
	}
	for _, c := range cases {
		problems := LintPackManifest(&c.man)
		if !containsSubstr(problems, c.want) {
			t.Errorf("%s: expected a problem containing %q, got %v", c.name, c.want, problems)
		}
	}
}

// TestLintUndeclaredSettingsRef proves the settings-ref lint catches a prompt
// that references a setting the pack never declares.
func TestLintUndeclaredSettingsRef(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "p", `
pack:
  name: p
  version: 1.0.0
  requires: { conductor: ">=0.1" }
workflows:
  flow:
    steps:
      - id: s
        type: agent
        agent: a
        prompt: "uses ${settings.nope}"
agents:
  a: { workspace: local }
`)
	problems, err := LintPackDir(filepath.Join(dir, "p"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(problems, "${settings.nope}") {
		t.Fatalf("expected an undeclared-settings-ref problem, got %v", problems)
	}
}

// TestApplyAgentOverrideListsAppend documents mergeMaps list semantics: an
// override of a list field ADDS to the bundle (append), it does not replace. A
// consumer can broaden a bundled agent's skill.verbs but not narrow it via
// override — narrowing requires a full bind to a global.
func TestApplyAgentOverrideListsAppend(t *testing.T) {
	base := AgentProfile{
		Workspace: "worktree",
		Skill:     &SkillPolicy{Verbs: []string{"gh.comment"}},
	}
	out, err := applyAgentOverride(base, map[string]any{
		"skill": map[string]any{"verbs": []any{"gh.submit_review"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// append semantics: both verbs present.
	if !contains(out.Skill.Verbs, "gh.comment") || !contains(out.Skill.Verbs, "gh.submit_review") {
		t.Fatalf("override should append to the bundled list, got %v", out.Skill.Verbs)
	}
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
