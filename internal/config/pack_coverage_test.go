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
	node := yaml.Node{Kind: yaml.MappingNode}
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
		{"memory", PackManifest{Memory: node}},
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
	for _, want := range []string{"deprecated", "not declared"} {
		if !strings.Contains(ws, want) {
			t.Errorf("expected a warning containing %q, got:\n%s", want, ws)
		}
	}
}

// TestPackGithubTriggerArmedNeedsRepos proves the fail-closed consent boundary:
// a github pack trigger armed with enabled:true but no repos is rejected (an
// empty repo set would otherwise match every repo the connector observes),
// while the same arm WITH repos succeeds.
func TestPackGithubTriggerArmedNeedsRepos(t *testing.T) {
	const manifest = `
pack:
  name: gh-pack
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    connectors: [github]
triggers:
  - name: on_review
    on: github.review_requested
    steps: [ { id: s, run: js, code: "return {}" } ]
`
	write := func(t *testing.T, arm string) string {
		dir := t.TempDir()
		writePackSource(t, dir, "src", manifest)
		body := `
connectors: { gh: { type: github } }
packs:
  p:
    source: ./src
    connectors: { github: gh }
    triggers:
      on_review:
` + arm
		path := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// Armed, no repos → hard error naming the fix.
	if _, err := resolveAndLoad(t, write(t, "        enabled: true\n")); err == nil {
		t.Fatal("armed github pack trigger with no repos was accepted; the consent boundary must reject it")
	} else if !strings.Contains(err.Error(), "repos") {
		t.Fatalf("error did not name repos as the fix: %v", err)
	}

	// Armed, WITH repos → accepted.
	if _, err := resolveAndLoad(t, write(t, "        enabled: true\n        repos: [acme/app]\n")); err != nil {
		t.Fatalf("armed github pack trigger WITH repos must be accepted: %v", err)
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

// TestApplyAgentOverrideReplacesLists locks in the security-relevant override
// semantics: a pack override REPLACES a bundled list (it does not append), so a
// consumer can NARROW a bundled agent's skill.verbs — not only widen it.
func TestApplyAgentOverrideReplacesLists(t *testing.T) {
	base := AgentProfile{
		Workspace: "worktree",
		Skill:     &SkillPolicy{Verbs: []string{"gh.comment", "gh.submit_review"}},
	}
	out, err := applyAgentOverride(base, map[string]any{
		"skill": map[string]any{"verbs": []any{"gh.comment"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// replace semantics: the override's single verb wins; the bundle's extra
	// verb is dropped (a real restriction).
	if len(out.Skill.Verbs) != 1 || out.Skill.Verbs[0] != "gh.comment" {
		t.Fatalf("override should REPLACE the bundled list (narrowing), got %v", out.Skill.Verbs)
	}
	// non-overridden scalar fields survive the deep-merge.
	if out.Workspace != "worktree" {
		t.Fatalf("non-overridden field should survive, got workspace=%q", out.Workspace)
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

// The ref-lint warns about a bound name used inside a code body and a
// {{ vault }} template — references the rewriter cannot reach.
func TestPackRefLintWarnings(t *testing.T) {
	dir := t.TempDir()
	writePackSource(t, dir, "src/p", `
pack:
  name: p
  version: 1.0.0
  requires:
    conductor: ">=0.1"
    stores: [cache]
    secrets: { tok: { desc: x } }
workflows:
  flow:
    steps:
      - id: a
        run: js
        code: "const v = ctx.store('cache'); return { v }"
      - id: b
        run: js
        code: "return { s: '{{ vault \"house\" \"k\" }}' }"
`)
	body := `
connectors: { gh: { type: github } }
vaults: { house: { type: file, dir: /tmp/pc-reflint } }
stores: { redis1: { type: boltdb, path: /tmp/pc-reflint.db } }
packs:
  p:
    source: ./src/p
    stores: { cache: redis1 }
    secrets: { tok: house/k }
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
	if !strings.Contains(ws, "code: step body references bound name(s) cache") {
		t.Errorf("expected a code-ref warning for 'cache', got:\n%s", ws)
	}
	if !strings.Contains(ws, "vault") {
		t.Errorf("expected a vault-template warning, got:\n%s", ws)
	}
}
